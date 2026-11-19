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

// TestIterateBondedValidatorsByPowerStopsAtZeroPowerSuffix guards the bound
// on the bonded-validator power-index walks (IterateBondedValidatorsByPower,
// GetBondedValidatorsByPower), which are reached with no gas bound from the
// gov EndBlocker tally/quorum checks and the distribution epoch hook. The
// walks must stop at the zero-power suffix of the power-descending index:
// bonded validators always have consensus power > 0 once the EndBlocker has
// run, and the unbonded dust validators anyone can plant via
// MsgCreateValidator sit in that suffix forever.
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

	// The walk may only pay O(bonded) store reads. The gas budget below fits
	// a bounded walk but not one over every planted entry: the bonded count
	// (1) is below MaxValidators (100), so the collected-bonded cap alone
	// never stops the walk.
	gasCtx := ctx.WithGasMeter(storetypes.NewGasMeter(20_000))
	var visited []string
	err := keeper.IterateBondedValidatorsByPower(gasCtx, func(_ int64, validator stakingtypes.ValidatorI) (stop bool) {
		visited = append(visited, validator.GetOperator())
		return false
	})
	require.NoError(t, err)
	require.Equal(t, []string{bondedVal.OperatorAddress}, visited)

	// A dangling zero-power index entry proves the walk never visits the
	// zero-power suffix: a walk that fetched every visited entry's record
	// would panic on this missing one.
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
