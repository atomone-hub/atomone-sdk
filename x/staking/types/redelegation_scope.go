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
// total stake can use RedelegationSource to defer that validation until the
// destination delegation exists.
func WithRedelegationSource(ctx context.Context, valSrcAddr sdk.ValAddress) context.Context {
	if sdkCtx, ok := ctx.(sdk.Context); ok {
		return sdkCtx.WithValue(redelegationSourceKey{}, valSrcAddr)
	}
	return context.WithValue(ctx, redelegationSourceKey{}, valSrcAddr)
}

// IsRedelegationSource reports whether ctx was derived with
// WithRedelegationSource for exactly the validator valAddr, that is whether the
// hook being run belongs to the source half of a redelegation away from valAddr.
// It is false for the destination half and outside of a redelegation.
func IsRedelegationSource(ctx context.Context, valAddr sdk.ValAddress) bool {
	src, ok := ctx.Value(redelegationSourceKey{}).(sdk.ValAddress)
	return ok && src.Equals(valAddr)
}
