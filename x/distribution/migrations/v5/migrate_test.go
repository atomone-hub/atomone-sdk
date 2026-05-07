package v5_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/distribution"
	v5 "github.com/cosmos/cosmos-sdk/x/distribution/migrations/v5"
	dstrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
)

// setupFeePool creates a FeePool collection backed by the provided store key.
func setupFeePool(t *testing.T, ctx sdk.Context, storeKey *storetypes.KVStoreKey) collections.Item[dstrtypes.FeePool] {
	t.Helper()
	cdc := moduletestutil.MakeTestEncodingConfig(distribution.AppModuleBasic{}).Codec
	sb := collections.NewSchemaBuilder(runtime.NewKVStoreService(storeKey))
	fp := collections.NewItem(sb, dstrtypes.FeePoolKey, "fee_pool", codec.CollValue[dstrtypes.FeePool](cdc))
	_, err := sb.Build()
	require.NoError(t, err)
	require.NoError(t, fp.Set(ctx, dstrtypes.InitialFeePool()))
	return fp
}

// testDistribKeeper is an in-memory implementation of the distributionKeeper interface.
type testDistribKeeper struct {
	current     map[string]dstrtypes.ValidatorCurrentRewards
	outstanding map[string]dstrtypes.ValidatorOutstandingRewards
}

func newTestDistribKeeper() *testDistribKeeper {
	return &testDistribKeeper{
		current:     make(map[string]dstrtypes.ValidatorCurrentRewards),
		outstanding: make(map[string]dstrtypes.ValidatorOutstandingRewards),
	}
}

func (k *testDistribKeeper) IterateValidatorCurrentRewards(_ context.Context, fn func(sdk.ValAddress, dstrtypes.ValidatorCurrentRewards) bool) {
	for addr, r := range k.current {
		if fn(sdk.ValAddress(addr), r) {
			break
		}
	}
}

func (k *testDistribKeeper) SetValidatorCurrentRewards(_ context.Context, val sdk.ValAddress, r dstrtypes.ValidatorCurrentRewards) error {
	k.current[string(val)] = r
	return nil
}

func (k *testDistribKeeper) GetValidatorOutstandingRewards(_ context.Context, val sdk.ValAddress) (dstrtypes.ValidatorOutstandingRewards, error) {
	return k.outstanding[string(val)], nil
}

func (k *testDistribKeeper) SetValidatorOutstandingRewards(_ context.Context, val sdk.ValAddress, r dstrtypes.ValidatorOutstandingRewards) error {
	k.outstanding[string(val)] = r
	return nil
}

// testBankKeeper records coins sent to the bonded pool.
type testBankKeeper struct{ sent sdk.Coins }

func (b *testBankKeeper) SendCoinsFromModuleToModule(_ context.Context, _, _ string, amt sdk.Coins) error {
	b.sent = b.sent.Add(amt...)
	return nil
}

// testStakingKeeper records tokens added per validator.
type testStakingKeeper struct {
	bondDenom string
	added     map[string]math.Int
}

func (s *testStakingKeeper) BondDenom(_ context.Context) (string, error) {
	return s.bondDenom, nil
}

func (s *testStakingKeeper) AddValidatorTokens(_ context.Context, addr sdk.ValAddress, amt math.Int) error {
	prev, ok := s.added[string(addr)]
	if !ok {
		prev = math.ZeroInt()
	}
	s.added[string(addr)] = prev.Add(amt)
	return nil
}

