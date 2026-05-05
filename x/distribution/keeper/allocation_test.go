package keeper_test

import (
	"errors"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	sdkaddress "cosmossdk.io/core/address"
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

type suite struct {
	ctx             sdk.Context
	distrKeeper     keeper.Keeper
	stakingKeeper   *distrtestutil.MockStakingKeeper
	accountKeeper   *distrtestutil.MockAccountKeeper
	bankKeeper      *distrtestutil.MockBankKeeper
	feeCollectorAcc *authtypes.ModuleAccount
	valCodec        sdkaddress.Codec
}

func setupTestKeeper(t *testing.T, nakamotoBonusCoefficient math.LegacyDec, height uint64) *suite {
	t.Helper()

	ctrl := gomock.NewController(t)
	key := storetypes.NewKVStoreKey(disttypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{})
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: time.Now(), Height: int64(height)})

	bankKeeper := distrtestutil.NewMockBankKeeper(ctrl)
	stakingKeeper := distrtestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := distrtestutil.NewMockAccountKeeper(ctrl)

	accountKeeper.EXPECT().GetModuleAddress("distribution").Return(distrAcc.GetAddress()).AnyTimes()
	valCodec := address.NewBech32Codec("cosmosvaloper")
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(valCodec).AnyTimes()
	feeCollectorAcc := authtypes.NewEmptyModuleAccount("fee_collector")
	accountKeeper.EXPECT().GetModuleAccount(gomock.Any(), "fee_collector").Return(feeCollectorAcc).AnyTimes()

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

	require.NoError(t, distrKeeper.FeePool.Set(ctx, disttypes.InitialFeePool()))

	params, err := distrKeeper.Params.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		params = disttypes.DefaultParams()
	} else {
		require.NoError(t, err)
	}
	require.NoError(t, distrKeeper.Params.Set(ctx, params))

	err = distrKeeper.SetNakamotoBonusCoefficient(ctx, nakamotoBonusCoefficient)
	require.NoError(t, err)

	return &suite{
		ctx:             ctx,
		distrKeeper:     distrKeeper,
		stakingKeeper:   stakingKeeper,
		accountKeeper:   accountKeeper,
		bankKeeper:      bankKeeper,
		feeCollectorAcc: feeCollectorAcc,
		valCodec:        valCodec,
	}
}

func TestAllocateTokensToValidatorWithCommission(t *testing.T) {
	s := setupTestKeeper(t, math.LegacyZeroDec(), 100)

	// create a validator with 50% commission
	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	val.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk0)).Return(val, nil).AnyTimes()

	// allocate tokens (non-bond denom distributed through F1, bond denom is auto-staked)
	tokens := sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(10)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(10)},
	}
	_, err = s.distrKeeper.AllocateTokensToValidator(s.ctx, val, tokens)
	require.NoError(t, err)

	valBz, err := s.valCodec.StringToBytes(val.GetOperator())
	require.NoError(t, err)

	// check commission: 50% of both denoms
	expectedCommission := sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(5)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(5)},
	}
	valCommission, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valBz)
	require.NoError(t, err)
	require.Equal(t, expectedCommission, valCommission.Commission)

	// check current rewards: only non-bond denom goes to current rewards since bond denom is auto-staked
	currentRewards, err := s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valBz)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(5)}}, currentRewards.Rewards)
}

