package simulation_test

import (
	"math/rand"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"gotest.tools/v3/assert"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/address"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/cosmos/cosmos-sdk/x/mint/simulation"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

func TestProposalMsgs(t *testing.T) {
	// initialize parameters
	s := rand.NewSource(1)
	r := rand.New(s)

	ctx := sdk.NewContext(nil, cmtproto.Header{}, true, nil)
	accounts := simtypes.RandomAccounts(r, 3)

	// execute ProposalMsgs function
	weightedProposalMsgs := simulation.ProposalMsgs()
	assert.Assert(t, len(weightedProposalMsgs) == 1)

	w0 := weightedProposalMsgs[0]

	// tests w0 interface:
	assert.Equal(t, simulation.OpWeightMsgUpdateParams, w0.AppParamsKey())
	assert.Equal(t, simulation.DefaultWeightMsgUpdateParams, w0.DefaultWeight())

	msg := w0.MsgSimulatorFn()(r, ctx, accounts)
	msgUpdateParams, ok := msg.(*types.MsgUpdateParams)
	assert.Assert(t, ok)

	assert.Equal(t, sdk.AccAddress(address.Module("gov")).String(), msgUpdateParams.Authority)
	assert.DeepEqual(t, sdkmath.LegacyNewDecWithPrec(18, 2), msgUpdateParams.Params.GoalBonded)
	assert.DeepEqual(t, sdkmath.LegacyNewDecWithPrec(50, 2), msgUpdateParams.Params.InflationMax)
	assert.DeepEqual(t, sdkmath.LegacyNewDecWithPrec(12, 2), msgUpdateParams.Params.InflationMin)
	assert.DeepEqual(t, sdkmath.LegacyNewDecWithPrec(60, 2), msgUpdateParams.Params.InflationRateChange)
	assert.Equal(t, "xKGLwQvuyN", msgUpdateParams.Params.MintDenom)
	assert.Equal(t, time.Second, msgUpdateParams.Params.MaxBlockElapsed)
}
