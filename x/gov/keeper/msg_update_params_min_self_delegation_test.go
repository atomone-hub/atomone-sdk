package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/keeper"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// TestUpdateParamsMinGovernorSelfDelegationInvariant checks that UpdateParams
// deactivates active governors below a raised MinGovernorSelfDelegation: the
// minimum is otherwise only enforced pointwise, and a below-min ACTIVE
// governor makes the exported genesis panic InitGenesis on restart. A param
// raise is not the governor's doing, so the governor is deactivated with
// DeactivatedByValidator set and can reactivate immediately after topping up.
func TestUpdateParamsMinGovernorSelfDelegationInvariant(t *testing.T) {
	govKeeper, accKeeper, bankKeeper, stakingKeeper, distrKeeper, _, ctx := setupGovKeeper(t, mockAccountKeeperExpectations)
	// 2 validators, 2 delegators, 3 governors: governors[0], governors[1]
	// active, governors[2] inactive.
	s := newFixture(t, ctx, 2, 2, 3, govKeeper, mocks{
		accKeeper:          accKeeper,
		bankKeeper:         bankKeeper,
		stakingKeeper:      stakingKeeper,
		distributionKeeper: distrKeeper,
	})

	fullMin := v1.DefaultMinGovernorSelfDelegation.Int64()

	// active governor A: bonded exactly at the default minimum; it will fall
	// below the raised one.
	govA := s.activeGovernors[0].GetAddress()
	delA := sdk.AccAddress(govA)
	require.NoError(t, govKeeper.DelegateToGovernor(ctx, delA, govA))
	s.delegate(delA, s.valAddrs[0], fullMin)

	// active governor B: bonded above the raised minimum; it must stay active.
	govB := s.activeGovernors[1].GetAddress()
	delB := sdk.AccAddress(govB)
	require.NoError(t, govKeeper.DelegateToGovernor(ctx, delB, govB))
	s.delegate(delB, s.valAddrs[1], 3*fullMin)

	params, err := govKeeper.Params.Get(ctx)
	require.NoError(t, err)

	// Initialize the last min deposit trackers like InitGenesis does, so the
	// throttler bookkeeping in UpdateParams runs against clean state.
	now := ctx.BlockTime()
	require.NoError(t, govKeeper.LastMinDeposit.Set(ctx, v1.LastMinDeposit{
		Value: params.MinDepositThrottler.FloorValue,
		Time:  &now,
	}))
	require.NoError(t, govKeeper.LastMinInitialDeposit.Set(ctx, v1.LastMinDeposit{
		Value: params.MinInitialDepositThrottler.FloorValue,
		Time:  &now,
	}))

	bondedA, err := govKeeper.GetGovernorBondedTokens(ctx, govA)
	require.NoError(t, err)
	require.True(t, bondedA.Equal(v1.DefaultMinGovernorSelfDelegation), "governor A bonded: %s", bondedA)

	// Raise the minimum above governor A's bonded tokens through the public
	// entry point governance proposals hit.
	newParams := params
	newParams.MinGovernorSelfDelegation = sdkmath.NewInt(2 * fullMin).String()
	msgSrvr := keeper.NewMsgServerImpl(govKeeper)
	_, err = msgSrvr.UpdateParams(ctx, &v1.MsgUpdateParams{
		Authority: govKeeper.GetAuthority(),
		Params:    newParams,
	})
	require.NoError(t, err)

	storedParams, err := govKeeper.Params.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, newParams.MinGovernorSelfDelegation, storedParams.MinGovernorSelfDelegation)

	// Governor A is below the raised minimum: deactivated, with the
	// not-self-caused mark that exempts it from GovernorStatusChangePeriod.
	govAStored, err := govKeeper.Governors.Get(ctx, govA)
	require.NoError(t, err)
	assert.False(t, govAStored.IsActive(), "active governor below the raised minimum must be deactivated")
	assert.True(t, govAStored.DeactivatedByValidator, "a param raise is not the governor's doing")

	// Governor B still meets the raised minimum: it must remain active.
	govBStored, err := govKeeper.Governors.Get(ctx, govB)
	require.NoError(t, err)
	assert.True(t, govBStored.IsActive(), "active governor above the raised minimum must stay active")

	// The InitGenesis precondition holds on the resulting live state: no
	// ACTIVE governor is below the minimum.
	minSelfDelegation := sdkmath.NewInt(2 * fullMin)
	err = govKeeper.Governors.Walk(ctx, nil, func(_ types.GovernorAddress, governor v1.Governor) (bool, error) {
		if governor.IsActive() {
			bonded, err := govKeeper.GetGovernorBondedTokens(ctx, governor.GetAddress())
			require.NoError(t, err)
			assert.True(t, bonded.GTE(minSelfDelegation),
				"active governor %s bonded %s below minimum %s",
				governor.GovernorAddress, bonded, minSelfDelegation)
		}
		return false, nil
	})
	require.NoError(t, err)

	// Topping up meets the new minimum: reactivation succeeds immediately,
	// without waiting GovernorStatusChangePeriod.
	s.delegate(delA, s.valAddrs[0], fullMin)
	_, err = msgSrvr.UpdateGovernorStatus(ctx, v1.NewMsgUpdateGovernorStatus(delA, v1.Active))
	require.NoError(t, err)
	govAStored, err = govKeeper.Governors.Get(ctx, govA)
	require.NoError(t, err)
	assert.True(t, govAStored.IsActive(), "governor that topped up must reactivate without waiting")
	assert.False(t, govAStored.DeactivatedByValidator, "reactivation clears the mark")

	// Lowering the minimum back is accepted and does not resurrect governors.
	_, err = msgSrvr.UpdateParams(ctx, &v1.MsgUpdateParams{
		Authority: govKeeper.GetAuthority(),
		Params:    params,
	})
	require.NoError(t, err)
	govBStored, err = govKeeper.Governors.Get(ctx, govB)
	require.NoError(t, err)
	assert.True(t, govBStored.IsActive())
}
