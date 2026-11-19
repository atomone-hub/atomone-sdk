package keeper_test

import (
	"testing"

	"gotest.tools/v3/assert"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/keeper"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

func (gf *governorFixture) consAddr(t *testing.T, i int) sdk.ConsAddress {
	t.Helper()
	val, err := gf.stakingKeeper.GetValidator(gf.ctx, gf.vals[i])
	assert.NilError(t, err)
	consAddr, err := val.GetConsAddr()
	assert.NilError(t, err)
	return consAddr
}

// jail jails validator i and applies the validator set update, which moves
// the validator from Bonded to Unbonding.
func (gf *governorFixture) jail(t *testing.T, i int) {
	t.Helper()
	assert.NilError(t, gf.stakingKeeper.Jail(gf.ctx, gf.consAddr(t, i)))
	_, err := gf.stakingKeeper.EndBlocker(gf.ctx)
	assert.NilError(t, err)
}

// unjail unjails validator i and applies the validator set update, which
// bonds the validator again.
func (gf *governorFixture) unjail(t *testing.T, i int) {
	t.Helper()
	assert.NilError(t, gf.stakingKeeper.Unjail(gf.ctx, gf.consAddr(t, i)))
	_, err := gf.stakingKeeper.EndBlocker(gf.ctx)
	assert.NilError(t, err)
}

func (gf *governorFixture) assertDeactivatedByValidator(t *testing.T, want bool) {
	t.Helper()
	g, err := gf.govKeeper.Governors.Get(gf.ctx, gf.gov)
	assert.NilError(t, err)
	assert.Equal(t, g.DeactivatedByValidator, want, "deactivated by validator")
}

func (gf *governorFixture) setStatus(status v1.GovernorStatus) error {
	_, err := keeper.NewMsgServerImpl(gf.govKeeper).UpdateGovernorStatus(gf.ctx, v1.NewMsgUpdateGovernorStatus(gf.govAddr, status))
	return err
}

// The governor was created moments ago, so GovernorStatusChangePeriod has not
// elapsed since its last status change in any of these tests.

func TestGovernorReactivatesAfterValidatorJailWithoutWaiting(t *testing.T) {
	gf := newGovernorFixture(t, v1.DefaultMinGovernorSelfDelegation)

	gf.jail(t, 2)
	gf.assertState(t, false, map[int]bool{})
	gf.assertDeactivatedByValidator(t, true)

	gf.unjail(t, 2)
	assert.NilError(t, gf.setStatus(v1.Active))
	gf.assertState(t, true, map[int]bool{2: true})
	gf.assertDeactivatedByValidator(t, false)
}

func TestGovernorReactivatesAfterRedelegatingAwayFromJailedValidator(t *testing.T) {
	min := v1.DefaultMinGovernorSelfDelegation
	gf := newGovernorFixture(t, min)

	gf.jail(t, 2)
	gf.assertState(t, false, map[int]bool{})

	gf.redelegate(t, 2, 0, min)
	assert.NilError(t, gf.setStatus(v1.Active))
	gf.assertState(t, true, map[int]bool{0: true})
}

func TestGovernorReactivatesAfterValidatorSlashWithoutWaiting(t *testing.T) {
	min := v1.DefaultMinGovernorSelfDelegation
	gf := newGovernorFixture(t, min)

	val, err := gf.stakingKeeper.GetValidator(gf.ctx, gf.vals[2])
	assert.NilError(t, err)
	power := gf.stakingKeeper.TokensToConsensusPower(gf.ctx, val.GetTokens())
	_, err = gf.stakingKeeper.Slash(gf.ctx, gf.consAddr(t, 2), gf.ctx.BlockHeight(), power, math.LegacyNewDecWithPrec(1, 2))
	assert.NilError(t, err)
	gf.assertState(t, false, map[int]bool{})
	gf.assertDeactivatedByValidator(t, true)

	// the slashed stake is short of the minimum, so the request is refused for
	// that reason and not because of the status change period
	err = gf.setStatus(v1.Active)
	assert.ErrorIs(t, err, types.ErrInsufficientGovernorDelegation)
}

// A governor that goes below the minimum by its own undelegation still waits
// GovernorStatusChangePeriod before becoming active again.
func TestGovernorWaitsAfterOwnUndelegation(t *testing.T) {
	gf := newGovernorFixture(t, v1.DefaultMinGovernorSelfDelegation)

	shares, err := gf.stakingKeeper.ValidateUnbondAmount(gf.ctx, gf.govAddr, gf.vals[2], math.NewInt(1))
	assert.NilError(t, err)
	_, _, err = gf.stakingKeeper.Undelegate(gf.ctx, gf.govAddr, gf.vals[2], shares)
	assert.NilError(t, err)
	gf.assertState(t, false, map[int]bool{})
	gf.assertDeactivatedByValidator(t, false)

	err = gf.setStatus(v1.Active)
	assert.ErrorIs(t, err, types.ErrGovernorStatusChangePeriod)
}

// Reactivating is a status change of the governor's own, so the next one
// waits GovernorStatusChangePeriod as usual.
func TestGovernorWaitsAfterReactivatingFromValidatorJail(t *testing.T) {
	gf := newGovernorFixture(t, v1.DefaultMinGovernorSelfDelegation)

	gf.jail(t, 2)
	gf.unjail(t, 2)
	assert.NilError(t, gf.setStatus(v1.Active))

	err := gf.setStatus(v1.Inactive)
	assert.ErrorIs(t, err, types.ErrGovernorStatusChangePeriod)
}

func TestGovernorDeactivationByValidatorEmitsEvent(t *testing.T) {
	gf := newGovernorFixture(t, v1.DefaultMinGovernorSelfDelegation)
	gf.ctx = gf.ctx.WithEventManager(sdk.NewEventManager())

	gf.jail(t, 2)

	var found bool
	for _, e := range gf.ctx.EventManager().Events() {
		if e.Type != types.EventTypeGovernorChangeStatus {
			continue
		}
		gov, okGov := e.GetAttribute(types.AttributeKeyGovernor)
		status, okStatus := e.GetAttribute(types.AttributeKeyStatus)
		if okGov && okStatus && gov.Value == gf.gov.String() && status.Value == types.AttributeValueStatusInactive {
			found = true
		}
	}
	assert.Assert(t, found, "no %s event for the governor", types.EventTypeGovernorChangeStatus)
}

// Delegation changes of an already inactive governor do not touch the mark,
// so undelegating after a validator deactivation keeps the exemption
// (accepted, see the x/gov README).
func TestGovernorUndelegationAfterValidatorJailKeepsExemption(t *testing.T) {
	min := v1.DefaultMinGovernorSelfDelegation
	gf := newGovernorFixture(t, min.MulRaw(2))

	gf.jail(t, 2)
	shares, err := gf.stakingKeeper.ValidateUnbondAmount(gf.ctx, gf.govAddr, gf.vals[2], min)
	assert.NilError(t, err)
	_, _, err = gf.stakingKeeper.Undelegate(gf.ctx, gf.govAddr, gf.vals[2], shares)
	assert.NilError(t, err)
	gf.assertDeactivatedByValidator(t, true)

	gf.unjail(t, 2)
	assert.NilError(t, gf.setStatus(v1.Active))
	gf.assertState(t, true, map[int]bool{2: true})
}
