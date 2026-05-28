package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/keeper"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestBeforeDelegationRemoved exercises the gov staking hook fired when a
// delegation is about to be deleted from the staking store. The hook must
// reevaluate the governor's min-self-delegation against the post-removal
// state, otherwise a governor whose total only crossed the threshold via the
// to-be-removed delegation would be left active (stale-active bug).
func TestBeforeDelegationRemoved(t *testing.T) {
	halfMin := v1.DefaultMinGovernorSelfDelegation.QuoRaw(2).Int64()
	fullMin := v1.DefaultMinGovernorSelfDelegation.Int64()

	tests := []struct {
		name string
		// setup returns (delegator account, validator whose delegation is about to be removed,
		// governor address to inspect after the hook, expected active state after the hook).
		// govAddr is the zero value if no governor should exist for the delegator.
		setup func(*fixture) (delAddr sdk.AccAddress, valAddr sdk.ValAddress, govAddr types.GovernorAddress)
		// expectActive is checked only when govAddr != zero
		expectActive bool
	}{
		{
			name: "stale-active regression: two delegations crossing threshold by sum, full unbond of one drops below",
			setup: func(s *fixture) (sdk.AccAddress, sdk.ValAddress, types.GovernorAddress) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				s.delegate(delAddr, s.valAddrs[0], halfMin)
				s.delegate(delAddr, s.valAddrs[1], halfMin)
				return delAddr, s.valAddrs[0], govAddr
			},
			expectActive: false,
		},
		{
			name: "remaining delegation alone meets threshold: stays active",
			setup: func(s *fixture) (sdk.AccAddress, sdk.ValAddress, types.GovernorAddress) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				s.delegate(delAddr, s.valAddrs[0], 1)
				s.delegate(delAddr, s.valAddrs[1], fullMin)
				return delAddr, s.valAddrs[0], govAddr
			},
			expectActive: true,
		},
		{
			name: "single delegation at threshold, full unbond drops below: goes inactive",
			setup: func(s *fixture) (sdk.AccAddress, sdk.ValAddress, types.GovernorAddress) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				s.delegate(delAddr, s.valAddrs[0], fullMin)
				return delAddr, s.valAddrs[0], govAddr
			},
			expectActive: false,
		},
		{
			name: "already inactive governor: hook is a no-op",
			setup: func(s *fixture) (sdk.AccAddress, sdk.ValAddress, types.GovernorAddress) {
				govAddr := s.inactiveGovernor.GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				s.delegate(delAddr, s.valAddrs[0], fullMin)
				return delAddr, s.valAddrs[0], govAddr
			},
			expectActive: false,
		},
		{
			name: "non-governor delegator: hook is a no-op (no panic)",
			setup: func(s *fixture) (sdk.AccAddress, sdk.ValAddress, types.GovernorAddress) {
				delAddr := s.delAddrs[0]
				s.delegate(delAddr, s.valAddrs[0], fullMin)
				return delAddr, s.valAddrs[0], types.GovernorAddress{}
			},
			expectActive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			govKeeper, accKeeper, bankKeeper, stakingKeeper, distrKeeper, _, ctx := setupGovKeeper(t, mockAccountKeeperExpectations)
			s := newFixture(t, ctx, 2, 2, 2, govKeeper, mocks{
				accKeeper:          accKeeper,
				bankKeeper:         bankKeeper,
				stakingKeeper:      stakingKeeper,
				distributionKeeper: distrKeeper,
			})

			delAddr, valAddr, govAddr := tt.setup(s)

			err := govKeeper.StakingHooks().BeforeDelegationRemoved(s.ctx, delAddr, valAddr)
			require.NoError(t, err)

			if len(govAddr) == 0 {
				return
			}
			stored, err := govKeeper.Governors.Get(s.ctx, govAddr)
			require.NoError(t, err)
			assert.Equal(t, tt.expectActive, stored.IsActive(), "governor active status after BeforeDelegationRemoved")
		})
	}
}

