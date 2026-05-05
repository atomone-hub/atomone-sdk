package keeper

import (
	"context"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// AllocateTokens performs reward and fee distribution to all validators based
// on the F1 fee distribution and Nakamoto bonus specifications.
func (k Keeper) AllocateTokens(ctx context.Context, totalPreviousPower int64, bondedVotes []abci.VoteInfo) error {
	// fetch and clear the collected fees for distribution, since this is
	// called in BeginBlock, collected fees will be from the previous block
	// (and distributed to the previous proposer)
	feeCollector := k.authKeeper.GetModuleAccount(ctx, k.feeCollectorName)
	feesCollectedInt := k.bankKeeper.GetAllBalances(ctx, feeCollector.GetAddress())
	feesCollected := sdk.NewDecCoinsFromCoins(feesCollectedInt...)

	// transfer collected fees to the distribution module account
	err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, k.feeCollectorName, types.ModuleName, feesCollectedInt)
	if err != nil {
		return err
	}

	// temporary workaround to keep CanWithdrawInvariant happy
	// general discussions here: https://github.com/cosmos/cosmos-sdk/issues/2906#issuecomment-441867634
	feePool, err := k.FeePool.Get(ctx)
	if err != nil {
		return err
	}

	if totalPreviousPower == 0 {
		feePool.CommunityPool = feePool.CommunityPool.Add(feesCollected...)
		return k.FeePool.Set(ctx, feePool)
	}

	// Get community tax and Nakamoto bonus ratio η
	communityTax, err := k.GetCommunityTax(ctx)
	if err != nil {
		return err
	}

	nb, err := k.GetNakamotoBonus(ctx)
	if err != nil {
		return err
	}

	nakamotoCoefficient, err := k.GetNakamotoBonusCoefficient(ctx)
	if err != nil {
		return err
	}

	// Compute total validator rewards (after community tax)
	voteMultiplier := math.LegacyOneDec().Sub(communityTax)
	validatorTotalReward := feesCollected.MulDecTruncate(voteMultiplier)
	nbPerValidator := sdk.NewDecCoins()

	if nb.Enabled {
		// Split reward into Proportional (PR_i) and Nakamoto Bonus (NB_i)
		nakamotoBonus := validatorTotalReward.MulDecTruncate(nakamotoCoefficient)
		validatorTotalReward = validatorTotalReward.Sub(nakamotoBonus)

		// Compute per-validator fixed Nakamoto bonus
		numValidators := int64(len(bondedVotes))
		if numValidators > 0 && !nakamotoBonus.IsZero() {
			// Distribute Nakamoto bonus across all denominations
			for _, coin := range nakamotoBonus {
				amount := coin.Amount.QuoTruncate(math.LegacyNewDec(numValidators))
				nbPerValidator = nbPerValidator.Add(sdk.NewDecCoinFromDec(coin.Denom, amount))
			}
		}
	}

	remaining := feesCollected

	// Distribute rewards to each validator
	for _, vote := range bondedVotes {
		validator, err := k.stakingKeeper.ValidatorByConsAddr(ctx, vote.Validator.Address)
		if err != nil {
			return err
		}

		// Compute proportional share based on voting power
		powerFraction := math.LegacyNewDec(vote.Validator.Power).QuoTruncate(math.LegacyNewDec(totalPreviousPower))
		proportional := validatorTotalReward.MulDecTruncate(powerFraction)

		// Add fixed Nakamoto bonus to proportional share
		reward := proportional.Add(nbPerValidator...)

		dust, err := k.AllocateTokensToValidator(ctx, validator, reward)
		if err != nil {
			return err
		}
		// Bond denom auto-staked portion was sent to the bonded pool and left the
		// distribution module. Add back the decimal truncation dust (which stayed)
		// so the community pool accounting remains correct.
		remaining = remaining.Sub(reward).Add(dust...)
	}

	// allocate community funding
	feePool.CommunityPool = feePool.CommunityPool.Add(remaining...)
	return k.FeePool.Set(ctx, feePool)
}

