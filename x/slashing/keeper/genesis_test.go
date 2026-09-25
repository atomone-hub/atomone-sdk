package keeper_test

import (
	"context"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttime "github.com/cometbft/cometbft/types/time"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	slashingkeeper "github.com/cosmos/cosmos-sdk/x/slashing/keeper"
	slashingtestutil "github.com/cosmos/cosmos-sdk/x/slashing/testutil"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
)

func (s *KeeperTestSuite) TestExportAndInitGenesis() {
	ctx, keeper := s.ctx, s.slashingKeeper
	require := s.Require()

	keeper.SetParams(ctx, slashingtestutil.TestParams())

	// No validator backs these consensus addresses; address resolution falls
	// back to the address itself.
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	consAddr1 := sdk.ConsAddress(sdk.AccAddress([]byte("addr1_______________")))
	consAddr2 := sdk.ConsAddress(sdk.AccAddress([]byte("addr2_______________")))

	info1 := types.NewValidatorSigningInfo(consAddr1, int64(4), int64(3),
		time.Now().UTC().Add(100000000000), false, int64(10))
	info2 := types.NewValidatorSigningInfo(consAddr2, int64(5), int64(4),
		time.Now().UTC().Add(10000000000), false, int64(10))

	keeper.SetValidatorSigningInfo(ctx, consAddr1, info1)
	keeper.SetValidatorSigningInfo(ctx, consAddr2, info2)
	genesisState := keeper.ExportGenesis(ctx)

	require.Equal(genesisState.Params, slashingtestutil.TestParams())
	require.Len(genesisState.SigningInfos, 2)
	require.Equal(genesisState.SigningInfos[0].ValidatorSigningInfo, info1)

	// Tombstone validators after genesis shouldn't effect genesis state
	err := keeper.Tombstone(ctx, consAddr1)
	require.NoError(err)
	err = keeper.Tombstone(ctx, consAddr2)
	require.NoError(err)

	ok := keeper.IsTombstoned(ctx, consAddr1)
	require.True(ok)

	newInfo1, _ := keeper.GetValidatorSigningInfo(ctx, consAddr1)
	require.NotEqual(info1, newInfo1)

	// Initialize genesis with genesis state before tombstone
	s.stakingKeeper.EXPECT().IterateValidators(ctx, gomock.Any()).Return(nil)
	require.NoError(keeper.InitGenesis(ctx, s.stakingKeeper, genesisState))

	// Validator isTombstoned should return false as GenesisState is initialized
	ok = keeper.IsTombstoned(ctx, consAddr1)
	require.False(ok)

	newInfo1, _ = keeper.GetValidatorSigningInfo(ctx, consAddr1)
	newInfo2, _ := keeper.GetValidatorSigningInfo(ctx, consAddr2)
	require.Equal(info1, newInfo1)
	require.Equal(info2, newInfo2)
}

// TestExportGenesis_RetainedRecordsDoNotDuplicateMissedBlocks covers export
// when no validator resolves (e.g. the validator was removed after rotating
// its key): the retained old-address record and the migrated record share one
// bitmap keyed under the initial address, and only that address's entry may
// carry the missed blocks.
//
// It uses a standalone mock setup because gomock serves the first registered
// matching expectation, so the suite-level ValidatorIdentifier default would
// swallow the per-test rotation mapping.
func TestExportGenesis_RetainedRecordsDoNotDuplicateMissedBlocks(t *testing.T) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := sdktestutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: cmttime.Now()})
	encCfg := moduletestutil.MakeTestEncodingConfig()

	oldConsAddr := sdk.ConsAddress([]byte("oldaddr_____________"))
	newConsAddr := sdk.ConsAddress([]byte("newaddr_____________"))

	ctrl := gomock.NewController(t)
	stakingKeeper := slashingtestutil.NewMockStakingKeeper(ctrl)
	stakingKeeper.EXPECT().ValidatorAddressCodec().Return(address.NewBech32Codec("cosmosvaloper")).AnyTimes()
	stakingKeeper.EXPECT().ConsensusAddressCodec().Return(address.NewBech32Codec("cosmosvalcons")).AnyTimes()
	// No validator backs any address; the rotation maps newConsAddr back to
	// oldConsAddr, where the missed-block bitmap is keyed.
	stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	stakingKeeper.EXPECT().ValidatorIdentifier(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, addr sdk.ConsAddress) (sdk.ConsAddress, error) {
			if addr.Equals(newConsAddr) {
				return oldConsAddr, nil
			}
			return nil, nil
		}).AnyTimes()

	keeper := slashingkeeper.NewKeeper(
		encCfg.Codec,
		encCfg.Amino,
		storeService,
		stakingKeeper,
		authtypes.NewModuleAddress(govtypes.ModuleName).String(),
	)
	keeper.SetParams(ctx, slashingtestutil.TestParams())

	keeper.SetValidatorSigningInfo(ctx, oldConsAddr,
		types.NewValidatorSigningInfo(oldConsAddr, int64(0), int64(0), time.Unix(0, 0), false, int64(0)))
	keeper.SetValidatorSigningInfo(ctx, newConsAddr,
		types.NewValidatorSigningInfo(newConsAddr, int64(0), int64(0), time.Unix(0, 0), false, int64(0)))

	// A missed block recorded through the new key lands under the old key.
	require.NoError(t, keeper.SetMissedBlockBitmapValue(ctx, newConsAddr, 3, true))

	genesisState := keeper.ExportGenesis(ctx)

	missedByAddr := map[string][]types.MissedBlock{}
	totalMissed := 0
	for _, mb := range genesisState.MissedBlocks {
		missedByAddr[mb.Address] = mb.MissedBlocks
		totalMissed += len(mb.MissedBlocks)
	}

	require.Equal(t, 2, len(genesisState.SigningInfos))
	require.Equal(t, 1, totalMissed)
	require.Equal(t, 1, len(missedByAddr[oldConsAddr.String()]))
	require.Equal(t, 0, len(missedByAddr[newConsAddr.String()]))
}
