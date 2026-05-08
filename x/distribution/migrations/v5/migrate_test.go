package v5_test

// The v4->v5 migration touches enough of the distribution keeper's surface
// that the only way to properly test the migration is to do it
// against real keepers spun up via the integration test framework. The test
// still lives in this package (rather than under tests/integration/...) for
// cleanliness.

import (
	"testing"

	cmtabcitypes "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil/integration"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authsims "github.com/cosmos/cosmos-sdk/x/auth/simulation"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/bank"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/distribution"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	v5 "github.com/cosmos/cosmos-sdk/x/distribution/migrations/v5"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
	"github.com/cosmos/cosmos-sdk/x/staking"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtestutil "github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

var (
	pks        = simtestutil.CreateTestPubKeys(3)
	valConsPk0 = pks[0]
)

// fixture wires up the real auth, bank, staking and distribution keepers.
type fixture struct {
	sdkCtx        sdk.Context
	cdc           codec.Codec
	storeKeys     map[string]*storetypes.KVStoreKey
	bankKeeper    bankkeeper.Keeper
	distrKeeper   distrkeeper.Keeper
	stakingKeeper *stakingkeeper.Keeper

	addr    sdk.AccAddress
	valAddr sdk.ValAddress
}

func initFixture(t testing.TB) *fixture {
	t.Helper()
	keys := storetypes.NewKVStoreKeys(
		authtypes.StoreKey, banktypes.StoreKey, distrtypes.StoreKey, stakingtypes.StoreKey,
	)
	cdc := moduletestutil.MakeTestEncodingConfig(auth.AppModuleBasic{}, distribution.AppModuleBasic{}).Codec

	logger := log.NewTestLogger(t)
	cms := integration.CreateMultiStore(keys, logger)
	newCtx := sdk.NewContext(cms, cmttypes.Header{}, true, logger)
	authority := authtypes.NewModuleAddress("gov")

	maccPerms := map[string][]string{
		distrtypes.ModuleName:          {authtypes.Minter},
		minttypes.ModuleName:           {authtypes.Minter},
		stakingtypes.BondedPoolName:    {authtypes.Burner, authtypes.Staking},
		stakingtypes.NotBondedPoolName: {authtypes.Burner, authtypes.Staking},
	}

	accountKeeper := authkeeper.NewAccountKeeper(
		cdc,
		runtime.NewKVStoreService(keys[authtypes.StoreKey]),
		authtypes.ProtoBaseAccount,
		maccPerms,
		addresscodec.NewBech32Codec(sdk.Bech32MainPrefix),
		sdk.Bech32MainPrefix,
		authority.String(),
	)
	bankKeeper := bankkeeper.NewBaseKeeper(
		cdc,
		runtime.NewKVStoreService(keys[banktypes.StoreKey]),
		accountKeeper,
		map[string]bool{accountKeeper.GetAuthority(): false},
		authority.String(),
		log.NewNopLogger(),
	)
	stakingKeeper := stakingkeeper.NewKeeper(
		cdc, runtime.NewKVStoreService(keys[stakingtypes.StoreKey]),
		accountKeeper, bankKeeper, authority.String(),
		addresscodec.NewBech32Codec(sdk.Bech32PrefixValAddr),
		addresscodec.NewBech32Codec(sdk.Bech32PrefixConsAddr),
	)
	distrKeeper := distrkeeper.NewKeeper(
		cdc, runtime.NewKVStoreService(keys[distrtypes.StoreKey]),
		accountKeeper, bankKeeper, stakingKeeper,
		distrtypes.ModuleName, authority.String(),
	)

	addr := sdk.AccAddress(pks[0].Address())
	valAddr := sdk.ValAddress(addr)
	valConsAddr := sdk.ConsAddress(valConsPk0.Address())

	ctx := newCtx.WithProposer(valConsAddr).WithVoteInfos([]cmtabcitypes.VoteInfo{
		{Validator: cmtabcitypes.Validator{Address: valAddr, Power: 100}, BlockIdFlag: cmttypes.BlockIDFlagCommit},
	})

	app := integration.NewIntegrationApp(ctx, logger, keys, cdc, map[string]appmodule.AppModule{
		authtypes.ModuleName:    auth.NewAppModule(cdc, accountKeeper, authsims.RandomGenesisAccounts, nil),
		banktypes.ModuleName:    bank.NewAppModule(cdc, bankKeeper, accountKeeper, nil),
		stakingtypes.ModuleName: staking.NewAppModule(cdc, stakingKeeper, accountKeeper, bankKeeper, nil),
		distrtypes.ModuleName:   distribution.NewAppModule(cdc, distrKeeper, accountKeeper, bankKeeper, stakingKeeper, nil),
	})

	sdkCtx := sdk.UnwrapSDKContext(app.Context())
	require.NoError(t, stakingKeeper.SetParams(sdkCtx, stakingtypes.DefaultParams()))
	stakingKeeper.SetHooks(stakingtypes.NewMultiStakingHooks(distrKeeper.Hooks()))

	return &fixture{
		sdkCtx:        sdkCtx,
		cdc:           cdc,
		storeKeys:     keys,
		bankKeeper:    bankKeeper,
		distrKeeper:   distrKeeper,
		stakingKeeper: stakingKeeper,
		addr:          addr,
		valAddr:       valAddr,
	}
}

// runMigration is a tiny wrapper over v5.MigrateStore that pulls the
// storeService and codec out of the fixture so test bodies stay readable.
func (f *fixture) runMigration(t testing.TB) {
	t.Helper()
	require.NoError(t,
		v5.MigrateStore(
			f.sdkCtx, f.distrKeeper,
			runtime.NewKVStoreService(f.storeKeys[distrtypes.StoreKey]),
			f.cdc,
			f.distrKeeper.FeePool, f.bankKeeper, f.stakingKeeper,
		),
	)
}

// fundOperator mints `amount` of `bondDenom` to the distribution module and
// forwards it to the operator account so CreateValidator's self-delegation
// has spendable balance.
func (f *fixture) fundOperator(t testing.TB, amount math.Int) {
	t.Helper()
	bondDenom, err := f.stakingKeeper.BondDenom(f.sdkCtx)
	require.NoError(t, err)
	require.NoError(t, f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName, sdk.NewCoins(sdk.NewCoin(bondDenom, amount))))
	require.NoError(t, f.bankKeeper.SendCoinsFromModuleToAccount(f.sdkCtx, distrtypes.ModuleName, sdk.AccAddress(f.valAddr), sdk.NewCoins(sdk.NewCoin(bondDenom, amount))))
}

