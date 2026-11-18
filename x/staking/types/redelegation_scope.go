package types

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

type redelegationSourceKey struct{}

// WithRedelegationSource returns a context marking the staking operations
// performed on it as the two halves of a single redelegation away from
// valSrcAddr: an unbond from valSrcAddr followed by a delegation to the
// destination validator.
//
// Between the two halves the delegator's total stake is transiently lower than
// it is once the redelegation completes. Hooks that validate the delegator's
// total stake can use IsRedelegationSource to defer that validation until the
// destination delegation exists.
//
// The marker is set on the sdk.Context itself, so it is kept by sdk.UnwrapSDKContext.
// It panics if ctx does not hold an sdk.Context.
func WithRedelegationSource(ctx context.Context, valSrcAddr sdk.ValAddress) context.Context {
	return sdk.UnwrapSDKContext(ctx).WithValue(redelegationSourceKey{}, valSrcAddr)
}

// IsRedelegationSource reports whether ctx was derived with
// WithRedelegationSource for exactly the validator valAddr, that is whether the
// hook being run belongs to the source half of a redelegation away from valAddr.
// It is false for the destination half and outside of a redelegation.
func IsRedelegationSource(ctx context.Context, valAddr sdk.ValAddress) bool {
	src, ok := ctx.Value(redelegationSourceKey{}).(sdk.ValAddress)
	return ok && src.Equals(valAddr)
}
