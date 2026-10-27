package keeper_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// Regression test for the constitution-amendment EndBlocker DoS: an oversized
// amendment diff, an unbounded message count per proposal, and unbounded
// constitution growth must all be rejected.
func TestConstitutionAmendmentBounds(t *testing.T) {
	govKeeper, _, _, _, _, _, ctx := setupGovKeeper(t)
	authority := types.NewModuleAddress(govtypes.ModuleName)
	srv := keeper.NewMsgServerImpl(govKeeper)

	require.NoError(t, govKeeper.Constitution.Set(ctx, "line one\nline two\n"))

	// 1. A valid but oversized amendment diff must be rejected by the
	// ValidateBasic cap (runs per message inside SubmitProposal).
	oversized := validInsertionDiff(v1.MaxConstitutionAmendmentLength + 1)
	_, err := govKeeper.SubmitProposal(ctx,
		[]sdk.Msg{v1.NewMsgProposeConstitutionAmendment(authority, oversized)},
		"", "title", "summary", sdk.AccAddress([]byte("proposer___addr___")))
	require.ErrorContains(t, err, "amendment length exceeds maximum")

	c, err := govKeeper.Constitution.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, "line one\nline two\n", c)

	// 2. A proposal carrying more than MaxProposalMessages messages must be
	// rejected; a proposal at the cap is accepted.
	amendmentMsgs := make([]sdk.Msg, v1.MaxProposalMessages+1)
	for i := range amendmentMsgs {
		amendmentMsgs[i] = v1.NewMsgProposeConstitutionAmendment(authority, "@@ -1,0 +1,1 @@\n+x\n")
	}
	_, err = govKeeper.SubmitProposal(ctx, amendmentMsgs, "", "title", "summary", sdk.AccAddress([]byte("proposer___addr___")))
	require.ErrorContains(t, err, "maximum is")

	amendmentMsgs = amendmentMsgs[:v1.MaxProposalMessages]
	prop, err := govKeeper.SubmitProposal(ctx, amendmentMsgs, "", "title", "summary", sdk.AccAddress([]byte("proposer___addr___")))
	require.NoError(t, err)
	require.NotZero(t, prop.Id)

	// 3. An amendment whose result would grow past MaxConstitutionLength must
	// fail, so the constitution cannot grow unboundedly across proposals.
	big := strings.Repeat("c\n", v1.MaxConstitutionLength/2) // exactly MaxConstitutionLength bytes
	require.NoError(t, govKeeper.Constitution.Set(ctx, big))
	insertion := validInsertionDiff(v1.MaxConstitutionAmendmentLength)
	_, err = srv.ProposeConstitutionAmendment(ctx, v1.NewMsgProposeConstitutionAmendment(authority, insertion))
	require.ErrorContains(t, err, "maximum size")
	c, err = govKeeper.Constitution.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, big, c)

	// 4. A constitution already over MaxConstitutionLength (e.g. from a
	// genesis predating the cap) must still be amendable, as long as the
	// amendment shrinks it: the cap blocks growth, not recovery. The result
	// stays over the cap, so only the growth-only clause accepts it — a plain
	// absolute cap would reject this amendment.
	overCap := strings.Repeat("c\n", v1.MaxConstitutionLength/2+50_000) // ~100 KB over the cap
	require.NoError(t, govKeeper.Constitution.Set(ctx, overCap))
	shrink := deletionDiff(10_000) // removes 10,000 "c" lines (~20 KB); result stays ~80 KB over the cap
	_, err = srv.ProposeConstitutionAmendment(ctx, v1.NewMsgProposeConstitutionAmendment(authority, shrink))
	require.NoError(t, err, "a shrinking amendment on an over-cap constitution must be accepted")
	c, err = govKeeper.Constitution.Get(ctx)
	require.NoError(t, err)
	require.Len(t, c, len(overCap)-20_000)
	require.Greater(t, len(c), v1.MaxConstitutionLength)
}

// TestSubmitProposalCountsNestedAuthzMessages guards the message-count bound
// against bypass via authz.MsgExec: a proposal carrying a single MsgExec whose
// inner messages are not gov-signed passes the signer check and the self-exec
// check, but authz dispatches every inner message in the EndBlocker, so the
// inner messages must count toward MaxProposalMessages.
func TestSubmitProposalCountsNestedAuthzMessages(t *testing.T) {
	govKeeper, _, _, _, _, _, ctx := setupGovKeeper(t)
	authority := types.NewModuleAddress(govtypes.ModuleName)

	innerMsgs := make([]*codectypes.Any, v1.MaxProposalMessages+1)
	for i := range innerMsgs {
		anyMsg, err := codectypes.NewAnyWithValue(&banktypes.MsgSend{
			FromAddress: sdk.AccAddress([]byte("sender_______________")).String(),
			ToAddress:   sdk.AccAddress([]byte("recipient_____________")).String(),
			Amount:      sdk.NewCoins(sdk.NewInt64Coin("stake", 1)),
		})
		require.NoError(t, err)
		innerMsgs[i] = anyMsg
	}

	// The MsgExec signer is its grantee (the gov module), so the proposal
	// passes the per-message signer check; the inner MsgSends are not
	// gov-signed, so it is not a self-exec chain either.
	exec := &authz.MsgExec{Grantee: authority.String(), Msgs: innerMsgs}
	_, err := govKeeper.SubmitProposal(ctx, []sdk.Msg{exec}, "", "title", "summary", sdk.AccAddress([]byte("proposer___addr___")))
	require.ErrorContains(t, err, "maximum is", "nested inner messages must count toward the message cap")
}

// validInsertionDiff builds a parseable insertion-only unified diff whose
// total encoded size is at least size bytes.
func validInsertionDiff(size int) string {
	line := "+" + strings.Repeat("p", 60) + "\n"
	n := 0
	for total := len("@@ -1,0 +1,0 @@\n"); total < size; total += len(line) {
		n++
	}
	return fmt.Sprintf("@@ -1,0 +1,%d @@\n%s", n, strings.Repeat(line, n))
}

// deletionDiff builds a parseable deletion-only unified diff removing the
// first n "c" lines.
func deletionDiff(n int) string {
	return fmt.Sprintf("@@ -1,%d +1,0 @@\n%s", n, strings.Repeat("-c\n", n))
}
