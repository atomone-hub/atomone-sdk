package keeper

import (
	"context"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// AutoStakeValidatorCommission moves the bond denom portion of a validator's
// accumulated commission into the operator's self-delegation by routing it
// through the standard staking Delegate path. The integer amount is sent
// from the distribution module to the operator's account, then immediately
// re-delegated via stakingKeeper.Delegate (which fires
// BeforeDelegationSharesModified / AfterDelegationModified hooks normally,
// keeping shares-based F1 accounting consistent with the standard hook
// flow). The truncation dust is swept to the community pool. Non-bond
// denominations remain in accumulatedCommission for the operator to claim
// manually via MsgWithdrawValidatorCommission.
//
// Two callers invoke this helper:
//
//   - MsgWithdrawValidatorCommission, before the standard non-bond payout,
//     so that any operator-initiated commission claim auto-stakes the bond
//     denom portion. Operators retain control of timing.
//
//   - The AfterEpochEnd hook handler at the configured commission
//     auto-stake epoch identifier, iterating the bonded validator set so
//     bond denom commission compounds at a predictable cadence even when
//     operators don't manually withdraw.
//
// Returns the integer amount auto-staked. A zero amount indicates there
// was no integer bond denom to auto-stake (the validator may still have
// non-bond commission and/or sub-integer bond denom dust accumulated).
func (k Keeper) AutoStakeValidatorCommission(ctx context.Context, valAddr sdk.ValAddress) (sdk.Coin, error) {
	bondDenom, err := k.stakingKeeper.BondDenom(ctx)
	if err != nil {
		return sdk.Coin{}, err
	}

	accumCommission, err := k.GetValidatorAccumulatedCommission(ctx, valAddr)
	if err != nil {
		return sdk.Coin{}, err
	}

	bondDenomDec := accumCommission.Commission.AmountOf(bondDenom)
	if !bondDenomDec.IsPositive() {
		return sdk.NewCoin(bondDenom, math.ZeroInt()), nil
	}

	bondDenomInt := bondDenomDec.TruncateInt()
	dust := bondDenomDec.Sub(math.LegacyNewDecFromInt(bondDenomInt))

	// Remove the bond denom portion (integer + dust) from accumulatedCommission.
	// sdk.NewDecCoins filters zero amounts so non-bond commission is preserved.
	bondPortion := sdk.NewDecCoins(sdk.NewDecCoinFromDec(bondDenom, bondDenomDec))
	newCommission := accumCommission.Commission.Sub(bondPortion)
	if err := k.SetValidatorAccumulatedCommission(ctx, valAddr,
		types.ValidatorAccumulatedCommission{Commission: newCommission}); err != nil {
		return sdk.Coin{}, err
	}

	// Outstanding tracks total module-balance claims; subtract the entire
	// bond denom portion (integer goes to bonded pool via Delegate; dust
	// goes to the community pool, also leaving outstanding's accounting).
	outstanding, err := k.GetValidatorOutstandingRewards(ctx, valAddr)
	if err != nil {
		return sdk.Coin{}, err
	}
	outstanding.Rewards = outstanding.Rewards.Sub(bondPortion)
	if err := k.SetValidatorOutstandingRewards(ctx, valAddr, outstanding); err != nil {
		return sdk.Coin{}, err
	}

	if dust.IsPositive() {
		feePool, err := k.FeePool.Get(ctx)
		if err != nil {
			return sdk.Coin{}, err
		}
		feePool.CommunityPool = feePool.CommunityPool.Add(sdk.NewDecCoinFromDec(bondDenom, dust))
		if err := k.FeePool.Set(ctx, feePool); err != nil {
			return sdk.Coin{}, err
		}
	}

	if !bondDenomInt.IsPositive() {
		// Only sub-integer dust to handle; nothing to delegate.
		return sdk.NewCoin(bondDenom, math.ZeroInt()), nil
	}

	// Move the integer bond denom from the distribution module to the
	// operator's account so the standard Delegate path can pull it via
	// subtractAccount=true. Distribution → operator → bonded pool in two
	// hops, but the alternative (custom module-to-pool primitive +
	// hand-rolled hook firing) is what we explicitly want to avoid.
	operatorAcc := sdk.AccAddress(valAddr)
	bondCoins := sdk.NewCoins(sdk.NewCoin(bondDenom, bondDenomInt))
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, operatorAcc, bondCoins); err != nil {
		return sdk.Coin{}, err
	}

	validator, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return sdk.Coin{}, err
	}
	if _, err := k.stakingKeeper.Delegate(ctx, operatorAcc, bondDenomInt,
		stakingtypes.Unbonded, validator, true /* subtractAccount */); err != nil {
		return sdk.Coin{}, err
	}

	staked := sdk.NewCoin(bondDenom, bondDenomInt)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeAutoStakeCommission,
			sdk.NewAttribute(sdk.AttributeKeyAmount, staked.String()),
			sdk.NewAttribute(types.AttributeKeyValidator, valAddr.String()),
		),
	)

	return staked, nil
}

// AutoStakeBondedValidatorsCommission iterates the current bonded
// validator set and runs AutoStakeValidatorCommission for each. Called
// by the AfterEpochEnd hook handler when the configured commission
// auto-stake epoch identifier fires.
//
// Validators that are unbonding, unbonded, or jailed are not in the
// bonded set and are therefore skipped. They no longer accrue commission
// (AllocateTokens only iterates bondedVotes), so any commission they
// earned during their bonded period sits in accumulatedCommission until
// they are removed (at which point the AfterValidatorRemoved hook
// handles the residual via the existing force-payout). The bound on
// what can be liquidated through that escape route is one epoch's worth
// of commission per validator lifetime.
func (k Keeper) AutoStakeBondedValidatorsCommission(ctx context.Context) error {
	validators, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return err
	}
	for _, val := range validators {
		valAddr, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(val.GetOperator())
		if err != nil {
			return err
		}
		if _, err := k.AutoStakeValidatorCommission(ctx, valAddr); err != nil {
			return err
		}
	}
	return nil
}
