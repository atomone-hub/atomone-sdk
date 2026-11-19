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

// TestIterateBondedValidatorsByPowerStopsAtZeroPowerSuffix checks that the
// bonded-validator power-index walks (unmetered: gov EndBlocker tally and the
// distribution epoch hook) stop at the zero-power suffix instead of walking
// every planted dust entry.
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

	// unbonded dust validators: zero consensus power, index entries permanent
	const planted = 200
	for i := 1; i <= planted; i++ {
		pubKey := PKs[i]
		val := stakingtestutil.NewValidator(t, sdk.ValAddress(pubKey.Address().Bytes()), pubKey)
		val, _ = val.AddTokensFromDel(math.OneInt())
		require.Equal(t, stakingtypes.Unbonded, val.Status)
		require.NoError(t, keeper.SetValidator(ctx, val))
		require.NoError(t, keeper.SetValidatorByPowerIndex(ctx, val))
	}

	// The gas budget fits a walk over the bonded set but not over the planted
	// entries (bonded < MaxValidators, so the cap alone never stops it).
	gasCtx := ctx.WithGasMeter(storetypes.NewGasMeter(20_000))
	var visited []string
	err := keeper.IterateBondedValidatorsByPower(gasCtx, func(_ int64, validator stakingtypes.ValidatorI) (stop bool) {
		visited = append(visited, validator.GetOperator())
		return false
	})
	require.NoError(t, err)
	require.Equal(t, []string{bondedVal.OperatorAddress}, visited)

	// A dangling zero-power entry: a walk that fetched it would panic on the
	// missing record.
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

	// GetBondedValidatorsByPower is bounded the same way
	vals, err := keeper.GetBondedValidatorsByPower(ctx.WithGasMeter(storetypes.NewGasMeter(20_000)))
	require.NoError(t, err)
	require.Len(t, vals, 1)
	require.Equal(t, bondedVal.OperatorAddress, vals[0].OperatorAddress)
}
