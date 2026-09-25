package keeper

import (
	"context"
	"time"

	"github.com/bits-and-blooms/bitset"

	"cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
)

// getPreviousConsKey checks if the key was rotated, returns the old consKey to
// get the missed blocks because missed blocks are still pointing to the old key.
func (k Keeper) getPreviousConsKey(ctx context.Context, addr sdk.ConsAddress) (sdk.ConsAddress, error) {
	oldPk, err := k.sk.ValidatorIdentifier(ctx, addr)
	if err != nil {
		return nil, err
	}

	if oldPk != nil {
		return oldPk, nil
	}

	return addr, nil
}

// currentConsAddr returns the validator's current consensus address for a
// (possibly rotated) consensus address. Equivocation evidence can be keyed to
// an old consensus address because CometBFT keeps a rotated key in the active
// validator set for ValidatorUpdateDelay blocks. When no validator resolves, the
// input address is returned unchanged.
func (k Keeper) currentConsAddr(ctx context.Context, consAddr sdk.ConsAddress) sdk.ConsAddress {
	validator, err := k.sk.ValidatorByConsAddr(ctx, consAddr)
	if err != nil || validator == nil {
		return consAddr
	}

	current, err := validator.GetConsAddr()
	if err != nil || len(current) == 0 {
		return consAddr
	}

	return sdk.ConsAddress(current)
}

// signingInfoAddr returns the consensus address under which the validator's
// live signing info is stored: the validator's current consensus address when
// it resolves and holds a record, otherwise the given address, which may hold
// the record retained for a rotated-away key.
func (k Keeper) signingInfoAddr(ctx context.Context, consAddr sdk.ConsAddress) sdk.ConsAddress {
	current := k.currentConsAddr(ctx, consAddr)
	if k.hasValidatorSigningInfo(ctx, current) {
		return current
	}

	return consAddr
}

// isCurrentValidatorConsAddr reports whether consAddr is the current consensus
// address of a validator, as opposed to an address the validator rotated away
// from, which resolves to the validator through the old-to-new mapping.
func (k Keeper) isCurrentValidatorConsAddr(ctx context.Context, consAddr sdk.ConsAddress) bool {
	validator, err := k.sk.ValidatorByConsAddr(ctx, consAddr)
	if err != nil || validator == nil {
		return false
	}

	current, err := validator.GetConsAddr()
	return err == nil && len(current) > 0 && sdk.ConsAddress(current).Equals(consAddr)
}

// performConsensusPubKeyUpdate updates the cons address to its pub key relation.
// It migrates signing info from the old pubkey to the new pubkey.
//
// The record under the old consensus address is deliberately retained: the
// old key stays in CometBFT's active validator set for ValidatorUpdateDelay
// blocks after the rotation, so equivocation evidence keyed to the old address
// can still arrive, and the evidence handler panics when signing info is
// missing there.
func (k Keeper) performConsensusPubKeyUpdate(ctx context.Context, oldPubKey, newPubKey cryptotypes.PubKey) error {
	// Connect new consensus address with PubKey.
	if err := k.AddPubkey(ctx, newPubKey); err != nil {
		return err
	}

	oldConsAddr := sdk.ConsAddress(oldPubKey.Address())
	newConsAddr := sdk.ConsAddress(newPubKey.Address())

	// Migrate ValidatorSigningInfo from oldPubKey to newPubKey.
	signingInfo, err := k.GetValidatorSigningInfo(ctx, oldConsAddr)
	if err != nil {
		return types.ErrInvalidConsPubKey.Wrap("failed to get signing info for old public key")
	}
	signingInfo.Address = newConsAddr.String()

	return k.SetValidatorSigningInfo(ctx, newConsAddr, signingInfo)
}

