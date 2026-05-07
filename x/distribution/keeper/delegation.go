package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// initialize starting info for a new delegation
func (k Keeper) initializeDelegation(ctx context.Context, val sdk.ValAddress, del sdk.AccAddress) error {
	// period has already been incremented - we want to store the period ended by this delegation action
	valCurrentRewards, err := k.GetValidatorCurrentRewards(ctx, val)
	if err != nil {
		return err
	}
	previousPeriod := valCurrentRewards.Period - 1

	// increment reference count for the period we're going to track
	err = k.incrementReferenceCount(ctx, val, previousPeriod)
	if err != nil {
		return err
	}

	delegation, err := k.stakingKeeper.Delegation(ctx, del, val)
	if err != nil {
		return err
	}

	// Snapshot the delegator's share count. F1 ratios are stored as
	// rewards-per-share (not rewards-per-token), so the delegator's share count
	// is the right unit to multiply against later. This is invariant to the
	// per-share exchange rate, which can shift between init and withdrawal due
	// to auto-staking and slashing — neither modifies validator.DelegatorShares.
	//
	// The DelegatorStartingInfo proto field is named `Stake` for historical
	// reasons (it held tokens-from-shares under the old tokens-based F1). It
	// now stores the delegator's share count. We keep the proto name for
	// backwards compatibility with stored state but use `shares` in code so
	// the semantics are clear.
	shares := delegation.GetShares()
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return k.SetDelegatorStartingInfo(ctx, val, del, types.NewDelegatorStartingInfo(previousPeriod, shares, uint64(sdkCtx.BlockHeight())))
}

// calculateDelegationRewardsBetween returns the rewards accrued by a delegation
// across periods [startingPeriod, endingPeriod].
//
// The `shares` argument is the delegator's share-count snapshot stored in
// DelegatorStartingInfo at init time. The proto field is still named `Stake`
// (see initializeDelegation for the rationale), but in shares-based F1 it
// holds shares, not tokens-from-shares.
func (k Keeper) calculateDelegationRewardsBetween(ctx context.Context, val stakingtypes.ValidatorI,
	startingPeriod, endingPeriod uint64, shares math.LegacyDec,
) (sdk.DecCoins, error) {
	// sanity check
	if startingPeriod > endingPeriod {
		panic("startingPeriod cannot be greater than endingPeriod")
	}

	// sanity check
	if shares.IsNegative() {
		panic("shares should not be negative")
	}

	valBz, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(val.GetOperator())
	if err != nil {
		panic(err)
	}

	// rewards = (ratio_at_ending - ratio_at_starting) × delegator_shares
	starting, err := k.GetValidatorHistoricalRewards(ctx, valBz, startingPeriod)
	if err != nil {
		return sdk.DecCoins{}, err
	}

	ending, err := k.GetValidatorHistoricalRewards(ctx, valBz, endingPeriod)
	if err != nil {
		return sdk.DecCoins{}, err
	}

	difference := ending.CumulativeRewardRatio.Sub(starting.CumulativeRewardRatio)
	if difference.IsAnyNegative() {
		panic("negative rewards should not be possible")
	}
	// note: necessary to truncate so we don't allow withdrawing more rewards than owed
	rewards := difference.MulDecTruncate(shares)
	return rewards, nil
}

