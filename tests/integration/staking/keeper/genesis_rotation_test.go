package keeper_test

import (
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"cosmossdk.io/math"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktestutil "github.com/cosmos/cosmos-sdk/x/bank/testutil"
	"github.com/cosmos/cosmos-sdk/x/staking"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestGenesisConsKeyRotationRoundTrip rotates a consensus key, exports genesis,
// and imports it into a fresh chain. Every rotation lookup must behave there
// as it did on the exporting chain.
func TestGenesisConsKeyRotationRoundTrip(t *testing.T) {
	t.Parallel()
	f := initFixture(t)
	ctx := f.sdkCtx
	f.stakingKeeper.SetDistributionKeeper(noopDistributionKeeper{})
	msgServer := keeper.NewMsgServerImpl(f.stakingKeeper)

	_, valAddrs, _ := createValidators(t, f, []int64{100, 50, 0})
	valAddr := valAddrs[0]
	val, err := f.stakingKeeper.GetValidator(ctx, valAddr)
	assert.NilError(t, err)
	oldConsAddr := sdk.ConsAddress(val.ConsensusPubkey.GetCachedValue().(cryptotypes.PubKey).Address())
	newPubKey := PKs[10]
	newConsAddr := sdk.ConsAddress(newPubKey.Address())

	rotateMsg, err := types.NewMsgRotateConsPubKey(valAddr.String(), newPubKey)
	assert.NilError(t, err)
	_, err = msgServer.RotateConsPubKey(ctx, rotateMsg)
	assert.NilError(t, err)
	applyValidatorSetUpdates(t, ctx, f.stakingKeeper, 2)

	exported := f.stakingKeeper.ExportGenesis(ctx)
	assert.Equal(t, 1, len(exported.RotationIndexRecords))
	assert.Equal(t, 1, len(exported.RotationHistory))
	assert.Equal(t, 1, len(exported.RotationQueue))
	assert.Equal(t, 1, len(exported.RotatedConsAddresses))
	assert.Equal(t, 1, len(exported.ConsAddrIdentifiers))
	// The fixture holds a validator with no delegator shares, which genesis
	// validation rejects, so validate the rotation state on its own.
	rotationState := *exported
	rotationState.Validators = nil
	assert.NilError(t, staking.ValidateGenesis(&rotationState))

	// Import into a fresh chain. InitGenesis requires the module pools to hold
	// exactly the exported bonded and not-bonded tokens.
	f2 := initFixture(t)
	ctx2 := f2.sdkCtx
	f2.stakingKeeper.SetDistributionKeeper(noopDistributionKeeper{})
	msgServer2 := keeper.NewMsgServerImpl(f2.stakingKeeper)
	_, err = f2.stakingKeeper.GetValidatorByConsAddr(ctx2, oldConsAddr)
	assert.Assert(t, errors.Is(err, types.ErrNoValidatorFound))

	bonded, notBonded := math.ZeroInt(), math.ZeroInt()
	for _, v := range exported.Validators {
		if v.IsBonded() {
			bonded = bonded.Add(v.Tokens)
		} else {
			notBonded = notBonded.Add(v.Tokens)
		}
	}
	for _, ubd := range exported.UnbondingDelegations {
		for _, entry := range ubd.Entries {
			notBonded = notBonded.Add(entry.Balance)
		}
	}
	bondDenom, err := f2.stakingKeeper.BondDenom(ctx2)
	assert.NilError(t, err)
	for pool, amount := range map[string]math.Int{types.BondedPoolName: bonded, types.NotBondedPoolName: notBonded} {
		if amount.IsPositive() {
			assert.NilError(t, banktestutil.FundModuleAccount(ctx2, f2.bankKeeper, pool, sdk.NewCoins(sdk.NewCoin(bondDenom, amount))))
		}
	}

	// Import the address strings in their all-uppercase bech32 form. The
	// re-export below must still match the canonical export.
	imported := *exported
	imported.RotationHistory = []types.ConsPubKeyRotationHistory{exported.RotationHistory[0]}
	imported.RotationHistory[0].OperatorAddress = strings.ToUpper(exported.RotationHistory[0].OperatorAddress)
	imported.RotationQueue = []types.RotationQueueRecord{{
		ValAddrs: types.ValAddrsOfRotatedConsKeys{Addresses: []string{strings.ToUpper(exported.RotationQueue[0].ValAddrs.Addresses[0])}},
		Time:     exported.RotationQueue[0].Time,
	}}
	f2.stakingKeeper.InitGenesis(ctx2, &imported)

	got, err := f2.stakingKeeper.GetValidatorByConsAddr(ctx2, oldConsAddr)
	assert.NilError(t, err)
	assert.Equal(t, valAddr.String(), got.GetOperator())

	identifier, err := f2.stakingKeeper.ValidatorIdentifier(ctx2, newConsAddr)
	assert.NilError(t, err)
	assert.DeepEqual(t, oldConsAddr, identifier)

	history, err := f2.stakingKeeper.GetValidatorConsPubKeyRotationHistory(ctx2, valAddr)
	assert.NilError(t, err)
	assert.Equal(t, 1, len(history))

	// The one-rotation-per-unbonding-period limit survives the import.
	secondMsg, err := types.NewMsgRotateConsPubKey(valAddr.String(), PKs[11])
	assert.NilError(t, err)
	_, err = msgServer2.RotateConsPubKey(ctx2, secondMsg)
	assert.ErrorContains(t, err, "exceeding maximum consensus pubkey rotations")

	// So does the reservation of the rotated-to key.
	val2Msg, err := types.NewMsgRotateConsPubKey(valAddrs[1].String(), newPubKey)
	assert.NilError(t, err)
	_, err = msgServer2.RotateConsPubKey(ctx2, val2Msg)
	assert.ErrorContains(t, err, "public key was already used")

	reexported := f2.stakingKeeper.ExportGenesis(ctx2)
	assert.DeepEqual(t, exported.RotationIndexRecords, reexported.RotationIndexRecords)
	assert.DeepEqual(t, exported.RotationQueue, reexported.RotationQueue)
	assert.DeepEqual(t, exported.RotatedConsAddresses, reexported.RotatedConsAddresses)
	assert.DeepEqual(t, exported.ConsAddrIdentifiers, reexported.ConsAddrIdentifiers)
	assert.Equal(t, exported.RotationHistory[0].OperatorAddress, reexported.RotationHistory[0].OperatorAddress)
	assert.Equal(t, exported.RotationHistory[0].Height, reexported.RotationHistory[0].Height)
	assert.DeepEqual(t, exported.RotationHistory[0].NewConsPubkey.Value, reexported.RotationHistory[0].NewConsPubkey.Value)
}
