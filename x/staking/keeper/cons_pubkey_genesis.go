package keeper

import (
	"bytes"
	"context"
	"time"

	corestore "cosmossdk.io/core/store"
	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// sortableTimeLen is the width of the time suffix in rotation index keys.
// sdk.FormatTimeBytes always produces this many bytes.
var sortableTimeLen = len(sdk.FormatTimeBytes(time.Time{}))

// initConsKeyRotationGenesis writes the consensus key rotation state from genesis.
// The block-indexed rotation history is not part of genesis: EndBlock prunes it
// in the block that writes it.
func (k Keeper) initConsKeyRotationGenesis(ctx context.Context, data *types.GenesisState) error {
	store := k.storeService.OpenKVStore(ctx)

	for _, record := range data.RotationIndexRecords {
		if err := store.Set(types.GetValidatorConsKeyRotationIndexKey(record.Address, record.Time), []byte{}); err != nil {
			return err
		}
	}

	for i := range data.RotationHistory {
		history := data.RotationHistory[i]
		valAddr, err := k.validatorAddressCodec.StringToBytes(history.OperatorAddress)
		if err != nil {
			return err
		}
		bz, err := k.cdc.Marshal(&history)
		if err != nil {
			return err
		}
		if err := store.Set(types.GetValidatorConsPubKeyRotationHistoryKey(valAddr, history.Height), bz); err != nil {
			return err
		}
	}

	for i := range data.RotationQueue {
		record := data.RotationQueue[i]
		bz, err := k.cdc.Marshal(&record.ValAddrs)
		if err != nil {
			return err
		}
		if err := store.Set(types.GetValidatorConsKeyRotationQueueKey(record.Time), bz); err != nil {
			return err
		}
	}

	for _, rotated := range data.RotatedConsAddresses {
		if err := store.Set(types.GetOldToNewConsAddrMapKey(rotated.OldAddress), rotated.NewAddress); err != nil {
			return err
		}
	}

	for _, identifier := range data.ConsAddrIdentifiers {
		if err := store.Set(types.GetConsAddrToValidatorIdentifierMapKey(identifier.ConsAddress), identifier.InitialConsAddress); err != nil {
			return err
		}
	}

	return nil
}

// exportConsKeyRotationGenesis reads the consensus key rotation state into gs.
func (k Keeper) exportConsKeyRotationGenesis(ctx context.Context, gs *types.GenesisState) error {
	store := k.storeService.OpenKVStore(ctx)

	err := iteratePrefix(store, types.ValidatorConsensusKeyRotationRecordIndexKey, func(key, _ []byte) error {
		addrEnd := len(key) - sortableTimeLen
		ts, err := sdk.ParseTimeBytes(key[addrEnd:])
		if err != nil {
			return err
		}
		gs.RotationIndexRecords = append(gs.RotationIndexRecords, types.RotationIndexRecord{
			Address: bytes.Clone(key[:addrEnd]),
			Time:    ts,
		})
		return nil
	})
	if err != nil {
		return err
	}

	err = iteratePrefix(store, types.ValidatorConsPubKeyRotationHistoryKey, func(_, value []byte) error {
		var history types.ConsPubKeyRotationHistory
		if err := k.cdc.Unmarshal(value, &history); err != nil {
			return err
		}
		gs.RotationHistory = append(gs.RotationHistory, history)
		return nil
	})
	if err != nil {
		return err
	}

	err = iteratePrefix(store, types.ValidatorConsensusKeyRotationRecordQueueKey, func(key, value []byte) error {
		ts, err := sdk.ParseTimeBytes(key)
		if err != nil {
			return err
		}
		var addrs types.ValAddrsOfRotatedConsKeys
		if err := k.cdc.Unmarshal(value, &addrs); err != nil {
			return err
		}
		gs.RotationQueue = append(gs.RotationQueue, types.RotationQueueRecord{ValAddrs: addrs, Time: ts})
		return nil
	})
	if err != nil {
		return err
	}

	err = iteratePrefix(store, types.OldToNewConsAddrMap, func(key, value []byte) error {
		gs.RotatedConsAddresses = append(gs.RotatedConsAddresses, types.RotatedConsAddress{
			OldAddress: bytes.Clone(key),
			NewAddress: bytes.Clone(value),
		})
		return nil
	})
	if err != nil {
		return err
	}

	return iteratePrefix(store, types.ConsAddrToValidatorIdentifierMapPrefix, func(key, value []byte) error {
		gs.ConsAddrIdentifiers = append(gs.ConsAddrIdentifiers, types.ConsAddrIdentifier{
			ConsAddress:        bytes.Clone(key),
			InitialConsAddress: bytes.Clone(value),
		})
		return nil
	})
}

// iteratePrefix calls fn with each key under prefix, with the prefix stripped,
// and its value.
func iteratePrefix(store corestore.KVStore, prefix []byte, fn func(key, value []byte) error) error {
	iter, err := store.Iterator(prefix, storetypes.PrefixEndBytes(prefix))
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		if err := fn(iter.Key()[len(prefix):], iter.Value()); err != nil {
			return err
		}
	}
	return nil
}