// calculate the total rewards accrued by a delegation
func (k Keeper) CalculateDelegationRewards(ctx context.Context, val stakingtypes.ValidatorI, del stakingtypes.DelegationI, endingPeriod uint64) (rewards sdk.DecCoins, err error) {
	addrCodec := k.authKeeper.AddressCodec()
	delAddr, err := addrCodec.StringToBytes(del.GetDelegatorAddr())
	if err != nil {
		return sdk.DecCoins{}, err
	}

	valAddr, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(del.GetValidatorAddr())
	if err != nil {
		return sdk.DecCoins{}, err
	}

	// fetch starting info for delegation
	startingInfo, err := k.GetDelegatorStartingInfo(ctx, sdk.ValAddress(valAddr), sdk.AccAddress(delAddr))
	if err != nil {
		return rewards, err
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if startingInfo.Height == uint64(sdkCtx.BlockHeight()) {
		// started this height, no rewards yet
		return rewards, err
	}

	startingPeriod := startingInfo.PreviousPeriod
	// The DelegatorStartingInfo proto field is named `Stake` but, under
	// shares-based F1, holds the delegator's share count at init time.
	startingShares := startingInfo.Stake

	// Shares only change when the delegator delegates, undelegates, or
	// redelegates — and any of those goes through BeforeDelegationSharesModified,
	// which withdraws rewards and re-runs initializeDelegation, refreshing
	// startingShares. So slashing and auto-staking, which mutate
	// validator.Tokens but never validator.DelegatorShares, leave
	// startingShares aligned with del.GetShares() between modifications.
	currentShares := del.GetShares()
	if startingShares.GT(currentShares) {
		// If startingShares exceeds current shares, the delegator's shares
		// shrank without BeforeDelegationSharesModified firing — a bug.
		panic(fmt.Sprintf("stored starting shares for delegator %s exceed current shares"+
			"\n\tstored:\t%s"+
			"\n\tcurrent:\t%s",
			del.GetDelegatorAddr(), startingShares, currentShares))
	}

	// calculate rewards for final period
	delRewards, err := k.calculateDelegationRewardsBetween(ctx, val, startingPeriod, endingPeriod, startingShares)
	if err != nil {
		return sdk.DecCoins{}, err
	}

	rewards = rewards.Add(delRewards...)
	return rewards, nil
}

func (k Keeper) withdrawDelegationRewards(ctx context.Context, val stakingtypes.ValidatorI, del stakingtypes.DelegationI) (sdk.Coins, error) {
	addrCodec := k.authKeeper.AddressCodec()
	delAddr, err := addrCodec.StringToBytes(del.GetDelegatorAddr())
	if err != nil {
		return nil, err
	}

	valAddr, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(del.GetValidatorAddr())
	if err != nil {
		return nil, err
	}

	// check existence of delegator starting info
	hasInfo, err := k.HasDelegatorStartingInfo(ctx, sdk.ValAddress(valAddr), sdk.AccAddress(delAddr))
	if err != nil {
		return nil, err
	}

	if !hasInfo {
		return nil, types.ErrEmptyDelegationDistInfo
	}

	// end current period and calculate rewards
	endingPeriod, err := k.IncrementValidatorPeriod(ctx, val)
	if err != nil {
		return nil, err
	}

	rewardsRaw, err := k.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	if err != nil {
		return nil, err
	}

	outstanding, err := k.GetValidatorOutstandingRewardsCoins(ctx, sdk.ValAddress(valAddr))
	if err != nil {
		return nil, err
	}

	// defensive edge case may happen on the very final digits
	// of the decCoins due to operation order of the distribution mechanism.
	rewards := rewardsRaw.Intersect(outstanding)
	if !rewards.Equal(rewardsRaw) {
		logger := k.Logger(ctx)
		logger.Info(
			"rounding error withdrawing rewards from validator",
			"delegator", del.GetDelegatorAddr(),
			"validator", val.GetOperator(),
			"got", rewards.String(),
			"expected", rewardsRaw.String(),
		)
	}

	// truncate reward dec coins, return remainder to community pool
	finalRewards, remainder := rewards.TruncateDecimal()

	// add coins to user account
	if !finalRewards.IsZero() {
		withdrawAddr, err := k.GetDelegatorWithdrawAddr(ctx, delAddr)
		if err != nil {
			return nil, err
		}

		err = k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, withdrawAddr, finalRewards)
		if err != nil {
			return nil, err
		}
	}

	// update the outstanding rewards and the community pool only if the
	// transaction was successful
	err = k.SetValidatorOutstandingRewards(ctx, sdk.ValAddress(valAddr), types.ValidatorOutstandingRewards{Rewards: outstanding.Sub(rewards)})
	if err != nil {
		return nil, err
	}

	feePool, err := k.FeePool.Get(ctx)
	if err != nil {
		return nil, err
	}

	feePool.CommunityPool = feePool.CommunityPool.Add(remainder...)
	err = k.FeePool.Set(ctx, feePool)
	if err != nil {
		return nil, err
	}

	// decrement reference count of starting period
	startingInfo, err := k.GetDelegatorStartingInfo(ctx, sdk.ValAddress(valAddr), sdk.AccAddress(delAddr))
	if err != nil {
		return nil, err
	}

	startingPeriod := startingInfo.PreviousPeriod
	err = k.decrementReferenceCount(ctx, sdk.ValAddress(valAddr), startingPeriod)
	if err != nil {
		return nil, err
	}

	// remove delegator starting info
	err = k.DeleteDelegatorStartingInfo(ctx, sdk.ValAddress(valAddr), sdk.AccAddress(delAddr))
	if err != nil {
		return nil, err
	}

	if finalRewards.IsZero() {
		baseDenom, _ := sdk.GetBaseDenom()
		if baseDenom == "" {
			baseDenom = sdk.DefaultBondDenom
		}

		// Note, we do not call the NewCoins constructor as we do not want the zero
		// coin removed.
		finalRewards = sdk.Coins{sdk.NewCoin(baseDenom, math.ZeroInt())}
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeWithdrawRewards,
			sdk.NewAttribute(sdk.AttributeKeyAmount, finalRewards.String()),
			sdk.NewAttribute(types.AttributeKeyValidator, val.GetOperator()),
			sdk.NewAttribute(types.AttributeKeyDelegator, del.GetDelegatorAddr()),
		),
	)

	return finalRewards, nil
}
