package v5

// This file is a snapshot of the OLD tokens-based F1 reward-calculation logic
// from before the v4->v5 upgrade. It is used only during migration to compute
// the rewards a delegation has accrued under the pre-upgrade scheme, so they
// can be paid out cleanly before F1 state is reset to a shares-based, clean
// slate.
//
// The post-migration keeper does NOT contain this logic —
// calculateDelegationRewardsBetween in keeper/delegation.go now uses
// shares-based math and no longer iterates slash events. We deliberately
// duplicate the old algorithm here so the migration can run with the new
// binary while still producing numerically identical results to what a
// delegator would have received via WithdrawDelegationRewards under the
// previous binary.

import (
	"context"

	storetypes "cosmossdk.io/core/store"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	dstrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
)

// legacyCalculateDelegationRewards reproduces the pre-upgrade behaviour of
// keeper.CalculateDelegationRewards: it walks the validator's slash events in
// [startingHeight, endingHeight) and, at each event's period boundary, splits
// the reward computation and scales the delegator's stake by (1 - fraction).
// The `stake` argument is the OLD-semantic value (tokens-from-shares snapshot
// stored in DelegatorStartingInfo.Stake).
func legacyCalculateDelegationRewards(
	ctx context.Context,
	m Migrator,
	storeService storetypes.KVStoreService,
	cdc codec.BinaryCodec,
	valAddr sdk.ValAddress,
	startingPeriod, endingPeriod uint64,
	stake math.LegacyDec,
	startingHeight uint64,
) (sdk.DecCoins, error) {
	rewards := sdk.DecCoins{}

	endingHeight := uint64(sdk.UnwrapSDKContext(ctx).BlockHeight())
	if endingHeight > startingHeight {
		var iterErr error
		iterateLegacySlashEvents(ctx, storeService, cdc, valAddr, startingHeight, endingHeight,
			func(_ uint64, event dstrtypes.ValidatorSlashEvent) (stop bool) {
				eventPeriod := event.ValidatorPeriod
				if eventPeriod > startingPeriod {
					partial, err := legacyRewardsBetween(ctx, m, valAddr, startingPeriod, eventPeriod, stake)
					if err != nil {
						iterErr = err
						return true
					}
					rewards = rewards.Add(partial...)

					// Note: it is necessary to truncate so we don't allow withdrawing
					// more rewards than owed. Mirrors the original behaviour exactly.
					stake = stake.MulTruncate(math.LegacyOneDec().Sub(event.Fraction))
					startingPeriod = eventPeriod
				}
				return false
			},
		)
		if iterErr != nil {
			return nil, iterErr
		}
	}

	final, err := legacyRewardsBetween(ctx, m, valAddr, startingPeriod, endingPeriod, stake)
	if err != nil {
		return nil, err
	}
	return rewards.Add(final...), nil
}

// legacyRewardsBetween implements the per-segment formula
// `(cumRatio[ending] - cumRatio[starting]) x stake`, truncated, exactly as
// the pre-upgrade keeper.calculateDelegationRewardsBetween did.
func legacyRewardsBetween(
	ctx context.Context,
	m Migrator,
	valAddr sdk.ValAddress,
	startingPeriod, endingPeriod uint64,
	stake math.LegacyDec,
) (sdk.DecCoins, error) {
	if startingPeriod > endingPeriod {
		// defensive — should not happen for well-formed migration input
		return sdk.DecCoins{}, nil
	}
	starting, err := m.GetValidatorHistoricalRewards(ctx, valAddr, startingPeriod)
	if err != nil {
		return nil, err
	}
	ending, err := m.GetValidatorHistoricalRewards(ctx, valAddr, endingPeriod)
	if err != nil {
		return nil, err
	}
	difference := ending.CumulativeRewardRatio.Sub(starting.CumulativeRewardRatio)
	if difference.IsAnyNegative() {
		// Can't happen under correct accounting but guarding migration anyway.
		return sdk.DecCoins{}, nil
	}
	return difference.MulDecTruncate(stake), nil
}

// legacyIncrementValidatorPeriod replicates the OLD tokens-based period bump:
// it computes ratio = currentRewards / val.GetTokens() and writes
// historical[currentPeriod] = previousCumRatio + ratio. This is necessary
// during the migration so that pending rewards in ValidatorCurrentRewards
// roll into a final cumulative ratio that legacyCalculateDelegationRewards
// can then read.
//
// The function takes the validator's current Tokens directly so callers can
// pass the value before any auto-staking has happened.
func legacyIncrementValidatorPeriod(
	ctx context.Context,
	m Migrator,
	valAddr sdk.ValAddress,
	valTokens math.Int,
) (uint64, error) {
	rewards, err := m.GetValidatorCurrentRewards(ctx, valAddr)
	if err != nil {
		return 0, err
	}

	var current sdk.DecCoins
	if !valTokens.IsZero() {
		current = rewards.Rewards.QuoDecTruncate(math.LegacyNewDecFromInt(valTokens))
	}

	historical, err := m.GetValidatorHistoricalRewards(ctx, valAddr, rewards.Period-1)
	if err != nil {
		return 0, err
	}

	newRatio := historical.CumulativeRewardRatio.Add(current...)
	if err := m.SetValidatorHistoricalRewards(ctx, valAddr, rewards.Period,
		dstrtypes.NewValidatorHistoricalRewards(newRatio, 1)); err != nil {
		return 0, err
	}

	if err := m.SetValidatorCurrentRewards(ctx, valAddr,
		dstrtypes.NewValidatorCurrentRewards(sdk.DecCoins{}, rewards.Period+1)); err != nil {
		return 0, err
	}

	return rewards.Period, nil
}

// iterateLegacySlashEvents walks the validator's slash events stored under the
// pre-upgrade ValidatorSlashEvent prefix, in heights [startingHeight,
// endingHeight). It is used solely by legacyCalculateDelegationRewards above
// to reproduce the pre-upgrade reward computation; the post-upgrade keeper
// neither writes new entries via this layout nor reads them from it.
func iterateLegacySlashEvents(
	ctx context.Context,
	storeService storetypes.KVStoreService,
	cdc codec.BinaryCodec,
	val sdk.ValAddress,
	startingHeight, endingHeight uint64,
	fn func(height uint64, event dstrtypes.ValidatorSlashEvent) (stop bool),
) {
	store := runtime.KVStoreAdapter(storeService.OpenKVStore(ctx))
	iter := store.Iterator(
		dstrtypes.GetValidatorSlashEventKeyPrefix(val, startingHeight),
		dstrtypes.GetValidatorSlashEventKeyPrefix(val, endingHeight+1),
	)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var event dstrtypes.ValidatorSlashEvent
		cdc.MustUnmarshal(iter.Value(), &event)
		_, height := dstrtypes.GetValidatorSlashEventAddressHeight(iter.Key())
		if fn(height, event) {
			break
		}
	}
}
