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

type PubKeyTypesGuardSuite struct {
	suite.Suite
	ctx    sdk.Context
	keeper *consensusparamkeeper.Keeper
}

func (s *PubKeyTypesGuardSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(consensusparamkeeper.StoreKey)
	testCtx := testutil.DefaultContextWithDB(s.T(), key, storetypes.NewTransientStoreKey("transient_test"))
	header := cmtproto.Header{Height: 5}
	s.ctx = testCtx.Ctx.WithBlockHeader(header)
	encCfg := moduletestutil.MakeTestEncodingConfig()
	storeService := runtime.NewKVStoreService(key)
	s.keeper = &consensusparamkeeper.Keeper{}
	*s.keeper = consensusparamkeeper.NewKeeper(encCfg.Codec, storeService, authtypes.NewModuleAddress("gov").String(), runtime.EventService{})

	// seed the params store with the defaults: PubKeyTypes ["ed25519"]
	s.Require().NoError(s.keeper.ParamsStore.Set(s.ctx, cmttypes.DefaultConsensusParams().ToProto()))
}

func TestPubKeyTypesGuardSuite(t *testing.T) {
	suite.Run(t, new(PubKeyTypesGuardSuite))
}

// A gov proposal submitting a secp256k1-only Validator.PubKeyTypes (a valid,
// known CometBFT type) previously passed every check, was persisted, and
// later panicked all validators in CometBFT's applyBlock at the next
// validator set update. UpdateParams must reject it.
func (s *PubKeyTypesGuardSuite) TestUpdateParamsRejectsRemovingActivePubKeyType() {
	def := cmttypes.DefaultConsensusParams().ToProto()
	msg := &types.MsgUpdateParams{
		Authority: s.keeper.GetAuthority(),
		Block:     &cmtproto.BlockParams{MaxBytes: def.Block.MaxBytes, MaxGas: def.Block.MaxGas},
		Evidence:  &cmtproto.EvidenceParams{MaxAgeNumBlocks: def.Evidence.MaxAgeNumBlocks, MaxAgeDuration: def.Evidence.MaxAgeDuration, MaxBytes: def.Evidence.MaxBytes},
		Validator: &cmtproto.ValidatorParams{PubKeyTypes: []string{cmttypes.ABCIPubKeyTypeSecp256k1}},
	}

	_, err := s.keeper.UpdateParams(s.ctx, msg)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "cannot remove pubkey type")

	// state must be unchanged
	stored, err := s.keeper.ParamsStore.Get(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal([]string{cmttypes.ABCIPubKeyTypeEd25519}, stored.Validator.PubKeyTypes)
}

// Extending PubKeyTypes (keeping every currently allowed type) must still
// succeed.
func (s *PubKeyTypesGuardSuite) TestUpdateParamsAllowsExtendingPubKeyTypes() {
	def := cmttypes.DefaultConsensusParams().ToProto()
	msg := &types.MsgUpdateParams{
		Authority: s.keeper.GetAuthority(),
		Block:     &cmtproto.BlockParams{MaxBytes: def.Block.MaxBytes, MaxGas: def.Block.MaxGas},
		Evidence:  &cmtproto.EvidenceParams{MaxAgeNumBlocks: def.Evidence.MaxAgeNumBlocks, MaxAgeDuration: def.Evidence.MaxAgeDuration, MaxBytes: def.Evidence.MaxBytes},
		Validator: &cmtproto.ValidatorParams{PubKeyTypes: []string{cmttypes.ABCIPubKeyTypeEd25519, cmttypes.ABCIPubKeyTypeSecp256k1}},
	}

	_, err := s.keeper.UpdateParams(s.ctx, msg)
	s.Require().NoError(err)

	stored, err := s.keeper.ParamsStore.Get(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal([]string{cmttypes.ABCIPubKeyTypeEd25519, cmttypes.ABCIPubKeyTypeSecp256k1}, stored.Validator.PubKeyTypes)
}
