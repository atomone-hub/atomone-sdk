package keeper_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
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

	// 3. An amendment whose result would exceed MaxConstitutionLength must
	// fail, so the constitution cannot grow unboundedly across proposals.
	big := strings.Repeat("c\n", v1.MaxConstitutionLength/2-1)
	require.NoError(t, govKeeper.Constitution.Set(ctx, big))
	insertion := validInsertionDiff(v1.MaxConstitutionAmendmentLength)
	_, err = srv.ProposeConstitutionAmendment(ctx, v1.NewMsgProposeConstitutionAmendment(authority, insertion))
	require.ErrorContains(t, err, "maximum size")
	c, err = govKeeper.Constitution.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, big, c)
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
