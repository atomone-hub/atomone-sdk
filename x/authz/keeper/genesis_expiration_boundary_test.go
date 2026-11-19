package keeper_test

import (
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/cosmos/cosmos-sdk/x/authz/keeper"
	authzmodule "github.com/cosmos/cosmos-sdk/x/authz/module"
	authztestutil "github.com/cosmos/cosmos-sdk/x/authz/testutil"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// TestInitGenesisExpirationEqualBlockTime checks that InitGenesis only
// imports grants whose expiration is strictly after the genesis block time —
// the exact complement of NewGrant's invariant — and imports nothing else:
// a boundary grant would be rejected by SaveGrant and panic InitChain on
// every node importing the genesis.
func TestInitGenesisExpirationEqualBlockTime(t *testing.T) {
	key := storetypes.NewKVStoreKey(keeper.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))

	blockTime := time.Now().UTC().Round(0)
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Height: 1, Time: blockTime})

	encCfg := moduletestutil.MakeTestEncodingConfig(authzmodule.AppModuleBasic{})

	ctrl := gomock.NewController(t)
	accountKeeper := authztestutil.NewMockAccountKeeper(ctrl)
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()

	baseApp := baseapp.NewBaseApp("authz", log.NewNopLogger(), testCtx.DB, encCfg.TxConfig.TxDecoder())
	baseApp.SetCMS(testCtx.CMS)

	bank.RegisterInterfaces(encCfg.InterfaceRegistry)

	msr := baseApp.MsgServiceRouter()
	msr.SetInterfaceRegistry(encCfg.InterfaceRegistry)

	k := keeper.NewKeeper(storeService, encCfg.Codec, msr, accountKeeper)

	grantee := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
	granter := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
	otherGranter := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())

	newAuth := func() *codectypes.Any {
		a, err := codectypes.NewAnyWithValue(&bank.SendAuthorization{
			SpendLimit: sdk.NewCoins(sdk.NewCoin("foo", sdkmath.NewInt(1_000))),
		})
		if err != nil {
			t.Fatalf("NewAnyWithValue: %v", err)
		}
		return a
	}

	past := blockTime.Add(-time.Hour)
	boundary := blockTime // expiration exactly equal to the genesis block time
	future := blockTime.Add(time.Hour)

	genesis := authz.NewGenesisState([]authz.GrantAuthorization{
		// strictly past: always skipped
		{Granter: otherGranter.String(), Grantee: grantee.String(), Expiration: &past, Authorization: newAuth()},
		// boundary: not strictly after the genesis time, skipped
		{Granter: granter.String(), Grantee: grantee.String(), Expiration: &boundary, Authorization: newAuth()},
		// strictly future: must be imported
		{Granter: granter.String(), Grantee: grantee.String(), Expiration: &future, Authorization: newAuth()},
	})

	// Must not panic.
	k.InitGenesis(ctx, genesis)

	// Only the strictly-future grant survives the import.
	exported := k.ExportGenesis(ctx)
	if len(exported.Authorization) != 1 {
		t.Fatalf("expected exactly 1 imported grant (the future one), got %d", len(exported.Authorization))
	}
	if !exported.Authorization[0].Expiration.Equal(future) {
		t.Fatalf("expected the surviving grant to expire at %v, got %v", future, exported.Authorization[0].Expiration)
	}
}
