package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/mint"
	"github.com/cosmos/cosmos-sdk/x/mint/keeper"
	minttestutil "github.com/cosmos/cosmos-sdk/x/mint/testutil"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

var minterAcc = authtypes.NewEmptyModuleAccount(types.ModuleName, authtypes.Minter)

type GenesisTestSuite struct {
	suite.Suite

	sdkCtx        sdk.Context
	keeper        keeper.Keeper
	cdc           codec.BinaryCodec
	accountKeeper types.AccountKeeper
	key           *storetypes.KVStoreKey
}

func TestGenesisTestSuite(t *testing.T) {
	suite.Run(t, new(GenesisTestSuite))
}

func (s *GenesisTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(s.T(), key, storetypes.NewTransientStoreKey("transient_test"))
	encCfg := moduletestutil.MakeTestEncodingConfig(mint.AppModuleBasic{})

	// gomock initializations
	ctrl := gomock.NewController(s.T())
	s.cdc = codec.NewProtoCodec(encCfg.InterfaceRegistry)
	s.sdkCtx = testCtx.Ctx
	s.key = key

	stakingKeeper := minttestutil.NewMockStakingKeeper(ctrl)
	accountKeeper := minttestutil.NewMockAccountKeeper(ctrl)
	bankKeeper := minttestutil.NewMockBankKeeper(ctrl)
	s.accountKeeper = accountKeeper
	accountKeeper.EXPECT().GetModuleAddress(minterAcc.Name).Return(minterAcc.GetAddress())
	accountKeeper.EXPECT().GetModuleAccount(s.sdkCtx, minterAcc.Name).Return(minterAcc).AnyTimes()

	s.keeper = keeper.NewKeeper(s.cdc, runtime.NewKVStoreService(key), stakingKeeper, accountKeeper, bankKeeper, "", "")
}

func (s *GenesisTestSuite) TestImportExportGenesis() {
	genesisState := types.DefaultGenesisState()
	genesisState.Minter = types.NewMinter(math.LegacyNewDecWithPrec(20, 2), math.LegacyNewDec(1))
	genesisState.Params = types.NewParams(
		"testDenom",
		math.LegacyNewDecWithPrec(15, 2),
		math.LegacyNewDecWithPrec(22, 2),
		math.LegacyNewDecWithPrec(9, 2),
		math.LegacyNewDecWithPrec(69, 2),
		12*time.Second,
	)

	s.keeper.InitGenesis(s.sdkCtx, s.accountKeeper, genesisState)

	minter, err := s.keeper.Minter.Get(s.sdkCtx)
	s.Require().Equal(genesisState.Minter, minter)
	s.Require().NoError(err)

	invalidCtx := testutil.DefaultContextWithDB(s.T(), s.key, storetypes.NewTransientStoreKey("transient_test"))
	_, err = s.keeper.Minter.Get(invalidCtx.Ctx)
	s.Require().ErrorIs(err, collections.ErrNotFound)

	params, err := s.keeper.Params.Get(s.sdkCtx)
	s.Require().Equal(genesisState.Params, params)
	s.Require().NoError(err)

	genesisState2 := s.keeper.ExportGenesis(s.sdkCtx)
	s.Require().Equal(genesisState, genesisState2)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	minter.PreviousBlockTime = &t0
	s.Require().NoError(s.keeper.Minter.Set(s.sdkCtx, minter))

	exported := s.keeper.ExportGenesis(s.sdkCtx)
	s.Require().Nil(exported.Minter.PreviousBlockTime)
	s.Require().Equal(genesisState.Minter, exported.Minter)
}

func (s *GenesisTestSuite) TestInitGenesisClearsPreviousBlockTime() {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	genesisState := types.DefaultGenesisState()
	genesisState.Minter.PreviousBlockTime = &t0

	s.keeper.InitGenesis(s.sdkCtx, s.accountKeeper, genesisState)

	minter, err := s.keeper.Minter.Get(s.sdkCtx)
	s.Require().NoError(err)
	s.Require().Nil(minter.PreviousBlockTime)
}

func (s *GenesisTestSuite) TestInitGenesisRejectsInvalidParams() {
	genesisState := types.DefaultGenesisState()
	genesisState.Params.MaxBlockElapsed = 0

	s.Require().PanicsWithError("max block elapsed must be positive: 0s", func() {
		s.keeper.InitGenesis(s.sdkCtx, s.accountKeeper, genesisState)
	})
}
