package keeper_test

import (
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/distribution"
	"github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtestutil "github.com/cosmos/cosmos-sdk/x/distribution/testutil"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestCalculateRewardsBasic(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return(sdk.DefaultBondDenom, nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), disttypes.ModuleName, stakingtypes.BondedPoolName, gomock.Any()).Return(nil).AnyTimes()
	stakingKeeper.EXPECT().AddValidatorTokensOnly(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, disttypes.DefaultParams()))

	// create validator with 50% commission
	valAddr := sdk.ValAddress(valConsAddr0)
	addr := sdk.AccAddress(valAddr)
	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(1000))
	require.NoError(t, err)
	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

	// delegation mock
	del := stakingtypes.NewDelegation(addr.String(), valAddr.String(), val.DelegatorShares)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(3)
	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr, valAddr).Return(del, nil)

	// run the necessary hooks manually (given that we are not running an actual staking module)
	err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, addr, valAddr)
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// historical count should be 2 (once for validator init, once for delegation init)
	require.Equal(t, uint64(2), distrKeeper.GetValidatorHistoricalReferenceCount(ctx))

	// end period
	endingPeriod, _ := distrKeeper.IncrementValidatorPeriod(ctx, val)

	// historical count should be 2 still
	require.Equal(t, uint64(2), distrKeeper.GetValidatorHistoricalReferenceCount(ctx))

	// calculate delegation rewards
	rewards, err := distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// rewards should be zero
	require.True(t, rewards.IsZero())

	// allocate some rewards
	initial := int64(10)
	tokens := sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(initial)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(initial)},
	}
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// end period
	endingPeriod, _ = distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// bond denom auto-staked, photon delegator share (50% of 10 = 5) stays in F1
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(initial / 2)}}, rewards)

	// commission: 50% of both denoms
	valCommission, err := distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(initial / 2)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(initial / 2)},
	}, valCommission.Commission)
}

func TestCalculateRewardsAfterSlash(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return(sdk.DefaultBondDenom, nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), disttypes.ModuleName, stakingtypes.BondedPoolName, gomock.Any()).Return(nil).AnyTimes()
	stakingKeeper.EXPECT().AddValidatorTokensOnly(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, disttypes.DefaultParams()))

	// create validator with 50% commission
	valAddr := sdk.ValAddress(valConsAddr0)
	addr := sdk.AccAddress(valAddr)
	valPower := int64(100)
	stake := sdk.TokensFromConsensusPower(100, sdk.DefaultPowerReduction)
	val, err := distrtestutil.CreateValidator(valConsPk0, stake)
	require.NoError(t, err)
	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

	del := stakingtypes.NewDelegation(addr.String(), valAddr.String(), val.DelegatorShares)

	// set mock calls
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(4)
	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr, valAddr).Return(del, nil)

	// run the necessary hooks manually (given that we are not running an actual staking module)
	err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, addr, valAddr)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// end period
	endingPeriod, _ := distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards
	rewards, err := distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// rewards should be zero
	require.True(t, rewards.IsZero())

	// start out block height
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)

	// slash the validator by 50% (simulated with manual calls; we assume the validator is bonded)
	slashedTokens := distrtestutil.SlashValidator(
		ctx,
		valConsAddr0,
		ctx.BlockHeight(),
		valPower,
		math.LegacyNewDecWithPrec(5, 1),
		&val,
		&distrKeeper,
		stakingKeeper,
	)
	require.True(t, slashedTokens.IsPositive(), "expected positive slashed tokens, got: %s", slashedTokens)

	// increase block height
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)

	// allocate some rewards — photon stays in F1; bond denom is auto-staked
	initial := sdk.TokensFromConsensusPower(10, sdk.DefaultPowerReduction)
	tokens := sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDecFromInt(initial)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecFromInt(initial)},
	}
	require.NoError(t, func() error { _, e := distrKeeper.AllocateTokensToValidator(ctx, val, tokens); return e }())

	// end period
	endingPeriod, _ = distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// bond-denom auto-staked; photon delegator share (50% of initial) stays in F1
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDecFromInt(initial.QuoRaw(2))}}, rewards)

	// commission: 50% of each denom
	valCommission, err := distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDecFromInt(initial.QuoRaw(2))},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecFromInt(initial.QuoRaw(2))},
	}, valCommission.Commission)
}