// TestBeforeValidatorSlashed exercises the gov staking hook fired before a
// validator is slashed. Active governors whose projected post-slash bonded
// amount falls below the min self-delegation must be set to inactive.
func TestBeforeValidatorSlashed(t *testing.T) {
	fullMin := v1.DefaultMinGovernorSelfDelegation.Int64()
	halfSlash := math.LegacyNewDecWithPrec(5, 1) // 0.5

	tests := []struct {
		name string
		// setup returns (validator being slashed, slash fraction, governors to inspect, expected active states aligned with governors).
		setup func(*fixture) (valAddr sdk.ValAddress, fraction math.LegacyDec, governors []types.GovernorAddress, expectActive []bool)
	}{
		{
			name: "active governor barely meeting threshold, half slash drops below: goes inactive",
			setup: func(s *fixture) (sdk.ValAddress, math.LegacyDec, []types.GovernorAddress, []bool) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				s.delegate(delAddr, s.valAddrs[0], fullMin)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				return s.valAddrs[0], halfSlash, []types.GovernorAddress{govAddr}, []bool{false}
			},
		},
		{
			name: "active governor well above threshold, half slash still above: stays active",
			setup: func(s *fixture) (sdk.ValAddress, math.LegacyDec, []types.GovernorAddress, []bool) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				s.delegate(delAddr, s.valAddrs[0], 3*fullMin)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				return s.valAddrs[0], halfSlash, []types.GovernorAddress{govAddr}, []bool{true}
			},
		},
		{
			name: "active governor not delegated to slashed validator: stays active",
			setup: func(s *fixture) (sdk.ValAddress, math.LegacyDec, []types.GovernorAddress, []bool) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				s.delegate(delAddr, s.valAddrs[0], fullMin)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				// slash a different validator
				return s.valAddrs[1], halfSlash, []types.GovernorAddress{govAddr}, []bool{true}
			},
		},
		{
			name: "inactive governor delegated to slashed validator: stays inactive (hook skips)",
			setup: func(s *fixture) (sdk.ValAddress, math.LegacyDec, []types.GovernorAddress, []bool) {
				govAddr := s.inactiveGovernor.GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				s.delegate(delAddr, s.valAddrs[0], fullMin)
				// no DelegateToGovernor for inactive governor (they have no gov self-delegation);
				// the hook walks the reverse index, which is empty for this case.
				return s.valAddrs[0], halfSlash, []types.GovernorAddress{govAddr}, []bool{false}
			},
		},
		{
			name: "two active governors both at threshold delegated to same validator: both go inactive",
			setup: func(s *fixture) (sdk.ValAddress, math.LegacyDec, []types.GovernorAddress, []bool) {
				govAddrs := []types.GovernorAddress{
					s.activeGovernors[0].GetAddress(),
					s.activeGovernors[1].GetAddress(),
				}
				for _, govAddr := range govAddrs {
					delAddr := sdk.AccAddress(govAddr)
					s.delegate(delAddr, s.valAddrs[0], fullMin)
					require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				}
				return s.valAddrs[0], halfSlash, govAddrs, []bool{false, false}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			govKeeper, accKeeper, bankKeeper, stakingKeeper, distrKeeper, _, ctx := setupGovKeeper(t, mockAccountKeeperExpectations)
			// numGovernors = 3 so newFixture creates 2 active + 1 inactive for the multi-governor case.
			s := newFixture(t, ctx, 2, 2, 3, govKeeper, mocks{
				accKeeper:          accKeeper,
				bankKeeper:         bankKeeper,
				stakingKeeper:      stakingKeeper,
				distributionKeeper: distrKeeper,
			})

			valAddr, fraction, governors, expectActive := tt.setup(s)
			require.Equal(t, len(governors), len(expectActive))

			err := govKeeper.StakingHooks().BeforeValidatorSlashed(s.ctx, valAddr, fraction)
			require.NoError(t, err)

			for i, govAddr := range governors {
				stored, err := govKeeper.Governors.Get(s.ctx, govAddr)
				require.NoError(t, err)
				assert.Equal(t, expectActive[i], stored.IsActive(),
					"governor %s active status after BeforeValidatorSlashed", govAddr.String())
			}
		})
	}
}

