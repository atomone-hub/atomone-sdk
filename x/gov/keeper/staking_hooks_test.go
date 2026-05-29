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
				// no DelegateToGovernor for an inactive governor: the hook walks
				// ActiveGovernorsByDelegatedValidator, which is empty for this case.
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
			name: "inactive governor: hook skips (no index entry without an active gov self-delegation)",
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

// TestActiveGovernorsByDelegatedValidator exercises the full maintenance contract:
// an entry exists for (val, gov) iff gov is an active governor whose own account
// has a staking delegation to val. Each case runs a scenario through the production
// hook / keeper surface, then asserts on the index entries, the cumulative shares
// (where relevant), and the governor's active status.
func TestActiveGovernorsByDelegatedValidator(t *testing.T) {
	fullMin := v1.DefaultMinGovernorSelfDelegation.Int64()

	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name string
		// run drives the scenario and returns the governor whose state to inspect.
		run func(t *testing.T, s *fixture, k *keeper.Keeper) types.GovernorAddress
		// expectIndexed[i] is the expected presence in
		// ActiveGovernorsByDelegatedValidator[s.valAddrs[i], gov].
		expectIndexed map[int]bool
		// expectCumulative[i] is the expected presence in
		// ValidatorSharesByGovernor[gov, s.valAddrs[i]] (nil to skip).
		expectCumulative map[int]bool
		// expectActive: nil to skip
		expectActive *bool
	}{
		{
			name: "lifecycle: self-stake + DelegateToGovernor adds entry; AfterDelegationModified adds a second; BeforeDelegationRemoved drops the first",
			run: func(t *testing.T, s *fixture, k *keeper.Keeper) types.GovernorAddress {
				gov := s.activeGovernors[0].GetAddress()
				acc := sdk.AccAddress(gov)
				s.delegate(acc, s.valAddrs[0], fullMin)
				require.NoError(t, k.DelegateToGovernor(s.ctx, acc, gov))
				s.delegate(acc, s.valAddrs[1], fullMin)
				require.NoError(t, k.StakingHooks().AfterDelegationModified(s.ctx, acc, s.valAddrs[1]))
				// re-running AfterDelegationModified is idempotent
				require.NoError(t, k.StakingHooks().AfterDelegationModified(s.ctx, acc, s.valAddrs[1]))
				// full-unbond val[0]; val[1] keeps the governor above threshold
				require.NoError(t, k.StakingHooks().BeforeDelegationRemoved(s.ctx, acc, s.valAddrs[0]))
				return gov
			},
			expectIndexed: map[int]bool{0: false, 1: true},
			expectActive:  boolPtr(true),
		},
		{
			name: "gov-delegator does not pollute: governor stakes val[0]; a separate delegator stakes val[1] and gov-delegates to the governor",
			run: func(t *testing.T, s *fixture, k *keeper.Keeper) types.GovernorAddress {
				gov := s.activeGovernors[0].GetAddress()
				acc := sdk.AccAddress(gov)
				del := s.delAddrs[0]
				s.delegate(acc, s.valAddrs[0], fullMin)
				require.NoError(t, k.DelegateToGovernor(s.ctx, acc, gov))
				s.delegate(del, s.valAddrs[1], fullMin)
				require.NoError(t, k.DelegateToGovernor(s.ctx, del, gov))
				return gov
			},
			expectIndexed:    map[int]bool{0: true, 1: false},
			expectCumulative: map[int]bool{0: true, 1: true},
		},
		{
			name: "non-governor delegator AfterDelegationModified must not error and must not add an index entry",
			run: func(t *testing.T, s *fixture, k *keeper.Keeper) types.GovernorAddress {
				gov := s.activeGovernors[0].GetAddress()
				acc := sdk.AccAddress(gov)
				del := s.delAddrs[0]
				s.delegate(acc, s.valAddrs[0], fullMin)
				require.NoError(t, k.DelegateToGovernor(s.ctx, acc, gov))
				s.delegate(del, s.valAddrs[1], fullMin)
				require.NoError(t, k.DelegateToGovernor(s.ctx, del, gov))
				// explicit re-fire of the staking hook with a non-governor delegator
				require.NoError(t, k.StakingHooks().AfterDelegationModified(s.ctx, del, s.valAddrs[1]))
				return gov
			},
			expectIndexed: map[int]bool{0: true, 1: false},
		},
		{
			name: "governor full-unbonds val[0] while a delegator still stakes there: cumulative stays, index entry is removed",
			run: func(t *testing.T, s *fixture, k *keeper.Keeper) types.GovernorAddress {
				gov := s.activeGovernors[0].GetAddress()
				acc := sdk.AccAddress(gov)
				del := s.delAddrs[0]
				// val[1] keeps the governor above threshold after the val[0] unbond
				s.delegate(acc, s.valAddrs[0], fullMin)
				s.delegate(acc, s.valAddrs[1], fullMin)
				require.NoError(t, k.DelegateToGovernor(s.ctx, acc, gov))
				// delegator also stakes to val[0] so cumulative > governor's own
				s.delegate(del, s.valAddrs[0], fullMin)
				require.NoError(t, k.DelegateToGovernor(s.ctx, del, gov))
				require.NoError(t, k.StakingHooks().BeforeDelegationRemoved(s.ctx, acc, s.valAddrs[0]))
				return gov
			},
			expectIndexed:    map[int]bool{0: false, 1: true},
			expectCumulative: map[int]bool{0: true, 1: true},
			expectActive:     boolPtr(true),
		},
		{
			name: "status flip: deactivation clears all entries; reactivation backfills from current staking",
			run: func(t *testing.T, s *fixture, k *keeper.Keeper) types.GovernorAddress {
				gov := s.activeGovernors[0].GetAddress()
				acc := sdk.AccAddress(gov)
				s.delegate(acc, s.valAddrs[0], fullMin)
				s.delegate(acc, s.valAddrs[1], fullMin)
				require.NoError(t, k.DelegateToGovernor(s.ctx, acc, gov))
				governor, err := k.Governors.Get(s.ctx, gov)
				require.NoError(t, err)
				require.NoError(t, k.SetGovernorInactive(s.ctx, governor))
				// at this point both entries should be cleared; backfill restores them
				require.NoError(t, k.SetActiveGovernorIndexEntries(s.ctx, gov))
				return gov
			},
			expectIndexed: map[int]bool{0: true, 1: true},
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

			gov := tt.run(t, s, govKeeper)

			for valIdx, want := range tt.expectIndexed {
				assertIndexed(t, ctx, govKeeper, s.valAddrs[valIdx], gov, want)
			}
			for valIdx, want := range tt.expectCumulative {
				has, err := govKeeper.ValidatorSharesByGovernor.Has(s.ctx, collections.Join(gov, s.valAddrs[valIdx]))
				require.NoError(t, err)
				assert.Equal(t, want, has, "ValidatorSharesByGovernor[%s,%s]", gov.String(), s.valAddrs[valIdx].String())
			}
			if tt.expectActive != nil {
				stored, err := govKeeper.Governors.Get(s.ctx, gov)
				require.NoError(t, err)
				assert.Equal(t, *tt.expectActive, stored.IsActive(), "governor active status")
			}
		})
	}
}

func assertIndexed(t *testing.T, ctx sdk.Context, k *keeper.Keeper, valAddr sdk.ValAddress, govAddr types.GovernorAddress, want bool) {
	t.Helper()
	has, err := k.ActiveGovernorsByDelegatedValidator.Has(ctx, collections.Join(valAddr, govAddr))
	require.NoError(t, err)
	assert.Equal(t, want, has, "ActiveGovernorsByDelegatedValidator[%s,%s]", valAddr.String(), govAddr.String())
}
