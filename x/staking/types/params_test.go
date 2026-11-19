package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestParamsEqual(t *testing.T) {
	p1 := types.DefaultParams()
	p2 := types.DefaultParams()

	ok := p1.Equal(p2)
	require.True(t, ok)

	p2.UnbondingTime = 60 * 60 * 24 * 2
	p2.BondDenom = "soup"

	ok = p1.Equal(p2)
	require.False(t, ok)
}

func TestValidateParams(t *testing.T) {
	params := types.DefaultParams()

	// default params have no error
	require.NoError(t, params.Validate())

	// validate mincommission
	params.MinCommissionRate = math.LegacyNewDec(-1)
	require.Error(t, params.Validate())

	params.MinCommissionRate = math.LegacyNewDec(2)
	require.Error(t, params.Validate())
}

// TestValidateParamsMaxValidatorsBound checks that Params.Validate() — the
// check MsgUpdateParams runs — rejects MaxValidators above the cap.
func TestValidateParamsMaxValidatorsBound(t *testing.T) {
	testCases := []struct {
		name    string
		value   uint32
		expPass bool
	}{
		{"zero rejected", 0, false},
		{"one accepted", 1, true},
		{"default accepted", types.DefaultMaxValidators, true},
		{"cap accepted", types.MaxMaxValidators, true},
		{"cap+1 rejected", types.MaxMaxValidators + 1, false},
		{"max uint32 rejected (OOM value)", 4294967295, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p := types.DefaultParams()
			p.MaxValidators = tc.value
			err := p.Validate()
			if tc.expPass {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
