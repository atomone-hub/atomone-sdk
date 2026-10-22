package keeper_test

import (
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtestutil "github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func (s *KeeperTestSuite) TestGetValidatorByConsAddrFollowsRotationChain() {
	require := s.Require()
	first := sdk.ConsAddress(ed25519.GenPrivKey().PubKey().Address())
	second := sdk.ConsAddress(ed25519.GenPrivKey().PubKey().Address())
	currentPk := ed25519.GenPrivKey().PubKey()
	valAddr := sdk.ValAddress(currentPk.Address())

	val := stakingtestutil.NewValidator(s.T(), valAddr, currentPk)
	require.NoError(s.stakingKeeper.SetValidator(s.ctx, val))
	require.NoError(s.stakingKeeper.SetValidatorByConsAddr(s.ctx, val))

	store := s.ctx.KVStore(s.storeKey)
	store.Set(stakingtypes.GetOldToNewConsAddrMapKey(first), second)
	store.Set(stakingtypes.GetOldToNewConsAddrMapKey(second), currentPk.Address())

	got, err := s.stakingKeeper.GetValidatorByConsAddr(s.ctx, first)
	require.NoError(err)
	require.Equal(val.GetOperator(), got.GetOperator())
}

func (s *KeeperTestSuite) TestGetValidatorByConsAddrStopsOnRotationCycle() {
	require := s.Require()
	first := sdk.ConsAddress(ed25519.GenPrivKey().PubKey().Address())
	second := sdk.ConsAddress(ed25519.GenPrivKey().PubKey().Address())

	store := s.ctx.KVStore(s.storeKey)
	store.Set(stakingtypes.GetOldToNewConsAddrMapKey(first), second)
	store.Set(stakingtypes.GetOldToNewConsAddrMapKey(second), first)

	_, err := s.stakingKeeper.GetValidatorByConsAddr(s.ctx, first)
	require.ErrorIs(err, stakingtypes.ErrNoValidatorFound)
}