func TestCalculateRewardsAfterManySlashes(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return(sdk.DefaultBondDenom, nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), disttypes.ModuleName, stakingtypes.BondedPoolName, gomock.Any()).Return(nil).AnyTimes()
	stakingKeeper.EXPECT().AddValidatorTokensOnly(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, disttypes.DefaultParams()))

	// create validator with 50% commission
	valAddr := sdk.ValAddress(valConsAddr0)
	addr := sdk.AccAddress(valAddr)
	valPower := int64(100)
	stake := sdk.TokensFromConsensusPower(valPower, sdk.DefaultPowerReduction)
	val, err := distrtestutil.CreateValidator(valConsPk0, stake)
	require.NoError(t, err)
	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

	// delegation mocks
	del := stakingtypes.NewDelegation(addr.String(), valAddr.String(), val.DelegatorShares)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(4)
	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr, valAddr).Return(del, nil)

	// run the necessary hooks manually (given that we are not running an actual staking module)
	err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, addr, valAddr)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// end period
	endingPeriod, _ := distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards
	rewards, err := distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// rewards should be zero
	require.True(t, rewards.IsZero())

	// start out block height
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)

	// slash the validator by 50% (simulated with manual calls; we assume the validator is bonded)
	slashedTokens := distrtestutil.SlashValidator(
		ctx,
		valConsAddr0,
		ctx.BlockHeight(),
		valPower,
		math.LegacyNewDecWithPrec(5, 1),
		&val,
		&distrKeeper,
		stakingKeeper,
	)
	require.True(t, slashedTokens.IsPositive(), "expected positive slashed tokens, got: %s", slashedTokens)

	// expect a call for the next slash with the updated validator
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(1)

	// increase block height
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)

	// allocate some rewards
	initial := sdk.TokensFromConsensusPower(10, sdk.DefaultPowerReduction)
	tokens := sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDecFromInt(initial)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecFromInt(initial)},
	}
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// slash the validator by 50% again
	slashedTokens = distrtestutil.SlashValidator(
		ctx,
		valConsAddr0,
		ctx.BlockHeight(),
		valPower/2,
		math.LegacyNewDecWithPrec(2, 1),
		&val,
		&distrKeeper,
		stakingKeeper,
	)
	require.True(t, slashedTokens.IsPositive(), "expected positive slashed tokens, got: %s", slashedTokens)

	// increase block height
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)

	// allocate some more rewards
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// end period
	endingPeriod, _ = distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// bond denom auto-staked, photon delegator share stays in F1 (two periods x 50% of initial each)
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDecFromInt(initial)}}, rewards)

	// commission: 50% of each denom x two allocations = initial in each
	valCommission, err := distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDecFromInt(initial)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecFromInt(initial)},
	}, valCommission.Commission)
}

func TestCalculateRewardsMultiDelegator(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return(sdk.DefaultBondDenom, nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), disttypes.ModuleName, stakingtypes.BondedPoolName, gomock.Any()).Return(nil).AnyTimes()
	stakingKeeper.EXPECT().AddValidatorTokensOnly(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, disttypes.DefaultParams()))

	// create validator with 50% commission
	valAddr := sdk.ValAddress(valConsAddr0)
	addr0 := sdk.AccAddress(valAddr)
	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)

	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

	del0 := stakingtypes.NewDelegation(addr0.String(), valAddr.String(), val.DelegatorShares)

	// set mock calls
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(4)
	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr0, valAddr).Return(del0, nil).Times(1)

	// run the necessary hooks manually (given that we are not running an actual staking module)
	err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, addr0, valAddr)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some rewards
	initial := int64(20)
	tokens := sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(initial)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(initial)},
	}
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// second delegation
	addr1 := sdk.AccAddress(valConsAddr1)
	_, del1, err := distrtestutil.Delegate(ctx, distrKeeper, addr1, &val, math.NewInt(100), nil, stakingKeeper)
	require.NoError(t, err)

	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr1, valAddr).Return(del1, nil)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(1)

	// call necessary hooks to update a delegation
	err = distrKeeper.Hooks().AfterDelegationModified(ctx, addr1, valAddr)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some more rewards
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// end period
	endingPeriod, _ := distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards for del1
	rewards, err := distrKeeper.CalculateDelegationRewards(ctx, val, del0, endingPeriod)
	require.NoError(t, err)

	// bond denom auto-staked, del0 gets photon: period1 (100% shares x 50% of 20 = 10) + period2 (50% shares x 5 = 5) = 15
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(15)}}, rewards)

	// calculate delegation rewards for del1
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del1, endingPeriod)
	require.NoError(t, err)

	// del1 only participates in period2: 50% shares x 5 delegator photon = 5
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(5)}}, rewards)

	// commission: 50% of each denom x two allocations = initial in each
	valCommission, err := distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(initial)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(initial)},
	}, valCommission.Commission)
}

