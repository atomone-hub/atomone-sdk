package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestConsPubKeyRotationHistoryUnpack verifies that after a store round-trip
// (marshal/unmarshal) the embedded Any pubkeys have their cached values
// populated. Without UnpackInterfaces implemented on ConsPubKeyRotationHistory,
// GetCachedValue() returns nil, which breaks checkConsKeyAlreadyUsed and
// ApplyAndReturnValidatorSetUpdates (the x/staking nondeterminism simulation
// failure: "new public key is nil").
func TestConsPubKeyRotationHistoryUnpack(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	types.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	oldPk := ed25519.GenPrivKey().PubKey()
	newPk := ed25519.GenPrivKey().PubKey()

	oldAny, err := codectypes.NewAnyWithValue(oldPk)
	require.NoError(t, err)
	newAny, err := codectypes.NewAnyWithValue(newPk)
	require.NoError(t, err)

	history := types.ConsPubKeyRotationHistory{
		OperatorAddress: sdk.ValAddress(oldPk.Address()).String(),
		OldConsPubkey:   oldAny,
		NewConsPubkey:   newAny,
		Height:          1,
		Fee:             sdk.NewInt64Coin(sdk.DefaultBondDenom, 1),
	}

	bz, err := cdc.Marshal(&history)
	require.NoError(t, err)

	var restored types.ConsPubKeyRotationHistory
	require.NoError(t, cdc.Unmarshal(bz, &restored))

	// After deserialization the Any cached values must be populated; before the
	// UnpackInterfaces fix these were nil.
	restoredNew, ok := restored.NewConsPubkey.GetCachedValue().(cryptotypes.PubKey)
	require.True(t, ok, "NewConsPubkey cached value must be a cryptotypes.PubKey, got %T", restored.NewConsPubkey.GetCachedValue())
	require.True(t, restoredNew.Equals(newPk))

	restoredOld, ok := restored.OldConsPubkey.GetCachedValue().(cryptotypes.PubKey)
	require.True(t, ok, "OldConsPubkey cached value must be a cryptotypes.PubKey")
	require.True(t, restoredOld.Equals(oldPk))
}
