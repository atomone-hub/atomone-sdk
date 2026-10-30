package staking_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"cosmossdk.io/math"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking"
	"github.com/cosmos/cosmos-sdk/x/staking/testutil"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestValidateGenesis(t *testing.T) {
	genValidators1 := make([]types.Validator, 1, 5)
	pk := ed25519.GenPrivKey().PubKey()
	genValidators1[0] = testutil.NewValidator(t, sdk.ValAddress(pk.Address()), pk)
	genValidators1[0].Tokens = math.OneInt()
	genValidators1[0].DelegatorShares = math.LegacyOneDec()

	oldPk, newPk := ed25519.GenPrivKey().PubKey(), ed25519.GenPrivKey().PubKey()
	oldConsAddr, newConsAddr := oldPk.Address().Bytes(), newPk.Address().Bytes()
	valAddr := sdk.ValAddress(pk.Address())
	otherValAddr := sdk.ValAddress(ed25519.GenPrivKey().PubKey().Address())
	rotationTime := time.Unix(1_700_000_000, 0).UTC()
	oldAny, err := codectypes.NewAnyWithValue(oldPk)
	assert.NoError(t, err)
	newAny, err := codectypes.NewAnyWithValue(newPk)
	assert.NoError(t, err)
	validHistory := types.ConsPubKeyRotationHistory{
		OperatorAddress: valAddr.String(),
		OldConsPubkey:   oldAny,
		NewConsPubkey:   newAny,
		Height:          1,
	}

	tests := []struct {
		name    string
		mutate  func(*types.GenesisState)
		wantErr bool
	}{
		{"default", func(*types.GenesisState) {}, false},
		// validate genesis validators
		{"duplicate validator", func(data *types.GenesisState) {
			data.Validators = genValidators1
			data.Validators = append(data.Validators, genValidators1[0])
		}, true},
		{"no delegator shares", func(data *types.GenesisState) {
			data.Validators = genValidators1
			data.Validators[0].DelegatorShares = math.LegacyZeroDec()
		}, true},
		{"jailed and bonded validator", func(data *types.GenesisState) {
			data.Validators = genValidators1
			data.Validators[0].Jailed = true
			data.Validators[0].Status = types.Bonded
		}, true},
		// validate consensus key rotation state
		{"valid rotation state", func(data *types.GenesisState) {
			data.RotationIndexRecords = []types.RotationIndexRecord{{Address: valAddr, Time: rotationTime}}
			data.RotationHistory = []types.ConsPubKeyRotationHistory{validHistory}
			data.RotationQueue = []types.RotationQueueRecord{{ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{valAddr.String()}}, Time: rotationTime}}
			data.RotatedConsAddresses = []types.RotatedConsAddress{{OldAddress: oldConsAddr, NewAddress: newConsAddr}}
			data.ConsAddrIdentifiers = []types.ConsAddrIdentifier{{ConsAddress: newConsAddr, InitialConsAddress: oldConsAddr}}
		}, false},
		{"two rotations queued at the same time", func(data *types.GenesisState) {
			data.RotationIndexRecords = []types.RotationIndexRecord{{Address: valAddr, Time: rotationTime}, {Address: otherValAddr, Time: rotationTime}}
			data.RotationQueue = []types.RotationQueueRecord{{ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{valAddr.String(), otherValAddr.String()}}, Time: rotationTime}}
		}, false},
		{"rotation index record without address", func(data *types.GenesisState) {
			data.RotationIndexRecords = []types.RotationIndexRecord{{Time: rotationTime}}
		}, true},
		{"duplicate rotation index record", func(data *types.GenesisState) {
			data.RotationIndexRecords = []types.RotationIndexRecord{{Address: valAddr, Time: rotationTime}, {Address: valAddr, Time: rotationTime}}
			data.RotationQueue = []types.RotationQueueRecord{{ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{valAddr.String()}}, Time: rotationTime}}
		}, true},
		{"rotation index record without queue record", func(data *types.GenesisState) {
			data.RotationIndexRecords = []types.RotationIndexRecord{{Address: valAddr, Time: rotationTime}}
		}, true},
		{"rotation index record queued at another time", func(data *types.GenesisState) {
			data.RotationIndexRecords = []types.RotationIndexRecord{{Address: valAddr, Time: rotationTime}}
			data.RotationQueue = []types.RotationQueueRecord{{ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{valAddr.String()}}, Time: rotationTime.Add(time.Second)}}
		}, true},
		{"rotation history without new pubkey", func(data *types.GenesisState) {
			history := validHistory
			history.NewConsPubkey = nil
			data.RotationHistory = []types.ConsPubKeyRotationHistory{history}
		}, true},
		{"rotation history with invalid operator address", func(data *types.GenesisState) {
			history := validHistory
			history.OperatorAddress = "invalid"
			data.RotationHistory = []types.ConsPubKeyRotationHistory{history}
		}, true},
		{"duplicate rotation history", func(data *types.GenesisState) {
			data.RotationHistory = []types.ConsPubKeyRotationHistory{validHistory, validHistory}
		}, true},
		{"rotation queue record without validators", func(data *types.GenesisState) {
			data.RotationQueue = []types.RotationQueueRecord{{Time: rotationTime}}
		}, true},
		{"rotation queue record without index record", func(data *types.GenesisState) {
			data.RotationQueue = []types.RotationQueueRecord{{ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{valAddr.String()}}, Time: rotationTime}}
		}, true},
		{"rotation queue record with invalid address", func(data *types.GenesisState) {
			data.RotationQueue = []types.RotationQueueRecord{{ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{"invalid"}}, Time: rotationTime}}
		}, true},
		{"rotation queue record listing a validator twice", func(data *types.GenesisState) {
			data.RotationIndexRecords = []types.RotationIndexRecord{{Address: valAddr, Time: rotationTime}}
			data.RotationQueue = []types.RotationQueueRecord{{ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{valAddr.String(), valAddr.String()}}, Time: rotationTime}}
		}, true},
		{"duplicate rotation queue record", func(data *types.GenesisState) {
			data.RotationIndexRecords = []types.RotationIndexRecord{{Address: valAddr, Time: rotationTime}, {Address: otherValAddr, Time: rotationTime}}
			data.RotationQueue = []types.RotationQueueRecord{
				{ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{valAddr.String()}}, Time: rotationTime},
				{ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{otherValAddr.String()}}, Time: rotationTime},
			}
		}, true},
		{"rotated consensus address without target", func(data *types.GenesisState) {
			data.RotatedConsAddresses = []types.RotatedConsAddress{{OldAddress: oldConsAddr}}
		}, true},
		{"duplicate rotated consensus address", func(data *types.GenesisState) {
			data.RotatedConsAddresses = []types.RotatedConsAddress{
				{OldAddress: oldConsAddr, NewAddress: newConsAddr},
				{OldAddress: oldConsAddr, NewAddress: valAddr},
			}
		}, true},
		{"duplicate consensus address identifier", func(data *types.GenesisState) {
			data.ConsAddrIdentifiers = []types.ConsAddrIdentifier{
				{ConsAddress: newConsAddr, InitialConsAddress: oldConsAddr},
				{ConsAddress: newConsAddr, InitialConsAddress: valAddr},
			}
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			genesisState := types.DefaultGenesisState()
			tt.mutate(genesisState)

			if tt.wantErr {
				assert.Error(t, staking.ValidateGenesis(genesisState))
			} else {
				assert.NoError(t, staking.ValidateGenesis(genesisState))
			}
		})
	}
}