func TestWithdrawDelegationRewardsBasic(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return(sdk.DefaultBondDenom, nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), disttypes.ModuleName, stakingtypes.BondedPoolName, gomock.Any()).Return(nil).AnyTimes()
	stakingKeeper.EXPECT().AddValidatorTokensOnly(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, disttypes.DefaultParams()))

	// create validator with 50% commission
	valAddr := sdk.ValAddress(valConsAddr0)
	addr := sdk.AccAddress(valAddr)
	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)

	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

	// delegation mock
	del := stakingtypes.NewDelegation(addr.String(), valAddr.String(), val.DelegatorShares)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(5)
	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr, valAddr).Return(del, nil).Times(3)

	// run the necessary hooks manually (given that we are not running an actual staking module)
	err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, addr, valAddr)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some rewards
	initial := sdk.TokensFromConsensusPower(10, sdk.DefaultPowerReduction)
	tokens := sdk.DecCoins{
		sdk.NewDecCoin("photon", initial),
		sdk.NewDecCoin(sdk.DefaultBondDenom, initial),
	}

	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// historical count should be 2 (initial + latest for delegation)
	require.Equal(t, uint64(2), distrKeeper.GetValidatorHistoricalReferenceCount(ctx))

	// withdraw rewards: photon delegator share (50% of initial) goes through F1 and should be sent
	expDelRewards := sdk.Coins{sdk.NewCoin("photon", initial.QuoRaw(2))}
	bankKeeper.EXPECT().SendCoinsFromModuleToAccount(ctx, disttypes.ModuleName, addr, expDelRewards)
	_, err = distrKeeper.WithdrawDelegationRewards(ctx, sdk.AccAddress(valAddr), valAddr)
	require.NoError(t, err)

	// historical count should still be 2 (added one record, cleared one)
	require.Equal(t, uint64(2), distrKeeper.GetValidatorHistoricalReferenceCount(ctx))

	// withdraw commission: 50% of each denom
	expCommission := sdk.Coins{sdk.NewCoin("photon", initial.QuoRaw(2)), sdk.NewCoin(sdk.DefaultBondDenom, initial.QuoRaw(2))}
	bankKeeper.EXPECT().SendCoinsFromModuleToAccount(ctx, disttypes.ModuleName, addr, expCommission)
	_, err = distrKeeper.WithdrawValidatorCommission(ctx, valAddr)
	require.NoError(t, err)
}

func TestCalculateRewardsAfterManySlashesInSameBlock(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return(sdk.DefaultBondDenom, nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), disttypes.ModuleName, stakingtypes.BondedPoolName, gomock.Any()).Return(nil).AnyTimes()
	stakingKeeper.EXPECT().AddValidatorTokensOnly(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, disttypes.DefaultParams()))

	// create validator with 50% commission
	valAddr := sdk.ValAddress(valConsAddr0)
	addr := sdk.AccAddress(valAddr)
	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)

	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

	// delegation mock
	del := stakingtypes.NewDelegation(addr.String(), valAddr.String(), val.DelegatorShares)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(5)
	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr, valAddr).Return(del, nil)

	// run the necessary hooks manually (given that we are not running an actual staking module)
	err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, addr, valAddr)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// end period
	endingPeriod, _ := distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards
	rewards, err := distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// rewards should be zero
	require.True(t, rewards.IsZero())

	// start out block height
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)

	// allocate some rewards
	initial := math.LegacyNewDecFromInt(sdk.TokensFromConsensusPower(10, sdk.DefaultPowerReduction))
	tokens := sdk.DecCoins{
		{Denom: "photon", Amount: initial},
		{Denom: sdk.DefaultBondDenom, Amount: initial},
	}
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	valPower := int64(100)
	// slash the validator by 50% (simulated with manual calls; we assume the validator is bonded)
	distrtestutil.SlashValidator(
		ctx,
		valConsAddr0,
		ctx.BlockHeight(),
		valPower,
		math.LegacyNewDecWithPrec(5, 1),
		&val,
		&distrKeeper,
		stakingKeeper,
	)

	// slash the validator by 50% again
	// stakingKeeper.Slash(ctx, valConsAddr0, ctx.BlockHeight(), valPower/2, math.LegacyNewDecWithPrec(5, 1))
	distrtestutil.SlashValidator(
		ctx,
		valConsAddr0,
		ctx.BlockHeight(),
		valPower/2,
		math.LegacyNewDecWithPrec(5, 1),
		&val,
		&distrKeeper,
		stakingKeeper,
	)

	// increase block height
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)

	// allocate some more rewards
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// end period
	endingPeriod, _ = distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// bond denom auto-staked, photon delegator share goes through F1 (two allocations × initial/2)
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: initial}}, rewards)

	// commission: 50% of each denom x two allocations = initial in each
	valCommission, err := distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: initial},
		{Denom: sdk.DefaultBondDenom, Amount: initial},
	}, valCommission.Commission)
}