// GetValidatorSigningInfo retruns the ValidatorSigningInfo for a specific validator
// ConsAddress. If not found it returns ErrNoSigningInfoFound, but other errors
// may be returned if there is an error reading from the store.
func (k Keeper) GetValidatorSigningInfo(ctx context.Context, address sdk.ConsAddress) (types.ValidatorSigningInfo, error) {
	store := k.storeService.OpenKVStore(ctx)
	var info types.ValidatorSigningInfo
	bz, err := store.Get(types.ValidatorSigningInfoKey(address))
	if err != nil {
		return info, err
	}

	if bz == nil {
		return info, types.ErrNoSigningInfoFound
	}

	err = k.cdc.Unmarshal(bz, &info)
	return info, err
}

// hasValidatorSigningInfo is the raw store check, without consensus address
// resolution.
func (k Keeper) hasValidatorSigningInfo(ctx context.Context, consAddr sdk.ConsAddress) bool {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(types.ValidatorSigningInfoKey(consAddr))
	return err == nil && bz != nil
}

// HasValidatorSigningInfo returns if a given validator has signing information
// persisted. The consensus address is resolved to the validator's current key
// first, so that old-key equivocation evidence finds the migrated record
// instead of reporting the validator as absent.
func (k Keeper) HasValidatorSigningInfo(ctx context.Context, consAddr sdk.ConsAddress) bool {
	return k.hasValidatorSigningInfo(ctx, k.signingInfoAddr(ctx, consAddr))
}

// SetValidatorSigningInfo sets the validator signing info to a consensus address key
func (k Keeper) SetValidatorSigningInfo(ctx context.Context, address sdk.ConsAddress, info types.ValidatorSigningInfo) error {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := k.cdc.Marshal(&info)
	if err != nil {
		return err
	}

	return store.Set(types.ValidatorSigningInfoKey(address), bz)
}

// IterateValidatorSigningInfos iterates over the stored ValidatorSigningInfo
func (k Keeper) IterateValidatorSigningInfos(ctx context.Context,
	handler func(address sdk.ConsAddress, info types.ValidatorSigningInfo) (stop bool),
) error {
	store := k.storeService.OpenKVStore(ctx)
	iter, err := store.Iterator(types.ValidatorSigningInfoKeyPrefix, storetypes.PrefixEndBytes(types.ValidatorSigningInfoKeyPrefix))
	if err != nil {
		return err
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		address := types.ValidatorSigningInfoAddress(iter.Key())
		var info types.ValidatorSigningInfo
		err = k.cdc.Unmarshal(iter.Value(), &info)
		if err != nil {
			return err
		}

		if handler(address, info) {
			break
		}
	}
	return nil
}

// JailUntil attempts to set a validator's JailedUntil attribute in its signing
// info. It will panic if the signing info does not exist for the validator.
// The consensus address is resolved to the validator's current key first:
// double-sign jailing via an old consensus address must extend the jail time
// that Unjail later reads on the current key.
func (k Keeper) JailUntil(ctx context.Context, consAddr sdk.ConsAddress, jailTime time.Time) error {
	consAddr = k.signingInfoAddr(ctx, consAddr)
	signInfo, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err != nil {
		return errors.Wrap(err, "cannot jail validator that does not have any signing information")
	}

	signInfo.JailedUntil = jailTime
	return k.SetValidatorSigningInfo(ctx, consAddr, signInfo)
}

// Tombstone attempts to tombstone a validator. It will panic if signing info for
// the given validator does not exist. The consensus address is resolved to the
// validator's current key first: tombstoning via an old consensus address must
// block Unjail, which reads the tombstone on the current key.
func (k Keeper) Tombstone(ctx context.Context, consAddr sdk.ConsAddress) error {
	consAddr = k.signingInfoAddr(ctx, consAddr)
	signInfo, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err != nil {
		return types.ErrNoSigningInfoFound.Wrap("cannot tombstone validator that does not have any signing information")
	}

	if signInfo.Tombstoned {
		return types.ErrValidatorTombstoned.Wrap("cannot tombstone validator that is already tombstoned")
	}

	signInfo.Tombstoned = true
	return k.SetValidatorSigningInfo(ctx, consAddr, signInfo)
}

