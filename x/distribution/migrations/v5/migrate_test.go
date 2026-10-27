package v5_test

// The v4->v5 migration touches enough of the distribution keeper's surface
// that the only way to properly test the migration is to do it
// against real keepers spun up via the integration test framework. The test
// still lives in this package (rather than under tests/integration/...) for
// cleanliness.

import (
	"fmt"
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
			f.distrKeeper.FeePool, f.distrKeeper.Params,
			f.bankKeeper, f.stakingKeeper,
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

// TestMigrateStore_InitCommissionAutoStakeEpochIdentifier asserts the
// migration writes the default identifier into Params at the v4->v5
// boundary.
func TestMigrateStore_InitCommissionAutoStakeEpochIdentifier(t *testing.T) {
	t.Parallel()
	f := initFixture(t)
	require.NoError(t, f.distrKeeper.FeePool.Set(f.sdkCtx, distrtypes.InitialFeePool()))

	// Simulate a pre-upgrade Params: same as DefaultParams but with the
	// new field empty (proto zero value), which is what loading a v4
	// Params record under the v5 schema would produce.
	preUpgrade := distrtypes.DefaultParams()
	preUpgrade.CommissionAutoStakeEpochIdentifier = ""
	require.NoError(t, f.distrKeeper.Params.Set(f.sdkCtx, preUpgrade))

	f.runMigration(t)

	post, err := f.distrKeeper.Params.Get(f.sdkCtx)
	require.NoError(t, err)
	require.Equal(t, distrtypes.DefaultCommissionAutoStakeEpochIdentifier, post.CommissionAutoStakeEpochIdentifier)
	// Existing fields preserved (community tax, nakamoto bonus, etc.).
	require.Equal(t, preUpgrade.CommunityTax, post.CommunityTax)
	require.Equal(t, preUpgrade.NakamotoBonus, post.NakamotoBonus)
}

// TestMigrateStore_SelfDelegationOnly covers the sole-delegator-equals-operator
// path. With one validator and only its self-delegation, the bond denom
// delegator-share portion auto-stakes (validator.Tokens grows; per-share
// exchange rate rises), the non-bond delegator-share portion pays out to
// the operator's bank balance, and accumulated commission is preserved
// verbatim across the migration — operators continue to claim it via
// MsgWithdrawValidatorCommission post-upgrade.
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

	preCommission, err := f.distrKeeper.GetValidatorAccumulatedCommission(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.False(t, preCommission.Commission.IsZero(),
		"setup invariant: validator should have accrued some commission")

	prevTokens := val.Tokens
	delAddr := sdk.AccAddress(f.valAddr)
	prePhoton := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, "photon")

	f.runMigration(t)

	updatedVal, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.True(t, updatedVal.Tokens.GT(prevTokens),
		"validator.Tokens should grow from auto-staked bond denom delegator rewards")

	postPhoton := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, "photon")
	require.True(t, postPhoton.Amount.GT(prePhoton.Amount),
		"operator (=sole delegator) photon balance should grow from non-bond F1 payout")

	// Commission is preserved verbatim across the migration
	postCommission, err := f.distrKeeper.GetValidatorAccumulatedCommission(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.Equal(t, preCommission.Commission, postCommission.Commission,
		"accumulatedCommission must be preserved across the migration")

	// OutstandingRewards is reset to exactly the preserved commission
	// (post-delegator-payout dust was swept to the community pool).
	postOutstanding, err := f.distrKeeper.GetValidatorOutstandingRewards(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.Equal(t, preCommission.Commission, postOutstanding.Rewards,
		"outstandingRewards must equal preserved commission post-migration")

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
// unchanged) while non-bond rewards are paid out. Commission is preserved
// verbatim across the migration (no force-pay): the operator's
// accumulatedCommission record is identical pre- and post-migration.
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
	preOpBond := f.bankKeeper.GetBalance(f.sdkCtx, sdk.AccAddress(f.valAddr), sdk.DefaultBondDenom)
	prevTokens := val.Tokens

	preCommission, err := f.distrKeeper.GetValidatorAccumulatedCommission(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.False(t, preCommission.Commission.IsZero(),
		"setup invariant: validator should have accrued some commission")

	f.runMigration(t)

	updatedVal, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.True(t, updatedVal.Tokens.GT(prevTokens),
		"validator.Tokens should grow from auto-staked bond denom delegator rewards")

	postDelBond := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, sdk.DefaultBondDenom)
	require.True(t, postDelBond.Amount.Equal(preDelBond.Amount),
		"external delegator's bond denom balance must be unchanged (auto-staked, not paid out); got %s -> %s",
		preDelBond, postDelBond)

	postDelPhoton := f.bankKeeper.GetBalance(f.sdkCtx, delAddr, "photon")
	require.True(t, postDelPhoton.Amount.GT(preDelPhoton.Amount),
		"external delegator's photon balance should grow from non-bond F1 payout")

	// Commission is left untouched at migration: the operator's
	// bond denom and photon bank balances are unchanged from commission
	// (the only deltas would come from the operator's own delegator-share
	// payout, but in this fixture the operator only has self-delegation
	// shares and those went via the auto-stake path for bond and via F1
	// payout for photon). The accumulatedCommission record is preserved
	// verbatim.
	postOpBond := f.bankKeeper.GetBalance(f.sdkCtx, sdk.AccAddress(f.valAddr), sdk.DefaultBondDenom)
	require.True(t, postOpBond.Amount.Equal(preOpBond.Amount),
		"operator bond denom balance must be unchanged (commission preserved); got %s -> %s",
		preOpBond, postOpBond)

	postCommission, err := f.distrKeeper.GetValidatorAccumulatedCommission(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.Equal(t, preCommission.Commission, postCommission.Commission,
		"accumulatedCommission must be preserved across the migration")

	postOutstanding, err := f.distrKeeper.GetValidatorOutstandingRewards(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.Equal(t, preCommission.Commission, postOutstanding.Rewards,
		"outstandingRewards must equal preserved commission post-migration")

	info, err := f.distrKeeper.GetDelegatorStartingInfo(f.sdkCtx, f.valAddr, delAddr)
	require.NoError(t, err)
	require.Equal(t, uint64(0), info.PreviousPeriod)
	del, err := f.stakingKeeper.GetDelegation(f.sdkCtx, delAddr, f.valAddr)
	require.NoError(t, err)
	require.True(t, info.Stake.Equal(del.GetShares()))
}

// requireStakingPoolsConsistent checks the staking module account invariant:
// the bonded and not-bonded pools hold exactly the tokens of the validators
// in the corresponding status (no unbonding delegations exist in these tests).
func requireStakingPoolsConsistent(t *testing.T, f *fixture) {
	t.Helper()
	bondedTokens, notBondedTokens := math.ZeroInt(), math.ZeroInt()
	vals, err := f.stakingKeeper.GetAllValidators(f.sdkCtx)
	require.NoError(t, err)
	for _, v := range vals {
		if v.IsBonded() {
			bondedTokens = bondedTokens.Add(v.Tokens)
		} else {
			notBondedTokens = notBondedTokens.Add(v.Tokens)
		}
	}
	bondedPool := f.bankKeeper.GetBalance(f.sdkCtx, f.stakingKeeper.GetBondedPool(f.sdkCtx).GetAddress(), sdk.DefaultBondDenom)
	notBondedPool := f.bankKeeper.GetBalance(f.sdkCtx, f.stakingKeeper.GetNotBondedPool(f.sdkCtx).GetAddress(), sdk.DefaultBondDenom)
	require.Equal(t, bondedTokens, bondedPool.Amount, "bonded pool balance must equal bonded validators' tokens")
	require.Equal(t, notBondedTokens, notBondedPool.Amount, "not-bonded pool balance must equal non-bonded validators' tokens")
}

// setPendingLegacyRewards seeds the validator's current period with bond denom
// rewards as the pre-upgrade F1 would have accumulated them, funding the
// distribution module accordingly.
func setPendingLegacyRewards(t *testing.T, f *fixture, amount math.Int) {
	t.Helper()
	f.fundDistribution(t, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, amount)))
	rewards := sdk.NewDecCoinsFromCoins(sdk.NewCoin(sdk.DefaultBondDenom, amount))
	cur, err := f.distrKeeper.GetValidatorCurrentRewards(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.NoError(t, f.distrKeeper.SetValidatorCurrentRewards(f.sdkCtx, f.valAddr,
		distrtypes.NewValidatorCurrentRewards(rewards, cur.Period)))
	require.NoError(t, f.distrKeeper.SetValidatorOutstandingRewards(f.sdkCtx, f.valAddr,
		distrtypes.ValidatorOutstandingRewards{Rewards: rewards}))
}

// TestMigrateStore_PendingRewardsStakedIntoEarningDelegation checks that each
// delegator's pending bond denom rewards are staked into their own delegation
// rather than spread over every delegator of the validator.
func TestMigrateStore_PendingRewardsStakedIntoEarningDelegation(t *testing.T) {
	for _, bonded := range []bool{false, true} {
		t.Run(fmt.Sprintf("bonded=%t", bonded), func(t *testing.T) {
			f := initFixture(t)
			f.fundOperator(t, f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 1000))
			tstaking := stakingtestutil.NewHelper(t, f.sdkCtx, f.stakingKeeper)
			tstaking.Commission = stakingtypes.NewCommissionRates(math.LegacyZeroDec(), math.LegacyNewDecWithPrec(5, 1), math.LegacyZeroDec())
			selfBond := f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 1)
			tstaking.CreateValidator(f.valAddr, valConsPk0, selfBond, true)
			if bonded {
				_, err := f.stakingKeeper.EndBlocker(f.sdkCtx)
				require.NoError(t, err)
			}

			// All pending rewards were earned by the operator, the only
			// delegator so far.
			pending := math.NewInt(1000)
			setPendingLegacyRewards(t, f, pending)

			// A delegator joining afterwards is owed none of them.
			newcomer := sdk.AccAddress(pks[2].Address())
			require.NoError(t, f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, selfBond))))
			require.NoError(t, f.bankKeeper.SendCoinsFromModuleToAccount(f.sdkCtx, distrtypes.ModuleName, newcomer, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, selfBond))))
			tstaking.Delegate(newcomer, f.valAddr, selfBond)

			preOpBalance := f.bankKeeper.GetBalance(f.sdkCtx, sdk.AccAddress(f.valAddr), sdk.DefaultBondDenom)

			f.runMigration(t)

			val, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
			require.NoError(t, err)
			require.Equal(t, bonded, val.IsBonded())
			opDel, err := f.stakingKeeper.GetDelegation(f.sdkCtx, sdk.AccAddress(f.valAddr), f.valAddr)
			require.NoError(t, err)
			newDel, err := f.stakingKeeper.GetDelegation(f.sdkCtx, newcomer, f.valAddr)
			require.NoError(t, err)

			require.Equal(t, math.LegacyNewDecFromInt(selfBond.Add(pending)), val.TokensFromShares(opDel.Shares),
				"operator's pending rewards must be staked into the operator's delegation")
			require.Equal(t, math.LegacyNewDecFromInt(selfBond), val.TokensFromShares(newDel.Shares),
				"newcomer must not receive rewards earned before delegating")
			require.Equal(t, preOpBalance, f.bankKeeper.GetBalance(f.sdkCtx, sdk.AccAddress(f.valAddr), sdk.DefaultBondDenom),
				"pending bond denom rewards must not be paid out")
			requireStakingPoolsConsistent(t, f)

			// F1 bookkeeping must be consistent with the new shares.
			info, err := f.distrKeeper.GetDelegatorStartingInfo(f.sdkCtx, f.valAddr, sdk.AccAddress(f.valAddr))
			require.NoError(t, err)
			require.True(t, info.Stake.Equal(opDel.Shares))
			msg, broken := distrkeeper.ReferenceCountInvariant(f.distrKeeper)(f.sdkCtx)
			require.False(t, broken, msg)
			msg, broken = distrkeeper.ModuleAccountInvariant(f.distrKeeper)(f.sdkCtx)
			require.False(t, broken, msg)
		})
	}
}

