package keeper_test

import (
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/distribution"
	"github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtestutil "github.com/cosmos/cosmos-sdk/x/distribution/testutil"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestSetWithdrawAddr(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(types.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: time.Now()})
	addrs := simtestutil.CreateIncrementalAccounts(2)

	delegatorAddr := addrs[0]
	withdrawAddr := addrs[1]

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())

	bankKeeper.EXPECT().BlockedAddr(withdrawAddr).Return(false).AnyTimes()
	bankKeeper.EXPECT().BlockedAddr(distrAcc.GetAddress()).Return(true).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	params := types.DefaultParams()
	params.WithdrawAddrEnabled = false
	require.NoError(t, distrKeeper.Params.Set(ctx, params))

	err := distrKeeper.SetWithdrawAddr(ctx, delegatorAddr, withdrawAddr)
	require.NotNil(t, err)

	params.WithdrawAddrEnabled = true
	require.NoError(t, distrKeeper.Params.Set(ctx, params))

	err = distrKeeper.SetWithdrawAddr(ctx, delegatorAddr, withdrawAddr)
	require.Nil(t, err)

	require.Error(t, distrKeeper.SetWithdrawAddr(ctx, delegatorAddr, distrAcc.GetAddress()))
}

func TestWithdrawValidatorCommission(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(types.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: time.Now()})
	addrs := simtestutil.CreateIncrementalAccounts(1)

	valAddr := sdk.ValAddress(addrs[0])

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())

	valCommission := sdk.DecCoins{
		sdk.NewDecCoinFromDec("mytoken", math.LegacyNewDec(5).Quo(math.LegacyNewDec(4))),
		sdk.NewDecCoinFromDec("stake", math.LegacyNewDec(3).Quo(math.LegacyNewDec(2))),
	}

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	require.NoError(t, distrKeeper.FeePool.Set(ctx, types.InitialFeePool()))

	// set outstanding rewards
	require.NoError(t, distrKeeper.SetValidatorOutstandingRewards(ctx, valAddr, types.ValidatorOutstandingRewards{Rewards: valCommission}))

	// set commission
	require.NoError(t, distrKeeper.SetValidatorAccumulatedCommission(ctx, valAddr, types.ValidatorAccumulatedCommission{Commission: valCommission}))

	// AutoStakeValidatorCommission routes the bond denom portion (1.5 stake)
	// of accumulated commission through the staking Delegate path: integer
	// (1 stake) is sent from distribution to the operator's account, then
	// re-delegated; sub-integer dust (0.5 stake) is swept to the community
	// pool. The non-bond portion (1.25 mytoken) follows the standard
	// withdraw path: integer (1 mytoken) paid to the operator's withdraw
	// address, dust (0.25 mytoken) left in accumulatedCommission.
	val := stakingtypes.Validator{}
	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return("stake", nil).AnyTimes()
	stakingKeeper.EXPECT().GetValidator(gomock.Any(), valAddr).Return(val, nil)
	bankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), "distribution", sdk.AccAddress(valAddr), sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1)))).Return(nil)
	stakingKeeper.EXPECT().Delegate(gomock.Any(), sdk.AccAddress(valAddr), math.NewInt(1), stakingtypes.Unbonded, val, true).Return(math.LegacyOneDec(), nil)
	// non-bond commission payout
	bankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), "distribution", addrs[0], sdk.NewCoins(sdk.NewCoin("mytoken", math.NewInt(1)))).Return(nil)

	_, err := distrKeeper.WithdrawValidatorCommission(ctx, valAddr)
	require.NoError(t, err)

	// check remainder: bond denom portion is fully gone (integer auto-staked,
	// dust to community pool); non-bond residue stays.
	remainderValCommission, err := distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	remainder := remainderValCommission.Commission
	require.Equal(t, sdk.DecCoins{
		sdk.NewDecCoinFromDec("mytoken", math.LegacyNewDec(1).Quo(math.LegacyNewDec(4))),
	}, remainder)

	// community pool received the 0.5 stake dust from auto-stake.
	feePool, err := distrKeeper.FeePool.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		sdk.NewDecCoinFromDec("stake", math.LegacyNewDecWithPrec(5, 1)),
	}, feePool.CommunityPool)
}

func TestGetTotalRewards(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(types.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: time.Now()})
	addrs := simtestutil.CreateIncrementalAccounts(2)

	valAddr0 := sdk.ValAddress(addrs[0])
	valAddr1 := sdk.ValAddress(addrs[1])

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	valCommission := sdk.DecCoins{
		sdk.NewDecCoinFromDec("mytoken", math.LegacyNewDec(5).Quo(math.LegacyNewDec(4))),
		sdk.NewDecCoinFromDec("stake", math.LegacyNewDec(3).Quo(math.LegacyNewDec(2))),
	}

	require.NoError(t, distrKeeper.SetValidatorOutstandingRewards(ctx, valAddr0, types.ValidatorOutstandingRewards{Rewards: valCommission}))
	require.NoError(t, distrKeeper.SetValidatorOutstandingRewards(ctx, valAddr1, types.ValidatorOutstandingRewards{Rewards: valCommission}))

	expectedRewards := valCommission.MulDec(math.LegacyNewDec(2))
	totalRewards := distrKeeper.GetTotalRewards(ctx)

	require.Equal(t, expectedRewards, totalRewards)
}

func TestFundCommunityPool(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(types.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: time.Now()})
	addrs := simtestutil.CreateIncrementalAccounts(1)

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, types.InitialFeePool()))

	initPool, err := distrKeeper.FeePool.Get(ctx)
	require.NoError(t, err)
	require.Empty(t, initPool.CommunityPool)

	amount := sdk.NewCoins(sdk.NewInt64Coin("stake", 100))
	bankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), addrs[0], "distribution", amount).Return(nil)
	err = distrKeeper.FundCommunityPool(ctx, amount, addrs[0])
	require.NoError(t, err)

	feePool, err := distrKeeper.FeePool.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, initPool.CommunityPool.Add(sdk.NewDecCoinsFromCoins(amount...)...), feePool.CommunityPool)
}