// IsTombstoned returns if a given validator by consensus address is tombstoned.
// The consensus address is resolved to the validator's current key first, so
// that old-key equivocation evidence is ignored once the validator has been
// tombstoned through any of its keys.
func (k Keeper) IsTombstoned(ctx context.Context, consAddr sdk.ConsAddress) bool {
	signInfo, err := k.GetValidatorSigningInfo(ctx, k.signingInfoAddr(ctx, consAddr))
	if err != nil {
		return false
	}

	return signInfo.Tombstoned
}

// getMissedBlockBitmapChunk gets the bitmap chunk at the given chunk index for
// a validator's missed block signing window.
func (k Keeper) getMissedBlockBitmapChunk(ctx context.Context, addr sdk.ConsAddress, chunkIndex int64) ([]byte, error) {
	store := k.storeService.OpenKVStore(ctx)
	chunk, err := store.Get(types.ValidatorMissedBlockBitmapKey(addr, chunkIndex))
	return chunk, err
}

// setMissedBlockBitmapChunk sets the bitmap chunk at the given chunk index for
// a validator's missed block signing window.
func (k Keeper) setMissedBlockBitmapChunk(ctx context.Context, addr sdk.ConsAddress, chunkIndex int64, chunk []byte) error {
	store := k.storeService.OpenKVStore(ctx)
	key := types.ValidatorMissedBlockBitmapKey(addr, chunkIndex)
	return store.Set(key, chunk)
}

// GetMissedBlockBitmapValue returns true if a validator missed signing a block
// at the given index and false otherwise. The index provided is assumed to be
// the index in the range [0, SignedBlocksWindow), which represents the bitmap
// where each bit represents a height, and is determined by the validator's
// IndexOffset modulo SignedBlocksWindow. This index is used to fetch the chunk
// in the bitmap and the relative bit in that chunk.
func (k Keeper) GetMissedBlockBitmapValue(ctx context.Context, addr sdk.ConsAddress, index int64) (bool, error) {
	// Check if the key was rotated; if so, use the returned consKey to get the missed blocks
	// because missed blocks are still pointing to the old key.
	addr, err := k.getPreviousConsKey(ctx, addr)
	if err != nil {
		return false, err
	}

	// get the chunk or "word" in the logical bitmap
	chunkIndex := index / types.MissedBlockBitmapChunkSize

	bs := bitset.New(uint(types.MissedBlockBitmapChunkSize))
	chunk, err := k.getMissedBlockBitmapChunk(ctx, addr, chunkIndex)
	if err != nil {
		return false, errors.Wrapf(err, "failed to get bitmap chunk; index: %d", index)
	}

	if chunk != nil {
		if err := bs.UnmarshalBinary(chunk); err != nil {
			return false, errors.Wrapf(err, "failed to decode bitmap chunk; index: %d", index)
		}
	}

	// get the bit position in the chunk of the logical bitmap, where Test()
	// checks if the bit is set.
	bitIndex := index % types.MissedBlockBitmapChunkSize
	return bs.Test(uint(bitIndex)), nil
}

