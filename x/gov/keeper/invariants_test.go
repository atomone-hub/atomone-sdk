package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/keeper"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

func TestGovernorsDelegationsInvariant_UppercaseOperatorAddress(t *testing.T) {
	govKeeper, accKeeper, bankKeeper, stakingKeeper, distrKeeper, _, ctx := setupGovKeeper(t, mockAccountKeeperExpectations)
	mocks := mocks{
		accKeeper:          accKeeper,
		bankKeeper:         bankKeeper,
		stakingKeeper:      stakingKeeper,
		distributionKeeper: distrKeeper,
	}
	params := v1.DefaultParams()
	params.MinGovernorSelfDelegation = "1"
	require.NoError(t, govKeeper.Params.Set(ctx, params))

	s := newFixture(t, ctx, 2, 1, 2, govKeeper, mocks)
	for _, govAddr := range s.govAddrs {
		accAddr := sdk.AccAddress(govAddr)
		s.delegate(accAddr, s.valAddrs[0], 1)
		require.NoError(t, govKeeper.DelegateToGovernor(ctx, accAddr, govAddr))
	}
	s.upcaseValidatorAddress(0)

	msg, broken := keeper.GovernorsDelegationsInvariant(govKeeper, stakingKeeper)(ctx)
	require.False(t, broken, msg)
}