func TestMigrateStore_BondDenomFlushedToPool(t *testing.T) {
	storeKey := storetypes.NewKVStoreKey("distribution_v5")
	tKey := storetypes.NewTransientStoreKey("transient_test")
	ctx := testutil.DefaultContextWithDB(t, storeKey, tKey).Ctx
	fpColl := setupFeePool(t, ctx, storeKey)

	dk := newTestDistribKeeper()
	bk := &testBankKeeper{}
	sk := &testStakingKeeper{bondDenom: sdk.DefaultBondDenom, added: make(map[string]math.Int)}

	valAddr := sdk.ValAddress([]byte("val-addr-1----------"))

	// 90 stake (bond) + 10 photon (non-bond) in current and outstanding rewards
	dk.current[string(valAddr)] = dstrtypes.ValidatorCurrentRewards{
		Rewards: sdk.DecCoins{
			{Denom: "photon", Amount: math.LegacyNewDec(10)},
			{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(90)},
		},
	}
	dk.outstanding[string(valAddr)] = dstrtypes.ValidatorOutstandingRewards{
		Rewards: sdk.DecCoins{
			{Denom: "photon", Amount: math.LegacyNewDec(10)},
			{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDec(90)},
		},
	}

	require.NoError(t, v5.MigrateStore(ctx, dk, fpColl, bk, sk))

	// Bond denom removed from current rewards; photon unchanged.
	cur := dk.current[string(valAddr)]
	require.True(t, cur.Rewards.AmountOf(sdk.DefaultBondDenom).IsZero())
	require.Equal(t, math.LegacyNewDec(10), cur.Rewards.AmountOf("photon"))

	// Outstanding rewards updated likewise.
	out := dk.outstanding[string(valAddr)]
	require.True(t, out.Rewards.AmountOf(sdk.DefaultBondDenom).IsZero())
	require.Equal(t, math.LegacyNewDec(10), out.Rewards.AmountOf("photon"))

	// 90 stake coins transferred to bonded pool.
	require.Equal(t, sdk.Coins{sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(90))}, bk.sent)
	require.Equal(t, math.NewInt(90), sk.added[string(valAddr)])

	// Exactly 90 (no decimal part) -> no community pool dust.
	fp, err := fpColl.Get(ctx)
	require.NoError(t, err)
	require.True(t, fp.CommunityPool.IsZero())
}

func TestMigrateStore_DecimalDustToCommunityPool(t *testing.T) {
	storeKey := storetypes.NewKVStoreKey("distribution_v5")
	tKey := storetypes.NewTransientStoreKey("transient_test")
	ctx := testutil.DefaultContextWithDB(t, storeKey, tKey).Ctx
	fpColl := setupFeePool(t, ctx, storeKey)

	dk := newTestDistribKeeper()
	bk := &testBankKeeper{}
	sk := &testStakingKeeper{bondDenom: sdk.DefaultBondDenom, added: make(map[string]math.Int)}

	valAddr := sdk.ValAddress([]byte("val-addr-1----------"))

	// 90.5 stake: integer (90) -> bonded pool; decimal (0.5) -> community pool.
	dk.current[string(valAddr)] = dstrtypes.ValidatorCurrentRewards{
		Rewards: sdk.DecCoins{{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecWithPrec(905, 1)}},
	}
	dk.outstanding[string(valAddr)] = dstrtypes.ValidatorOutstandingRewards{
		Rewards: sdk.DecCoins{{Denom: sdk.DefaultBondDenom, Amount: math.LegacyNewDecWithPrec(905, 1)}},
	}

	require.NoError(t, v5.MigrateStore(ctx, dk, fpColl, bk, sk))

	require.Equal(t, sdk.Coins{sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(90))}, bk.sent)
	require.Equal(t, math.NewInt(90), sk.added[string(valAddr)])

	fp, err := fpColl.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, math.LegacyNewDecWithPrec(5, 1), fp.CommunityPool.AmountOf(sdk.DefaultBondDenom))
}

func TestMigrateStore_NoBondDenomRewards(t *testing.T) {
	storeKey := storetypes.NewKVStoreKey("distribution_v5")
	tKey := storetypes.NewTransientStoreKey("transient_test")
	ctx := testutil.DefaultContextWithDB(t, storeKey, tKey).Ctx
	fpColl := setupFeePool(t, ctx, storeKey)

	dk := newTestDistribKeeper()
	bk := &testBankKeeper{}
	sk := &testStakingKeeper{bondDenom: sdk.DefaultBondDenom, added: make(map[string]math.Int)}

	valAddr := sdk.ValAddress([]byte("val-addr-1----------"))

	// Only non-bond rewards: migration should leave everything unchanged.
	dk.current[string(valAddr)] = dstrtypes.ValidatorCurrentRewards{
		Rewards: sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(50)}},
	}
	dk.outstanding[string(valAddr)] = dstrtypes.ValidatorOutstandingRewards{
		Rewards: sdk.DecCoins{{Denom: "photon", Amount: math.LegacyNewDec(50)}},
	}

	require.NoError(t, v5.MigrateStore(ctx, dk, fpColl, bk, sk))

	// Nothing sent to bonded pool, nothing added to validator.
	require.Empty(t, bk.sent)
	require.Empty(t, sk.added)

	// Rewards unchanged.
	require.Equal(t, math.LegacyNewDec(50), dk.current[string(valAddr)].Rewards.AmountOf("photon"))
	require.Equal(t, math.LegacyNewDec(50), dk.outstanding[string(valAddr)].Rewards.AmountOf("photon"))

	fp, err := fpColl.Get(ctx)
	require.NoError(t, err)
	require.True(t, fp.CommunityPool.IsZero())
}