// SetMissedBlockBitmapValue sets, i.e. flips, a bit in the validator's missed
// block bitmap. When missed=true, the bit is set, otherwise it set to zero. The
// index provided is assumed to be the index in the range [0, SignedBlocksWindow),
// which represents the bitmap where each bit represents a height, and is
// determined by the validator's IndexOffset modulo SignedBlocksWindow. This
// index is used to fetch the chunk in the bitmap and the relative bit in that
// chunk.
func (k Keeper) SetMissedBlockBitmapValue(ctx context.Context, addr sdk.ConsAddress, index int64, missed bool) error {
	// Check if the key was rotated; if so, use the returned consKey to set the missed blocks
	// because missed blocks are still pointing to the old key.
	addr, err := k.getPreviousConsKey(ctx, addr)
	if err != nil {
		return err
	}

	// get the chunk or "word" in the logical bitmap
	chunkIndex := index / types.MissedBlockBitmapChunkSize

	bs := bitset.New(uint(types.MissedBlockBitmapChunkSize))
	chunk, err := k.getMissedBlockBitmapChunk(ctx, addr, chunkIndex)
	if err != nil {
		return errors.Wrapf(err, "failed to get bitmap chunk; index: %d", index)
	}

	if chunk != nil {
		if err := bs.UnmarshalBinary(chunk); err != nil {
			return errors.Wrapf(err, "failed to decode bitmap chunk; index: %d", index)
		}
	}

	// get the bit position in the chunk of the logical bitmap
	bitIndex := uint(index % types.MissedBlockBitmapChunkSize)
	if missed {
		bs.Set(bitIndex)
	} else {
		bs.Clear(bitIndex)
	}

	updatedChunk, err := bs.MarshalBinary()
	if err != nil {
		return errors.Wrapf(err, "failed to encode bitmap chunk; index: %d", index)
	}

	k.setMissedBlockBitmapChunk(ctx, addr, chunkIndex, updatedChunk)
	return nil
}

// DeleteMissedBlockBitmap removes a validator's missed block bitmap from state.
func (k Keeper) DeleteMissedBlockBitmap(ctx context.Context, addr sdk.ConsAddress) error {
	// Check if the key was rotated; if so, use the returned consKey to delete the missed blocks
	// because missed blocks are still pointing to the old key.
	addr, err := k.getPreviousConsKey(ctx, addr)
	if err != nil {
		return err
	}

	store := k.storeService.OpenKVStore(ctx)
	prefix := types.ValidatorMissedBlockBitmapPrefixKey(addr)
	iter, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		err = store.Delete(iter.Key())
		if err != nil {
			return err
		}
	}
	return nil
}

// IterateMissedBlockBitmap iterates over a validator's signed blocks window
// bitmap and performs a callback function on each index, i.e. block height, in
// the range [0, SignedBlocksWindow).
//
// Note: A callback will only be executed over all bitmap chunks that exist in
// state.
func (k Keeper) IterateMissedBlockBitmap(ctx context.Context, addr sdk.ConsAddress, cb func(index int64, missed bool) (stop bool)) error {
	// Check if the key was rotated; if so, use the returned consKey to iterate the missed blocks
	// because missed blocks are still pointing to the old key.
	addr, err := k.getPreviousConsKey(ctx, addr)
	if err != nil {
		return err
	}

	store := k.storeService.OpenKVStore(ctx)
	prefix := types.ValidatorMissedBlockBitmapPrefixKey(addr)
	iter, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return err
	}
	defer iter.Close()

	var index int64
	for ; iter.Valid(); iter.Next() {
		bs := bitset.New(uint(types.MissedBlockBitmapChunkSize))

		if err := bs.UnmarshalBinary(iter.Value()); err != nil {
			return errors.Wrapf(err, "failed to decode bitmap chunk; index: %v", string(iter.Key()))
		}

		for i := uint(0); i < types.MissedBlockBitmapChunkSize; i++ {
			// execute the callback, where Test() returns true if the bit is set
			if cb(index, bs.Test(i)) {
				break
			}

			index++
		}
	}
	return nil
}

// GetValidatorMissedBlocks returns array of missed blocks for given validator.
func (k Keeper) GetValidatorMissedBlocks(ctx context.Context, addr sdk.ConsAddress) ([]types.MissedBlock, error) {
	signedBlocksWindow, err := k.SignedBlocksWindow(ctx)
	if err != nil {
		return nil, err
	}

	missedBlocks := make([]types.MissedBlock, 0, signedBlocksWindow)
	err = k.IterateMissedBlockBitmap(ctx, addr, func(index int64, missed bool) (stop bool) {
		if missed {
			missedBlocks = append(missedBlocks, types.NewMissedBlock(index, missed))
		}

		return false
	})

	return missedBlocks, err
}
