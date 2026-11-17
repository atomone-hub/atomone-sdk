package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtestutil "github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestIterateBondedValidatorsByPowerStopsAtZeroPowerSuffix is a regression
// test for unmetered full power-index walks in
// IterateBondedValidatorsByPower and GetBondedValidatorsByPower (reached with
// no gas bound from the gov EndBlocker tally/quorum checks and the
// distribution epoch hook). The walks counted only bonded validators against
// MaxValidators, so whenever fewer than MaxValidators validators were bonded
// they paid a store read plus a full unmarshal for every entry of the power
// index, including the unbonded zero-power validators anyone can permanently
// plant via MsgCreateValidator with a 1-token self-delegation. The walks must
// instead stop at the first zero-power key: bonded validators always have
// consensus power > 0 (the EndBlocker never bonds zero-power validators), and
// the reverse power-order iteration makes zero-power entries a contiguous
// suffix.
func TestIterateBondedValidatorsByPowerStopsAtZeroPowerSuffix(t *testing.T) {
	key := storetypes.NewKVStoreKey(stakingtypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := sdktestutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	ctx := testCtx.Ctx
	encCfg := moduletestutil.MakeTestEncodingConfig()

	ctrl := gomock.NewController(t)
	accountKeeper := stakingtestutil.NewMockAccountKeeper(ctrl)
	accountKeeper.EXPECT().GetModuleAddress(stakingtypes.BondedPoolName).Return(bondedAcc.GetAddress())
	accountKeeper.EXPECT().GetModuleAddress(stakingtypes.NotBondedPoolName).Return(notBondedAcc.GetAddress())
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()

	keeper := stakingkeeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		stakingtestutil.NewMockBankKeeper(ctrl),
		authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		address.NewBech32Codec("cosmosvaloper"),
		address.NewBech32Codec("cosmosvalcons"),
	)
	require.NoError(t, keeper.SetParams(ctx, stakingtypes.DefaultParams()))

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")

	// one bonded validator with consensus power 1
	bondedPubKey := PKs[0]
	bondedAddr := sdk.ValAddress(bondedPubKey.Address().Bytes())
	bondedVal := stakingtestutil.NewValidator(t, bondedAddr, bondedPubKey)
	bondedVal, _ = bondedVal.AddTokensFromDel(keeper.TokensFromConsensusPower(ctx, 1))
	bondedVal.Status = stakingtypes.Bonded
	require.NoError(t, keeper.SetValidator(ctx, bondedVal))
	require.NoError(t, keeper.SetValidatorByPowerIndex(ctx, bondedVal))

	// the attacker's permanent state: unbonded validators holding a single
	// dust token, i.e. zero consensus power, whose power-index entries
	// MsgCreateValidator writes unconditionally and nothing ever removes
	const planted = 200
	for i := 1; i <= planted; i++ {
		pubKey := PKs[i]
		val := stakingtestutil.NewValidator(t, sdk.ValAddress(pubKey.Address().Bytes()), pubKey)
		val, _ = val.AddTokensFromDel(math.OneInt())
		require.Equal(t, stakingtypes.Unbonded, val.Status)
		require.NoError(t, keeper.SetValidator(ctx, val))
		require.NoError(t, keeper.SetValidatorByPowerIndex(ctx, val))
	}

	// The walk may only pay O(bonded) store reads. Give it a gas budget that
	// a bounded walk easily fits in but a walk over every planted entry
	// cannot: the bonded count (1) is below MaxValidators (100), so the
	// unpatched loop walked the whole planted suffix.
	gasCtx := ctx.WithGasMeter(storetypes.NewGasMeter(20_000))
	var visited []string
	err := keeper.IterateBondedValidatorsByPower(gasCtx, func(_ int64, validator stakingtypes.ValidatorI) (stop bool) {
		visited = append(visited, validator.GetOperator())
		return false
	})
	require.NoError(t, err)
	require.Equal(t, []string{bondedVal.OperatorAddress}, visited)

	// A dangling zero-power index entry proves the walk never visits the
	// zero-power suffix at all: the unpatched walk fetched the record of
	// every entry it visited and panicked on a missing one.
	poisonPubKey := PKs[planted+1]
	poisonAddr := sdk.ValAddress(poisonPubKey.Address().Bytes())
	poisonStr, err := valAddrCodec.BytesToString(poisonAddr)
	require.NoError(t, err)
	poisonKey := stakingtypes.GetValidatorsByPowerIndexKey(
		stakingtypes.Validator{OperatorAddress: poisonStr, Tokens: math.OneInt()},
		keeper.PowerReduction(ctx),
		valAddrCodec,
	)
	ctx.KVStore(key).Set(poisonKey, []byte(poisonAddr))

	visited = nil
	err = keeper.IterateBondedValidatorsByPower(ctx, func(_ int64, validator stakingtypes.ValidatorI) (stop bool) {
		visited = append(visited, validator.GetOperator())
		return false
	})
	require.NoError(t, err)
	require.Equal(t, []string{bondedVal.OperatorAddress}, visited)

	// the distribution-epoch sibling walk (GetBondedValidatorsByPower) must
	// be bounded the same way
	vals, err := keeper.GetBondedValidatorsByPower(ctx.WithGasMeter(storetypes.NewGasMeter(20_000)))
	require.NoError(t, err)
	require.Len(t, vals, 1)
	require.Equal(t, bondedVal.OperatorAddress, vals[0].OperatorAddress)
}
