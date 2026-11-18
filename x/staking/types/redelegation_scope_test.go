package types_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

type wrapperKey struct{}

func TestIsRedelegationSource(t *testing.T) {
	src := sdk.ValAddress("source_______________")
	other := sdk.ValAddress("other________________")
	sdkCtx := sdk.Context{}.WithContext(context.Background())

	for name, base := range map[string]context.Context{
		"sdk context":              sdkCtx,
		"sdk context in a wrapper": context.WithValue(sdkCtx, wrapperKey{}, 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, types.IsRedelegationSource(base, src), "outside of a redelegation")

			ctx := types.WithRedelegationSource(base, src)
			require.True(t, types.IsRedelegationSource(ctx, src))
			require.False(t, types.IsRedelegationSource(ctx, other), "destination half")
			require.False(t, types.IsRedelegationSource(base, src), "the parent context is not marked")

			// the marker is kept when the context is unwrapped
			require.True(t, types.IsRedelegationSource(sdk.UnwrapSDKContext(ctx), src))
		})
	}
}
