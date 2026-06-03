package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// ValidateInitialDeposit is a helper function used only in deposit tests which returns the same
// functionality of validateInitialDeposit private function.
func (keeper Keeper) ValidateInitialDeposit(ctx sdk.Context, initialDeposit sdk.Coins) error {
	params, err := keeper.Params.Get(ctx)
	if err != nil {
		return err
	}

	return keeper.validateInitialDeposit(ctx, params, initialDeposit)
}

// ValidateGovernorMinSelfDelegationWithExclusions exposes the private exclusion-aware
// min-self-delegation validation for tests.
func (keeper Keeper) ValidateGovernorMinSelfDelegationWithExclusions(ctx sdk.Context, governor v1.Governor, excludeValAddrs map[string]struct{}) bool {
	return keeper.validateGovernorMinSelfDelegationWithExclusions(ctx, governor, excludeValAddrs)
}
