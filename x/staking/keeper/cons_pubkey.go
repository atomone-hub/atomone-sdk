package keeper

import (
	"bytes"
	"context"
	"time"

	errorsmod "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

const maxRotations = 1

// setConsPubKeyRotationHistory stores a consensus pubkey rotation history entry
// and enqueues the rotation for deferred processing after the unbonding period.
func (k Keeper) setConsPubKeyRotationHistory(ctx context.Context, valAddr sdk.ValAddress, oldPubKey, newPubKey *codectypes.Any, fee sdk.Coin) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	store := k.storeService.OpenKVStore(ctx)
	height := uint64(sdkCtx.BlockHeight())

	// Check if another rotation in this block already uses the same new consensus pubkey.
	blockRotations, err := k.GetBlockConsPubKeyRotationHistory(ctx)
	if err != nil {
		return err
	}
	for _, r := range blockRotations {
		if r.NewConsPubkey != nil && newPubKey != nil && bytes.Equal(r.NewConsPubkey.Value, newPubKey.Value) {
			return types.ErrValidatorPubKeyExists
		}
	}

	valAddrStr, err := k.validatorAddressCodec.BytesToString(valAddr)
	if err != nil {
		return err
	}

	history := types.ConsPubKeyRotationHistory{
		OperatorAddress: valAddrStr,
		OldConsPubkey:   oldPubKey,
		NewConsPubkey:   newPubKey,
		Height:          height,
		Fee:             fee,
	}

	bz, err := k.cdc.Marshal(&history)
	if err != nil {
		return err
	}

	// Store in the validator-indexed history (permanent record).
	key := types.GetValidatorConsPubKeyRotationHistoryKey(valAddr, height)
	if err := store.Set(key, bz); err != nil {
		return err
	}

	// Also store in the height-indexed history (pruned after processing) to allow
	// efficient EndBlock iteration by current block height.
	blockKey := types.GetBlockConsPubKeyRotationHistoryKey(height, valAddr)
	if err := store.Set(blockKey, bz); err != nil {
		return err
	}

	unbondingTime, err := k.UnbondingTime(ctx)
	if err != nil {
		return err
	}

	queueTime := sdkCtx.BlockHeader().Time.Add(unbondingTime)

	indexKey := types.GetValidatorConsKeyRotationIndexKey(valAddr, queueTime)
	if err := store.Set(indexKey, []byte{}); err != nil {
		return err
	}

	return k.setConsKeyQueue(ctx, queueTime, valAddr)
}

// updateToNewPubkey updates the validator with a new consensus pubkey during EndBlock.
func (k Keeper) updateToNewPubkey(ctx context.Context, val types.Validator, oldPubKey, newPubKey *codectypes.Any, fee sdk.Coin) error {
	store := k.storeService.OpenKVStore(ctx)

	consAddr, err := val.GetConsAddr()
	if err != nil {
		return err
	}

	// Remove old ValidatorByConsAddr entry.
	if err := store.Delete(types.GetValidatorByConsAddrKey(consAddr)); err != nil {
		return err
	}

	// Delete old power index before updating.
	if err := k.DeleteValidatorByPowerIndex(ctx, val); err != nil {
		return err
	}

	// Update the validator's consensus pubkey.
	val.ConsensusPubkey = newPubKey

	if err := k.SetValidator(ctx, val); err != nil {
		return err
	}
	if err := k.SetValidatorByConsAddr(ctx, val); err != nil {
		return err
	}
	if err := k.SetValidatorByPowerIndex(ctx, val); err != nil {
		return err
	}

	oldPk, ok := oldPubKey.GetCachedValue().(cryptotypes.PubKey)
	if !ok {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidType, "expecting cryptotypes.PubKey, got %T", oldPubKey.GetCachedValue())
	}

	newPk, ok := newPubKey.GetCachedValue().(cryptotypes.PubKey)
	if !ok {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidType, "expecting cryptotypes.PubKey, got %T", newPubKey.GetCachedValue())
	}

	// Store old-to-new consensus address mapping.
	if err := store.Set(types.GetOldToNewConsAddrMapKey(oldPk.Address()), newPk.Address()); err != nil {
		return err
	}

	if err := k.setConsAddrToValidatorIdentifierMap(ctx, sdk.ConsAddress(oldPk.Address()), sdk.ConsAddress(newPk.Address())); err != nil {
		return err
	}

	return k.Hooks().AfterConsensusPubKeyUpdate(ctx, oldPk, newPk, fee)
}

