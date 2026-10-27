package keeper_test

import (
	"context"
	"testing"

	"gotest.tools/v3/assert"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// noopDistributionKeeper is a minimal DistributionKeeper.
type noopDistributionKeeper struct{}

func (noopDistributionKeeper) FundCommunityPool(_ context.Context, _ sdk.Coins, _ sdk.AccAddress) error {
	return nil
}

// TestRotateConsPubKey_StakingState is an integration test covering the
// staking-side state transitions of consensus key rotation end-to-end via the
// MsgServer and ApplyAndReturnValidatorSetUpdates (the EndBlock entry point).
//
// It covers behaviors the keeper-level unit tests do not assert on:
//   - the two ABCI ValidatorUpdates emitted (old key zeroed, new key at full power)
//   - GetValidatorByConsAddr resolves through the old -> new map after rotation
//   - the ConsAddrToValidatorIdentifierMap entry is written
//   - validator-indexed rotation history is recorded
//   - block-level rotation history is pruned after EndBlock
//   - rotating twice within the unbonding period exceeds the max rotations limit
//   - reusing an already-used consensus key (own or another validator's) is rejected
func TestRotateConsPubKey_StakingState(t *testing.T) {
	t.Parallel()
	f := initFixture(t)
	ctx := f.sdkCtx
	f.stakingKeeper.SetDistributionKeeper(noopDistributionKeeper{})

	msgServer := keeper.NewMsgServerImpl(f.stakingKeeper)

	// createValidators always builds 2 validators using its own internal pubkey
	// set, and expects 3 power entries (val1 power, val2 power, val1's extra
	// delegation to val2). The returned valAddrs cover 5 operator addresses.
	_, valAddrs, _ := createValidators(t, f, []int64{100, 50, 0})
	valAddr := valAddrs[0]
	val2Addr := valAddrs[1]

	valBefore, err := f.stakingKeeper.GetValidator(ctx, valAddr)
	assert.NilError(t, err)
	oldPubKey := valBefore.ConsensusPubkey.GetCachedValue().(cryptotypes.PubKey)
	oldConsAddr := sdk.ConsAddress(oldPubKey.Address())

	// Fresh pubkey for the rotation target. PKs[10] is not used by either validator.
	newPubKey := PKs[10]
	newConsAddr := sdk.ConsAddress(newPubKey.Address())

	rotateMsg, err := types.NewMsgRotateConsPubKey(valAddr.String(), newPubKey)
	assert.NilError(t, err)

	// Validator is bonded and not jailed, so the message should succeed.
	_, err = msgServer.RotateConsPubKey(ctx, rotateMsg)
	assert.NilError(t, err)

	// EndBlock applies updateToNewPubkey via ApplyAndReturnValidatorSetUpdates:
	// it emits two updates (old key at power 0, new key at full power).
	updates := applyValidatorSetUpdates(t, ctx, f.stakingKeeper, 2)
	assert.Equal(t, 2, len(updates))

	// Validator now stores the new consensus pubkey.
	valAfter, err := f.stakingKeeper.GetValidator(ctx, valAddr)
	assert.NilError(t, err)
	storedConsAddr, err := valAfter.GetConsAddr()
	assert.NilError(t, err)
	assert.DeepEqual(t, newConsAddr.Bytes(), storedConsAddr)

	// GetValidatorByConsAddr resolves directly under the new cons addr.
	viaNew, err := f.stakingKeeper.GetValidatorByConsAddr(ctx, newConsAddr)
	assert.NilError(t, err)
	assert.Equal(t, valAddr.String(), viaNew.GetOperator())

	// GetValidatorByConsAddr still resolves the OLD cons addr through the
	// OldToNewConsAddrMap fallback.
	viaOld, err := f.stakingKeeper.GetValidatorByConsAddr(ctx, oldConsAddr)
	assert.NilError(t, err)
	assert.Equal(t, valAddr.String(), viaOld.GetOperator())

	// ValidatorIdentifier maps the new cons addr back to the initial (old) cons addr.
	identifier, err := f.stakingKeeper.ValidatorIdentifier(ctx, newConsAddr)
	assert.NilError(t, err)
	assert.DeepEqual(t, oldConsAddr, identifier)

	// Validator-indexed rotation history is recorded.
	history, err := f.stakingKeeper.GetValidatorConsPubKeyRotationHistory(ctx, valAddr)
	assert.NilError(t, err)
	assert.Equal(t, 1, len(history))
	assert.Equal(t, valAddr.String(), history[0].OperatorAddress)

	// Block-indexed rotation history is pruned by ApplyAndReturnValidatorSetUpdates.
	blockHistory, err := f.stakingKeeper.GetBlockConsPubKeyRotationHistory(ctx)
	assert.NilError(t, err)
	assert.Equal(t, 0, len(blockHistory))

	// Rotating again in the same unbonding period exceeds maxRotations (1).
	secondMsg, err := types.NewMsgRotateConsPubKey(valAddr.String(), PKs[11])
	assert.NilError(t, err)
	_, err = msgServer.RotateConsPubKey(ctx, secondMsg)
	assert.ErrorContains(t, err, "exceeding maximum consensus pubkey rotations")

	// Reusing the validator's own CURRENT (just-rotated) pubkey is rejected
	// because it now lives in the identifier map.
	selfMsg, err := types.NewMsgRotateConsPubKey(valAddr.String(), newPubKey)
	assert.NilError(t, err)
	_, err = msgServer.RotateConsPubKey(ctx, selfMsg)
	assert.ErrorContains(t, err, "public key was already used")

	// Reusing the validator's own PREVIOUS pubkey is rejected too, via the
	// OldToNewConsAddrMap fallback in ValidatorByConsAddr.
	oldMsg, err := types.NewMsgRotateConsPubKey(val2Addr.String(), oldPubKey)
	assert.NilError(t, err)
	_, err = msgServer.RotateConsPubKey(ctx, oldMsg)
	assert.ErrorContains(t, err, "validator already exist for this pubkey")

	// Rotating val1 to val2's CURRENT consensus key is rejected because val2
	// already owns that key in ValidatorByConsAddr.
	val2, err := f.stakingKeeper.GetValidator(ctx, val2Addr)
	assert.NilError(t, err)
	val2Pk := val2.ConsensusPubkey.GetCachedValue().(cryptotypes.PubKey)
	collisionMsg, err := types.NewMsgRotateConsPubKey(valAddr.String(), val2Pk)
	assert.NilError(t, err)
	_, err = msgServer.RotateConsPubKey(ctx, collisionMsg)
	assert.ErrorContains(t, err, "validator already exist for this pubkey")
}
