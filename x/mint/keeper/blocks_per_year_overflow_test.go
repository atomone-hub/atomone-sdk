package keeper_test

import (
	"testing"

	sdkmath "cosmossdk.io/math"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

// TestUpdateParamsBlocksPerYearOverflow exercises the authority-gated
// MsgUpdateParams entry point (the path a governance proposal takes) with
// BlocksPerYear values that do not fit in an int64. Such a value used to pass
// Params.Validate (validateBlocksPerYear only rejected zero), get persisted,
// and then panic mint's BeginBlocker on every block: the uint64->int64 cast
// flips the value negative, the per-block provisions turn negative, and
// sdk.NewCoin panics on a negative amount. baseapp.beginBlock has no panic
// recovery, so this permanently halts the chain.
func (s *IntegrationTestSuite) TestUpdateParamsBlocksPerYearOverflow() {
	testCases := []struct {
		name          string
		blocksPerYear uint64
		expectErr     bool
	}{
		{
			name:          "valid: current default",
			blocksPerYear: uint64(60 * 60 * 8766 / 5),
			expectErr:     false,
		},
		{
			name:          "valid: maximum int64",
			blocksPerYear: uint64(1<<63 - 1),
			expectErr:     false,
		},
		{
			name:          "invalid: 2^63 (high bit set, int64 cast flips negative)",
			blocksPerYear: 1 << 63,
			expectErr:     true,
		},
		{
			name:          "invalid: maximum uint64 (int64 cast becomes -1)",
			blocksPerYear: ^uint64(0),
			expectErr:     true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		s.Run(tc.name, func() {
			msg := &types.MsgUpdateParams{
				Authority: s.mintKeeper.GetAuthority(),
				Params: types.Params{
					MintDenom:           sdk.DefaultBondDenom,
					InflationRateChange: sdkmath.LegacyNewDecWithPrec(13, 2),
					InflationMax:         sdkmath.LegacyNewDecWithPrec(20, 2),
					InflationMin:         sdkmath.LegacyNewDecWithPrec(7, 2),
					GoalBonded:           sdkmath.LegacyNewDecWithPrec(67, 2),
					BlocksPerYear:       tc.blocksPerYear,
				},
			}

			_, err := s.msgServer.UpdateParams(s.ctx, msg)
			if tc.expectErr {
				s.Require().Errorf(err, "BlocksPerYear %d must be rejected by MsgUpdateParams", tc.blocksPerYear)

				// the overflowing value must not have been persisted
				params, perr := s.mintKeeper.Params.Get(s.ctx)
				s.Require().NoError(perr)
				s.Require().NotEqualf(tc.blocksPerYear, params.BlocksPerYear, "overflowing BlocksPerYear must not be stored")
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

// TestMinterBlocksPerYearOverflow is a defense-in-depth check on the panic
// site itself: even if an overflowing BlocksPerYear somehow ends up in state
// (InitGenesis does not validate params, and a chain already halted by this
// bug still has the value stored), the minter computations must not panic and
// must not produce negative amounts.
func TestMinterBlocksPerYearOverflow(t *testing.T) {
	params := types.Params{
		MintDenom:           sdk.DefaultBondDenom,
		InflationRateChange: sdkmath.LegacyNewDecWithPrec(13, 2),
		InflationMax:        sdkmath.LegacyNewDecWithPrec(20, 2),
		InflationMin:        sdkmath.LegacyNewDecWithPrec(7, 2),
		GoalBonded:          sdkmath.LegacyNewDecWithPrec(67, 2),
		BlocksPerYear:       ^uint64(0),
	}
	minter := types.Minter{
		Inflation:        sdkmath.LegacyNewDecWithPrec(13, 2),
		AnnualProvisions: sdkmath.LegacyNewDec(1_000_000),
	}

	// NextInflationRate must not panic and must not flip the change sign
	inflation := minter.NextInflationRate(params, sdkmath.LegacyNewDecWithPrec(67, 2))
	require.False(t, inflation.IsNegative())

	// BlockProvision must not panic and must not produce a negative coin
	// amount (previously: 1e6 / -1 = -1e6 -> sdk.NewCoin panics)
	coin := minter.BlockProvision(params)
	require.False(t, coin.IsNegative())
	require.True(t, coin.Amount.IsZero())
}
