package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/x/dynamicfee/testutil"
	"github.com/cosmos/cosmos-sdk/x/dynamicfee/types"
)

func TestInitGenesis(t *testing.T) {
	k, ctx := testutil.SetupKeeper(t, 0)
	t.Run("default genesis should not panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			k.InitGenesis(ctx, *types.DefaultGenesisState())
		})
	})

	t.Run("default AIMD genesis should not panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			k.InitGenesis(ctx, *types.DefaultAIMDGenesisState())
		})
	})

	t.Run("bad genesis state should panic", func(t *testing.T) {
		gs := types.DefaultGenesisState()
		gs.Params.Window = 0
		require.Panics(t, func() {
			k.InitGenesis(ctx, *gs)
		})
	})

	t.Run("mismatch in params and state for window should panic", func(t *testing.T) {
		gs := types.DefaultAIMDGenesisState()
		gs.Params.Window = 1

		require.Panics(t, func() {
			k.InitGenesis(ctx, *gs)
		})
	})

	t.Run("state index out of range should panic", func(t *testing.T) {
		gs := types.DefaultAIMDGenesisState()
		gs.State.Index = uint64(len(gs.State.Window))

		require.Panics(t, func() {
			k.InitGenesis(ctx, *gs)
		})
	})
}

func TestExportGenesis(t *testing.T) {
	k, ctx := testutil.SetupKeeper(t, 0)
	t.Run("export genesis should not panic for default eip-1559", func(t *testing.T) {
		gs := types.DefaultGenesisState()
		k.InitGenesis(ctx, *gs)

		var exportedGenesis *types.GenesisState
		require.NotPanics(t, func() {
			exportedGenesis = k.ExportGenesis(ctx)
		})

		require.Equal(t, gs, exportedGenesis)
	})

	t.Run("export genesis should not panic for default AIMD eip-1559", func(t *testing.T) {
		gs := types.DefaultAIMDGenesisState()
		k.InitGenesis(ctx, *gs)

		var exportedGenesis *types.GenesisState
		require.NotPanics(t, func() {
			exportedGenesis = k.ExportGenesis(ctx)
		})

		require.Equal(t, gs, exportedGenesis)
	})
}
