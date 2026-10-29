package keeper

import (
	"context"
	"time"

	"github.com/cometbft/cometbft/crypto"

	sdkmath "cosmossdk.io/math"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
)

var _ types.StakingHooks = Hooks{}

// Hooks wrapper struct for slashing keeper
type Hooks struct {
	k Keeper
}

// Return the slashing hooks
func (k Keeper) Hooks() Hooks {
	return Hooks{k}
}

// AfterValidatorBonded updates the signing info start height or create a new signing info
func (h Hooks) AfterValidatorBonded(ctx context.Context, consAddr sdk.ConsAddress, _ sdk.ValAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	signingInfo, err := h.k.GetValidatorSigningInfo(ctx, consAddr)
	if err == nil {
		signingInfo.StartHeight = sdkCtx.BlockHeight()

		// A live validator can never be tombstoned here: Unjail rejects
		// tombstoned validators, so a tombstone cannot survive the only path
		// back to the Bonded state. A tombstoned record at this point is a
		// leftover of a previous, removed validator that used the same
		// consensus key: validator removal deletes only the addr-pubkey
		// relation, the signing info record survives it, and the key can be
		// re-registered afterwards. Clear the tombstone so double-sign
		// evidence against the new validator is processed instead of being
		// ignored by the evidence handler's IsTombstoned early-return.
		signingInfo.Tombstoned = false
	} else {
		// A brand-new signing-info record is created. Any missed-block bitmap
		// still stored under this consensus address belongs to a previous
		// validator that used the same key: consensus-key rotation migrates the
		// signing-info record to the new key and deletes the old key's record,
		// but deliberately leaves the bitmap keyed under the old (initial)
		// consensus address for the rotated validator's own use. If that key is
		// later re-adopted by a new validator, the stale bitmap paired with the
		// fresh MissedBlocksCounter would let the counter go negative as the
		// new validator signs and freeze below the jail threshold while it
		// misses every block — permanent downtime-jail evasion. Delete the
		// stale bitmap so the fresh record starts paired with an empty bitmap.
		if err := h.k.DeleteMissedBlockBitmap(ctx, consAddr); err != nil {
			return err
		}
		signingInfo = types.NewValidatorSigningInfo(
			consAddr,
			sdkCtx.BlockHeight(),
			0,
			time.Unix(0, 0),
			false,
			0,
		)
	}

	return h.k.SetValidatorSigningInfo(ctx, consAddr, signingInfo)
}

// AfterValidatorRemoved deletes the address-pubkey relation when a validator is removed,
func (h Hooks) AfterValidatorRemoved(ctx context.Context, consAddr sdk.ConsAddress, _ sdk.ValAddress) error {
	return h.k.deleteAddrPubkeyRelation(ctx, crypto.Address(consAddr))
}

// AfterValidatorCreated adds the address-pubkey relation when a validator is created.
func (h Hooks) AfterValidatorCreated(ctx context.Context, valAddr sdk.ValAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	validator, err := h.k.sk.Validator(ctx, valAddr)
	if err != nil {
		return err
	}

	consPk, err := validator.ConsPubKey()
	if err != nil {
		return err
	}

	return h.k.AddPubkey(sdkCtx, consPk)
}

func (h Hooks) AfterValidatorBeginUnbonding(_ context.Context, _ sdk.ConsAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeValidatorModified(_ context.Context, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeDelegationCreated(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeDelegationSharesModified(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeDelegationRemoved(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterDelegationModified(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeValidatorSlashed(_ context.Context, _ sdk.ValAddress, _ sdkmath.LegacyDec) error {
	return nil
}

func (h Hooks) AfterUnbondingInitiated(_ context.Context, _ uint64) error {
	return nil
}

// AfterConsensusPubKeyUpdate triggers the functions to rotate the signing-infos
// also sets address pubkey relation.
// The old key's cons-address -> pubkey relation is deliberately kept: a rotated
// key remains in CometBFT's active validator set for ValidatorUpdateDelay
// blocks, and the evidence handler drops equivocation reports whose consensus
// address has no pubkey relation.
func (h Hooks) AfterConsensusPubKeyUpdate(ctx context.Context, oldPubKey, newPubKey cryptotypes.PubKey, _ sdk.Coin) error {
	return h.k.performConsensusPubKeyUpdate(ctx, oldPubKey, newPubKey)
}

func (h Hooks) BeforeConsensusPubKeyRotation(_ context.Context, _ sdk.ValAddress, _, _ cryptotypes.PubKey) error {
	return nil
}
