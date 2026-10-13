package keeper_test

import (
	"testing"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"cosmossdk.io/math"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/x/dynamicfee/testutil"
	"github.com/cosmos/cosmos-sdk/x/dynamicfee/types"
)

// TestUpdateDynamicfeeInt64OverflowMaxBlockGas is a regression test for a
// permanent chain halt: a governance proposal embedding MsgUpdateParams with
// DefaultMaxBlockGas = 2^63 and TargetBlockUtilization = 1 used to pass
// Params.ValidateBasic (which only rejected DefaultMaxBlockGas == 0) and
// commit, after which the dynamicfee EndBlocker panicked unrecovered on
// every block: GetTargetBlockGas converted the maxBlockGas *
// TargetBlockUtilization product with Int64(), which panics with
// "Int64() out of bound" once the product exceeds MaxInt64, and it is
// evaluated by GetNetGas for the EndBlocker's log call outside of any
// recover.
func TestUpdateDynamicfeeInt64OverflowMaxBlockGas(t *testing.T) {
	k, ctx := testutil.SetupKeeper(t, 0)

	// The exact param set used by the exploit: the only fields changed from
	// the defaults are the DefaultMaxBlockGas magnitude and the utilization,
	// both of which passed ValidateBasic before the fix.
	poisoned := types.DefaultParams()
	poisoned.TargetBlockUtilization = math.LegacyOneDec()
	poisoned.DefaultMaxBlockGas = 1 << 63

	// Governance gates proposals at submission with ValidateBasic; the
	// poisoned set must be rejected there.
	msg := &types.MsgUpdateParams{Authority: k.GetAuthority(), Params: poisoned}
	require.Error(t, msg.ValidateBasic())

	// Even if the poisoned params were already committed (only possible on
	// a chain attacked before this fix shipped), the EndBlocker must run to
	// completion. Use the default consensus configuration
	// (Block.MaxGas = -1) so GetMaxBlockGas falls back to
	// DefaultMaxBlockGas.
	ctx = ctx.WithConsensusParams(tmproto.ConsensusParams{Block: &tmproto.BlockParams{MaxGas: -1}})
	require.NoError(t, k.SetParams(ctx, poisoned))
	require.NoError(t, k.SetState(ctx, types.NewState(poisoned.Window, poisoned.MinBaseGasPrice, poisoned.MinLearningRate)))

	require.NotPanics(t, func() {
		require.NoError(t, k.UpdateDynamicfee(ctx))
	})
}