func TestAllocateTokensToManyValidators(t *testing.T) {
	s := setupTestKeeper(t, math.LegacyZeroDec(), 100)

	// reset fee pool & set params
	require.NoError(t, s.distrKeeper.Params.Set(s.ctx, disttypes.DefaultParams()))
	require.NoError(t, s.distrKeeper.FeePool.Set(s.ctx, disttypes.InitialFeePool()))

	// create validator with 50% commission
	valAddr0 := sdk.ValAddress(valConsAddr0)
	val0, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	val0.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0))
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk0)).Return(val0, nil).AnyTimes()

	// create second validator with 0% commission
	valAddr1 := sdk.ValAddress(valConsAddr1)
	val1, err := distrtestutil.CreateValidator(valConsPk1, math.NewInt(100))
	require.NoError(t, err)
	val1.Commission = stakingtypes.NewCommission(math.LegacyNewDec(0), math.LegacyNewDec(0), math.LegacyNewDec(0))
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk1)).Return(val1, nil).AnyTimes()

	// assert the initial state: zero outstanding rewards, zero community pool, zero commission, zero current rewards
	val0OutstandingRewards, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.True(t, val0OutstandingRewards.Rewards.IsZero())

	val1OutstandingRewards, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.True(t, val1OutstandingRewards.Rewards.IsZero())

	feePool, err := s.distrKeeper.FeePool.Get(s.ctx)
	require.NoError(t, err)
	require.True(t, feePool.CommunityPool.IsZero())

	val0Commission, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr0)
	require.NoError(t, err)
	require.True(t, val0Commission.Commission.IsZero())

	val1Commission, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr1)
	require.NoError(t, err)
	require.True(t, val1Commission.Commission.IsZero())

	val0CurrentRewards, err := s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.True(t, val0CurrentRewards.Rewards.IsZero())

	val1CurrentRewards, err := s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.True(t, val1CurrentRewards.Rewards.IsZero())

	// allocate tokens as if both had voted and second was proposer
	fees := sdk.NewCoins(sdk.NewCoin("photon", math.NewInt(100)), sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(100)))
	s.bankKeeper.EXPECT().GetAllBalances(gomock.Any(), s.feeCollectorAcc.GetAddress()).Return(fees)
	s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "fee_collector", disttypes.ModuleName, fees)

	votes := []abci.VoteInfo{
		{Validator: abci.Validator{Address: valConsPk0.Address(), Power: 100}},
		{Validator: abci.Validator{Address: valConsPk1.Address(), Power: 100}},
	}
	require.NoError(t, s.distrKeeper.AllocateTokens(s.ctx, 200, votes))

	// 2% tax on 100 photon = 2 photon community tax
	// Each validator gets 98*0.5=49 photon reward
	// val0 (50% commission): commission={photon:24.5,stake:24.5}, sharedForF1={photon:24.5}
	//   outstanding = commission + sharedForF1 = {photon:49, stake:24.5}
	// val1 (0% commission): sharedForF1={photon:49}, outstanding={photon:49}
	// stake: val0 gets 24 int auto-staked (dust=0.5 -> community pool), val1 gets 49 int auto-staked
	// community pool: {photon:2, stake:2.5} (2% tax + val0 stake dust 0.5)

	val0OutstandingRewards, err = s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(49)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecWithPrec(245, 1)},
	}, val0OutstandingRewards.Rewards)

	val1OutstandingRewards, err = s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(49)}}, val1OutstandingRewards.Rewards)

	feePool, err = s.distrKeeper.FeePool.Get(s.ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(2)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecWithPrec(25, 1)},
	}, feePool.CommunityPool)

	val0Commission, err = s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr0)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDecWithPrec(245, 1)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecWithPrec(245, 1)},
	}, val0Commission.Commission)

	val1Commission, err = s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr1)
	require.NoError(t, err)
	require.True(t, val1Commission.Commission.IsZero())

	// bond denom is auto staked, so only photon goes to current rewards
	val0CurrentRewards, err = s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDecWithPrec(245, 1)}}, val0CurrentRewards.Rewards)

	val1CurrentRewards, err = s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(49)}}, val1CurrentRewards.Rewards)
}