// TestMigrateStore_PendingRewardsPaidOutOnInvalidExchangeRate checks that a
// validator which cannot accept delegations (all tokens slashed away) does not
// block the migration: its delegators receive their pending rewards instead.
func TestMigrateStore_PendingRewardsPaidOutOnInvalidExchangeRate(t *testing.T) {
	f := initFixture(t)
	f.fundOperator(t, f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 1000))
	tstaking := stakingtestutil.NewHelper(t, f.sdkCtx, f.stakingKeeper)
	tstaking.Commission = stakingtypes.NewCommissionRates(math.LegacyZeroDec(), math.LegacyNewDecWithPrec(5, 1), math.LegacyZeroDec())
	tstaking.CreateValidator(f.valAddr, valConsPk0, math.NewInt(100), true)

	pending := math.NewInt(1000)
	setPendingLegacyRewards(t, f, pending)
	// Close the period so the rewards are attributed before the tokens vanish.
	val, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	_, err = f.distrKeeper.IncrementValidatorPeriod(f.sdkCtx, val)
	require.NoError(t, err)

	// Simulate a 100% slash: tokens burned, shares left in place.
	require.NoError(t, f.bankKeeper.BurnCoins(f.sdkCtx, stakingtypes.NotBondedPoolName, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, val.Tokens))))
	val.Tokens = math.ZeroInt()
	require.NoError(t, f.stakingKeeper.SetValidator(f.sdkCtx, val))
	require.True(t, val.InvalidExRate())

	preOpBalance := f.bankKeeper.GetBalance(f.sdkCtx, sdk.AccAddress(f.valAddr), sdk.DefaultBondDenom)

	f.runMigration(t)

	postOpBalance := f.bankKeeper.GetBalance(f.sdkCtx, sdk.AccAddress(f.valAddr), sdk.DefaultBondDenom)
	require.Equal(t, preOpBalance.Amount.Add(pending), postOpBalance.Amount)
	requireStakingPoolsConsistent(t, f)
}
