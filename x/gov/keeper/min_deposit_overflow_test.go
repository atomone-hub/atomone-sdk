package keeper_test

import (
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkgov "github.com/cosmos/cosmos-sdk/x/gov"
	"github.com/cosmos/cosmos-sdk/x/gov/keeper"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// hostileThrottlerParams returns params with a uint64-max throttler target and
// a 1e58 floor: individually accepted by the pre-fix Params.ValidateBasic, and
// enough to overflow LegacyDec.Mul in GetNewMinDeposit from the EndBlocker's
// decrease branch (percChange ~ -1.9e19 times a 1e58 stored deposit).
func hostileThrottlerParams() (v1.Params, sdk.Coins) {
	floor := sdk.NewCoins(sdk.NewCoin("stake", math.NewIntFromBigInt(
		new(big.Int).Exp(big.NewInt(10), big.NewInt(58), nil))))
	updatePeriod := time.Second
	params := v1.DefaultParams()
	params.MinDepositThrottler = &v1.MinDepositThrottler{
		FloorValue:                        floor,
		UpdatePeriod:                      &updatePeriod,
		TargetActiveProposals:             ^uint64(0),
		DecreaseSensitivityTargetDistance: 1,
		IncreaseRatio:                     "0.050000000000000000",
		DecreaseRatio:                     "0.999999999999999999",
	}
	params.MinInitialDepositThrottler = &v1.MinInitialDepositThrottler{
		FloorValue:                        floor,
		UpdatePeriod:                      &updatePeriod,
		TargetProposals:                   ^uint64(0),
		DecreaseSensitivityTargetDistance: 1,
		IncreaseRatio:                     "0.050000000000000000",
		DecreaseRatio:                     "0.999999999999999999",
	}
	return params, floor
}

// TestUpdateParamsRejectsOversizedThrottlerFloors checks that the params-update
// path (the handler a passed governance proposal executes) rejects the hostile
// throttler params instead of storing them.
func TestUpdateParamsRejectsOversizedThrottlerFloors(t *testing.T) {
	k, _, _, _, _, _, ctx := setupGovKeeper(t)
	params, _ := hostileThrottlerParams()

	require.ErrorContains(t, params.ValidateBasic(), "minimum deposit floor amount too large")

	_, err := keeper.NewMsgServerImpl(k).UpdateParams(ctx, &v1.MsgUpdateParams{
		Authority: govAcct.String(),
		Params:    params,
	})
	require.ErrorContains(t, err, "minimum deposit floor amount too large")
}

// TestEndBlockerSurvivesPoisonedThrottlerState simulates a chain whose state
// was poisoned before the floor cap existed (params stored by an older binary).
// The EndBlocker must not panic on them.
func TestEndBlockerSurvivesPoisonedThrottlerState(t *testing.T) {
	k, _, _, _, _, _, ctx := setupGovKeeper(t)
	params, floor := hostileThrottlerParams()

	require.NoError(t, k.Params.Set(ctx, params))
	past := ctx.BlockTime().Add(-time.Hour)
	require.NoError(t, k.LastMinDeposit.Set(ctx, v1.LastMinDeposit{Value: floor, Time: &past}))
	require.NoError(t, k.LastMinInitialDeposit.Set(ctx, v1.LastMinDeposit{Value: floor, Time: &past}))

	require.NotPanics(t, func() {
		_ = sdkgov.EndBlocker(ctx.WithBlockTime(ctx.BlockTime().Add(time.Hour)), k)
	})
}
