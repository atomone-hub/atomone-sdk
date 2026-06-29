package v1_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

var (
	coinsPos   = sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1000))
	coinsMulti = sdk.NewCoins(sdk.NewInt64Coin(sdk.DefaultBondDenom, 1000), sdk.NewInt64Coin("foo", 10000))
	addrs      = []sdk.AccAddress{
		sdk.AccAddress("test1"),
		sdk.AccAddress("test2"),
	}
)

func init() {
	coinsMulti.Sort()
}

func TestMsgDepositGetSignBytes(t *testing.T) {
	addr := sdk.AccAddress("addr1")
	msg := v1.NewMsgDeposit(addr, 0, coinsPos)
	pc := codec.NewProtoCodec(types.NewInterfaceRegistry())
	res, err := pc.MarshalAminoJSON(msg)
	require.NoError(t, err)
	expected := `{"type":"cosmos-sdk/v1/MsgDeposit","value":{"amount":[{"amount":"1000","denom":"stake"}],"depositor":"cosmos1v9jxgu33kfsgr5","proposal_id":"0"}}`
	require.Equal(t, expected, string(res))
}

// this tests that Amino JSON MsgSubmitProposal.GetSignBytes() still works with Content as Any using the ModuleCdc
func TestMsgSubmitProposal_GetSignBytes(t *testing.T) {
	pc := codec.NewProtoCodec(types.NewInterfaceRegistry())
	testcases := []struct {
		name      string
		proposal  []sdk.Msg
		title     string
		summary   string
		expSignBz string
	}{
		{
			"MsgVote",
			[]sdk.Msg{v1.NewMsgVote(addrs[0], 1, v1.OptionYes, "")},
			"gov/MsgVote",
			"Proposal for a governance vote msg",
			`{"type":"cosmos-sdk/v1/MsgSubmitProposal","value":{"initial_deposit":[],"messages":[{"type":"cosmos-sdk/v1/MsgVote","value":{"option":1,"proposal_id":"1","voter":"cosmos1w3jhxap3gempvr"}}],"summary":"Proposal for a governance vote msg","title":"gov/MsgVote"}}`,
		},
		{
			"MsgSend",
			[]sdk.Msg{banktypes.NewMsgSend(addrs[0], addrs[0], sdk.NewCoins())},
			"bank/MsgSend",
			"Proposal for a bank msg send",
			fmt.Sprintf(`{"type":"cosmos-sdk/v1/MsgSubmitProposal","value":{"initial_deposit":[],"messages":[{"type":"cosmos-sdk/MsgSend","value":{"amount":[],"from_address":"%s","to_address":"%s"}}],"summary":"Proposal for a bank msg send","title":"bank/MsgSend"}}`, addrs[0], addrs[0]),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := v1.NewMsgSubmitProposal(tc.proposal, sdk.NewCoins(), sdk.AccAddress{}.String(), "", tc.title, tc.summary)
			require.NoError(t, err)
			bz, err := pc.MarshalAminoJSON(msg)
			require.NoError(t, err)
			require.Equal(t, tc.expSignBz, string(bz))
		})
	}
}

func TestContainsSelfExecAsAuthority(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig()
	v1.RegisterInterfaces(encCfg.InterfaceRegistry)
	authz.RegisterInterfaces(encCfg.InterfaceRegistry)
	banktypes.RegisterInterfaces(encCfg.InterfaceRegistry)
	cdc := encCfg.Codec

	authority := addrs[0]
	other := addrs[1]
	// amendment's signer is its Authority field (set to authority).
	amendment := v1.NewMsgProposeConstitutionAmendment(authority, "@@ -1 +1 @@\n-\n+Test")
	// send's signer is its FromAddress (set to other).
	send := banktypes.NewMsgSend(other, authority, coinsPos)

	pack := func(msg sdk.Msg) *types.Any {
		a, err := types.NewAnyWithValue(msg)
		require.NoError(t, err)
		return a
	}
	// nest wraps leaf in `depth` authz.MsgExec layers, each with grantee == authority.
	nest := func(depth int, leaf sdk.Msg) sdk.Msg {
		cur := pack(leaf)
		var exec *authz.MsgExec
		for i := 0; i < depth; i++ {
			exec = &authz.MsgExec{Grantee: authority.String(), Msgs: []*types.Any{cur}}
			cur = pack(exec)
		}
		return exec
	}

	tests := []struct {
		name    string
		msgs    []sdk.Msg
		wantHit bool
		wantErr bool
	}{
		{"direct self-exec amendment", []sdk.Msg{nest(1, amendment)}, true, false},
		{"nested self-exec (3 layers)", []sdk.Msg{nest(3, amendment)}, true, false},
		{"self-exec at depth cap (8)", []sdk.Msg{nest(8, amendment)}, true, false},
		{"depth beyond cap (9)", []sdk.Msg{nest(9, amendment)}, false, true},
		{
			"cross-account wrapped (inner signed by other)",
			[]sdk.Msg{&authz.MsgExec{Grantee: authority.String(), Msgs: []*types.Any{pack(send)}}},
			false, false,
		},
		{
			"grantee not authority (chain broken)",
			[]sdk.Msg{&authz.MsgExec{Grantee: other.String(), Msgs: []*types.Any{pack(amendment)}}},
			false, false,
		},
		{"plain message, no MsgExec", []sdk.Msg{amendment}, false, false},
		{
			"mixed inner: one cross-account, one self-exec leaf",
			[]sdk.Msg{&authz.MsgExec{Grantee: authority.String(), Msgs: []*types.Any{pack(send), pack(amendment)}}},
			true, false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hit, err := v1.ContainsSelfExecAsAuthority(cdc, tc.msgs, authority)
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorContains(t, err, "authz nesting depth exceeded")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantHit, hit)
		})
	}
}
