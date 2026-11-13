package keeper

import (
	"fmt"
	"strconv"
	"time"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

// MintFn defines the function that needs to be implemented in order to customize the minting process.
type MintFn func(ctx sdk.Context, k *Keeper) error

// MintFn runs the mintFn of the keeper.
func (k *Keeper) MintFn(ctx sdk.Context) error {
	return k.mintFn(ctx, k)
}

// DefaultMintFn returns a default mint function.
// The default MintFn has a requirement on staking as it uses bond to calculate inflation.
// It mints the share of the annual provisions corresponding to the time elapsed since the
// previous block, capped at the max_block_elapsed parameter, and adjusts the inflation
// rate in proportion to the same elapsed time.
func DefaultMintFn(ic types.InflationCalculationFn) MintFn {
	return func(ctx sdk.Context, k *Keeper) error {
		// fetch stored minter & params
		minter, err := k.Minter.Get(ctx)
		if err != nil {
			return err
		}

		params, err := k.Params.Get(ctx)
		if err != nil {
			return err
		}
		if params.MaxBlockElapsed <= 0 {
			return fmt.Errorf("invalid max_block_elapsed parameter: %s", params.MaxBlockElapsed)
		}

		// recalculate inflation rate
		totalStakingSupply, err := k.StakingTokenSupply(ctx)
		if err != nil {
			return err
		}

		bondedRatio, err := k.BondedRatio(ctx)
		if err != nil {
			return err
		}

		now := ctx.BlockTime()
		if minter.PreviousBlockTime == nil {
			// No reference point yet: the first block after genesis or after the
			// upgrade that introduces the field. Record this block's time, refresh
			// the annual provisions so that queries stay meaningful, and mint
			// nothing.
			minter.PreviousBlockTime = &now
			minter.AnnualProvisions = minter.NextAnnualProvisions(params, totalStakingSupply)
			return k.Minter.Set(ctx, minter)
		}
		if now.Before(*minter.PreviousBlockTime) {
			// CometBFT requires block times to increase strictly, so this is a
			// broken invariant rather than a case to recover from.
			return fmt.Errorf("block time %s is before the previous block time %s", now.UTC(), minter.PreviousBlockTime.UTC())
		}

		elapsed := now.Sub(*minter.PreviousBlockTime)
		if elapsed > params.MaxBlockElapsed {
			elapsed = params.MaxBlockElapsed
		}

		minter.Inflation = ic(ctx, minter, params, bondedRatio, elapsed)
		minter.AnnualProvisions = minter.NextAnnualProvisions(params, totalStakingSupply)
		mintedCoin := minter.BlockProvision(params, elapsed)
		minter.PreviousBlockTime = &now
		if err = k.Minter.Set(ctx, minter); err != nil {
			return err
		}

		// mint coins, update supply
		mintedCoins := sdk.NewCoins(mintedCoin)

		err = k.MintCoins(ctx, mintedCoins)
		if err != nil {
			return err
		}

		// send the minted coins to the fee collector account
		err = k.AddCollectedFees(ctx, mintedCoins)
		if err != nil {
			return err
		}

		if mintedCoin.Amount.IsInt64() {
			defer telemetry.ModuleSetGauge(types.ModuleName, float32(mintedCoin.Amount.Int64()), "minted_tokens")
		}

		emitMintEvent(ctx, bondedRatio, minter, mintedCoin.Amount, elapsed)
		return nil
	}
}

// emitMintEvent emits the mint event of a block.
func emitMintEvent(ctx sdk.Context, bondedRatio math.LegacyDec, minter types.Minter, amount math.Int, elapsed time.Duration) {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeMint,
			sdk.NewAttribute(types.AttributeKeyBondedRatio, bondedRatio.String()),
			sdk.NewAttribute(types.AttributeKeyInflation, minter.Inflation.String()),
			sdk.NewAttribute(types.AttributeKeyAnnualProvisions, minter.AnnualProvisions.String()),
			sdk.NewAttribute(sdk.AttributeKeyAmount, amount.String()),
			sdk.NewAttribute(types.AttributeKeyElapsed, strconv.FormatInt(elapsed.Nanoseconds(), 10)),
		),
	)
}