func TestCalculateRewardsMultiDelegatorMultiSlash(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return(sdk.DefaultBondDenom, nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), disttypes.ModuleName, stakingtypes.BondedPoolName, gomock.Any()).Return(nil).AnyTimes()
	stakingKeeper.EXPECT().AddValidatorTokensOnly(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, disttypes.DefaultParams()))

	valPower := int64(100)

	// create validator with 50% commission
	valAddr := sdk.ValAddress(valConsAddr0)
	addr := sdk.AccAddress(valAddr)
	val, err := distrtestutil.CreateValidator(valConsPk0, sdk.TokensFromConsensusPower(valPower, sdk.DefaultPowerReduction))
	require.NoError(t, err)
	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

	// validator and delegation mocks
	del := stakingtypes.NewDelegation(addr.String(), valAddr.String(), val.DelegatorShares)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(3)
	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr, valAddr).Return(del, nil)

	// run the necessary hooks manually (given that we are not running an actual staking module)
	err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, addr, valAddr)
	require.NoError(t, err)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(2)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some rewards
	initial := math.LegacyNewDecFromInt(sdk.TokensFromConsensusPower(30, sdk.DefaultPowerReduction))
	tokens := sdk.DecCoins{
		{Denom: "photon", Amount: initial},
		{Denom: sdk.DefaultBondDenom, Amount: initial},
	}
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// slash the validator
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)
	distrtestutil.SlashValidator(
		ctx,
		valConsAddr0,
		ctx.BlockHeight(),
		valPower,
		math.LegacyNewDecWithPrec(5, 1),
		&val,
		&distrKeeper,
		stakingKeeper,
	)
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)

	// update validator mock
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(1)

	// second delegation
	_, del2, err := distrtestutil.Delegate(
		ctx,
		distrKeeper,
		sdk.AccAddress(valConsAddr1),
		&val,
		sdk.TokensFromConsensusPower(100, sdk.DefaultPowerReduction),
		nil,
		stakingKeeper,
	)
	require.NoError(t, err)

	// new delegation mock and update validator mock
	stakingKeeper.EXPECT().Delegation(gomock.Any(), sdk.AccAddress(valConsAddr1), valAddr).Return(del2, nil)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(1)

	// call necessary hooks to update a delegation
	err = distrKeeper.Hooks().AfterDelegationModified(ctx, sdk.AccAddress(valConsAddr1), valAddr)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some more rewards
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// slash the validator again
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)
	distrtestutil.SlashValidator(
		ctx,
		valConsAddr0,
		ctx.BlockHeight(),
		valPower,
		math.LegacyNewDecWithPrec(5, 1),
		&val,
		&distrKeeper,
		stakingKeeper,
	)
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 3)

	// end period
	endingPeriod, _ := distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards for del1
	rewards, err := distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// del1 had full shares for period1 allocation, 50% for period2; slash adjustments yield 2/3 of total photon rewards
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(20_000_000)}}, rewards)

	// calculate delegation rewards for del2
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del2, endingPeriod)
	require.NoError(t, err)

	// del2 only participates in period2 with 50% shares; slash adjustments yield 1/3 of total photon rewards
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(10_000_000)}}, rewards)

	// commission: 50% of each denom x two allocations = initial in each
	valCommission, err := distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: initial},
		{Denom: sdk.DefaultBondDenom, Amount: initial},
	}, valCommission.Commission)
}