// setConsAddrToValidatorIdentifierMap maps the new consensus address back to the
// initial (original) consensus address, supporting chained lookups.
func (k Keeper) setConsAddrToValidatorIdentifierMap(ctx context.Context, oldConsAddr, newConsAddr sdk.ConsAddress) error {
	store := k.storeService.OpenKVStore(ctx)

	// Chase the chain: if oldConsAddr itself has a mapping, use that as the identifier.
	bz, err := store.Get(types.GetConsAddrToValidatorIdentifierMapKey(oldConsAddr))
	if err != nil {
		return err
	}
	if bz != nil {
		oldConsAddr = bz
	}

	return store.Set(types.GetConsAddrToValidatorIdentifierMapKey(newConsAddr), oldConsAddr)
}

// ValidatorIdentifier returns the initial consensus address for a rotated key.
// Returns nil if no mapping exists.
func (k Keeper) ValidatorIdentifier(ctx context.Context, newPk sdk.ConsAddress) (sdk.ConsAddress, error) {
	store := k.storeService.OpenKVStore(ctx)

	bz, err := store.Get(types.GetConsAddrToValidatorIdentifierMapKey(newPk))
	if err != nil {
		return nil, err
	}

	if bz == nil {
		return nil, nil
	}

	return bz, nil
}

// ExceedsMaxRotations checks if the validator has exceeded the allowed number of
// consensus key rotations within the current unbonding period.
func (k Keeper) ExceedsMaxRotations(ctx context.Context, valAddr sdk.ValAddress) error {
	store := k.storeService.OpenKVStore(ctx)

	prefix := append(types.ValidatorConsensusKeyRotationRecordIndexKey, valAddr...)
	iterator, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return err
	}
	defer iterator.Close()

	count := 0
	for ; iterator.Valid(); iterator.Next() {
		count++
	}

	if count >= maxRotations {
		return types.ErrExceedingMaxConsPubKeyRotations
	}

	return nil
}

// setConsKeyQueue appends the validator address to the rotation queue entry at ts.
func (k Keeper) setConsKeyQueue(ctx context.Context, ts time.Time, valAddr sdk.ValAddress) error {
	store := k.storeService.OpenKVStore(ctx)
	queueKey := types.GetValidatorConsKeyRotationQueueKey(ts)

	valAddrStr, err := k.validatorAddressCodec.BytesToString(valAddr)
	if err != nil {
		return err
	}

	var addrs types.ValAddrsOfRotatedConsKeys
	bz, err := store.Get(queueKey)
	if err != nil {
		return err
	}
	if bz != nil {
		if err := k.cdc.Unmarshal(bz, &addrs); err != nil {
			return err
		}
	}

	// Only append if not already present.
	for _, addr := range addrs.Addresses {
		if addr == valAddrStr {
			return nil
		}
	}

	addrs.Addresses = append(addrs.Addresses, valAddrStr)
	out, err := k.cdc.Marshal(&addrs)
	if err != nil {
		return err
	}

	return store.Set(queueKey, out)
}

// PurgeAllMaturedConsKeyRotatedKeys removes all rotation index entries for
// validators whose rotation waiting period has matured.
func (k Keeper) PurgeAllMaturedConsKeyRotatedKeys(ctx context.Context, maturedTime time.Time) error {
	addrs, err := k.getAndRemoveAllMaturedRotatedKeys(ctx, maturedTime)
	if err != nil {
		return err
	}

	for _, addr := range addrs {
		if err := k.deleteConsKeyIndexKey(ctx, addr, maturedTime); err != nil {
			return err
		}
	}

	return nil
}