// fundDistribution mints reward coins directly into the distribution module
// so AllocateTokensToValidator (which sends bond denom out of the module to
// the bonded pool) and the migration's payouts have something to draw from.
func (f *fixture) fundDistribution(t testing.TB, coins sdk.Coins) {
	t.Helper()
	require.NoError(t, f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName, coins))
}

// TestMigrateStore_NoState runs the migration on an empty chain. It must
// complete without error and leave the FeePool untouched.
func TestMigrateStore_NoState(t *testing.T) {
	t.Parallel()
	f := initFixture(t)
	require.NoError(t, f.distrKeeper.FeePool.Set(f.sdkCtx, distrtypes.InitialFeePool()))

	f.runMigration(t)

	fp, err := f.distrKeeper.FeePool.Get(f.sdkCtx)
	require.NoError(t, err)
	require.True(t, fp.CommunityPool.IsZero())
}

// TestMigrateStore_SelfDelegationOnly covers the sole-delegator-equals-operator
// path. With one validator and only its self-delegation, every bond denom
// reward eventually ends up in validator.Tokens (delegator portion via
// auto-stake; commission portion paid back to the operator who bonds it
// later) and every non-bond reward ends up in the operator's bank balance.
func TestMigrateStore_SelfDelegationOnly(t *testing.T) {
	t.Parallel()
	f := initFixture(t)
	f.fundOperator(t, f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 1000))

	tstaking := stakingtestutil.NewHelper(t, f.sdkCtx, f.stakingKeeper)
	tstaking.Commission = stakingtypes.NewCommissionRates(
		math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0),
	)
	tstaking.CreateValidator(f.valAddr, valConsPk0, math.NewInt(100), true)

	f.fundDistribution(t, sdk.NewCoins(
		sdk.NewCoin("photon", math.NewInt(1000)),
		sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(100)),
	))
	val, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	_, err = f.distrKeeper.AllocateTokensToValidator(f.sdkCtx, val, sdk.DecCoins{
		sdk.NewDecCoin("photon", math.NewInt(1000)),
		sdk.NewDecCoin(sdk.DefaultBondDenom, math.NewInt(100)),
	})
	require.NoError(t, err)

	prevTokens := val.Tokens
	delAddr := sdk.AccAddress(f.valAddr)
	prePhoton := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, "photon")

	f.runMigration(t)

	updatedVal, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.True(t, updatedVal.Tokens.GT(prevTokens),
		"validator.Tokens should grow from auto-staked bond-denom delegator rewards")

	postPhoton := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, "photon")
	require.True(t, postPhoton.Amount.GT(prePhoton.Amount),
		"operator (=sole delegator) photon balance should grow from non-bond F1 payout")

	current, err := f.distrKeeper.GetValidatorCurrentRewards(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.True(t, current.Rewards.IsZero())
	require.Equal(t, uint64(1), current.Period)

	info, err := f.distrKeeper.GetDelegatorStartingInfo(f.sdkCtx, f.valAddr, delAddr)
	require.NoError(t, err)
	require.Equal(t, uint64(0), info.PreviousPeriod)
	delegation, err := f.stakingKeeper.GetDelegation(f.sdkCtx, delAddr, f.valAddr)
	require.NoError(t, err)
	require.True(t, info.Stake.Equal(delegation.GetShares()))
}

