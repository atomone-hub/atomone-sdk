package types_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestIsRedelegationSource(t *testing.T) {
	src := sdk.ValAddress("source_______________")
	other := sdk.ValAddress("other________________")

	for name, base := range map[string]context.Context{
		"sdk context":   sdk.Context{}.WithContext(context.Background()),
		"plain context": context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, types.IsRedelegationSource(base, src), "outside of a redelegation")

			ctx := types.WithRedelegationSource(base, src)
			require.True(t, types.IsRedelegationSource(ctx, src))
			require.False(t, types.IsRedelegationSource(ctx, other), "destination half")
			require.False(t, types.IsRedelegationSource(base, src), "the parent context is not marked")
		})
	}
}