// AllocateTokensToValidator distributes a validator's reward allocation.
// Bond denom delegator rewards are auto-staked: tokens are sent directly to the
// bonded pool and validator.Tokens is increased (raising the per-share exchange
// rate). Commission and non-bond-denom rewards continue through the F1 mechanism
// and remain withdrawable. Returns the bond denom decimal truncation dust so the
// caller can route it to the community pool.
func (k Keeper) AllocateTokensToValidator(
	ctx context.Context,
	val stakingtypes.ValidatorI,
	tokens sdk.DecCoins,
) (bondDust sdk.DecCoins, err error) {
	// split tokens between validator and delegators according to commission
	commission := tokens.MulDec(val.GetCommission())
	shared := tokens.Sub(commission)

	valBz, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(val.GetOperator())
	if err != nil {
		return nil, err
	}

	bondDenom, err := k.stakingKeeper.BondDenom(ctx)
	if err != nil {
		return nil, err
	}

	// --- Auto-stake bond denom delegator rewards ---
	// Truncate to an integer so it can be transferred as sdk.Coins; the decimal
	// remainder is returned to the caller for community pool accounting.
	sharedBondDec := shared.AmountOf(bondDenom)
	sharedBondInt := sharedBondDec.TruncateInt()
	if dust := sharedBondDec.Sub(math.LegacyNewDecFromInt(sharedBondInt)); dust.IsPositive() {
		bondDust = sdk.NewDecCoins(sdk.NewDecCoinFromDec(bondDenom, dust))
	}

	if sharedBondInt.IsPositive() {
		// Transfer to bonded pool first (satisfies ModuleAccountInvariant), then update validator.Tokens.
		if err := k.bankKeeper.SendCoinsFromModuleToModule(
			ctx, types.ModuleName, stakingtypes.BondedPoolName,
			sdk.NewCoins(sdk.NewCoin(bondDenom, sharedBondInt)),
		); err != nil {
			return nil, err
		}
		if err := k.stakingKeeper.AddValidatorTokensOnly(ctx, valBz, sharedBondInt); err != nil {
			return nil, err
		}
	}

	// Exclude all bond denom from F1: integer went to bonded pool, dust goes to
	// community pool via the caller. sdk.NewDecCoins filters zero amounts automatically.
	sharedForF1 := shared.Sub(sdk.NewDecCoins(sdk.NewDecCoinFromDec(bondDenom, sharedBondDec)))

	// --- F1 path: commission (all denoms) + non-bond-denom delegator rewards ---
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeCommission,
			sdk.NewAttribute(sdk.AttributeKeyAmount, commission.String()),
			sdk.NewAttribute(types.AttributeKeyValidator, val.GetOperator()),
		),
	)

	currentCommission, err := k.GetValidatorAccumulatedCommission(ctx, valBz)
	if err != nil {
		return nil, err
	}
	currentCommission.Commission = currentCommission.Commission.Add(commission...)
	if err = k.SetValidatorAccumulatedCommission(ctx, valBz, currentCommission); err != nil {
		return nil, err
	}

	currentRewards, err := k.GetValidatorCurrentRewards(ctx, valBz)
	if err != nil {
		return nil, err
	}
	currentRewards.Rewards = currentRewards.Rewards.Add(sharedForF1...)
	if err = k.SetValidatorCurrentRewards(ctx, valBz, currentRewards); err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeRewards,
			sdk.NewAttribute(sdk.AttributeKeyAmount, tokens.String()),
			sdk.NewAttribute(types.AttributeKeyValidator, val.GetOperator()),
		),
	)

	outstanding, err := k.GetValidatorOutstandingRewards(ctx, valBz)
	if err != nil {
		return nil, err
	}
	// Outstanding tracks only what remains in the distribution module (F1 amounts).
	// The auto-staked bond denom portion was sent to the bonded pool, so exclude it.
	outstanding.Rewards = outstanding.Rewards.Add(commission...).Add(sharedForF1...)
	if err = k.SetValidatorOutstandingRewards(ctx, valBz, outstanding); err != nil {
		return nil, err
	}

	return bondDust, nil
}
