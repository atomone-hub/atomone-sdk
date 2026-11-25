package tx

import (
	"testing"

	"github.com/stretchr/testify/require"

	txv1beta1 "cosmossdk.io/api/cosmos/tx/v1beta1"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
)

func TestAdaptModeInfoMalformedMulti(t *testing.T) {
	tests := []struct {
		name  string
		multi *tx.ModeInfo_Multi
	}{
		{"nil Multi", nil},
		{"nil Bitarray", &tx.ModeInfo_Multi{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacy := &tx.ModeInfo{Sum: &tx.ModeInfo_Multi_{Multi: tc.multi}}
			require.NotPanics(t, func() {
				adaptModeInfo(legacy, &txv1beta1.ModeInfo{})
			})
		})
	}
}

func TestModeInfoAndSigToSignatureDataSigCountMismatch(t *testing.T) {
	single := &tx.ModeInfo{Sum: &tx.ModeInfo_Single_{
		Single: &tx.ModeInfo_Single{Mode: signing.SignMode_SIGN_MODE_DIRECT},
	}}
	modeInfo := &tx.ModeInfo{Sum: &tx.ModeInfo_Multi_{
		Multi: &tx.ModeInfo_Multi{ModeInfos: []*tx.ModeInfo{single, single}},
	}}
	sig, err := (&cryptotypes.MultiSignature{Signatures: [][]byte{{1}}}).Marshal()
	require.NoError(t, err)

	require.NotPanics(t, func() {
		_, err = ModeInfoAndSigToSignatureData(modeInfo, sig)
	})
	require.ErrorContains(t, err, "expected 2 multisig signatures, got 1")
}

func TestModeInfoAndSigToSignatureDataRejectsMalformed(t *testing.T) {
	require.NotPanics(t, func() {
		_, err := ModeInfoAndSigToSignatureData(&tx.ModeInfo{}, nil)
		require.ErrorContains(t, err, "unexpected ModeInfo data type")
	})

	single := &tx.ModeInfo{Sum: &tx.ModeInfo_Single_{
		Single: &tx.ModeInfo_Single{Mode: signing.SignMode_SIGN_MODE_DIRECT},
	}}
	modeInfo := &tx.ModeInfo{Sum: &tx.ModeInfo_Multi_{
		Multi: &tx.ModeInfo_Multi{
			Bitarray:  &cryptotypes.CompactBitArray{ExtraBitsStored: 16, Elems: []byte{0xC0}},
			ModeInfos: []*tx.ModeInfo{single},
		},
	}}
	sig, err := (&cryptotypes.MultiSignature{Signatures: [][]byte{{1}}}).Marshal()
	require.NoError(t, err)
	_, err = ModeInfoAndSigToSignatureData(modeInfo, sig)
	require.ErrorContains(t, err, "invalid multisig bit array")
}

func TestGetSigningTxDataNilPublicKey(t *testing.T) {
	w := newBuilder(nil)
	w.tx.AuthInfo.SignerInfos = []*tx.SignerInfo{{ModeInfo: &tx.ModeInfo{Sum: &tx.ModeInfo_Single_{
		Single: &tx.ModeInfo_Single{Mode: signing.SignMode_SIGN_MODE_DIRECT},
	}}}}

	require.NotPanics(t, func() {
		data := w.GetSigningTxData()
		require.Len(t, data.AuthInfo.SignerInfos, 1)
		require.Nil(t, data.AuthInfo.SignerInfos[0].PublicKey)
	})
}
