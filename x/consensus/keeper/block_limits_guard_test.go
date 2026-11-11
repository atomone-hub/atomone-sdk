package keeper_test

import (
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"

	"github.com/cosmos/cosmos-sdk/x/consensus/types"
)

func (s *MaxGasGuardSuite) blockLimitsMsg(maxBytes, evidenceMaxBytes, maxGas int64) *types.MsgUpdateParams {
	def := cmttypes.DefaultConsensusParams().ToProto()
	return &types.MsgUpdateParams{
		Authority: s.keeper.GetAuthority(),
		Block:     &cmtproto.BlockParams{MaxBytes: maxBytes, MaxGas: maxGas},
		Evidence:  &cmtproto.EvidenceParams{MaxAgeNumBlocks: def.Evidence.MaxAgeNumBlocks, MaxAgeDuration: def.Evidence.MaxAgeDuration, MaxBytes: evidenceMaxBytes},
		Validator: &cmtproto.ValidatorParams{PubKeyTypes: def.Validator.PubKeyTypes},
	}
}

// overheadBytes is the room CometBFT reserves in a block for the header and
// the last commit of the largest validator set it accepts.
func overheadBytes() int64 {
	return cmttypes.MaxOverheadForBlock + cmttypes.MaxHeaderBytes + cmttypes.MaxCommitBytes(cmttypes.MaxVotesCount)
}

func (s *MaxGasGuardSuite) assertBlockUnchanged() {
	def := cmttypes.DefaultConsensusParams().ToProto()
	stored, err := s.keeper.ParamsStore.Get(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(def.Block.MaxBytes, stored.Block.MaxBytes)
	s.Require().Equal(def.Block.MaxGas, stored.Block.MaxGas)
}

// A Block.MaxBytes that cannot hold a block's header, last commit and evidence
// makes CometBFT panic in MaxDataBytesNoEvidence when committing the block that
// stores it, and again on every restart.
func (s *MaxGasGuardSuite) TestUpdateParamsRejectsMaxBytesBelowOverhead() {
	for _, tc := range []struct{ maxBytes, evidenceMaxBytes int64 }{
		{500, 0},
		{11830, 0},
		{overheadBytes() - 1, 0},
		{overheadBytes() + 49999, 50000},
	} {
		_, err := s.keeper.UpdateParams(s.ctx, s.blockLimitsMsg(tc.maxBytes, tc.evidenceMaxBytes, -1))
		s.Require().Error(err, "MaxBytes %d, evidence %d", tc.maxBytes, tc.evidenceMaxBytes)
		s.Require().Contains(err.Error(), "Block.MaxBytes")
		s.assertBlockUnchanged()
	}
}

func (s *MaxGasGuardSuite) TestUpdateParamsAllowsMaxBytesAboveOverhead() {
	for _, tc := range []struct{ maxBytes, evidenceMaxBytes int64 }{
		{-1, 50000},
		{overheadBytes(), 0},
		{overheadBytes() + 50000, 50000},
		{2000000, 50000},
	} {
		_, err := s.keeper.UpdateParams(s.ctx, s.blockLimitsMsg(tc.maxBytes, tc.evidenceMaxBytes, -1))
		s.Require().NoError(err, "MaxBytes %d, evidence %d", tc.maxBytes, tc.evidenceMaxBytes)

		stored, err := s.keeper.ParamsStore.Get(s.ctx)
		s.Require().NoError(err)
		s.Require().Equal(tc.maxBytes, stored.Block.MaxBytes)

		if tc.maxBytes != -1 {
			// the accepted values never make CometBFT's block size helpers panic
			s.Require().NotPanics(func() {
				cmttypes.MaxDataBytesNoEvidence(tc.maxBytes, cmttypes.MaxVotesCount)
				cmttypes.MaxDataBytes(tc.maxBytes, tc.evidenceMaxBytes, cmttypes.MaxVotesCount)
			})
		}
	}
}

// A Block.MaxGas below the gas of an ordinary transaction makes the ante
// handler refuse every transaction, including the votes needed to repair it.
func (s *MaxGasGuardSuite) TestUpdateParamsRejectsMaxGasBelowFloor() {
	for _, maxGas := range []int64{1, 100, 999999} {
		_, err := s.keeper.UpdateParams(s.ctx, s.blockLimitsMsg(cmttypes.DefaultConsensusParams().Block.MaxBytes, 0, maxGas))
		s.Require().Error(err, "MaxGas %d", maxGas)
		s.Require().Contains(err.Error(), "Block.MaxGas")
		s.assertBlockUnchanged()
	}
}

func (s *MaxGasGuardSuite) TestUpdateParamsAllowsMaxGasFloor() {
	_, err := s.keeper.UpdateParams(s.ctx, s.blockLimitsMsg(cmttypes.DefaultConsensusParams().Block.MaxBytes, 0, 1000000))
	s.Require().NoError(err)
}