func TestCalculateRewardsMultiDelegatorMultWithdraw(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return(sdk.DefaultBondDenom, nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), disttypes.ModuleName, stakingtypes.BondedPoolName, gomock.Any()).Return(nil).AnyTimes()
	stakingKeeper.EXPECT().AddValidatorTokensOnly(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, disttypes.DefaultParams()))

	// create validator with 50% commission
	valAddr := sdk.ValAddress(valConsAddr0)
	addr := sdk.AccAddress(valAddr)
	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))

	// validator and delegation mocks
	del := stakingtypes.NewDelegation(addr.String(), valAddr.String(), val.DelegatorShares)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(3)
	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr, valAddr).Return(del, nil).Times(5)

	// run the necessary hooks manually (given that we are not running an actual staking module)
	err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, addr, valAddr)
	require.NoError(t, err)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(2)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some rewards
	initial := int64(20)
	tokens := sdk.DecCoins{
		sdk.NewDecCoin("photon", math.NewInt(initial)),
		sdk.NewDecCoin(sdk.DefaultBondDenom, math.NewInt(initial)),
	}
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// historical count should be 2 (validator init, delegation init)
	require.Equal(t, uint64(2), distrKeeper.GetValidatorHistoricalReferenceCount(ctx))

	// second delegation
	_, del2, err := distrtestutil.Delegate(
		ctx,
		distrKeeper,
		sdk.AccAddress(valConsAddr1),
		&val,
		math.NewInt(100),
		nil,
		stakingKeeper,
	)
	require.NoError(t, err)

	// new delegation mock and update validator mock
	stakingKeeper.EXPECT().Delegation(gomock.Any(), sdk.AccAddress(valConsAddr1), valAddr).Return(del2, nil).Times(3)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(6)

	// call necessary hooks to update a delegation
	err = distrKeeper.Hooks().AfterDelegationModified(ctx, sdk.AccAddress(valConsAddr1), valAddr)
	require.NoError(t, err)

	// historical count should be 3 (second delegation init)
	require.Equal(t, uint64(3), distrKeeper.GetValidatorHistoricalReferenceCount(ctx))

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some more rewards
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// first delegator withdraws 15 photon (period1: 100% shares x 10 delegator photon + period2: 50% shares x 10 = 5)
	bankKeeper.EXPECT().SendCoinsFromModuleToAccount(ctx, disttypes.ModuleName, addr, sdk.Coins{sdk.NewCoin("photon", math.NewInt(15))})
	_, err = distrKeeper.WithdrawDelegationRewards(ctx, addr, valAddr)
	require.NoError(t, err)

	// second delegator withdraws 5 photon (only period2: 50% shares x 10 delegator photon)
	bankKeeper.EXPECT().SendCoinsFromModuleToAccount(ctx, disttypes.ModuleName, sdk.AccAddress(valConsAddr1), sdk.Coins{sdk.NewCoin("photon", math.NewInt(5))})
	_, err = distrKeeper.WithdrawDelegationRewards(ctx, sdk.AccAddress(valConsAddr1), valAddr)
	require.NoError(t, err)

	// historical count should be 3 (validator init + two delegations)
	require.Equal(t, uint64(3), distrKeeper.GetValidatorHistoricalReferenceCount(ctx))

	// validator withdraws commission: 50% x 20 x 2 blocks = {20 photon, 20 stake}
	expCommission := sdk.Coins{sdk.NewCoin("photon", math.NewInt(initial)), sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(initial))}
	bankKeeper.EXPECT().SendCoinsFromModuleToAccount(ctx, disttypes.ModuleName, addr, expCommission)
	_, err = distrKeeper.WithdrawValidatorCommission(ctx, valAddr)
	require.NoError(t, err)

	// end period
	endingPeriod, _ := distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards for del1
	rewards, err := distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// rewards for del1 should be zero
	require.True(t, rewards.IsZero())

	// calculate delegation rewards for del2
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del2, endingPeriod)
	require.NoError(t, err)

	// rewards for del2 should be zero
	require.True(t, rewards.IsZero())

	// commission should be zero
	valCommission, err := distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	require.True(t, valCommission.Commission.IsZero())

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some more rewards
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// first delegator withdraws 5 photon (50% shares x 50% of block4 allocation = 5)
	bankKeeper.EXPECT().SendCoinsFromModuleToAccount(ctx, disttypes.ModuleName, addr, sdk.Coins{sdk.NewCoin("photon", math.NewInt(5))})
	_, err = distrKeeper.WithdrawDelegationRewards(ctx, addr, valAddr)
	require.NoError(t, err)

	// end period
	endingPeriod, _ = distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards for del1
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// del1 just withdrew, so no new rewards yet
	require.True(t, rewards.IsZero())

	// calculate delegation rewards for del2
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del2, endingPeriod)
	require.NoError(t, err)

	// del2 has accumulated 5 photon from block4 (did not withdraw yet)
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(5)}}, rewards)

	// commission should be {photon:initial/2, stake:initial/2} from block4
	valCommission, err = distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(initial / 2)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(initial / 2)},
	}, valCommission.Commission)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some more rewards
	require.NoError(t, func() error { _, e := distrKeeper.AllocateTokensToValidator(ctx, val, tokens); return e }())

	// withdraw commission: block4 (10 photon, 10 stake) + block5 (10 photon, 10 stake) = {20 photon, 20 stake}
	expCommission = sdk.Coins{sdk.NewCoin("photon", math.NewInt(initial)), sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(initial))}
	bankKeeper.EXPECT().SendCoinsFromModuleToAccount(ctx, disttypes.ModuleName, addr, expCommission)
	_, err = distrKeeper.WithdrawValidatorCommission(ctx, valAddr)
	require.NoError(t, err)

	// end period
	endingPeriod, _ = distrKeeper.IncrementValidatorPeriod(ctx, val)

	// calculate delegation rewards for del1
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del, endingPeriod)
	require.NoError(t, err)

	// del1 gets 50% shares x block5 delegator photon (5) = 5
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(5)}}, rewards)

	// calculate delegation rewards for del2
	rewards, err = distrKeeper.CalculateDelegationRewards(ctx, val, del2, endingPeriod)
	require.NoError(t, err)

	// del2 gets block4 (5) + block5 (5) = 10 photon (del2 never withdrew)
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(10)}}, rewards)

	// commission should be zero (just withdrew)
	valCommission, err = distrKeeper.GetValidatorAccumulatedCommission(ctx, valAddr)
	require.NoError(t, err)
	require.True(t, valCommission.Commission.IsZero())
}

