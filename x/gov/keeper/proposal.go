package keeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// SubmitProposal creates a new proposal given an array of messages
func (keeper Keeper) SubmitProposal(ctx context.Context, messages []sdk.Msg, metadata, title, summary string, proposer sdk.AccAddress) (v1.Proposal, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	err := keeper.assertMetadataLength(metadata)
	if err != nil {
		return v1.Proposal{}, err
	}

	// assert summary is no longer than predefined max length of metadata
	err = keeper.assertSummaryLength(summary)
	if err != nil {
		return v1.Proposal{}, err
	}

	// assert title is no longer than predefined max length of metadata
	err = keeper.assertMetadataLength(title)
	if err != nil {
		return v1.Proposal{}, err
	}

	// Reject proposals containing a self-executing authz.MsgExec (grantee == granter ==
	// authority). Checked before the per-message routing loop below.
	selfExec, err := v1.ContainsSelfExecAsAuthority(keeper.cdc, messages, keeper.GetGovernanceAccount(ctx).GetAddress())
	if err != nil {
		return v1.Proposal{}, err
	}
	if selfExec {
		return v1.Proposal{}, errorsmod.Wrap(types.ErrInvalidProposalMsg, "governance proposals may not contain a self-executing authz.MsgExec (grantee == granter == authority)")
	}

	// Will hold a comma-separated string of all Msg type URLs.
	msgsStr := ""

	// Loop through all messages and confirm that each has a handler and the gov module account
	// as the only signer
	for _, msg := range messages {
		msgsStr += fmt.Sprintf(",%s", sdk.MsgTypeURL(msg))

		// perform a basic validation of the message
		if m, ok := msg.(sdk.HasValidateBasic); ok {
			if err := m.ValidateBasic(); err != nil {
				return v1.Proposal{}, errorsmod.Wrap(types.ErrInvalidProposalMsg, err.Error())
			}
		}

		signers, _, err := keeper.cdc.GetMsgV1Signers(msg)
		if err != nil {
			return v1.Proposal{}, err
		}
		if len(signers) != 1 {
			return v1.Proposal{}, types.ErrInvalidSigner
		}

		// assert that the governance module account is the only signer of the messages
		if !bytes.Equal(signers[0], keeper.GetGovernanceAccount(ctx).GetAddress()) {
			return v1.Proposal{}, errorsmod.Wrapf(types.ErrInvalidSigner, "%s", sdk.AccAddress(signers[0]).String())
		}

		// use the msg service router to see that there is a valid route for that message.
		handler := keeper.router.Handler(msg)
		if handler == nil {
			return v1.Proposal{}, errorsmod.Wrap(types.ErrUnroutableProposalMsg, sdk.MsgTypeURL(msg))
		}

		// Only if it's a MsgExecLegacyContent do we try to execute the
		// proposal in a cached context.
		// For other Msgs, we do not verify the proposal messages any further.
		// They may fail upon execution.
		// ref: https://github.com/cosmos/cosmos-sdk/pull/10868#discussion_r784872842
		if msg, ok := msg.(*v1.MsgExecLegacyContent); ok {
			cacheCtx, _ := sdkCtx.CacheContext()
			if _, err := handler(cacheCtx, msg); err != nil {
				if errors.Is(err, types.ErrNoProposalHandlerExists) {
					return v1.Proposal{}, err
				}
				return v1.Proposal{}, errorsmod.Wrap(types.ErrInvalidProposalContent, err.Error())
			}
		}

	}

	proposalID, err := keeper.ProposalID.Next(ctx)
	if err != nil {
		return v1.Proposal{}, err
	}

	params, err := keeper.Params.Get(ctx)
	if err != nil {
		return v1.Proposal{}, err
	}

	submitTime := sdkCtx.BlockHeader().Time
	depositPeriod := params.MaxDepositPeriod

	proposal, err := v1.NewProposal(messages, proposalID, submitTime, submitTime.Add(*depositPeriod), metadata, title, summary, proposer)
	if err != nil {
		return v1.Proposal{}, err
	}

	err = keeper.SetProposal(ctx, proposal)
	if err != nil {
		return v1.Proposal{}, err
	}
	err = keeper.InactiveProposalsQueue.Set(ctx, collections.Join(*proposal.DepositEndTime, proposalID), proposalID)
	if err != nil {
		return v1.Proposal{}, err
	}

	keeper.UpdateMinInitialDeposit(ctx, false)

	// called right after a proposal is submitted
	err = keeper.Hooks().AfterProposalSubmission(ctx, proposalID)
	if err != nil {
		return v1.Proposal{}, err
	}

	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeSubmitProposal,
			sdk.NewAttribute(types.AttributeKeyProposalID, fmt.Sprintf("%d", proposalID)),
			sdk.NewAttribute(types.AttributeKeyProposalProposer, proposer.String()),
			sdk.NewAttribute(types.AttributeKeyProposalMessages, msgsStr),
		),
	)

	return proposal, nil
}

