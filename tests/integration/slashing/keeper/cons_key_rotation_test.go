package keeper_test

import (
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtestutil "github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestConsensusKeyRotation_PreservesMissedBlocks is an integration test for the
// consensus key rotation feature end-to-end:
//
//  1. Create a bonded validator with slashing signing info keyed under its
//     original consensus address.
//  2. Record a missed block under the old consensus address.
//  3. Rotate the consensus key via MsgRotateConsPubKey and run the EndBlocker
//     (which triggers staking's updateToNewPubkey -> slashing's
//     AfterConsensusPubKeyUpdate hook).
//  4. Verify that:
//     - signing info was migrated to the new consensus address, while the
//     old-address record is retained (frozen) for old-key evidence
//     accountability.
//     - the pubkey relation for the old consensus address is retained.
//     - the missed-block bitmap stays keyed under the old consensus address
//     (1 entry) and remains reachable from the new consensus address via the
//     ValidatorIdentifier indirection.
//     - the validator now uses the new consensus pubkey.
func TestConsensusKeyRotation_PreservesMissedBlocks(t *testing.T) {
	t.Parallel()
	f := initFixture(t)
	ctx := f.ctx

	// Fresh validator + its initial consensus key.
	oldPriv := ed25519.GenPrivKey()
	oldPk := oldPriv.PubKey()
	oldConsAddr := sdk.ConsAddress(oldPk.Address())

	valAddr := sdk.ValAddress(f.addrDels[0])

	// Register the pubkey + signing info before the validator is created so the
	// slashing begin blocker has the records to consult.
	assert.NilError(t, f.slashingKeeper.AddPubkey(ctx, oldPk))
	signingInfo := slashingtypes.NewValidatorSigningInfo(
		oldConsAddr,
		ctx.BlockHeight(),
		int64(0),
		time.Unix(0, 0),
		false,
		int64(0),
	)
	assert.NilError(t, f.slashingKeeper.SetValidatorSigningInfo(ctx, oldConsAddr, signingInfo))

	// Create a bonded validator using the old consensus key.
	tstaking := stakingtestutil.NewHelper(t, ctx, f.stakingKeeper)
	tstaking.CreateValidatorWithValPower(valAddr, oldPk, 100, true)
	_, err := f.stakingKeeper.EndBlocker(ctx)
	assert.NilError(t, err)

	// Validator is bonded and finds itself via the old cons addr.
	val, err := f.stakingKeeper.GetValidator(ctx, valAddr)
	assert.NilError(t, err)
	assert.Equal(t, stakingtypes.Bonded, val.GetStatus())

	// Mark one block as missed under the old consensus address.
	assert.NilError(t, f.slashingKeeper.SetMissedBlockBitmapValue(ctx, oldConsAddr, 10, true))
	got, err := f.slashingKeeper.GetMissedBlockBitmapValue(ctx, oldConsAddr, 10)
	assert.NilError(t, err)
	assert.Equal(t, true, got)

	// Rotate the consensus key. After EndBlock, staking's updateToNewPubkey
	// fires AfterConsensusPubKeyUpdate on slashing.
	newPriv := ed25519.GenPrivKey()
	newPk := newPriv.PubKey()
	newConsAddr := sdk.ConsAddress(newPk.Address())

	msgServer := stakingkeeper.NewMsgServerImpl(f.stakingKeeper)
	rotateMsg, err := stakingtypes.NewMsgRotateConsPubKey(valAddr.String(), newPk)
	assert.NilError(t, err)

	_, err = msgServer.RotateConsPubKey(ctx, rotateMsg)
	assert.NilError(t, err)

	_, err = f.stakingKeeper.EndBlocker(ctx)
	assert.NilError(t, err)

	// Validator's stored consensus address is now derived from the new key.
	val, err = f.stakingKeeper.GetValidator(ctx, valAddr)
	assert.NilError(t, err)
	storedConsAddr, err := val.GetConsAddr()
	assert.NilError(t, err)
	assert.DeepEqual(t, newConsAddr.Bytes(), storedConsAddr)

	// staking now maps new cons addr -> old cons addr (the "validator identifier").
	identifier, err := f.stakingKeeper.ValidatorIdentifier(ctx, newConsAddr)
	assert.NilError(t, err)
	assert.DeepEqual(t, oldConsAddr, identifier)

	// Signing info was migrated: the live record now lives at the new address
	// with the new address embedded in it, and the old-address record is
	// retained (frozen) so old-key equivocation evidence still finds it.
	retainedInfo, err := f.slashingKeeper.GetValidatorSigningInfo(ctx, oldConsAddr)
	assert.NilError(t, err)
	assert.Equal(t, oldConsAddr.String(), retainedInfo.Address)
	migratedInfo, err := f.slashingKeeper.GetValidatorSigningInfo(ctx, newConsAddr)
	assert.NilError(t, err)
	assert.Equal(t, newConsAddr.String(), migratedInfo.Address)

	// The pubkey relation for the new consensus address is set, and the one for
	// the old address is retained (old-key evidence accountability).
	savedPk, err := f.slashingKeeper.GetPubkey(ctx, newConsAddr.Bytes())
	assert.NilError(t, err)
	assert.DeepEqual(t, newPk.Bytes(), savedPk.Bytes())
	savedOldPk, err := f.slashingKeeper.GetPubkey(ctx, oldConsAddr.Bytes())
	assert.NilError(t, err)
	assert.DeepEqual(t, oldPk.Bytes(), savedOldPk.Bytes())

	// The bitmap stays under the OLD consensus address and still holds 1 entry.
	oldMissed, err := f.slashingKeeper.GetValidatorMissedBlocks(ctx, oldConsAddr)
	assert.NilError(t, err)
	assert.Equal(t, 1, len(oldMissed))

	// And it is reachable transparently from the NEW consensus address through
	// the ValidatorIdentifier indirection in getPreviousConsKey.
	newMissed, err := f.slashingKeeper.GetValidatorMissedBlocks(ctx, newConsAddr)
	assert.NilError(t, err)
	assert.Equal(t, 1, len(newMissed))

	// Further updates via the new consensus address must hit the same underlying
	// bitmap (still keyed under oldConsAddr).
	assert.NilError(t, f.slashingKeeper.SetMissedBlockBitmapValue(ctx, newConsAddr, 11, true))
	stillMissed, err := f.slashingKeeper.GetValidatorMissedBlocks(ctx, oldConsAddr)
	assert.NilError(t, err)
	assert.Equal(t, 2, len(stillMissed))
}

// TestConsensusKeyRotation_GenesisExport_SingleMissedBlocksCopy verifies that
// genesis export emits the shared missed-block bitmap exactly once: under the
// validator's current consensus address, not again under the retained
// old-address record. Both records resolve to the one physical bitmap keyed
// under the initial consensus address, so exporting it per record would
// double-count downtime in tooling that sums missed blocks across entries.
func TestConsensusKeyRotation_GenesisExport_SingleMissedBlocksCopy(t *testing.T) {
	t.Parallel()
	f := initFixture(t)
	ctx := f.ctx

	oldPk := ed25519.GenPrivKey().PubKey()
	oldConsAddr := sdk.ConsAddress(oldPk.Address())
	valAddr := sdk.ValAddress(f.addrDels[2])

	assert.NilError(t, f.slashingKeeper.AddPubkey(ctx, oldPk))
	signingInfo := slashingtypes.NewValidatorSigningInfo(
		oldConsAddr,
		ctx.BlockHeight(),
		int64(0),
		time.Unix(0, 0),
		false,
		int64(0),
	)
	assert.NilError(t, f.slashingKeeper.SetValidatorSigningInfo(ctx, oldConsAddr, signingInfo))

	tstaking := stakingtestutil.NewHelper(t, ctx, f.stakingKeeper)
	tstaking.CreateValidatorWithValPower(valAddr, oldPk, 100, true)
	_, err := f.stakingKeeper.EndBlocker(ctx)
	assert.NilError(t, err)

	// One missed block, keyed under the old consensus address.
	assert.NilError(t, f.slashingKeeper.SetMissedBlockBitmapValue(ctx, oldConsAddr, 10, true))

	newPk := ed25519.GenPrivKey().PubKey()
	newConsAddr := sdk.ConsAddress(newPk.Address())
	msgServer := stakingkeeper.NewMsgServerImpl(f.stakingKeeper)
	rotateMsg, err := stakingtypes.NewMsgRotateConsPubKey(valAddr.String(), newPk)
	assert.NilError(t, err)
	_, err = msgServer.RotateConsPubKey(ctx, rotateMsg)
	assert.NilError(t, err)
	_, err = f.stakingKeeper.EndBlocker(ctx)
	assert.NilError(t, err)

	genesis := f.slashingKeeper.ExportGenesis(ctx)

	// Both signing-info records of the rotated validator are exported (the
	// fixture also pre-seeds unrelated records, so match by address).
	infoAddrs := map[string]bool{}
	for _, si := range genesis.SigningInfos {
		infoAddrs[si.Address] = true
	}
	assert.Assert(t, infoAddrs[oldConsAddr.String()])
	assert.Assert(t, infoAddrs[newConsAddr.String()])

	missedByAddr := map[string][]slashingtypes.MissedBlock{}
	totalMissed := 0
	for _, mb := range genesis.MissedBlocks {
		missedByAddr[mb.Address] = mb.MissedBlocks
		totalMissed += len(mb.MissedBlocks)
	}

	// The physical bitmap is exported exactly once, under the validator's
	// current consensus address; the retained old-address entry is empty.
	assert.Equal(t, 1, totalMissed)
	assert.Equal(t, 1, len(missedByAddr[newConsAddr.String()]))
	assert.Equal(t, 0, len(missedByAddr[oldConsAddr.String()]))
}
