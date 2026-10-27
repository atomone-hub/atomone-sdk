package keeper_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/collections"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/cosmos/cosmos-sdk/x/gov/types/v1beta1"
)

// TODO(tip): remove this
func (suite *KeeperTestSuite) TestGetSetProposal() {
	tp := TestProposal
	proposal, err := suite.govKeeper.SubmitProposal(suite.ctx, tp, "", "test", "summary", suite.addrs[0])
	suite.Require().NoError(err)
	proposalID := proposal.Id
	suite.govKeeper.SetProposal(suite.ctx, proposal)

	gotProposal, err := suite.govKeeper.Proposals.Get(suite.ctx, proposalID)
	suite.Require().Nil(err)
	suite.Require().Equal(proposal, gotProposal)
}

// TODO(tip): remove this
func (suite *KeeperTestSuite) TestDeleteProposal() {
	// delete non-existing proposal
	suite.Require().ErrorIs(suite.govKeeper.DeleteProposal(suite.ctx, 10), collections.ErrNotFound)

	tp := TestProposal
	proposal, err := suite.govKeeper.SubmitProposal(suite.ctx, tp, "", "test", "summary", suite.addrs[0])
	suite.Require().NoError(err)
	proposalID := proposal.Id
	suite.govKeeper.SetProposal(suite.ctx, proposal)
	suite.Require().NotPanics(func() {
		suite.govKeeper.DeleteProposal(suite.ctx, proposalID)
	}, "")
}

func (suite *KeeperTestSuite) TestActivateVotingPeriod() {
	tp := TestProposal
	proposal, err := suite.govKeeper.SubmitProposal(suite.ctx, tp, "", "test", "summary", suite.addrs[0])
	suite.Require().NoError(err)

	suite.Require().Nil(proposal.VotingStartTime)

	suite.govKeeper.ActivateVotingPeriod(suite.ctx, proposal)

	proposal, err = suite.govKeeper.Proposals.Get(suite.ctx, proposal.Id)
	suite.Require().Nil(err)
	suite.Require().True(proposal.VotingStartTime.Equal(suite.ctx.BlockHeader().Time))

	has, err := suite.govKeeper.ActiveProposalsQueue.Has(suite.ctx, collections.Join(*proposal.VotingEndTime, proposal.Id))
	suite.Require().NoError(err)
	suite.Require().True(has)
	suite.Require().NoError(suite.govKeeper.DeleteProposal(suite.ctx, proposal.Id))
}

func (suite *KeeperTestSuite) TestDeleteProposalInVotingPeriod() {
	suite.reset()

	params, err := suite.govKeeper.Params.Get(suite.ctx)
	suite.Require().NoError(err)
	params.QuorumCheckCount = 1
	suite.Require().NoError(suite.govKeeper.Params.Set(suite.ctx, params))

	tp := TestProposal
	proposal, err := suite.govKeeper.SubmitProposal(suite.ctx, tp, "", "test", "summary", suite.addrs[0])
	suite.Require().NoError(err)
	suite.Require().Nil(proposal.VotingStartTime)

	suite.Require().NoError(suite.govKeeper.ActivateVotingPeriod(suite.ctx, proposal))

	proposal, err = suite.govKeeper.Proposals.Get(suite.ctx, proposal.Id)
	suite.Require().Nil(err)
	suite.Require().True(proposal.VotingStartTime.Equal(suite.ctx.BlockHeader().Time))

	has, err := suite.govKeeper.ActiveProposalsQueue.Has(suite.ctx, collections.Join(*proposal.VotingEndTime, proposal.Id))
	suite.Require().NoError(err)
	suite.Require().True(has)

	// add vote
	voteOptions := []*v1.WeightedVoteOption{{Option: v1.OptionYes, Weight: "1.0"}}
	err = suite.govKeeper.AddVote(suite.ctx, proposal.Id, suite.addrs[0], voteOptions, "")
	suite.Require().NoError(err)

	var inQueue bool
	err = suite.govKeeper.QuorumCheckQueue.Walk(suite.ctx, nil, func(key collections.Pair[time.Time, uint64], _ v1.QuorumCheckQueueEntry) (bool, error) {
		if key.K2() == proposal.Id {
			inQueue = true
		}
		return false, nil
	})
	suite.Require().NoError(err)
	suite.Require().True(inQueue)

	suite.Require().NoError(suite.govKeeper.DeleteProposal(suite.ctx, proposal.Id))

	inQueue = false
	err = suite.govKeeper.QuorumCheckQueue.Walk(suite.ctx, nil, func(key collections.Pair[time.Time, uint64], _ v1.QuorumCheckQueueEntry) (bool, error) {
		if key.K2() == proposal.Id {
			inQueue = true
		}
		return false, nil
	})
	suite.Require().NoError(err)
	suite.Require().False(inQueue)

	// add vote but proposal is deleted along with its VotingPeriodProposalKey
	err = suite.govKeeper.AddVote(suite.ctx, proposal.Id, suite.addrs[0], voteOptions, "")
	suite.Require().ErrorContains(err, ": inactive proposal")
}

type invalidProposalRoute struct{ v1beta1.TextProposal }

func (invalidProposalRoute) ProposalRoute() string { return "nonexistingroute" }