func TestAllocateTokens_NakamotoBonusDisabled(t *testing.T) {
	s := setupTestKeeper(t, math.LegacyNewDecWithPrec(5, 2), 100) // η = 0.05 (should not matter since disabled)

	// Set nakamoto_bonus_enabled parameter to false
	params, err := s.distrKeeper.Params.Get(s.ctx)
	require.NoError(t, err)
	params.NakamotoBonus.Enabled = false
	require.NoError(t, s.distrKeeper.Params.Set(s.ctx, params))

	// η can be any value, should have no effect
	err = s.distrKeeper.SetNakamotoBonusCoefficient(s.ctx, math.LegacyNewDecWithPrec(5, 2))
	require.NoError(t, err)

	// Setup validators: two validators, equal power, 0% commission
	valAddr0 := sdk.ValAddress(valConsAddr0)
	val0, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	val0.Commission = stakingtypes.NewCommission(
		math.LegacyZeroDec(), math.LegacyZeroDec(), math.LegacyZeroDec(),
	)
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk0)).Return(val0, nil).AnyTimes()

	valAddr1 := sdk.ValAddress(valConsAddr1)
	val1, err := distrtestutil.CreateValidator(valConsPk1, math.NewInt(100))
	require.NoError(t, err)
	val1.Commission = stakingtypes.NewCommission(
		math.LegacyZeroDec(), math.LegacyZeroDec(), math.LegacyZeroDec(),
	)
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk1)).Return(val1, nil).AnyTimes()

	abciValA := abci.Validator{
		Address: valConsPk0.Address(),
		Power:   100,
	}
	abciValB := abci.Validator{
		Address: valConsPk1.Address(),
		Power:   100,
	}

	// photon distributed through F1, bond denom is auto-staked
	fees := sdk.NewCoins(sdk.NewCoin("photon", math.NewInt(100)), sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(100)))
	s.bankKeeper.EXPECT().GetAllBalances(gomock.Any(), s.feeCollectorAcc.GetAddress()).Return(fees)
	s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "fee_collector", disttypes.ModuleName, fees)

	votes := []abci.VoteInfo{
		{Validator: abciValA},
		{Validator: abciValB},
	}

	require.NoError(t, s.distrKeeper.AllocateTokens(s.ctx, 200, votes))

	// With nakamoto_bonus_enabled = false, all rewards are proportional (no bonus).
	// 2% tax: each denom loses 2 -> 98 left, each validator gets 49 photon and 49 bond denom.
	// 0% commission: bond denom is fully auto-staked (49 int, 0 dust), photon goes to F1.
	var expectedCommission sdk.DecCoins
	expectedPhotonReward := sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(49)}}

	val0OutstandingRewards, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.Equal(t, expectedPhotonReward, val0OutstandingRewards.Rewards)

	val1OutstandingRewards, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.Equal(t, expectedPhotonReward, val1OutstandingRewards.Rewards)

	feePool, err := s.distrKeeper.FeePool.Get(s.ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(2)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(2)},
	}, feePool.CommunityPool)

	val0Commission, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr0)
	require.NoError(t, err)
	require.Equal(t, expectedCommission, val0Commission.Commission)

	val1Commission, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr1)
	require.NoError(t, err)
	require.Equal(t, expectedCommission, val1Commission.Commission)

	val0CurrentRewards, err := s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.Equal(t, expectedPhotonReward, val0CurrentRewards.Rewards)

	val1CurrentRewards, err := s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.Equal(t, expectedPhotonReward, val1CurrentRewards.Rewards)
}

