package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

// InitGenesis new mint genesis
func (keeper Keeper) InitGenesis(ctx sdk.Context, ak types.AccountKeeper, data *types.GenesisState) {
	if err := types.ValidateGenesis(*data); err != nil {
		panic(err)
	}

	// previous_block_time only has meaning against the clock of the chain that
	// recorded it, so a chain starting from a genesis has no reference point:
	// its first block records one and mints nothing.
	minter := data.Minter
	minter.PreviousBlockTime = nil
	if err := keeper.Minter.Set(ctx, minter); err != nil {
		panic(err)
	}

	if err := keeper.Params.Set(ctx, data.Params); err != nil {
		panic(err)
	}

	ak.GetModuleAccount(ctx, types.ModuleName)
}

// ExportGenesis returns a GenesisState for a given context and keeper.
func (keeper Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	minter, err := keeper.Minter.Get(ctx)
	if err != nil {
		panic(err)
	}

	// See InitGenesis: the reference point is not carried across an export.
	minter.PreviousBlockTime = nil

	params, err := keeper.Params.Get(ctx)
	if err != nil {
		panic(err)
	}

	return types.NewGenesisState(minter, params)
}
