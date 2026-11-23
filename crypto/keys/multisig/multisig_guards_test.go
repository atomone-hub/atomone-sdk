package multisig_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	kmultisig "github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
)

// The bit array marks two signers but only one sub-signature is supplied.
func TestVerifyMultisignatureFewerSigsThanBits(t *testing.T) {
	priv := secp256k1.GenPrivKey()
	pks := []cryptotypes.PubKey{priv.PubKey(), secp256k1.GenPrivKey().PubKey()}
	msg := []byte("msg")
	validSig, err := priv.Sign(msg)
	require.NoError(t, err)
	pk := kmultisig.NewLegacyAminoPubKey(1, pks)

	bits := cryptotypes.NewCompactBitArray(2)
	bits.SetIndex(0, true)
	bits.SetIndex(1, true)
	sig := &signing.MultiSignatureData{
		BitArray: bits,
		Signatures: []signing.SignatureData{
			&signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON, Signature: validSig},
		},
	}

	require.NotPanics(t, func() {
		err = pk.VerifyMultisignature(func(signing.SignMode) ([]byte, error) { return msg, nil }, sig)
	})
	require.ErrorContains(t, err, "signature size is incorrect 1")
}
