package v5

import (
	"context"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	dstrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// bankKeeper defines the subset of bank keeper methods needed for migration.
type bankKeeper interface {
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins) error
}

// stakingKeeper defines the subset of staking keeper methods needed for migration.
type stakingKeeper interface {
	BondDenom(ctx context.Context) (string, error)
	AddValidatorTokensOnly(ctx context.Context, valAddr sdk.ValAddress, tokensToAdd math.Int) error
}

// distributionKeeper defines the distribution keeper methods needed for migration.
type distributionKeeper interface {
	IterateValidatorCurrentRewards(ctx context.Context, fn func(sdk.ValAddress, dstrtypes.ValidatorCurrentRewards) bool)
	SetValidatorCurrentRewards(ctx context.Context, val sdk.ValAddress, rewards dstrtypes.ValidatorCurrentRewards) error
	GetValidatorOutstandingRewards(ctx context.Context, val sdk.ValAddress) (dstrtypes.ValidatorOutstandingRewards, error)
	SetValidatorOutstandingRewards(ctx context.Context, val sdk.ValAddress, rewards dstrtypes.ValidatorOutstandingRewards) error
}

// MigrateStore migrates x/distribution from consensus version 4 to 5.
// It flushes accumulated bond denom delegator rewards from ValidatorCurrentRewards
// into the bonded pool, updating each validator's Tokens to match. Commission
// and non-bond-denom rewards are left untouched and remain claimable. Decimal
// truncation dust is credited to the community pool.
func MigrateStore(
	ctx sdk.Context,
	dk distributionKeeper,
	feePool collections.Item[dstrtypes.FeePool],
	bk bankKeeper,
	sk stakingKeeper,
) error {
	bondDenom, err := sk.BondDenom(ctx)
	if err != nil {
		return err
	}

	fp, err := feePool.Get(ctx)
	if err != nil {
		return err
	}

	var migErr error
	dk.IterateValidatorCurrentRewards(ctx, func(valAddr sdk.ValAddress, rewards dstrtypes.ValidatorCurrentRewards) bool {
		bondDec := rewards.Rewards.AmountOf(bondDenom)
		if !bondDec.IsPositive() {
			return false
		}

		bondInt := bondDec.TruncateInt()
		if dust := bondDec.Sub(math.LegacyNewDecFromInt(bondInt)); dust.IsPositive() {
			fp.CommunityPool = fp.CommunityPool.Add(sdk.NewDecCoinFromDec(bondDenom, dust))
		}

		if bondInt.IsPositive() {
			if err := bk.SendCoinsFromModuleToModule(
				ctx, dstrtypes.ModuleName, stakingtypes.BondedPoolName,
				sdk.NewCoins(sdk.NewCoin(bondDenom, bondInt)),
			); err != nil {
				migErr = err
				return true
			}
			if err := sk.AddValidatorTokensOnly(ctx, valAddr, bondInt); err != nil {
				migErr = err
				return true
			}
		}

		// Remove bond denom from current rewards, keep non-bond amounts.
		rewards.Rewards = rewards.Rewards.Sub(sdk.NewDecCoins(sdk.NewDecCoinFromDec(bondDenom, bondDec)))
		if err := dk.SetValidatorCurrentRewards(ctx, valAddr, rewards); err != nil {
			migErr = err
			return true
		}

		// Sync outstanding rewards: subtract the bond denom that left the module.
		outstanding, err := dk.GetValidatorOutstandingRewards(ctx, valAddr)
		if err != nil {
			migErr = err
			return true
		}
		outstanding.Rewards = outstanding.Rewards.Sub(sdk.NewDecCoins(sdk.NewDecCoinFromDec(bondDenom, bondDec)))
		if err := dk.SetValidatorOutstandingRewards(ctx, valAddr, outstanding); err != nil {
			migErr = err
			return true
		}

		return false
	})
	if migErr != nil {
		return migErr
	}

	return feePool.Set(ctx, fp)
}