// DeleteBlockConsPubKeyRotationHistory removes the height-indexed rotation history
// entries for the current block. Called after EndBlock processing to keep the
// height index pruned.
func (k Keeper) DeleteBlockConsPubKeyRotationHistory(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	store := k.storeService.OpenKVStore(ctx)
	currentHeight := uint64(sdkCtx.BlockHeight())

	prefix := types.GetBlockConsPubKeyRotationHistoryPrefix(currentHeight)
	iterator, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return err
	}
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		if err := store.Delete(iterator.Key()); err != nil {
			return err
		}
	}

	return nil
}

// deleteConsKeyIndexKey deletes all rotation index entries for a validator
// with timestamps up to and including ts.
func (k Keeper) deleteConsKeyIndexKey(ctx context.Context, valAddr sdk.ValAddress, ts time.Time) error {
	store := k.storeService.OpenKVStore(ctx)

	prefix := append(types.ValidatorConsensusKeyRotationRecordIndexKey, valAddr...)
	iterator, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return err
	}
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		key := iterator.Key()
		// Extract the time suffix from the key: prefix (1 byte) + valAddr + timeBytes
		timeBz := key[1+len(valAddr):]
		keyTime, err := sdk.ParseTimeBytes(timeBz)
		if err != nil {
			return err
		}
		if !keyTime.After(ts) {
			if err := store.Delete(key); err != nil {
				return err
			}
		}
	}

	return nil
}

// getAndRemoveAllMaturedRotatedKeys collects all validator addresses from rotation
// queue entries that have matured, and deletes those queue entries.
func (k Keeper) getAndRemoveAllMaturedRotatedKeys(ctx context.Context, matureTime time.Time) ([][]byte, error) {
	store := k.storeService.OpenKVStore(ctx)

	iterator, err := store.Iterator(
		types.ValidatorConsensusKeyRotationRecordQueueKey,
		storetypes.InclusiveEndBytes(types.GetValidatorConsKeyRotationQueueKey(matureTime)),
	)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	var addrs [][]byte

	for ; iterator.Valid(); iterator.Next() {
		var rotatedKeys types.ValAddrsOfRotatedConsKeys
		if err := k.cdc.Unmarshal(iterator.Value(), &rotatedKeys); err != nil {
			return nil, err
		}

		for _, addrStr := range rotatedKeys.Addresses {
			addr, err := k.validatorAddressCodec.StringToBytes(addrStr)
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, addr)
		}

		if err := store.Delete(iterator.Key()); err != nil {
			return nil, err
		}
	}

	return addrs, nil
}

// GetBlockConsPubKeyRotationHistory returns all rotation history entries for the current block height.
// Uses the height-indexed store prefix for efficient lookup.
func (k Keeper) GetBlockConsPubKeyRotationHistory(ctx context.Context) ([]types.ConsPubKeyRotationHistory, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	store := k.storeService.OpenKVStore(ctx)
	currentHeight := uint64(sdkCtx.BlockHeight())

	prefix := types.GetBlockConsPubKeyRotationHistoryPrefix(currentHeight)
	iterator, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	var histories []types.ConsPubKeyRotationHistory
	for ; iterator.Valid(); iterator.Next() {
		var h types.ConsPubKeyRotationHistory
		if err := k.cdc.Unmarshal(iterator.Value(), &h); err != nil {
			return nil, err
		}
		histories = append(histories, h)
	}

	return histories, nil
}

// GetValidatorConsPubKeyRotationHistory returns all rotation history entries for a given validator.
func (k Keeper) GetValidatorConsPubKeyRotationHistory(ctx context.Context, operatorAddress sdk.ValAddress) ([]types.ConsPubKeyRotationHistory, error) {
	store := k.storeService.OpenKVStore(ctx)

	prefix := append(types.ValidatorConsPubKeyRotationHistoryKey, operatorAddress...)
	iterator, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	var histories []types.ConsPubKeyRotationHistory
	for ; iterator.Valid(); iterator.Next() {
		var h types.ConsPubKeyRotationHistory
		if err := k.cdc.Unmarshal(iterator.Value(), &h); err != nil {
			return nil, err
		}
		histories = append(histories, h)
	}

	return histories, nil
}
