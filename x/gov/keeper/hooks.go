package keeper

import (
	context "context"
	"errors"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

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

// AfterDelegationModified is called when a delegation is created or modified
// We trigger a governor shares increase here adding all delegation shares.
// It is balanced by the full-amount decrease in BeforeDelegationSharesModified
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
	// Calculate the new shares and update the Governor's shares
	shares := delegation.GetShares()
	h.k.IncreaseGovernorShares(sdkCtx, govAddr, valAddr, shares)

	// if the delegator is also an active governor, ensure min self-delegation requirement is met,
	// otherwise set governor to inactive
	delGovAddr := types.GovernorAddress(delAddr.Bytes())
	if governor, err := h.k.Governors.Get(ctx, delGovAddr); err == nil && governor.IsActive() {
		if governor.GetAddress().String() != govDelegation.GovernorAddress {
			panic("active governor delegating to another governor")
		}
		// if the governor no longer meets the min self-delegation, set to inactive
		if !h.k.ValidateGovernorMinSelfDelegation(sdkCtx, governor) {
			if err := h.setGovernorInactive(ctx, sdkCtx, governor); err != nil {
				return err
			}
		}
	}

	return nil
}

// BeforeDelegationRemoved is called when a delegation is removed
// We verify if the delegator is also an active governor and if so check
// that the min self-delegation requirement is still met, otherwise set governor
// status to inactive
func (h Hooks) BeforeDelegationRemoved(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	// if the delegator is also an active governor, ensure min self-delegation requirement is met,
	// otherwise set governor to inactive
	delGovAddr := types.GovernorAddress(delAddr.Bytes())
	if governor, err := h.k.Governors.Get(ctx, delGovAddr); err == nil && governor.IsActive() {
		govDelegation, err := h.k.GovernanceDelegations.Get(ctx, delAddr)
		if err != nil && !errors.Is(err, collections.ErrNotFound) {
			return err
		}
		if errors.Is(err, collections.ErrNotFound) {
			panic("active governor without governance self-delegation")
		}
		if governor.GetAddress().String() != govDelegation.GovernorAddress {
			panic("active governor delegating to another governor")
		}

		exclusion := map[string]struct{}{
			valAddr.String(): {},
		}
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		// if the governor no longer meets the min self-delegation, set to inactive
		if !h.k.validateGovernorMinSelfDelegationWithExclusions(sdkCtx, governor, exclusion) {
			if err := h.setGovernorInactive(ctx, sdkCtx, governor); err != nil {
				return err
			}
		}
	}

	return nil
}

// BeforeValidatorSlashed revalidates active governors that delegate to the slashed
// validator. If the projected post-slash bonded amount falls below the minimum
// self-delegation, the governor is set to inactive.
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
	return h.walkGovernorsForValidator(ctx, valAddr, func(govAddr types.GovernorAddress, governor v1.Governor) error {
		delegation, err := h.k.sk.GetDelegation(ctx, sdk.AccAddress(govAddr), valAddr)
		if err != nil {
			// index pointed here but the delegation is gone: defensively skip
			return nil
		}
		totalBonded, err := h.k.GetGovernorBondedTokens(sdkCtx, govAddr)
		if err != nil {
			return err
		}
		delBonded := delegation.GetShares().MulInt(valBondedTokens).Quo(valTotalShares)
		slashImpact := delBonded.Mul(fraction).TruncateInt()
		postSlashBonded := totalBonded.Sub(slashImpact)

		if !h.k.validateGovernorMinSelfDelegationWithTotalBonded(sdkCtx, governor, postSlashBonded) {
			return h.setGovernorInactive(ctx, sdkCtx, governor)
		}
		return nil
	})
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
// validator transitioning from Bonded to Unbonding (jail or active-set turnover).
// At hook time the validator's status is already Unbonding, so GetBondedTokens()
// returns zero for it and the standard validation correctly observes the
// post-transition bonded total — no exclusion or adjustment is needed.
func (h Hooks) AfterValidatorBeginUnbonding(ctx context.Context, _ sdk.ConsAddress, valAddr sdk.ValAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return h.walkGovernorsForValidator(ctx, valAddr, func(govAddr types.GovernorAddress, governor v1.Governor) error {
		if h.k.ValidateGovernorMinSelfDelegation(sdkCtx, governor) {
			return nil
		}
		return h.setGovernorInactive(ctx, sdkCtx, governor)
	})
}

// walkGovernorsForValidator iterates the reverse index for the given validator,
// loads each active governor, and invokes fn. Inactive governors and missing
// records are skipped — fn only runs for actionable, currently-active governors.
func (h Hooks) walkGovernorsForValidator(
	ctx context.Context,
	valAddr sdk.ValAddress,
	fn func(govAddr types.GovernorAddress, governor v1.Governor) error,
) error {
	rng := collections.NewPrefixedPairRange[sdk.ValAddress, types.GovernorAddress](valAddr)
	return h.k.GovernorsByValidator.Walk(ctx, rng, func(key collections.Pair[sdk.ValAddress, types.GovernorAddress]) (bool, error) {
		govAddr := key.K2()
		governor, err := h.k.Governors.Get(ctx, govAddr)
		if err != nil {
			// index entry without a governor record: stale entry, skip
			return false, nil
		}
		if !governor.IsActive() {
			return false, nil
		}
		return false, fn(govAddr, governor)
	})
}

// setGovernorInactive flips a governor's stored status to inactive and records
// the change time.
func (h Hooks) setGovernorInactive(ctx context.Context, sdkCtx sdk.Context, governor v1.Governor) error {
	governor.Status = v1.Inactive
	now := sdkCtx.BlockTime()
	governor.LastStatusChangeTime = &now
	return h.k.Governors.Set(ctx, governor.GetAddress(), governor)
}

func (h Hooks) BeforeDelegationCreated(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterUnbondingInitiated(_ context.Context, _ uint64) error {
	return nil
}
