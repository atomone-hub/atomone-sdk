package keeper_test

import (
	"time"

	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtestutil "github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// unbondingGenesisFixture builds an exported genesis carrying one unbonding
// validator with its own in-flight unbonding (id 7), an unbonding delegation
// with two entries (ids 5, on hold, and 6) and a redelegation (id 8), and arms
// the pool mocks InitGenesis reads: an empty bonded pool and a not-bonded
// pool holding the validator's tokens plus the undelegated balances.
func (s *KeeperTestSuite) unbondingGenesisFixture() (*stakingtypes.GenesisState, stakingtypes.Validator, stakingtypes.UnbondingDelegation, stakingtypes.Redelegation, []sdk.AccAddress, []sdk.ValAddress) {
	delAddrs, valAddrs := createValAddrs(3)
	valCodec, accCodec := address.NewBech32Codec("cosmosvaloper"), address.NewBech32Codec("cosmos")
	completion := s.ctx.BlockTime().Add(time.Hour)

	validator := stakingtestutil.NewValidator(s.T(), valAddrs[0], PKs[0])
	validator.Status = stakingtypes.Unbonding
	validator.UnbondingHeight = 1
	validator.UnbondingTime = completion
	validator.UnbondingIds = []uint64{7}
	validator.Tokens = math.NewInt(100)
	validator.DelegatorShares = math.LegacyNewDec(100)

	ubd := stakingtypes.NewUnbondingDelegation(delAddrs[0], valAddrs[0], 1, completion, math.NewInt(5), 5, valCodec, accCodec)
	ubd.Entries[0].UnbondingOnHoldRefCount = 1
	ubd.AddEntry(2, completion, math.NewInt(3), 6)

	red := stakingtypes.NewRedelegation(delAddrs[1], valAddrs[1], valAddrs[2], 1, completion, math.NewInt(4), math.LegacyNewDec(4), 8, valCodec, accCodec)

	params := stakingtypes.DefaultParams()
	notBonded := sdk.NewCoins(sdk.NewCoin(params.BondDenom, validator.Tokens.AddRaw(5).AddRaw(3)))
	s.accountKeeper.EXPECT().GetModuleAccount(gomock.Any(), stakingtypes.BondedPoolName).Return(bondedAcc)
	s.accountKeeper.EXPECT().GetModuleAccount(gomock.Any(), stakingtypes.NotBondedPoolName).Return(notBondedAcc)
	s.bankKeeper.EXPECT().GetAllBalances(gomock.Any(), bondedAcc.GetAddress()).Return(sdk.NewCoins())
	s.accountKeeper.EXPECT().SetModuleAccount(gomock.Any(), bondedAcc)
	s.bankKeeper.EXPECT().GetAllBalances(gomock.Any(), notBondedAcc.GetAddress()).Return(notBonded)

	genesis := &stakingtypes.GenesisState{
		Params:               params,
		LastTotalPower:       math.ZeroInt(),
		Validators:           []stakingtypes.Validator{validator},
		UnbondingDelegations: []stakingtypes.UnbondingDelegation{ubd},
		Redelegations:        []stakingtypes.Redelegation{red},
		Exported:             true,
	}
	return genesis, validator, ubd, red, delAddrs, valAddrs
}

// TestInitGenesisRebuildsUnbondingOperationIndexes: an export carries every
// operation's unbonding id and on-hold refcount but not the per-id indexes,
// types, or the id counter. The import rebuilds them, so a held entry can be
// released, new holds can be placed, and fresh operations get ids above the
// imported ones.
func (s *KeeperTestSuite) TestInitGenesisRebuildsUnbondingOperationIndexes() {
	ctx, keeper, require := s.ctx, s.stakingKeeper, s.Require()
	genesis, validator, ubd, red, delAddrs, valAddrs := s.unbondingGenesisFixture()

	keeper.InitGenesis(ctx, genesis)

	for id, want := range map[uint64]stakingtypes.UnbondingType{
		5: stakingtypes.UnbondingType_UnbondingDelegation,
		6: stakingtypes.UnbondingType_UnbondingDelegation,
		7: stakingtypes.UnbondingType_ValidatorUnbonding,
		8: stakingtypes.UnbondingType_Redelegation,
	} {
		got, err := keeper.GetUnbondingType(ctx, id)
		require.NoError(err, "id %d", id)
		require.Equal(want, got, "id %d", id)
	}

	gotUBD, err := keeper.GetUnbondingDelegationByUnbondingID(ctx, 5)
	require.NoError(err)
	require.Equal(ubd, gotUBD)
	gotRED, err := keeper.GetRedelegationByUnbondingID(ctx, 8)
	require.NoError(err)
	require.Equal(red, gotRED)
	gotVal, err := keeper.GetValidatorByUnbondingID(ctx, 7)
	require.NoError(err)
	require.Equal(validator.OperatorAddress, gotVal.OperatorAddress)

	// The counter continues above the highest imported id.
	next, err := keeper.IncrementUnbondingID(ctx)
	require.NoError(err)
	require.Equal(uint64(9), next)

	// The entry imported on hold can be released, and a hold can be placed
	// on the other one.
	require.NoError(keeper.UnbondingCanComplete(ctx, 5))
	require.NoError(keeper.PutUnbondingOnHold(ctx, 6))
	after, err := keeper.GetUnbondingDelegation(ctx, delAddrs[0], valAddrs[0])
	require.NoError(err)
	require.Equal(int64(0), after.Entries[0].UnbondingOnHoldRefCount)
	require.Equal(int64(1), after.Entries[1].UnbondingOnHoldRefCount)
}

// TestInitGenesisNeverLowersTheUnbondingCounter: a counter already past the
// imported ids is left alone.
func (s *KeeperTestSuite) TestInitGenesisNeverLowersTheUnbondingCounter() {
	ctx, keeper, require := s.ctx, s.stakingKeeper, s.Require()
	for range 20 {
		_, err := keeper.IncrementUnbondingID(ctx)
		require.NoError(err)
	}
	genesis, _, _, _, _, _ := s.unbondingGenesisFixture()

	keeper.InitGenesis(ctx, genesis)

	next, err := keeper.IncrementUnbondingID(ctx)
	require.NoError(err)
	require.Equal(uint64(21), next)
}

// TestUnbondAllMatureValidatorsClearsUnbondingIds: a validator that completes
// its own unbonding while keeping shares has its UnbondingIds cleared in
// state, not only on a local copy, so no id whose index was just deleted
// lingers on it.
func (s *KeeperTestSuite) TestUnbondAllMatureValidatorsClearsUnbondingIds() {
	ctx, keeper, require := s.ctx, s.stakingKeeper, s.Require()
	_, valAddrs := createValAddrs(1)

	validator := stakingtestutil.NewValidator(s.T(), valAddrs[0], PKs[0])
	validator.Status = stakingtypes.Unbonding
	validator.UnbondingHeight = 1
	validator.UnbondingTime = ctx.BlockTime().Add(-time.Hour)
	validator.UnbondingIds = []uint64{3}
	validator.Tokens = math.NewInt(100)
	validator.DelegatorShares = math.LegacyNewDec(100)
	require.NoError(keeper.SetValidator(ctx, validator))
	require.NoError(keeper.SetValidatorByUnbondingID(ctx, validator, 3))
	require.NoError(keeper.SetUnbondingType(ctx, 3, stakingtypes.UnbondingType_ValidatorUnbonding))
	require.NoError(keeper.InsertUnbondingValidatorQueue(ctx, validator))

	require.NoError(keeper.UnbondAllMatureValidators(ctx.WithBlockHeight(2)))

	got, err := keeper.GetValidator(ctx, valAddrs[0])
	require.NoError(err)
	require.Equal(stakingtypes.Unbonded, got.Status)
	require.Empty(got.UnbondingIds, "completed unbonding ids must not stay on the validator")
	_, err = keeper.GetValidatorByUnbondingID(ctx, 3)
	require.ErrorIs(err, stakingtypes.ErrNoValidatorFound)
}
