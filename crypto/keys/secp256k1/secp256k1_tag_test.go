package secp256k1_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

func TestUnmarshalAminoRejectsInvalidSEC1Tag(t *testing.T) {
	valid := secp256k1.GenPrivKey().PubKey().Bytes()
	require.Contains(t, []byte{0x02, 0x03}, valid[0])

	var pk secp256k1.PubKey
	require.NoError(t, pk.UnmarshalAmino(valid))

	for _, tag := range []byte{0x00, 0x01, 0x04, 0xff} {
		bz := append([]byte{tag}, valid[1:]...)
		require.ErrorContains(t, (&secp256k1.PubKey{}).UnmarshalAmino(bz), "invalid pubkey SEC1 tag", "tag %#x", tag)
	}
}
