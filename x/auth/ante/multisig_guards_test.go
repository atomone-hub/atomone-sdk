package ante_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	storetypes "cosmossdk.io/store/types"

	kmultisig "github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	"github.com/cosmos/cosmos-sdk/x/auth/types"
)

func TestConsumeMultisignatureVerificationGasMalformed(t *testing.T) {
	pks := []cryptotypes.PubKey{secp256k1.GenPrivKey().PubKey(), secp256k1.GenPrivKey().PubKey()}
	pk := kmultisig.NewLegacyAminoPubKey(1, pks)
	single := &signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON, Signature: []byte{1}}

	tests := []struct {
		name   string
		bits   *cryptotypes.CompactBitArray
		sigs   []signing.SignatureData
		errMsg string
	}{
		{
			name: "fewer signatures than set bits",
			bits: func() *cryptotypes.CompactBitArray {
				b := cryptotypes.NewCompactBitArray(2)
				b.SetIndex(0, true)
				b.SetIndex(1, true)
				return b
			}(),
			sigs:   []signing.SignatureData{single},
			errMsg: "signature size is incorrect",
		},
		{
			name:   "bit array larger than key count",
			bits:   cryptotypes.NewCompactBitArray(3),
			errMsg: "bit array size is incorrect",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sig := &signing.MultiSignatureData{BitArray: tc.bits, Signatures: tc.sigs}
			var err error
			require.NotPanics(t, func() {
				err = ante.ConsumeMultisignatureVerificationGas(storetypes.NewInfiniteGasMeter(), sig, pk, types.DefaultParams(), 0)
			})
			require.ErrorContains(t, err, tc.errMsg)
		})
	}
}

// A simulated transaction carries an empty bit array, which must still be
// accepted when the multisig public key is already on chain.
func TestConsumeMultisignatureVerificationGasSimulation(t *testing.T) {
	pks := []cryptotypes.PubKey{secp256k1.GenPrivKey().PubKey(), secp256k1.GenPrivKey().PubKey()}
	pk := kmultisig.NewLegacyAminoPubKey(1, pks)

	for _, sig := range []*signing.MultiSignatureData{
		{},
		{BitArray: &cryptotypes.CompactBitArray{}},
	} {
		require.NoError(t, ante.ConsumeMultisignatureVerificationGas(storetypes.NewInfiniteGasMeter(), sig, pk, types.DefaultParams(), 0))
	}
}

// Two signer infos for a transaction with a single signer must be rejected
// instead of indexing past the signers list.
func TestSetPubKeySignerInfoCountMismatch(t *testing.T) {
	suite := SetupTestSuite(t, true)
	suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

	priv1, _, addr1 := testdata.KeyTestPubAddr()
	priv2, _, _ := testdata.KeyTestPubAddr()

	acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr1)
	suite.accountKeeper.SetAccount(suite.ctx, acc)

	require.NoError(t, suite.txBuilder.SetMsgs(testdata.NewTestMsg(addr1)))
	suite.txBuilder.SetFeeAmount(testdata.NewTestFeeAmount())
	suite.txBuilder.SetGasLimit(testdata.NewTestGasLimit())

	tx, err := suite.CreateTestTx(suite.ctx, []cryptotypes.PrivKey{priv1, priv2}, []uint64{0, 0}, []uint64{0, 0}, suite.ctx.ChainID(), signing.SignMode_SIGN_MODE_DIRECT)
	require.NoError(t, err)

	antehandler := sdk.ChainAnteDecorators(ante.NewSetPubKeyDecorator(suite.accountKeeper))
	require.NotPanics(t, func() {
		_, err = antehandler(suite.ctx, tx, false)
	})
	require.ErrorContains(t, err, "expected 1 signer infos, got 2")
}

// The signing data adapter needs the public key of every signer info, so a
// signer info without one is rejected with an error rather than a panic, even
// when the signer being verified has its key set.
func TestSigVerificationSignerInfoWithoutPubKey(t *testing.T) {
	for _, missing := range []int{0, 1} {
		t.Run(fmt.Sprintf("signer %d has no public key", missing), func(t *testing.T) {
			suite := SetupTestSuite(t, true)
			suite.txBuilder = suite.clientCtx.TxConfig.NewTxBuilder()

			var (
				msgs []sdk.Msg
				sigs []signing.SignatureV2
			)
			for i := 0; i < 2; i++ {
				_, pub, addr := testdata.KeyTestPubAddr()
				acc := suite.accountKeeper.NewAccountWithAddress(suite.ctx, addr)
				require.NoError(t, acc.SetPubKey(pub))
				suite.accountKeeper.SetAccount(suite.ctx, acc)

				msgs = append(msgs, testdata.NewTestMsg(addr))
				sig := signing.SignatureV2{
					Data:     &signing.SingleSignatureData{SignMode: signing.SignMode_SIGN_MODE_DIRECT, Signature: []byte{1}},
					Sequence: acc.GetSequence(),
				}
				if i != missing {
					sig.PubKey = pub
				}
				sigs = append(sigs, sig)
			}
			require.NoError(t, suite.txBuilder.SetMsgs(msgs...))
			suite.txBuilder.SetFeeAmount(testdata.NewTestFeeAmount())
			suite.txBuilder.SetGasLimit(testdata.NewTestGasLimit())
			require.NoError(t, suite.txBuilder.SetSignatures(sigs...))

			antehandler := sdk.ChainAnteDecorators(ante.NewSigVerificationDecorator(suite.accountKeeper, suite.clientCtx.TxConfig.SignModeHandler()))
			var err error
			require.NotPanics(t, func() {
				_, err = antehandler(suite.ctx.WithIsSigverifyTx(true), suite.txBuilder.GetTx(), false)
			})
			require.ErrorContains(t, err, fmt.Sprintf("signer info %d has no public key", missing))
		})
	}
}
