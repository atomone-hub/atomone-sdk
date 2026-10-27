package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/dynamicfee/testutil"
	"github.com/cosmos/cosmos-sdk/x/dynamicfee/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
)

// TestUpdateParamsRejectsOversizedWindow reproduces the dynamicfee halt via
// UpdateParams: an authority-signed params update with an oversized Window
// was persisted unvalidated and then drove an unbounded NewState allocation
// (window * 8 bytes) plus unmetered O(window) EndBlocker scans.
func TestUpdateParamsRejectsOversizedWindow(t *testing.T) {
	authority := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	msgServer, k, ctx := testutil.SetupMsgServer(t, 0)

	// Snapshot pre-update params and state.
	preParams, err := k.GetParams(ctx)
	require.NoError(t, err)
	preState, err := k.GetState(ctx)
	require.NoError(t, err)

	// A params set whose only invalidity is the Window magnitude.
	params := types.DefaultParams()
	params.Window = types.MaxWindow + 1

	_, err = msgServer.UpdateParams(ctx, &types.MsgUpdateParams{
		Authority: authority,
		Params:    params,
	})
	require.Error(t, err)

	// Nothing must have been persisted.
	postParams, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, preParams, postParams)
	postState, err := k.GetState(ctx)
	require.NoError(t, err)
	require.Equal(t, preState.Index, postState.Index)
	require.Equal(t, len(preState.Window), len(postState.Window))

	// Params.ValidateBasic must also reject the oversized window directly.
	require.Error(t, (&types.Params{Window: types.MaxWindow + 1}).ValidateBasic())

	// The boundary value is still accepted.
	params.Window = types.MaxWindow
	_, err = msgServer.UpdateParams(ctx, &types.MsgUpdateParams{
		Authority: authority,
		Params:    params,
	})
	require.NoError(t, err)
}
