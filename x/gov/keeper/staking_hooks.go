package keeper

import (
	context "context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Hooks wrapper struct for gov keeper
type Hooks struct {
	k Keeper
}

var _ stakingtypes.StakingHooks = Hooks{}

// Return the staking hooks
func (keeper Keeper) StakingHooks() Hooks {
	return Hooks{keeper}
}

// BeforeDelegationSharesModified is called when a delegation's shares are modified
// We trigger a governor shares decrease here subtracting all delegation shares.
// The right amount of shares will be possibly added back in AfterDelegationModified
func (h Hooks) BeforeDelegationSharesModified(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	// does the delegator have a governance delegation?
	govDelegation, err := h.k.GovernanceDelegations.Get(ctx, delAddr)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	}
	govAddr := types.MustGovernorAddressFromBech32(govDelegation.GovernorAddress)

	// Fetch the delegation
	delegation, _ := h.k.sk.GetDelegation(ctx, delAddr, valAddr)

	// update the Governor's Validator shares
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	h.k.DecreaseGovernorShares(sdkCtx, govAddr, valAddr, delegation.Shares)

	return nil
}

// AfterDelegationModified is called when a delegation is created or modified.
// We trigger a governor shares increase here adding all delegation shares.
// It is balanced by the full-amount decrease in BeforeDelegationSharesModified.
// If the delegator is an active governor's account, revalidate min self-delegation
// (flipping the governor inactive if it no longer holds) and keep the
// ActiveGovernorsByDelegatedValidator index in sync.
func (h Hooks) AfterDelegationModified(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	// does the delegator have a governance delegation?
	govDelegation, err := h.k.GovernanceDelegations.Get(ctx, delAddr)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	}

	// Fetch the delegation
	delegation, err := h.k.sk.GetDelegation(ctx, delAddr, valAddr)
	if err != nil {
		return err
	}

	govAddr := types.MustGovernorAddressFromBech32(govDelegation.GovernorAddress)

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	h.k.IncreaseGovernorShares(sdkCtx, govAddr, valAddr, delegation.GetShares())

	// is the delegator the governor of the gov delegation? (i.e. governor self-delegation)
	delGovAddr := types.GovernorAddress(delAddr.Bytes())
	if !delGovAddr.Equals(govAddr) {
		// regular gov delegator (not the governor), nothing more to do
		return nil
	}

	// delegator is the governor
	governor, err := h.k.Governors.Get(ctx, delGovAddr)
	if err != nil {
		return err
	}
	if !governor.IsActive() {
		// inactive governor self-delegation: do not touch the index (active-only invariant)
		return nil
	}
	if governor.GovernorAddress != govDelegation.GovernorAddress {
		panic("active governor delegating to another governor")
	}

	if !h.k.ValidateGovernorMinSelfDelegation(sdkCtx, governor) {
		return h.setGovernorInactive(ctx, sdkCtx, governor)
	}
	return h.k.ActiveGovernorsByDelegatedValidator.Set(ctx, collections.Join(valAddr, delGovAddr))
}