// TestAfterValidatorBeginUnbonding exercises the gov staking hook fired when a
// validator transitions from Bonded to Unbonding. At hook time the validator's
// status is already Unbonding, so GetBondedTokens() returns zero for it and the
// standard validation observes the post-transition bonded total.
func TestAfterValidatorBeginUnbonding(t *testing.T) {
	fullMin := v1.DefaultMinGovernorSelfDelegation.Int64()

	tests := []struct {
		name  string
		setup func(*fixture) (valAddr sdk.ValAddress, governors []types.GovernorAddress, expectActive []bool)
	}{
		{
			name: "single-validator governor: validator unbonds → goes inactive",
			setup: func(s *fixture) (sdk.ValAddress, []types.GovernorAddress, []bool) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				s.delegate(delAddr, s.valAddrs[0], fullMin)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				s.setValidatorStatus(0, stakingtypes.Unbonding)
				return s.valAddrs[0], []types.GovernorAddress{govAddr}, []bool{false}
			},
		},
		{
			name: "two-validator governor, only one unbonds, remainder meets threshold: stays active",
			setup: func(s *fixture) (sdk.ValAddress, []types.GovernorAddress, []bool) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				s.delegate(delAddr, s.valAddrs[0], 1)
				s.delegate(delAddr, s.valAddrs[1], fullMin)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				s.setValidatorStatus(0, stakingtypes.Unbonding)
				return s.valAddrs[0], []types.GovernorAddress{govAddr}, []bool{true}
			},
		},
		{
			name: "two-validator governor split equally, one unbonds drops below threshold: goes inactive",
			setup: func(s *fixture) (sdk.ValAddress, []types.GovernorAddress, []bool) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				half := v1.DefaultMinGovernorSelfDelegation.QuoRaw(2).Int64()
				s.delegate(delAddr, s.valAddrs[0], half)
				s.delegate(delAddr, s.valAddrs[1], half)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				s.setValidatorStatus(0, stakingtypes.Unbonding)
				return s.valAddrs[0], []types.GovernorAddress{govAddr}, []bool{false}
			},
		},
		{
			name: "governor not delegated to unbonding validator: unchanged",
			setup: func(s *fixture) (sdk.ValAddress, []types.GovernorAddress, []bool) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				s.delegate(delAddr, s.valAddrs[0], fullMin)
				require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				s.setValidatorStatus(1, stakingtypes.Unbonding)
				return s.valAddrs[1], []types.GovernorAddress{govAddr}, []bool{true}
			},
		},
		{
			name: "inactive governor: hook skips (reverse index has no entry without gov self-delegation)",
			setup: func(s *fixture) (sdk.ValAddress, []types.GovernorAddress, []bool) {
				govAddr := s.inactiveGovernor.GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				s.delegate(delAddr, s.valAddrs[0], fullMin)
				s.setValidatorStatus(0, stakingtypes.Unbonding)
				return s.valAddrs[0], []types.GovernorAddress{govAddr}, []bool{false}
			},
		},
		{
			name: "two governors at threshold sharing a validator: both go inactive when it unbonds",
			setup: func(s *fixture) (sdk.ValAddress, []types.GovernorAddress, []bool) {
				govAddrs := []types.GovernorAddress{
					s.activeGovernors[0].GetAddress(),
					s.activeGovernors[1].GetAddress(),
				}
				for _, govAddr := range govAddrs {
					delAddr := sdk.AccAddress(govAddr)
					s.delegate(delAddr, s.valAddrs[0], fullMin)
					require.NoError(t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				}
				s.setValidatorStatus(0, stakingtypes.Unbonding)
				return s.valAddrs[0], govAddrs, []bool{false, false}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			govKeeper, accKeeper, bankKeeper, stakingKeeper, distrKeeper, _, ctx := setupGovKeeper(t, mockAccountKeeperExpectations)
			s := newFixture(t, ctx, 2, 2, 3, govKeeper, mocks{
				accKeeper:          accKeeper,
				bankKeeper:         bankKeeper,
				stakingKeeper:      stakingKeeper,
				distributionKeeper: distrKeeper,
			})

			valAddr, governors, expectActive := tt.setup(s)
			require.Equal(t, len(governors), len(expectActive))

			err := govKeeper.StakingHooks().AfterValidatorBeginUnbonding(s.ctx, sdk.ConsAddress{}, valAddr)
			require.NoError(t, err)

			for i, govAddr := range governors {
				stored, err := govKeeper.Governors.Get(s.ctx, govAddr)
				require.NoError(t, err)
				assert.Equal(t, expectActive[i], stored.IsActive(),
					"governor %s active status after AfterValidatorBeginUnbonding", govAddr.String())
			}
		})
	}
}

