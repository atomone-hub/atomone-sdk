package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	distrtestutil "github.com/cosmos/cosmos-sdk/x/distribution/testutil"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestAutoStakeValidatorCommission_NoBondDenom asserts the helper is a no-op
// when the validator has no bond denom in accumulated commission.
func TestAutoStakeValidatorCommission_NoBondDenom(t *testing.T) {
	s := setupTestKeeper(t, math.LegacyZeroDec(), 100)

	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	valAddr, err := s.valCodec.StringToBytes(val.GetOperator())
	require.NoError(t, err)

	require.NoError(t, s.distrKeeper.SetValidatorAccumulatedCommission(s.ctx, valAddr,
		disttypes.ValidatorAccumulatedCommission{
			Commission: sdk.DecCoins{sdk.NewDecCoinFromDec("photon", math.LegacyNewDec(5))},
		}))
	require.NoError(t, s.distrKeeper.SetValidatorOutstandingRewards(s.ctx, valAddr,
		disttypes.ValidatorOutstandingRewards{
			Rewards: sdk.DecCoins{sdk.NewDecCoinFromDec("photon", math.LegacyNewDec(5))},
		}))

	staked, err := s.distrKeeper.AutoStakeValidatorCommission(s.ctx, valAddr)
	require.NoError(t, err)
	require.True(t, staked.Amount.IsZero())

	// Accumulated commission untouched.
	got, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{sdk.NewDecCoinFromDec("photon", math.LegacyNewDec(5))}, got.Commission)
}

// TestAutoStakeValidatorCommission_BondDenomOnly asserts integer auto-stake
// + dust-to-community-pool routing for a pure bond denom commission.
func TestAutoStakeValidatorCommission_BondDenomOnly(t *testing.T) {
	s := setupTestKeeper(t, math.LegacyZeroDec(), 100)

	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	valAddr, err := s.valCodec.StringToBytes(val.GetOperator())
	require.NoError(t, err)
	operatorAcc := sdk.AccAddress(valAddr)

	// 7.5 stake commission: 7 integer auto-stakes, 0.5 dust to community pool.
	require.NoError(t, s.distrKeeper.SetValidatorAccumulatedCommission(s.ctx, valAddr,
		disttypes.ValidatorAccumulatedCommission{
			Commission: sdk.DecCoins{sdk.NewDecCoinFromDec(sdk.DefaultBondDenom, math.LegacyNewDecWithPrec(75, 1))},
		}))
	require.NoError(t, s.distrKeeper.SetValidatorOutstandingRewards(s.ctx, valAddr,
		disttypes.ValidatorOutstandingRewards{
			Rewards: sdk.DecCoins{sdk.NewDecCoinFromDec(sdk.DefaultBondDenom, math.LegacyNewDecWithPrec(75, 1))},
		}))

	bondInt := math.NewInt(7)
	expectedBondCoins := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, bondInt))
	s.bankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), disttypes.ModuleName, operatorAcc, expectedBondCoins).Return(nil)
	s.stakingKeeper.EXPECT().GetValidator(gomock.Any(), sdk.ValAddress(valAddr)).Return(val, nil)
	s.stakingKeeper.EXPECT().Delegate(gomock.Any(), operatorAcc, bondInt, stakingtypes.Unbonded, val, true).Return(math.LegacyNewDecFromInt(bondInt), nil)

	staked, err := s.distrKeeper.AutoStakeValidatorCommission(s.ctx, sdk.ValAddress(valAddr))
	require.NoError(t, err)
	require.Equal(t, sdk.NewCoin(sdk.DefaultBondDenom, bondInt), staked)

	// Bond denom fully consumed (integer auto-staked, dust to community pool).
	got, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, sdk.ValAddress(valAddr))
	require.NoError(t, err)
	require.True(t, got.Commission.IsZero())

	// Outstanding decreased by the entire 7.5 stake.
	out, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, sdk.ValAddress(valAddr))
	require.NoError(t, err)
	require.True(t, out.Rewards.IsZero())

	// Community pool received 0.5 stake dust.
	feePool, err := s.distrKeeper.FeePool.Get(s.ctx)
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{sdk.NewDecCoinFromDec(sdk.DefaultBondDenom, math.LegacyNewDecWithPrec(5, 1))}, feePool.CommunityPool)
}