func (suite *KeeperTestSuite) TestSubmitProposal() {
	govAcct := suite.govKeeper.GetGovernanceAccount(suite.ctx).GetAddress().String()
	_, _, randomAddr := testdata.KeyTestPubAddr()
	tp := v1beta1.TextProposal{Title: "title", Description: "description"}

	testCases := []struct {
		content     v1beta1.Content
		authority   string
		metadata    string
		expedited   bool
		expectedErr error
	}{
		{&tp, govAcct, "", false, nil},
		{&tp, govAcct, "", true, nil},
		// Keeper does not check the validity of title and description, no error
		{&v1beta1.TextProposal{Title: "", Description: "description"}, govAcct, "", false, nil},
		{&v1beta1.TextProposal{Title: strings.Repeat("1234567890", 100), Description: "description"}, govAcct, "", false, nil},
		{&v1beta1.TextProposal{Title: "title", Description: ""}, govAcct, "", false, nil},
		{&v1beta1.TextProposal{Title: "title", Description: strings.Repeat("1234567890", 1000)}, govAcct, "", true, nil},
		// error when metadata is too long (>10000)
		{&tp, govAcct, strings.Repeat("a", 100001), true, types.ErrMetadataTooLong},
		// error when signer is not gov acct
		{&tp, randomAddr.String(), "", false, types.ErrInvalidSigner},
		// error only when invalid route
		{&invalidProposalRoute{}, govAcct, "", false, types.ErrNoProposalHandlerExists},
	}

	for i, tc := range testCases {
		prop, err := v1.NewLegacyContent(tc.content, tc.authority)
		suite.Require().NoError(err)
		_, err = suite.govKeeper.SubmitProposal(suite.ctx, []sdk.Msg{prop}, tc.metadata, "title", "", suite.addrs[0])
		suite.Require().True(errors.Is(tc.expectedErr, err), "tc #%d; got: %v, expected: %v", i, err, tc.expectedErr)
	}
}

// TestSubmitProposalSelfExec verifies how SubmitProposal treats authz.MsgExec proposal
// messages: a self-executing one (grantee == granter == authority, possibly nested,
// up to the nesting-depth cap) is rejected, while a valid cross-account MsgExec — grantee
// is the authority but the inner message is signed by another account — is accepted.
func (suite *KeeperTestSuite) TestSubmitProposalSelfExec() {
	govAddr := suite.govKeeper.GetGovernanceAccount(suite.ctx).GetAddress()
	other := suite.addrs[1]

	pack := func(msg sdk.Msg) *codectypes.Any {
		a, err := codectypes.NewAnyWithValue(msg)
		suite.Require().NoError(err)
		return a
	}
	// nestSelfExec wraps an authority-signed amendment in `depth` authz.MsgExec layers,
	// each with grantee == authority (a self-executing chain).
	nestSelfExec := func(depth int) sdk.Msg {
		cur := pack(v1.NewMsgProposeConstitutionAmendment(govAddr, "@@ -1 +1 @@\n-\n+Test"))
		var exec *authz.MsgExec
		for i := 0; i < depth; i++ {
			exec = &authz.MsgExec{Grantee: govAddr.String(), Msgs: []*codectypes.Any{cur}}
			cur = pack(exec)
		}
		return exec
	}
	crossAccount := &authz.MsgExec{
		Grantee: govAddr.String(),
		Msgs:    []*codectypes.Any{pack(banktypes.NewMsgSend(other, govAddr, sdk.NewCoins()))},
	}

	testCases := []struct {
		name        string
		msgs        []sdk.Msg
		expErr      error // nil => expect success
		errContains string
	}{
		{"direct self-exec is rejected", []sdk.Msg{nestSelfExec(1)}, types.ErrInvalidProposalMsg, "self-executing authz.MsgExec"},
		{"nested self-exec is rejected", []sdk.Msg{nestSelfExec(3)}, types.ErrInvalidProposalMsg, "self-executing authz.MsgExec"},
		{"self-exec at depth cap is rejected", []sdk.Msg{nestSelfExec(8)}, types.ErrInvalidProposalMsg, "self-executing authz.MsgExec"},
		{"self-exec beyond depth cap errors", []sdk.Msg{nestSelfExec(9)}, types.ErrInvalidProposalMsg, "authz nesting depth exceeded"},
		{"valid cross-account MsgExec passes", []sdk.Msg{crossAccount}, nil, ""},
	}
	for _, tc := range testCases {
		suite.Run(tc.name, func() {
			_, err := suite.govKeeper.SubmitProposal(suite.ctx, tc.msgs, "", "title", "summary", suite.addrs[0])
			if tc.expErr == nil {
				suite.Require().NoError(err)
				return
			}
			suite.Require().ErrorIs(err, tc.expErr)
			suite.Require().ErrorContains(err, tc.errContains)
		})
	}
}

func TestMigrateProposalMessages(t *testing.T) {
	content := v1beta1.NewTextProposal("Test", "description")
	contentMsg, err := v1.NewLegacyContent(content, sdk.AccAddress("test1").String())
	require.NoError(t, err)
	content, err = v1.LegacyContentFromMessage(contentMsg)
	require.NoError(t, err)
	require.Equal(t, "Test", content.GetTitle())
	require.Equal(t, "description", content.GetDescription())
}