func Test100PercentCommissionReward(t *testing.T) {
	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress())
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec(sdk.Bech32PrefixValAddr)).AnyTimes()
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec(sdk.Bech32MainPrefix)).AnyTimes()

	distrKeeper := keeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		stakingKeeper,
		"fee_collector",
		authtypes.NewModuleAddress("gov").String(),
	)

	stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return(sdk.DefaultBondDenom, nil).AnyTimes()
	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), disttypes.ModuleName, stakingtypes.BondedPoolName, gomock.Any()).Return(nil).AnyTimes()
	stakingKeeper.EXPECT().AddValidatorTokensOnly(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// reset fee pool
	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))
	require.NoError(t, distrKeeper.Params.Set(ctx, disttypes.DefaultParams()))

	// create validator with 50% commission
	valAddr := sdk.ValAddress(valConsAddr0)
	addr := sdk.AccAddress(valAddr)
	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(10, 1), math.LegacyNewDecWithPrec(10, 1), math.LegacyNewDec(0))

	// validator and delegation mocks
	del := stakingtypes.NewDelegation(addr.String(), valAddr.String(), val.DelegatorShares)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(3)
	stakingKeeper.EXPECT().Delegation(gomock.Any(), addr, valAddr).Return(del, nil).Times(3)

	// run the necessary hooks manually (given that we are not running an actual staking module)
	err = distrtestutil.CallCreateValidatorHooks(ctx, distrKeeper, addr, valAddr)
	require.NoError(t, err)
	stakingKeeper.EXPECT().Validator(gomock.Any(), valAddr).Return(val, nil).Times(2)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some rewards
	initial := int64(20)
	tokens := sdk.DecCoins{sdk.NewDecCoin(sdk.DefaultBondDenom, math.NewInt(initial))}
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some rewards
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some more rewards
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	// next block
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)

	// allocate some more rewards
	_, err = distrKeeper.AllocateTokensToValidator(ctx, val, tokens)
	require.NoError(t, err)

	rewards, err := distrKeeper.WithdrawDelegationRewards(ctx, addr, valAddr)
	require.NoError(t, err)

	zeroRewards := sdk.Coins{sdk.NewCoin(sdk.DefaultBondDenom, math.ZeroInt())}
	require.True(t, rewards.Equal(zeroRewards))

	events := ctx.EventManager().Events()
	lastEvent := events[len(events)-1]

	var hasValue bool
	for _, attr := range lastEvent.Attributes {
		if attr.Key == "amount" && attr.Value == "0stake" {
			hasValue = true
		}
	}
	require.True(t, hasValue)
}