// SetProposal sets a proposal to store.
func (keeper Keeper) SetProposal(ctx context.Context, proposal v1.Proposal) error {
	if proposal.Status == v1.StatusVotingPeriod {
		err := keeper.VotingPeriodProposals.Set(ctx, proposal.Id, []byte{1})
		if err != nil {
			return err
		}
	} else {
		err := keeper.VotingPeriodProposals.Remove(ctx, proposal.Id)
		if err != nil {
			return err
		}
	}

	return keeper.Proposals.Set(ctx, proposal.Id, proposal)
}

// DeleteProposal deletes a proposal from store.
func (keeper Keeper) DeleteProposal(ctx context.Context, proposalID uint64) error {
	proposal, err := keeper.Proposals.Get(ctx, proposalID)
	if err != nil {
		return err
	}

	if proposal.DepositEndTime != nil {
		err := keeper.InactiveProposalsQueue.Remove(ctx, collections.Join(*proposal.DepositEndTime, proposalID))
		if err != nil {
			return err
		}
	}
	if proposal.VotingEndTime != nil {
		err := keeper.ActiveProposalsQueue.Remove(ctx, collections.Join(*proposal.VotingEndTime, proposalID))
		if err != nil {
			return err
		}

		err = keeper.VotingPeriodProposals.Remove(ctx, proposalID)
		if err != nil {
			return err
		}

		err = keeper.QuorumCheckQueue.Walk(ctx, nil, func(key collections.Pair[time.Time, uint64], _ v1.QuorumCheckQueueEntry) (bool, error) {
			if key.K2() == proposalID {
				err := keeper.QuorumCheckQueue.Remove(ctx, key)
				if err != nil {
					return false, err
				}
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			return err
		}
	}

	return keeper.Proposals.Remove(ctx, proposalID)
}

// ActivateVotingPeriod activates the voting period of a proposal
func (keeper Keeper) ActivateVotingPeriod(ctx context.Context, proposal v1.Proposal) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	startTime := sdkCtx.BlockHeader().Time
	proposal.VotingStartTime = &startTime
	params, err := keeper.Params.Get(ctx)
	if err != nil {
		return err
	}

	votingPeriod := params.VotingPeriod
	endTime := proposal.VotingStartTime.Add(*votingPeriod)
	proposal.VotingEndTime = &endTime
	proposal.Status = v1.StatusVotingPeriod
	err = keeper.SetProposal(ctx, proposal)
	if err != nil {
		return err
	}

	err = keeper.InactiveProposalsQueue.Remove(ctx, collections.Join(*proposal.DepositEndTime, proposal.Id))
	if err != nil {
		return err
	}

	err = keeper.ActiveProposalsQueue.Set(ctx, collections.Join(*proposal.VotingEndTime, proposal.Id), proposal.Id)
	if err != nil {
		return err
	}

	keeper.UpdateMinDeposit(ctx, false)

	// Add proposal to quorum check queue if functionality is enabled.
	if params.QuorumCheckCount > 0 {
		quorumTimeoutTime := proposal.VotingStartTime.Add(*params.QuorumTimeout)
		quorumCheckEntry := v1.NewQuorumCheckQueueEntry(quorumTimeoutTime, params.QuorumCheckCount)
		return keeper.QuorumCheckQueue.Set(ctx, collections.Join(quorumTimeoutTime, proposal.Id), quorumCheckEntry)
	}

	return nil
}

// ProposalKinds returns a v1.ProposalKinds useful to determine which kind of
// messages are included in a proposal.
func (keeper Keeper) ProposalKinds(p v1.Proposal) v1.ProposalKinds {
	if len(p.Messages) == 0 {
		return v1.ProposalKindAny
	}
	var kinds v1.ProposalKinds
	for _, msg := range p.Messages {
		var sdkMsg sdk.Msg
		if err := keeper.cdc.UnpackAny(msg, &sdkMsg); err == nil {
			switch sdkMsg.(type) {
			case interface {
				IsProposalKindConstitutionAmendment()
			}:
				kinds |= v1.ProposalKindConstitutionAmendment
			case interface {
				IsProposalKindLaw()
			}:
				kinds |= v1.ProposalKindLaw
			default:
				kinds |= v1.ProposalKindAny
			}
		} else {
			// If we can't unpack the message, it's likely a broken proposal.
			// although almost impossible, we still want to handle it gracefully.
			// We assume that the proposal is of any kind.
			kinds |= v1.ProposalKindAny
		}
	}
	return kinds
}