// BeforeDelegationRemoved is called when a delegation is removed
// We verify if the delegator is also an active governor and if so check
// that the min self-delegation requirement is still met, otherwise set governor
// status to inactive. Update the ActiveGovernorsByDelegatedValidator index accordingly.
func (h Hooks) BeforeDelegationRemoved(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	delGovAddr := types.GovernorAddress(delAddr.Bytes())
	governor, err := h.k.Governors.Get(ctx, delGovAddr)
	if errors.Is(err, collections.ErrNotFound) {
		// not a governor, nothing to do
		return nil
	}
	if err != nil {
		return err
	}

	// the delegator is a governor (active or otherwise): drop the ActiveGovernorsByDelegatedValidator
	// entry for this validator, since their own staking delegation here is going away
	if err := h.k.ActiveGovernorsByDelegatedValidator.Remove(ctx, collections.Join(valAddr, delGovAddr)); err != nil {
		return err
	}

	if !governor.IsActive() {
		return nil
	}

	// active governor: revalidate against post-removal state
	govDelegation, err := h.k.GovernanceDelegations.Get(ctx, delAddr)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if errors.Is(err, collections.ErrNotFound) {
		panic("active governor without governance self-delegation")
	}
	if governor.GovernorAddress != govDelegation.GovernorAddress {
		panic("active governor delegating to another governor")
	}

	exclusion := map[string]struct{}{
		valAddr.String(): {},
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if !h.k.validateGovernorMinSelfDelegationWithExclusions(sdkCtx, governor, exclusion) {
		return h.setGovernorInactive(ctx, sdkCtx, governor)
	}
	return nil
}

// BeforeValidatorSlashed revalidates active governors that delegate to the slashed
// validator. If the projected post-slash bonded amount falls below the minimum
// self-delegation, the governor is set to inactive. Status flips and index
// cleanup are deferred until after the index walk completes so we don't write
// to the iterated domain (per the store-iterator contract).
func (h Hooks) BeforeValidatorSlashed(ctx context.Context, valAddr sdk.ValAddress, fraction math.LegacyDec) error {
	validator, err := h.k.sk.GetValidator(ctx, valAddr)
	if err != nil {
		// validator no longer exists; nothing to revalidate
		return nil
	}
	valTotalShares := validator.GetDelegatorShares()
	if valTotalShares.IsZero() {
		return nil
	}
	valBondedTokens := validator.GetBondedTokens()

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	var toDeactivate []v1.Governor
	err = h.walkGovernorsForValidator(ctx, valAddr, func(governor v1.Governor) error {
		govAddr := governor.GetAddress()
		delegation, err := h.k.sk.GetDelegation(ctx, sdk.AccAddress(govAddr), valAddr)
		if err != nil {
			// the index claims this governor delegates here but staking disagrees
			panic("active governor to validator index out of sync with staking delegation")
		}
		totalBonded, err := h.k.GetGovernorBondedTokens(sdkCtx, govAddr)
		if err != nil {
			return err
		}
		delBonded := delegation.GetShares().MulInt(valBondedTokens).Quo(valTotalShares)
		slashImpact := delBonded.Mul(fraction).TruncateInt()
		postSlashBonded := totalBonded.Sub(slashImpact)

		if !h.k.validateGovernorMinSelfDelegationWithTotalBonded(sdkCtx, governor, postSlashBonded) {
			toDeactivate = append(toDeactivate, governor)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, g := range toDeactivate {
		if err := h.setGovernorInactive(ctx, sdkCtx, g); err != nil {
			return err
		}
	}
	return nil
}

func (h Hooks) AfterValidatorCreated(_ context.Context, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeValidatorModified(_ context.Context, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterValidatorRemoved(_ context.Context, _ sdk.ConsAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterValidatorBonded(_ context.Context, _ sdk.ConsAddress, _ sdk.ValAddress) error {
	return nil
}

// AfterValidatorBeginUnbonding revalidates active governors that delegate to a
// validator transitioning from Bonded to Unbonding.
// At hook time the validator's status is already Unbonding, so GetBondedTokens()
// returns zero for it and the standard validation correctly observes the
// post-transition bonded total — no exclusion or adjustment is needed.
// Status flips and index cleanup are deferred until after the index walk
// completes (per the store-iterator contract).
func (h Hooks) AfterValidatorBeginUnbonding(ctx context.Context, _ sdk.ConsAddress, valAddr sdk.ValAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	var toDeactivate []v1.Governor
	err := h.walkGovernorsForValidator(ctx, valAddr, func(governor v1.Governor) error {
		if !h.k.ValidateGovernorMinSelfDelegation(sdkCtx, governor) {
			toDeactivate = append(toDeactivate, governor)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, g := range toDeactivate {
		if err := h.setGovernorInactive(ctx, sdkCtx, g); err != nil {
			return err
		}
	}
	return nil
}

func (h Hooks) BeforeDelegationCreated(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterUnbondingInitiated(_ context.Context, _ uint64) error {
	return nil
}

func (h Hooks) AfterConsensusPubKeyUpdate(_ context.Context, _ cryptotypes.PubKey, _ cryptotypes.PubKey, _ sdk.Coin) error {
	return nil
}

// walkGovernorsForValidator iterates ActiveGovernorsByDelegatedValidator for
// the given validator, loads each governor and invokes fn only for active
// ones. A missing governor record for an index entry indicates state
// corruption and panics.
func (h Hooks) walkGovernorsForValidator(
	ctx context.Context,
	valAddr sdk.ValAddress,
	fn func(governor v1.Governor) error,
) error {
	rng := collections.NewPrefixedPairRange[sdk.ValAddress, types.GovernorAddress](valAddr)
	return h.k.ActiveGovernorsByDelegatedValidator.Walk(ctx, rng, func(key collections.Pair[sdk.ValAddress, types.GovernorAddress]) (bool, error) {
		govAddr := key.K2()
		governor, err := h.k.Governors.Get(ctx, govAddr)
		if err != nil {
			panic("ActiveGovernorsByDelegatedValidator references a non-existent governor")
		}
		if !governor.IsActive() {
			return false, nil
		}
		return false, fn(governor)
	})
}

// setGovernorInactive flips a governor's stored status to inactive, records
// the change time, and clears every ActiveGovernorsByDelegatedValidator entry
// the governor holds.
//
// CONTRACT: callers must not be mid-walk over ActiveGovernorsByDelegatedValidator
// for this governor — that would write to the iterated domain and violate the
// store iterator contract. Validator hooks use a collect-then-deactivate
// pattern; other callers (AfterDelegationModified, BeforeDelegationRemoved)
// are not iterating that collection.
func (h Hooks) setGovernorInactive(ctx context.Context, sdkCtx sdk.Context, governor v1.Governor) error {
	governor.Status = v1.Inactive
	now := sdkCtx.BlockTime()
	governor.LastStatusChangeTime = &now
	if err := h.k.Governors.Set(ctx, governor.GetAddress(), governor); err != nil {
		return err
	}
	return h.k.removeActiveGovernorIndexEntries(sdkCtx, governor.GetAddress())
}