func TestAllocateTokensTruncation(t *testing.T) {
	s := setupTestKeeper(t, math.LegacyZeroDec(), 100)

	// reset fee pool
	require.NoError(t, s.distrKeeper.FeePool.Set(s.ctx, disttypes.InitialFeePool()))
	require.NoError(t, s.distrKeeper.Params.Set(s.ctx, disttypes.DefaultParams()))

	// create a validator with 10% commission
	valAddr0 := sdk.ValAddress(valConsAddr0)
	val0, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	val0.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDec(0))
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk0)).Return(val0, nil).AnyTimes()

	// create second validator with 10% commission
	valAddr1 := sdk.ValAddress(valConsAddr1)
	val1, err := distrtestutil.CreateValidator(valConsPk1, math.NewInt(100))
	require.NoError(t, err)
	val1.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDec(0))
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk1)).Return(val1, nil).AnyTimes()

	// create third validator with 10% commission
	valAddr2 := sdk.ValAddress(valConsAddr2)
	val2, err := stakingtypes.NewValidator(sdk.ValAddress(valConsAddr2).String(), valConsPk1, stakingtypes.Description{})
	require.NoError(t, err)
	val2.Commission = stakingtypes.NewCommission(math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDecWithPrec(1, 1), math.LegacyNewDec(0))
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk2)).Return(val2, nil).AnyTimes()

	// assert the initial state: zero outstanding rewards, zero community pool, zero commission, zero current rewards
	val0OutstandingRewards, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.True(t, val0OutstandingRewards.Rewards.IsZero())

	val1OutstandingRewards, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.True(t, val1OutstandingRewards.Rewards.IsZero())

	feePool, err := s.distrKeeper.FeePool.Get(s.ctx)
	require.NoError(t, err)
	require.True(t, feePool.CommunityPool.IsZero())

	val0Commission, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr0)
	require.NoError(t, err)
	require.True(t, val0Commission.Commission.IsZero())

	val1Commission, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr1)
	require.NoError(t, err)
	require.True(t, val1Commission.Commission.IsZero())

	val0CurrentRewards, err := s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.True(t, val0CurrentRewards.Rewards.IsZero())

	val1CurrentRewards, err := s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.True(t, val1CurrentRewards.Rewards.IsZero())

	// allocate tokens as if both had voted and second was proposer
	// photon goes through F1, bond denom is auto-staked
	fees := sdk.NewCoins(sdk.NewCoin("photon", math.NewInt(634195840)), sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(634195840)))
	s.bankKeeper.EXPECT().GetAllBalances(gomock.Any(), s.feeCollectorAcc.GetAddress()).Return(fees)
	s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "fee_collector", disttypes.ModuleName, fees)

	votes := []abci.VoteInfo{
		{Validator: abci.Validator{Address: valConsPk0.Address(), Power: 11}},
		{Validator: abci.Validator{Address: valConsPk1.Address(), Power: 10}},
		{Validator: abci.Validator{Address: valConsPk2.Address(), Power: 10}},
	}
	require.NoError(t, s.distrKeeper.AllocateTokens(s.ctx, 31, votes))

	val0OutstandingRewards, err = s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.True(t, val0OutstandingRewards.Rewards.IsValid())

	val1OutstandingRewards, err = s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.True(t, val1OutstandingRewards.Rewards.IsValid())

	val2OutstandingRewards, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr2)
	require.NoError(t, err)
	require.True(t, val2OutstandingRewards.Rewards.IsValid())
}

