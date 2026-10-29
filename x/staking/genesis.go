package staking

import (
	"fmt"

	cmttypes "github.com/cometbft/cometbft/types"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// WriteValidators returns a slice of bonded genesis validators.
func WriteValidators(ctx sdk.Context, keeper *keeper.Keeper) (vals []cmttypes.GenesisValidator, returnErr error) {
	err := keeper.IterateLastValidators(ctx, func(_ int64, validator types.ValidatorI) (stop bool) {
		pk, err := validator.ConsPubKey()
		if err != nil {
			returnErr = err
			return true
		}
		cmtPk, err := cryptocodec.ToCmtPubKeyInterface(pk)
		if err != nil {
			returnErr = err
			return true
		}

		vals = append(vals, cmttypes.GenesisValidator{
			Address: sdk.ConsAddress(cmtPk.Address()).Bytes(),
			PubKey:  cmtPk,
			Power:   validator.GetConsensusPower(keeper.PowerReduction(ctx)),
			Name:    validator.GetMoniker(),
		})

		return false
	})
	if err != nil {
		return nil, err
	}

	return
}

// ValidateGenesis validates the provided staking genesis state to ensure the
// expected invariants hold. (i.e. params in correct bounds, no duplicate validators)
func ValidateGenesis(data *types.GenesisState) error {
	if err := validateGenesisStateValidators(data.Validators); err != nil {
		return err
	}

	if err := validateGenesisConsKeyRotations(data); err != nil {
		return err
	}

	return data.Params.Validate()
}

// validateGenesisConsKeyRotations checks the consensus key rotation state for
// records InitGenesis cannot write or that would overwrite one another.
func validateGenesisConsKeyRotations(data *types.GenesisState) error {
	// A rotation writes an index record and lists the validator in the queue
	// entry at the same time. EndBlock purges an index record only through
	// that queue entry, so the two must match one to one.
	unqueued := make(map[string]types.RotationIndexRecord, len(data.RotationIndexRecords))
	for _, record := range data.RotationIndexRecords {
		if err := sdk.VerifyAddressFormat(record.Address); err != nil {
			return fmt.Errorf("invalid rotation index record address: %w", err)
		}
		key := string(types.GetValidatorConsKeyRotationIndexKey(record.Address, record.Time))
		if _, dup := unqueued[key]; dup {
			return fmt.Errorf("duplicate rotation index record for %s at %s", sdk.ValAddress(record.Address), record.Time)
		}
		unqueued[key] = record
	}

	queueTimes := make(map[string]struct{}, len(data.RotationQueue))
	for _, record := range data.RotationQueue {
		if len(record.ValAddrs.Addresses) == 0 {
			return fmt.Errorf("rotation queue record at %s lists no validators", record.Time)
		}
		timeKey := string(sdk.FormatTimeBytes(record.Time))
		if _, dup := queueTimes[timeKey]; dup {
			return fmt.Errorf("duplicate rotation queue record at %s", record.Time)
		}
		queueTimes[timeKey] = struct{}{}

		for _, addr := range record.ValAddrs.Addresses {
			valAddr, err := sdk.ValAddressFromBech32(addr)
			if err != nil {
				return fmt.Errorf("invalid rotation queue address at %s: %w", record.Time, err)
			}
			key := string(types.GetValidatorConsKeyRotationIndexKey(valAddr, record.Time))
			if _, ok := unqueued[key]; !ok {
				return fmt.Errorf("rotation queue record at %s lists %s without a matching index record", record.Time, addr)
			}
			delete(unqueued, key)
		}
	}

	for _, record := range data.RotationIndexRecords {
		if _, ok := unqueued[string(types.GetValidatorConsKeyRotationIndexKey(record.Address, record.Time))]; ok {
			return fmt.Errorf("rotation index record for %s at %s has no matching queue record", sdk.ValAddress(record.Address), record.Time)
		}
	}

	histories := make(map[string]struct{}, len(data.RotationHistory))
	for _, history := range data.RotationHistory {
		valAddr, err := sdk.ValAddressFromBech32(history.OperatorAddress)
		if err != nil {
			return fmt.Errorf("invalid rotation history operator address at height %d: %w", history.Height, err)
		}
		key := string(types.GetValidatorConsPubKeyRotationHistoryKey(valAddr, history.Height))
		if _, dup := histories[key]; dup {
			return fmt.Errorf("duplicate rotation history for %s at height %d", history.OperatorAddress, history.Height)
		}
		histories[key] = struct{}{}

		if history.OldConsPubkey == nil || history.NewConsPubkey == nil {
			return fmt.Errorf("rotation history for %s at height %d is missing a consensus pubkey", history.OperatorAddress, history.Height)
		}
	}

	rotated := make(map[string]struct{}, len(data.RotatedConsAddresses))
	for _, r := range data.RotatedConsAddresses {
		if err := sdk.VerifyAddressFormat(r.OldAddress); err != nil {
			return fmt.Errorf("invalid rotated consensus address: %w", err)
		}
		if err := sdk.VerifyAddressFormat(r.NewAddress); err != nil {
			return fmt.Errorf("invalid rotated-to consensus address for %X: %w", r.OldAddress, err)
		}
		if _, dup := rotated[string(r.OldAddress)]; dup {
			return fmt.Errorf("duplicate rotated consensus address %X", r.OldAddress)
		}
		rotated[string(r.OldAddress)] = struct{}{}
	}

	identified := make(map[string]struct{}, len(data.ConsAddrIdentifiers))
	for _, id := range data.ConsAddrIdentifiers {
		if err := sdk.VerifyAddressFormat(id.ConsAddress); err != nil {
			return fmt.Errorf("invalid consensus address identifier key: %w", err)
		}
		if err := sdk.VerifyAddressFormat(id.InitialConsAddress); err != nil {
			return fmt.Errorf("invalid initial consensus address for %X: %w", id.ConsAddress, err)
		}
		if _, dup := identified[string(id.ConsAddress)]; dup {
			return fmt.Errorf("duplicate consensus address identifier %X", id.ConsAddress)
		}
		identified[string(id.ConsAddress)] = struct{}{}
	}

	return nil
}

func validateGenesisStateValidators(validators []types.Validator) error {
	addrMap := make(map[string]bool, len(validators))

	for i := range validators {
		val := validators[i]
		consPk, err := val.ConsPubKey()
		if err != nil {
			return err
		}

		strKey := string(consPk.Bytes())

		if _, ok := addrMap[strKey]; ok {
			consAddr, err := val.GetConsAddr()
			if err != nil {
				return err
			}
			return fmt.Errorf("duplicate validator in genesis state: moniker %v, address %v", val.Description.Moniker, consAddr)
		}

		if val.Jailed && val.IsBonded() {
			consAddr, err := val.GetConsAddr()
			if err != nil {
				return err
			}
			return fmt.Errorf("validator is bonded and jailed in genesis state: moniker %v, address %v", val.Description.Moniker, consAddr)
		}

		if val.DelegatorShares.IsZero() && !val.IsUnbonding() {
			return fmt.Errorf("bonded/unbonded genesis validator cannot have zero delegator shares, validator: %v", val)
		}

		addrMap[strKey] = true
	}

	return nil
}