// TestAutoStakeBondedValidatorsCommission_EpochTrigger asserts the
// AfterEpochEnd hook routes through AutoStakeBondedValidatorsCommission
// and iterates the bonded validator set. Validators not in that set
// (jailed, unbonding, unbonded) are skipped, leaving their accumulated
// commission untouched until validator removal.
func TestAutoStakeBondedValidatorsCommission_EpochTrigger(t *testing.T) {
	s := setupTestKeeper(t, math.LegacyZeroDec(), 100)

	val0, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	valAddr0, err := s.valCodec.StringToBytes(val0.GetOperator())
	require.NoError(t, err)
	operator0 := sdk.AccAddress(valAddr0)

	commission := sdk.DecCoins{sdk.NewDecCoinFromDec(sdk.DefaultBondDenom, math.LegacyNewDec(2))}
	require.NoError(t, s.distrKeeper.SetValidatorAccumulatedCommission(s.ctx, valAddr0,
		disttypes.ValidatorAccumulatedCommission{Commission: commission}))
	require.NoError(t, s.distrKeeper.SetValidatorOutstandingRewards(s.ctx, valAddr0,
		disttypes.ValidatorOutstandingRewards{Rewards: commission}))

	// Only the bonded validator is iterated; we mock the staking call to
	// return a one-validator slice.
	s.stakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{val0}, nil)
	bondInt := math.NewInt(2)
	s.bankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), disttypes.ModuleName, operator0, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, bondInt))).Return(nil)
	s.stakingKeeper.EXPECT().GetValidator(gomock.Any(), sdk.ValAddress(valAddr0)).Return(val0, nil)
	s.stakingKeeper.EXPECT().Delegate(gomock.Any(), operator0, bondInt, stakingtypes.Unbonded, val0, true).Return(math.LegacyNewDecFromInt(bondInt), nil)

	require.NoError(t, s.distrKeeper.AutoStakeBondedValidatorsCommission(s.ctx))

	got, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, valAddr0)
	require.NoError(t, err)
	require.True(t, got.Commission.IsZero())
}

// TestAutoStakeValidatorCommission_MixedDenoms asserts only the bond denom
// portion is touched; non-bond commission is left in accumulatedCommission
// for MsgWithdrawValidatorCommission to handle.
func TestAutoStakeValidatorCommission_MixedDenoms(t *testing.T) {
	s := setupTestKeeper(t, math.LegacyZeroDec(), 100)

	val, err := distrtestutil.CreateValidator(valConsPk0, math.NewInt(100))
	require.NoError(t, err)
	valAddr, err := s.valCodec.StringToBytes(val.GetOperator())
	require.NoError(t, err)
	operatorAcc := sdk.AccAddress(valAddr)

	// 5 stake bond denom + 3 photon non-bond.
	commission := sdk.DecCoins{
		sdk.NewDecCoinFromDec("photon", math.LegacyNewDec(3)),
		sdk.NewDecCoinFromDec(sdk.DefaultBondDenom, math.LegacyNewDec(5)),
	}
	require.NoError(t, s.distrKeeper.SetValidatorAccumulatedCommission(s.ctx, valAddr,
		disttypes.ValidatorAccumulatedCommission{Commission: commission}))
	require.NoError(t, s.distrKeeper.SetValidatorOutstandingRewards(s.ctx, valAddr,
		disttypes.ValidatorOutstandingRewards{Rewards: commission}))

	bondInt := math.NewInt(5)
	s.bankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), disttypes.ModuleName, operatorAcc, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, bondInt))).Return(nil)
	s.stakingKeeper.EXPECT().GetValidator(gomock.Any(), sdk.ValAddress(valAddr)).Return(val, nil)
	s.stakingKeeper.EXPECT().Delegate(gomock.Any(), operatorAcc, bondInt, stakingtypes.Unbonded, val, true).Return(math.LegacyNewDecFromInt(bondInt), nil)

	staked, err := s.distrKeeper.AutoStakeValidatorCommission(s.ctx, sdk.ValAddress(valAddr))
	require.NoError(t, err)
	require.Equal(t, sdk.NewCoin(sdk.DefaultBondDenom, bondInt), staked)

	// Non-bond commission remains.
	got, err := s.distrKeeper.GetValidatorAccumulatedCommission(s.ctx, sdk.ValAddress(valAddr))
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{sdk.NewDecCoinFromDec("photon", math.LegacyNewDec(3))}, got.Commission)

	// Outstanding decreased by bond denom only.
	out, err := s.distrKeeper.GetValidatorOutstandingRewards(s.ctx, sdk.ValAddress(valAddr))
	require.NoError(t, err)
	require.Equal(t, sdk.DecCoins{sdk.NewDecCoinFromDec("photon", math.LegacyNewDec(3))}, out.Rewards)
}
