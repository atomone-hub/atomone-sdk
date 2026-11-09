package mint_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/depinject"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/cosmos/cosmos-sdk/x/mint"
	"github.com/cosmos/cosmos-sdk/x/mint/keeper"
	"github.com/cosmos/cosmos-sdk/x/mint/testutil"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

// fixedInflationFn is an InflationCalculationFn that always returns fixedInflation.
var fixedInflation = math.LegacyNewDecWithPrec(50, 2)

func fixedInflationFn(_ context.Context, _ types.Minter, _ types.Params, _ math.LegacyDec) math.LegacyDec {
	return fixedInflation
}

func TestItCreatesModuleAccountOnInitBlock(t *testing.T) {
	var accountKeeper authkeeper.AccountKeeper

	app, err := simtestutil.SetupAtGenesis(
		depinject.Configs(
			testutil.AppConfig,
			depinject.Supply(log.NewNopLogger()),
		), &accountKeeper)
	require.NoError(t, err)

	ctx := app.BaseApp.NewContext(false)
	acc := accountKeeper.GetAccount(ctx, authtypes.NewModuleAddress(types.ModuleName))
	require.NotNil(t, acc)
}

func TestProvideModuleHonoursInflationCalculationFn(t *testing.T) {
	var mintKeeper keeper.Keeper

	app, err := simtestutil.SetupAtGenesis(
		depinject.Configs(
			testutil.AppConfig,
			depinject.Supply(log.NewNopLogger(), types.InflationCalculationFn(fixedInflationFn)),
		), &mintKeeper)
	require.NoError(t, err)

	ctx := app.BaseApp.NewContext(false)
	require.NoError(t, mintKeeper.MintFn(ctx))

	minter, err := mintKeeper.Minter.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, fixedInflation, minter.Inflation)
}

func TestProvideModuleRejectsMintFnWithInflationCalculationFn(t *testing.T) {
	noopMintFn := keeper.MintFn(func(sdk.Context, *keeper.Keeper) error { return nil })

	require.PanicsWithValue(t, "x/mint: MintFn and InflationCalculationFn cannot both be supplied; set only one of them", func() {
		_, _ = simtestutil.SetupAtGenesis(
			depinject.Configs(
				testutil.AppConfig,
				depinject.Supply(log.NewNopLogger(), types.InflationCalculationFn(fixedInflationFn), noopMintFn),
			))
	})
}

// newMockedKeeper builds a mint keeper over mocks that accept one default
// minting round, with default params and minter stored.
func newMockedKeeper(t *testing.T, opts ...keeper.InitOption) (keeper.Keeper, sdk.Context) {
	t.Helper()

	encCfg := moduletestutil.MakeTestEncodingConfig(mint.AppModuleBasic{})
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := sdktestutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))

	ctrl := gomock.NewController(t)
	accountKeeper := testutil.NewMockAccountKeeper(ctrl)
	accountKeeper.EXPECT().GetModuleAddress(types.ModuleName).Return(sdk.AccAddress{})
	stakingKeeper := testutil.NewMockStakingKeeper(ctrl)
	stakingKeeper.EXPECT().StakingTokenSupply(gomock.Any()).Return(math.NewInt(1_000_000), nil).AnyTimes()
	stakingKeeper.EXPECT().BondedRatio(gomock.Any()).Return(math.LegacyNewDecWithPrec(67, 2), nil).AnyTimes()
	bankKeeper := testutil.NewMockBankKeeper(ctrl)
	bankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, gomock.Any()).Return(nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), types.ModuleName, authtypes.FeeCollectorName, gomock.Any()).Return(nil).AnyTimes()

	k := keeper.NewKeeper(
		encCfg.Codec,
		runtime.NewKVStoreService(key),
		stakingKeeper,
		accountKeeper,
		bankKeeper,
		authtypes.FeeCollectorName,
		authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		opts...,
	)
	require.NoError(t, k.Params.Set(testCtx.Ctx, types.DefaultParams()))
	require.NoError(t, k.Minter.Set(testCtx.Ctx, types.DefaultInitialMinter()))

	return k, testCtx.Ctx
}

func TestNewAppModuleHonoursInflationCalculationFn(t *testing.T) {
	k, ctx := newMockedKeeper(t)
	encCfg := moduletestutil.MakeTestEncodingConfig(mint.AppModuleBasic{})

	am := mint.NewAppModule(encCfg.Codec, k, nil, fixedInflationFn, nil)
	require.NoError(t, am.BeginBlock(ctx))

	minter, err := k.Minter.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, fixedInflation, minter.Inflation)
	// The keeper value given to NewAppModule keeps its own mint function.
	require.False(t, k.HasCustomMintFn())
}

func TestNewAppModuleRejectsInflationCalculationFnWithMintFn(t *testing.T) {
	noopMintFn := func(sdk.Context, *keeper.Keeper) error { return nil }
	k, _ := newMockedKeeper(t, keeper.WithMintFn(noopMintFn))
	encCfg := moduletestutil.MakeTestEncodingConfig(mint.AppModuleBasic{})

	require.Panics(t, func() {
		mint.NewAppModule(encCfg.Codec, k, nil, fixedInflationFn, nil)
	})
	require.NotPanics(t, func() {
		mint.NewAppModule(encCfg.Codec, k, nil, nil, nil)
	})
}