// TestGovernorsByValidatorIndex covers the reverse-index maintenance contract:
// an entry exists in GovernorsByValidator iff a corresponding entry exists in
// ValidatorSharesByGovernor. The index drives BeforeValidatorSlashed and
// AfterValidatorBeginUnbonding lookups, so its integrity is load-bearing.
func TestGovernorsByValidatorIndex(t *testing.T) {
	govKeeper, accKeeper, bankKeeper, stakingKeeper, distrKeeper, _, ctx := setupGovKeeper(t, mockAccountKeeperExpectations)
	s := newFixture(t, ctx, 2, 2, 2, govKeeper, mocks{
		accKeeper:          accKeeper,
		bankKeeper:         bankKeeper,
		stakingKeeper:      stakingKeeper,
		distributionKeeper: distrKeeper,
	})

	govAddr := s.activeGovernors[0].GetAddress()
	delAddr := sdk.AccAddress(govAddr)
	fullMin := v1.DefaultMinGovernorSelfDelegation.Int64()

	// initial state: no entries
	assertIndexed(t, ctx, govKeeper, s.valAddrs[0], govAddr, false)
	assertIndexed(t, ctx, govKeeper, s.valAddrs[1], govAddr, false)

	// stake to val[0] then delegate-to-governor: IncreaseGovernorShares fires and adds the index entry
	s.delegate(delAddr, s.valAddrs[0], fullMin)
	require.NoError(t, govKeeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
	assertIndexed(t, ctx, govKeeper, s.valAddrs[0], govAddr, true)
	assertIndexed(t, ctx, govKeeper, s.valAddrs[1], govAddr, false)

	// add a second validator delegation through the hooks path: IncreaseGovernorShares adds the second index entry
	s.delegate(delAddr, s.valAddrs[1], fullMin)
	govKeeper.IncreaseGovernorShares(s.ctx, govAddr, s.valAddrs[1], math.LegacyNewDec(fullMin))
	assertIndexed(t, ctx, govKeeper, s.valAddrs[0], govAddr, true)
	assertIndexed(t, ctx, govKeeper, s.valAddrs[1], govAddr, true)

	// partially decrease val[0]: entry remains while shares > 0
	govKeeper.DecreaseGovernorShares(s.ctx, govAddr, s.valAddrs[0], math.LegacyNewDec(fullMin/2))
	assertIndexed(t, ctx, govKeeper, s.valAddrs[0], govAddr, true)

	// fully decrease val[0]: index entry must be removed alongside the primary record
	govKeeper.DecreaseGovernorShares(s.ctx, govAddr, s.valAddrs[0], math.LegacyNewDec(fullMin-fullMin/2))
	assertIndexed(t, ctx, govKeeper, s.valAddrs[0], govAddr, false)
	assertIndexed(t, ctx, govKeeper, s.valAddrs[1], govAddr, true)
}

func assertIndexed(t *testing.T, ctx sdk.Context, k *keeper.Keeper, valAddr sdk.ValAddress, govAddr types.GovernorAddress, want bool) {
	t.Helper()
	has, err := k.GovernorsByValidator.Has(ctx, collections.Join(valAddr, govAddr))
	require.NoError(t, err)
	assert.Equal(t, want, has, "GovernorsByValidator[%s,%s]", valAddr.String(), govAddr.String())
}
