package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/x/dynamicfee/types"
)

func TestGenesis(t *testing.T) {
	t.Run("can create a new default genesis state", func(t *testing.T) {
		gs := types.DefaultGenesisState()
		require.NoError(t, gs.ValidateBasic())
	})

	t.Run("can accept a valid genesis state for AIMD eip-1559", func(t *testing.T) {
		gs := types.DefaultAIMDGenesisState()
		require.NoError(t, gs.ValidateBasic())
	})

	t.Run("rejects params.window that does not match state.window length", func(t *testing.T) {
		gs := types.DefaultAIMDGenesisState()
		gs.Params.Window = 4 // state.Window still has 8 slots
		require.Error(t, gs.ValidateBasic())
	})

	t.Run("rejects state index out of range", func(t *testing.T) {
		gs := types.DefaultAIMDGenesisState()
		gs.State.Index = uint64(len(gs.State.Window))
		require.Error(t, gs.ValidateBasic())
	})
}