// TestMigrateStore_OperatorAndExternalDelegator separates the commission and
// delegator-payout paths. An external account delegates separately from the
// validator's self-delegation so the bond denom delegator-share rewards are
// auto-staked (validator.Tokens grows; external delegator's bond balance
// unchanged) while non-bond rewards are paid out. Commission is NOT auto-staked,
// so the operator's bond balance grows by the bond denom commission portion.
func TestMigrateStore_OperatorAndExternalDelegator(t *testing.T) {
	t.Parallel()
	f := initFixture(t)
	f.fundOperator(t, f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 1000))

	delAddr := sdk.AccAddress(pks[2].Address())
	require.NoError(t, f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName,
		sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 1000)))))
	require.NoError(t, f.bankKeeper.SendCoinsFromModuleToAccount(f.sdkCtx, distrtypes.ModuleName,
		delAddr, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 1000)))))

	tstaking := stakingtestutil.NewHelper(t, f.sdkCtx, f.stakingKeeper)
	tstaking.Commission = stakingtypes.NewCommissionRates(
		math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDecWithPrec(5, 1), math.LegacyNewDec(0),
	)
	tstaking.CreateValidator(f.valAddr, valConsPk0, math.NewInt(100), true)
	tstaking.Delegate(delAddr, f.valAddr, math.NewInt(100))

	f.fundDistribution(t, sdk.NewCoins(
		sdk.NewCoin("photon", math.NewInt(1000)),
		sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(1000)),
	))
	val, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	_, err = f.distrKeeper.AllocateTokensToValidator(f.sdkCtx, val, sdk.DecCoins{
		sdk.NewDecCoin("photon", math.NewInt(1000)),
		sdk.NewDecCoin(sdk.DefaultBondDenom, math.NewInt(1000)),
	})
	require.NoError(t, err)

	preDelBond := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, sdk.DefaultBondDenom)
	preDelPhoton := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, "photon")
	prevTokens := val.Tokens

	f.runMigration(t)

	updatedVal, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.True(t, updatedVal.Tokens.GT(prevTokens),
		"validator.Tokens should grow from auto-staked bond-denom delegator rewards")

	postDelBond := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, sdk.DefaultBondDenom)
	require.True(t, postDelBond.Amount.Equal(preDelBond.Amount),
		"external delegator's bond-denom balance must be unchanged (auto-staked, not paid out); got %s -> %s",
		preDelBond, postDelBond)

	postDelPhoton := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, "photon")
	require.True(t, postDelPhoton.Amount.GT(preDelPhoton.Amount),
		"external delegator's photon balance should grow from non-bond F1 payout")

	info, err := f.distrKeeper.GetDelegatorStartingInfo(f.sdkCtx, f.valAddr, delAddr)
	require.NoError(t, err)
	require.Equal(t, uint64(0), info.PreviousPeriod)
	del, err := f.stakingKeeper.GetDelegation(f.sdkCtx, delAddr, f.valAddr)
	require.NoError(t, err)
	require.True(t, info.Stake.Equal(del.GetShares()))
}
