package gov_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"cosmossdk.io/api/amino"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov"
)

// maxAminoNameLength is the length limit of the amino names, as enforced by legacy.RegisterAminoMsg.
const maxAminoNameLength = 39

// The nodes derive the SIGN_MODE_LEGACY_AMINO_JSON sign bytes from the amino.name
// option of the proto messages, the LegacyAmino codec is what the clients using it
// to encode messages rely on. The two must agree, or a message signed against one
// is not valid against the other.
func TestLegacyAminoNamesMatchProto(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	legacyAmino := codec.NewLegacyAmino()
	sdk.RegisterInterfaces(registry)
	basic := gov.AppModuleBasic{}
	basic.RegisterInterfaces(registry)
	basic.RegisterLegacyAminoCodec(legacyAmino)

	var checked int
	for _, typeURL := range registry.ListImplementations(sdk.MsgInterfaceProtoName) {
		if !strings.HasPrefix(typeURL, "/cosmos.gov.") {
			continue
		}
		t.Run(typeURL, func(t *testing.T) {
			desc, err := proto.HybridResolver.FindDescriptorByName(protoreflect.FullName(strings.TrimPrefix(typeURL, "/")))
			require.NoError(t, err)
			protoName, _ := protov2.GetExtension(desc.(protoreflect.MessageDescriptor).Options(), amino.E_Name).(string)
			if protoName == "" {
				// the nodes encode a message without amino.name as a bare object, there is
				// no name to agree on
				t.Skip("no amino.name")
			}
			// longer names cannot be signed with a Ledger, see cosmos/cosmos-sdk#10870
			require.LessOrEqual(t, len(protoName), maxAminoNameLength)

			msg, err := registry.Resolve(typeURL)
			require.NoError(t, err)
			bz, err := legacyAmino.Amino.MarshalJSON(msg)
			require.NoError(t, err, "not registered in the LegacyAmino codec, amino.name is %q", protoName)
			var enc struct {
				Type string `json:"type"`
			}
			require.NoError(t, json.Unmarshal(bz, &enc))
			require.Equal(t, protoName, enc.Type)
		})
		checked++
	}
	require.Greater(t, checked, 10, "the gov messages were not found")
}
