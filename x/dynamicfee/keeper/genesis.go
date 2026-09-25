package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/dynamicfee/types"
)

// InitGenesis initializes the dynamicfee module's state from a given genesis state.
func (k *Keeper) InitGenesis(ctx sdk.Context, gs types.GenesisState) {
	if err := gs.ValidateBasic(); err != nil {
		panic(err)
	}

	// Initialize the dynamic fee pricing state and parameters.
	if err := k.SetParams(ctx, gs.Params); err != nil {
		panic(err)
	}

	if err := k.SetState(ctx, gs.State); err != nil {
		panic(err)
	}

	// always init enabled height to -1 until it is explicitly set later in the application
	k.SetEnabledHeight(ctx, -1)
}

// ExportGenesis returns a GenesisState for a given context.
func (k *Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	// Get the dynamicfee module's parameters.
	params, err := k.GetParams(ctx)
	if err != nil {
		panic(err)
	}

	// Get the dynamicfee module's state.
	state, err := k.GetState(ctx)
	if err != nil {
		panic(err)
	}

	return types.NewGenesisState(params, state)
}