func TestAllocateTokensWithNakamotoBonus(t *testing.T) {
	s := setupTestKeeper(t, math.LegacyNewDecWithPrec(2, 1), 100) // η = 0.20

	require.NoError(t, s.distrKeeper.FeePool.Set(s.ctx, disttypes.InitialFeePool()))

	// Create validators with imbalanced power distribution
	// High power validator (80% of voting power)
	valAddr0 := sdk.ValAddress(valConsAddr0)
	val0, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(800))
	require.NoError(t, err)
	val0.Commission = stakingtypes.NewCommission(
		math.LegacyZeroDec(), math.LegacyZeroDec(), math.LegacyZeroDec(),
	)
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk0)).Return(val0, nil).AnyTimes()

	// Low power validator (20% of voting power)
	valAddr1 := sdk.ValAddress(valConsAddr1)
	val1, err := distrtestutil.CreateValidator(valConsPk1, math.NewInt(200))
	require.NoError(t, err)
	val1.Commission = stakingtypes.NewCommission(
		math.LegacyZeroDec(), math.LegacyZeroDec(), math.LegacyZeroDec(),
	)
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), sdk.GetConsAddress(valConsPk1)).Return(val1, nil).AnyTimes()

	// photon goes through F1, bond denom is auto-staked
	fees := sdk.NewCoins(sdk.NewCoin("photon", math.NewInt(1000)), sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(1000)))
	s.bankKeeper.EXPECT().GetAllBalances(gomock.Any(), s.feeCollectorAcc.GetAddress()).Return(fees)
	s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "fee_collector", disttypes.ModuleName, fees)

	votes := []abci.VoteInfo{
		{Validator: abci.Validator{Address: valConsPk0.Address(), Power: 800}},
		{Validator: abci.Validator{Address: valConsPk1.Address(), Power: 200}},
	}

	require.NoError(t, s.distrKeeper.AllocateTokens(s.ctx, 1000, votes))

	// Expectation with η = 0.20, communityTax = 2%, fees = {photon:1000, stake:1000}:
	//
	// After tax (each denom independently):
	//   validatorTotal = 980
	//   nakamotoBonus  = 980 * 0.20 = 196
	//   proportional   = 980 - 196  = 784
	//   nbPerValidator = 196 / 2    = 98
	//
	// Per-validator allocation (same math for photon and stake):
	//   val0 (80%): 784 * 0.8 + 98 = 627.2 + 98 = 725.2
	//   val1 (20%): 784 * 0.2 + 98 = 156.8 + 98 = 254.8
	//
	// bond denom -> auto-staked:
	//   val0: int 725, dust 0.2 -> community pool
	//   val1: int 254, dust 0.8 -> community pool
	//   Community pool: 20 (tax) + 0.2 + 0.8 = 21
	//
	// photon -> F1:
	//   val0 currentRewards = 725.2, val1 currentRewards = 254.8
	//   Community pool photon: 20 (tax, no dust since 725.2+254.8=980 exactly)

	expectedVal0Reward := sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDecWithPrec(7252, 1)}, // 725.2
	}
	expectedVal1Reward := sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDecWithPrec(2548, 1)}, // 254.8
	}

	val0OutstandingRewards, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.Equal(t, expectedVal0Reward, val0OutstandingRewards.Rewards)

	val1OutstandingRewards, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.Equal(t, expectedVal1Reward, val1OutstandingRewards.Rewards)

	feePool, err := s.distrKeeper.FeePool.Get(s.ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{
		{Denom: "photon", Amount: math.LegacyNewDec(20)},
		{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(21)},
	}, feePool.CommunityPool)

	// Zero commission for both validators
	val0Commission, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr0)
	require.NoError(t, err)
	require.True(t, val0Commission.Commission.IsZero())

	val1Commission, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr1)
	require.NoError(t, err)
	require.True(t, val1Commission.Commission.IsZero())

	val0CurrentRewards, err := s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr0)
	require.NoError(t, err)
	require.Equal(t, expectedVal0Reward, val0CurrentRewards.Rewards)

	val1CurrentRewards, err := s.distrKeeper.GetValidatorCurrentRewards(s.ctx, valAddr1)
	require.NoError(t, err)
	require.Equal(t, expectedVal1Reward, val1CurrentRewards.Rewards)

	// The smaller validator (val1) earns more photon per delegator share due to the fixed Nakamoto bonus.
	// val0 RPS = 725.2 / 800 ≈ 0.9065; val1 RPS = 254.8 / 200 = 1.274
	val0PhotonRPS := val0CurrentRewards.Rewards.AmountOf("photon").Quo(math.LegacyNewDecFromInt(val0.DelegatorShares.TruncateInt()))
	val1PhotonRPS := val1CurrentRewards.Rewards.AmountOf("photon").Quo(math.LegacyNewDecFromInt(val1.DelegatorShares.TruncateInt()))
	require.True(t, val1PhotonRPS.GT(val0PhotonRPS), "val1 (smaller validator) should receive more photon per share via Nakamoto bonus")
}
