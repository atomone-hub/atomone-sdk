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

// A signature without a set bit is never verified, so accepting it would let
// anyone append bytes to a valid transaction.
func TestVerifyMultisignatureMoreSigsThanBits(t *testing.T) {
	priv := secp256k1.GenPrivKey()
	pks := []cryptotypes.PubKey{priv.PubKey(), secp256k1.GenPrivKey().PubKey()}
	msg := []byte("msg")
	validSig, err := priv.Sign(msg)
	require.NoError(t, err)
	pk := kmultisig.NewLegacyAminoPubKey(1, pks)
	single := &signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON, Signature: validSig}
	getSignBytes := func(signing.SignMode) ([]byte, error) { return msg, nil }

	bits := cryptotypes.NewCompactBitArray(2)
	bits.SetIndex(0, true)

	require.NoError(t, pk.VerifyMultisignature(getSignBytes, &signing.MultiSignatureData{
		BitArray:   bits,
		Signatures: []signing.SignatureData{single},
	}))

	err = pk.VerifyMultisignature(getSignBytes, &signing.MultiSignatureData{
		BitArray:   bits,
		Signatures: []signing.SignatureData{single, single},
	})
	require.ErrorContains(t, err, "signature size is incorrect 2, expected 1")
}

// Several encodings of the same signer set must not all verify.
func TestVerifyMultisignatureNonCanonicalBitArray(t *testing.T) {
	privs := []*secp256k1.PrivKey{secp256k1.GenPrivKey(), secp256k1.GenPrivKey(), secp256k1.GenPrivKey()}
	pks := make([]cryptotypes.PubKey, len(privs))
	for i, p := range privs {
		pks[i] = p.PubKey()
	}
	pk := kmultisig.NewLegacyAminoPubKey(1, pks)
	msg := []byte("msg")
	sigBz, err := privs[0].Sign(msg)
	require.NoError(t, err)
	sigs := []signing.SignatureData{
		&signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON, Signature: sigBz},
	}
	getSignBytes := func(signing.SignMode) ([]byte, error) { return msg, nil }

	canonical := &cryptotypes.CompactBitArray{ExtraBitsStored: 3, Elems: []byte{0x80}}
	require.NoError(t, pk.VerifyMultisignature(getSignBytes, &signing.MultiSignatureData{BitArray: canonical, Signatures: sigs}))

	for name, bits := range map[string]*cryptotypes.CompactBitArray{
		"unused bits set":     {ExtraBitsStored: 3, Elems: []byte{0x9F}},
		"extra bits above 8":  {ExtraBitsStored: 11, Elems: []byte{0x80}},
		"extra bits no elems": {ExtraBitsStored: 3},
	} {
		t.Run(name, func(t *testing.T) {
			err := pk.VerifyMultisignature(getSignBytes, &signing.MultiSignatureData{BitArray: bits, Signatures: sigs})
			require.ErrorContains(t, err, "invalid bit array")
		})
	}
}
