package keeper_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/collections"
	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// Checks that over-length Weight strings are rejected by length before the
// quadratic decimal parse, both at vote time and in Power.
type WeightLengthRegressionSuite struct {
	KeeperTestSuite
}

func TestWeightLengthRegression(t *testing.T) {
	suite.Run(t, new(WeightLengthRegressionSuite))
}

func (s *WeightLengthRegressionSuite) submitProposal() uint64 {
	govAcct := s.govKeeper.GetGovernanceAccount(s.ctx).GetAddress()
	proposer := s.addrs[0]
	minDeposit := s.govKeeper.GetMinDeposit(s.ctx)
	msg, err := v1.NewMsgSubmitProposal(
		[]sdk.Msg{&banktypes.MsgSend{
			FromAddress: govAcct.String(),
			ToAddress:   proposer.String(),
			Amount:      minDeposit,
		}},
		minDeposit,
		proposer.String(),
		"",
		"Proposal",
		"description of proposal",
	)
	s.Require().NoError(err)
	res, err := s.msgSrvr.SubmitProposal(s.ctx, msg)
	s.Require().NoError(err)
	return res.ProposalId
}

func (s *WeightLengthRegressionSuite) TestVoteWeightedRejectsOverlongWeight() {
	s.reset()
	proposalID := s.submitProposal()
	proposer := s.addrs[0]

	bigWeight := strings.Repeat("9", 1000000)             // invalid value, quadratic parse cost
	bloatedOne := "0" + strings.Repeat("0", 999998) + "1" // value 1, megabyte string

	for name, weight := range map[string]string{
		"invalid but expensive": bigWeight,
		"valid but bloated":     bloatedOne,
	} {
		s.Run(name, func() {
			voteReq := v1.NewMsgVoteWeighted(proposer, proposalID, v1.WeightedVoteOptions{
				&v1.WeightedVoteOption{Option: v1.OptionYes, Weight: weight},
			}, "")
			_, err := s.msgSrvr.VoteWeighted(s.ctx, voteReq)
			s.Require().Error(err, "over-length weight must be rejected before parsing")
			// no vote may be persisted
			_, err = s.govKeeper.Votes.Get(s.ctx, collections.Join(proposalID, proposer))
			s.Require().Error(err)
		})
	}

	// A canonical short weight still works.
	voteReq := v1.NewMsgVoteWeighted(proposer, proposalID, v1.NewNonSplitVoteOption(v1.OptionYes), "")
	_, err := s.msgSrvr.VoteWeighted(s.ctx, voteReq)
	s.Require().NoError(err)
}

func (s *WeightLengthRegressionSuite) TestPowerOnOverlongWeightIsCheap() {
	s.reset()
	// Power must not parse over-length weights (nor panic on them)
	w := v1.WeightedVoteOption{Option: v1.OptionYes, Weight: strings.Repeat("0", 100000) + "1"}
	p := w.Power(sdkmath.LegacyNewDec(100))
	s.Require().True(p.IsZero())
}
