package keeper_test

import (
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/suite"

	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	consensusparamkeeper "github.com/cosmos/cosmos-sdk/x/consensus/keeper"
	"github.com/cosmos/cosmos-sdk/x/consensus/types"
)

type MaxGasGuardSuite struct {
	suite.Suite
	ctx    sdk.Context
	keeper *consensusparamkeeper.Keeper
}

func (s *MaxGasGuardSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(consensusparamkeeper.StoreKey)
	testCtx := testutil.DefaultContextWithDB(s.T(), key, storetypes.NewTransientStoreKey("transient_test"))
	header := cmtproto.Header{Height: 5}
	s.ctx = testCtx.Ctx.WithBlockHeader(header)
	encCfg := moduletestutil.MakeTestEncodingConfig()
	storeService := runtime.NewKVStoreService(key)
	s.keeper = &consensusparamkeeper.Keeper{}
	*s.keeper = consensusparamkeeper.NewKeeper(encCfg.Codec, storeService, authtypes.NewModuleAddress("gov").String(), runtime.EventService{})

	// seed the params store with the defaults
	s.Require().NoError(s.keeper.ParamsStore.Set(s.ctx, cmttypes.DefaultConsensusParams().ToProto()))
}

func TestMaxGasGuardSuite(t *testing.T) {
	suite.Run(t, new(MaxGasGuardSuite))
}

func (s *MaxGasGuardSuite) updateParamsMsg(maxGas int64) *types.MsgUpdateParams {
	def := cmttypes.DefaultConsensusParams().ToProto()
	return &types.MsgUpdateParams{
		Authority: s.keeper.GetAuthority(),
		Block:     &cmtproto.BlockParams{MaxBytes: def.Block.MaxBytes, MaxGas: maxGas},
		Evidence:  &cmtproto.EvidenceParams{MaxAgeNumBlocks: def.Evidence.MaxAgeNumBlocks, MaxAgeDuration: def.Evidence.MaxAgeDuration, MaxBytes: def.Evidence.MaxBytes},
		Validator: &cmtproto.ValidatorParams{PubKeyTypes: def.Validator.PubKeyTypes},
	}
}

// A gov proposal submitting Block.MaxGas = 0 previously passed every check
// (CometBFT's ValidateBasic only rejects MaxGas < -1), was persisted, and then
// made every node's mempool reject all transactions forever: CometBFT's
// PostCheckMaxGas enforces MaxGas = 0 as a block gas budget of zero, so any
// CheckTx with GasWanted > 0 is rejected — and repairing the parameter is
// itself a transaction. UpdateParams must reject it.
func (s *MaxGasGuardSuite) TestUpdateParamsRejectsZeroMaxGas() {
	def := cmttypes.DefaultConsensusParams().ToProto()

	_, err := s.keeper.UpdateParams(s.ctx, s.updateParamsMsg(0))
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "Block.MaxGas")

	// state must be unchanged
	stored, err := s.keeper.ParamsStore.Get(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(def.Block.MaxGas, stored.Block.MaxGas)
}

// MaxGas = -1 (unlimited) and positive limits must still be accepted.
func (s *MaxGasGuardSuite) TestUpdateParamsAllowsValidMaxGas() {
	for _, maxGas := range []int64{-1, 4000000} {
		_, err := s.keeper.UpdateParams(s.ctx, s.updateParamsMsg(maxGas))
		s.Require().NoError(err, "MaxGas %d", maxGas)

		stored, err := s.keeper.ParamsStore.Get(s.ctx)
		s.Require().NoError(err)
		s.Require().Equal(maxGas, stored.Block.MaxGas)
	}
}
