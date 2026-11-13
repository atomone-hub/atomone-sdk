package keeper_test

import (
	"testing"

	"gotest.tools/v3/assert"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/keeper"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// governorFixture is a fixture with the gov staking hooks registered, three
// bonded validators and a governor staking on the third one.
type governorFixture struct {
	*fixture
	vals    []sdk.ValAddress
	govAddr sdk.AccAddress
	gov     types.GovernorAddress
}

func newGovernorFixture(t *testing.T, stake math.Int) *governorFixture {
	t.Helper()
	f := initFixture(t)
	f.stakingKeeper.SetHooks(f.govKeeper.StakingHooks())
	_, vals := createValidators(t, f, []int64{50000, 60000, 70000})

	govAddr := simtestutil.AddTestAddrs(f.bankKeeper, f.stakingKeeper, f.ctx, 1, stake)[0]
	val, found := f.stakingKeeper.GetValidator(f.ctx, vals[2])
	assert.Assert(t, found)
	_, err := f.stakingKeeper.Delegate(f.ctx, govAddr, stake, stakingtypes.Unbonded, val, true)
	assert.NilError(t, err)
	f.stakingKeeper.EndBlocker(f.ctx)

	_, err = keeper.NewMsgServerImpl(f.govKeeper).CreateGovernor(f.ctx, v1.NewMsgCreateGovernor(govAddr, v1.GovernorDescription{}))
	assert.NilError(t, err)

	gf := &governorFixture{fixture: f, vals: vals, govAddr: govAddr, gov: types.GovernorAddress(govAddr)}
	gf.assertState(t, true, map[int]bool{2: true})
	return gf
}

// assertState checks the governor status and which of the three validators
// have an ActiveGovernorsByDelegatedValidator entry for the governor.
func (gf *governorFixture) assertState(t *testing.T, active bool, indexed map[int]bool) {
	t.Helper()
	g, err := gf.govKeeper.Governors.Get(gf.ctx, gf.gov)
	assert.NilError(t, err)
	assert.Equal(t, g.IsActive(), active, "governor active")
	for i, val := range gf.vals {
		has, err := gf.govKeeper.ActiveGovernorsByDelegatedValidator.Has(gf.ctx, collections.Join(val, gf.gov))
		assert.NilError(t, err)
		assert.Equal(t, has, indexed[i], "index entry for validator %d", i)
	}
}

func (gf *governorFixture) redelegate(t *testing.T, from, to int, amount math.Int) {
	t.Helper()
	shares, err := gf.stakingKeeper.ValidateUnbondAmount(gf.ctx, gf.govAddr, gf.vals[from], amount)
	assert.NilError(t, err)
	_, err = gf.stakingKeeper.BeginRedelegation(gf.ctx, gf.govAddr, gf.vals[from], gf.vals[to], shares)
	assert.NilError(t, err)
}

func TestGovernorRedelegation(t *testing.T) {
	min := v1.DefaultMinGovernorSelfDelegation

	tests := []struct {
		name         string
		stake, moved math.Int
		active       bool
		indexed      map[int]bool
	}{
		{
			name:    "full redelegation of exactly the minimum keeps the governor active",
			stake:   min,
			moved:   min,
			active:  true,
			indexed: map[int]bool{0: true},
		},
		{
			name:    "partial redelegation from exactly the minimum keeps the governor active",
			stake:   min,
			moved:   math.NewInt(1),
			active:  true,
			indexed: map[int]bool{0: true, 2: true},
		},
		{
			name:    "full redelegation of twice the minimum keeps the governor active",
			stake:   min.MulRaw(2),
			moved:   min.MulRaw(2),
			active:  true,
			indexed: map[int]bool{0: true},
		},
		{
			name:    "redelegation leaving less than the minimum on the source keeps the governor active",
			stake:   min.MulRaw(2),
			moved:   min.AddRaw(1),
			active:  true,
			indexed: map[int]bool{0: true, 2: true},
		},
		{
			name:    "redelegation leaving the minimum on the source keeps the governor active",
			stake:   min.MulRaw(2),
			moved:   min,
			active:  true,
			indexed: map[int]bool{0: true, 2: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := newGovernorFixture(t, tt.stake)
			gf.redelegate(t, 2, 0, tt.moved)
			gf.assertState(t, tt.active, tt.indexed)
			total, err := gf.govKeeper.GetGovernorBondedTokens(gf.ctx, gf.gov)
			assert.NilError(t, err)
			assert.Assert(t, total.Equal(tt.stake), "total bonded %s, expected %s", total, tt.stake)
		})
	}
}

// A redelegation to a validator that is not bonded really does take the
// governor below the minimum, the governor must be deactivated.
func TestGovernorRedelegationToUnbondedValidator(t *testing.T) {
	min := v1.DefaultMinGovernorSelfDelegation
	gf := newGovernorFixture(t, min)

	pk := simtestutil.CreateTestPubKeys(6)[5]
	unbondedAddr := sdk.ValAddress(simtestutil.CreateIncrementalAccounts(1)[0])
	unbonded, err := stakingtypes.NewValidator(unbondedAddr.String(), pk, stakingtypes.Description{})
	assert.NilError(t, err)
	assert.NilError(t, gf.stakingKeeper.SetValidator(gf.ctx, unbonded))
	assert.NilError(t, gf.stakingKeeper.SetValidatorByConsAddr(gf.ctx, unbonded))
	assert.NilError(t, gf.stakingKeeper.SetNewValidatorByPowerIndex(gf.ctx, unbonded))
	gf.vals = append(gf.vals, unbondedAddr)

	shares, err := gf.stakingKeeper.ValidateUnbondAmount(gf.ctx, gf.govAddr, gf.vals[2], min)
	assert.NilError(t, err)
	_, err = gf.stakingKeeper.BeginRedelegation(gf.ctx, gf.govAddr, gf.vals[2], unbondedAddr, shares)
	assert.NilError(t, err)

	g, err := gf.govKeeper.Governors.Get(gf.ctx, gf.gov)
	assert.NilError(t, err)
	assert.Assert(t, !g.IsActive())
	has, err := gf.govKeeper.ActiveGovernorsByDelegatedValidator.Has(gf.ctx, collections.Join(unbondedAddr, gf.gov))
	assert.NilError(t, err)
	assert.Assert(t, !has)
}

// Undelegating is not a redelegation: it still deactivates a governor that
// falls below the minimum.
func TestGovernorUndelegation(t *testing.T) {
	min := v1.DefaultMinGovernorSelfDelegation

	t.Run("undelegating everything deactivates", func(t *testing.T) {
		gf := newGovernorFixture(t, min)
		shares, err := gf.stakingKeeper.ValidateUnbondAmount(gf.ctx, gf.govAddr, gf.vals[2], min)
		assert.NilError(t, err)
		_, _, err = gf.stakingKeeper.Undelegate(gf.ctx, gf.govAddr, gf.vals[2], shares)
		assert.NilError(t, err)
		gf.assertState(t, false, map[int]bool{})
	})

	t.Run("undelegating below the minimum deactivates", func(t *testing.T) {
		gf := newGovernorFixture(t, min)
		shares, err := gf.stakingKeeper.ValidateUnbondAmount(gf.ctx, gf.govAddr, gf.vals[2], math.NewInt(1))
		assert.NilError(t, err)
		_, _, err = gf.stakingKeeper.Undelegate(gf.ctx, gf.govAddr, gf.vals[2], shares)
		assert.NilError(t, err)
		gf.assertState(t, false, map[int]bool{})
	})
}
