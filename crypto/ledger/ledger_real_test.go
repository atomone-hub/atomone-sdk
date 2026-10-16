//go:build !ledger || !test_ledger_mock
// +build !ledger !test_ledger_mock

package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestOpenApp(t *testing.T) {
	app, err := openApp(newFakeCosmosDevice())
	require.NoError(t, err)
	require.IsType(t, &cosmosApp{}, app)

	_, err = openApp(errorDevice{sw: 0x6E00})
	require.ErrorContains(t, err, "open the Cosmos app on the Ledger device")

	_, err = openApp(errorDevice{sw: swDeviceLocked})
	require.ErrorContains(t, err, "unlock")

	_, err = openApp(failingDevice{})
	require.ErrorContains(t, err, "transfer failed")
}

// TestPrivKeyOnCosmosApp runs this package's Ledger key code, as the keyring
// does for --ledger keys, against the Cosmos app client.
func TestPrivKeyOnCosmosApp(t *testing.T) {
	d := newFakeCosmosDevice()
	SetDiscoverLedger(func() (SECP256K1, error) { return openApp(d) })
	t.Cleanup(func() { SetDiscoverLedger(discoverCosmosApp) })

	params := *hd.NewFundraiserParams(0, sdk.CoinType, 0)
	priv, addr, err := NewPrivKeySecp256k1(params, "atone")
	require.NoError(t, err)
	require.Equal(t, d.address("atone"), addr)
	require.Equal(t, d.key.PubKey().Bytes(), priv.PubKey().Bytes())

	msg := []byte(`{"account_number":"1","chain_id":"atomone-1","msgs":[]}`)
	for _, sign := range []func([]byte) ([]byte, error){
		priv.Sign, // SIGN_MODE_TEXTUAL
		priv.(PrivKeyLedgerSecp256k1).SignLedgerAminoJSON,
	} {
		sig, err := sign(msg)
		require.NoError(t, err)
		require.True(t, priv.PubKey().VerifySignature(msg, sig))
	}
}
