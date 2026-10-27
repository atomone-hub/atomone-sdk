package vesting_test

import (
	"fmt"
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttime "github.com/cometbft/cometbft/types/time"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authcodec "github.com/cosmos/cosmos-sdk/x/auth/codec"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/auth/vesting"
	vestingtestutil "github.com/cosmos/cosmos-sdk/x/auth/vesting/testutil"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
)

func newScalingHarness(t *testing.T) (sdk.Context, authkeeper.AccountKeeper, vestingtypes.MsgServer) {
	t.Helper()
	key := storetypes.NewKVStoreKey(authtypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: cmttime.Now()})
	encCfg := moduletestutil.MakeTestEncodingConfig()

	ctrl := gomock.NewController(t)
	bankKeeper := vestingtestutil.NewMockBankKeeper(ctrl)
	bankKeeper.EXPECT().BlockedAddr(gomock.Any()).Return(false).AnyTimes()
	bankKeeper.EXPECT().IsSendEnabledCoins(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	bankKeeper.EXPECT().SendCoins(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	accountKeeper := authkeeper.NewAccountKeeper(
		encCfg.Codec,
		storeService,
		authtypes.ProtoBaseAccount,
		map[string][]string{},
		authcodec.NewBech32Codec("cosmos"),
		"cosmos",
		authtypes.NewModuleAddress("gov").String(),
	)

	vestingtypes.RegisterInterfaces(encCfg.InterfaceRegistry)
	authtypes.RegisterInterfaces(encCfg.InterfaceRegistry)

	return ctx, accountKeeper, vesting.NewMsgServerImpl(accountKeeper, bankKeeper)
}

func scalingAddr(tag string, i int) sdk.AccAddress {
	b := make([]byte, 20)
	copy(b, fmt.Sprintf("%s%d", tag, i))
	return b
}

// distinctDenomPeriods returns n periods with one distinct denom each — the
// shape that maximized the old Coins.Add-in-loop quadratic.
func distinctDenomPeriods(n int) vestingtypes.Periods {
	periods := make(vestingtypes.Periods, n)
	for i := range periods {
		periods[i] = vestingtypes.Period{
			Length: 1,
			Amount: sdk.NewCoins(sdk.NewInt64Coin(fmt.Sprintf("denom%06d", i), 1)),
		}
	}
	return periods
}

// TestCreatePeriodicVestingAccountLinearScaling guards against reintroducing a
// per-period Coins.Add accumulation in CreatePeriodicVestingAccount (and in
// PeriodicVestingAccount.Validate, reached via NewPeriodicVestingAccount).
// Coins.Add re-inserts and re-sorts the whole accumulated set on every call,
// so summing N periods was O(N² log N) in unmetered CPU: a single
// mempool-legal message with tens of thousands of periods could stall block
// finalization for minutes. Scaling is asserted on allocation counts (a
// stable proxy for CPU) rather than wall-clock time.
func TestCreatePeriodicVestingAccountLinearScaling(t *testing.T) {
	ctx, accountKeeper, msgServer := newScalingHarness(t)

	// Correctness guard: repeated denoms across periods must still be
	// coalesced into a sorted total, as the old accumulation produced.
	toAddr := scalingAddr("to", 0)
	periods := vestingtypes.Periods{
		{Length: 1, Amount: sdk.NewCoins(sdk.NewInt64Coin("foo", 10))},
		{Length: 1, Amount: sdk.NewCoins(sdk.NewInt64Coin("bar", 5))},
		{Length: 1, Amount: sdk.NewCoins(sdk.NewInt64Coin("foo", 7))},
	}
	_, err := msgServer.CreatePeriodicVestingAccount(
		ctx,
		vestingtypes.NewMsgCreatePeriodicVestingAccount(scalingAddr("from", 0), toAddr, 100, periods),
	)
	require.NoError(t, err)

	acc, ok := accountKeeper.GetAccount(ctx, toAddr).(*vestingtypes.PeriodicVestingAccount)
	require.True(t, ok, "expected a periodic vesting account to be stored")
	require.Equal(t, "5bar,17foo", acc.OriginalVesting.String())

	allocsFor := func(n int) float64 {
		i := 0
		return testing.AllocsPerRun(3, func() {
			i++
			_, err := msgServer.CreatePeriodicVestingAccount(
				ctx,
				vestingtypes.NewMsgCreatePeriodicVestingAccount(scalingAddr(fmt.Sprintf("from%d-", n), i), scalingAddr(fmt.Sprintf("to%d-", n), i), 100, distinctDenomPeriods(n)),
			)
			require.NoError(t, err)
		})
	}

	small := allocsFor(200)
	large := allocsFor(1000)

	// A linear implementation allocates ~5x more for 1000 periods than for
	// 200; the quadratic Coins.Add implementation allocates ~25x more.
	// 8x leaves headroom against noise while failing any super-linear
	// accumulation.
	require.Lessf(t, large, small*8,
		"allocation growth from 200 to 1000 vesting periods is super-linear (%g -> %g, %.2fx)",
		small, large, large/small)
}

// TestCreatePeriodicVestingAccountPeriodCap checks that the stored schedule
// size is bounded: the schedule is re-evaluated on every fee deduction and
// send of the vesting account, so an unbounded period count is a persistent
// block-time-exhaustion vector. There is no ValidateBasic for this msg, so
// the server-side check is the enforced consensus bound.
func TestCreatePeriodicVestingAccountPeriodCap(t *testing.T) {
	ctx, _, msgServer := newScalingHarness(t)

	over := vestingtypes.NewMsgCreatePeriodicVestingAccount(
		scalingAddr("from", 1), scalingAddr("cap", 1), 1,
		distinctDenomPeriods(vestingtypes.MaxVestingPeriods+1),
	)
	_, err := msgServer.CreatePeriodicVestingAccount(ctx, over)
	require.ErrorContains(t, err, "exceeds maximum allowed")

	at := vestingtypes.NewMsgCreatePeriodicVestingAccount(
		scalingAddr("from", 2), scalingAddr("cap", 2), 1,
		distinctDenomPeriods(vestingtypes.MaxVestingPeriods),
	)
	_, err = msgServer.CreatePeriodicVestingAccount(ctx, at)
	require.NoError(t, err)
}
