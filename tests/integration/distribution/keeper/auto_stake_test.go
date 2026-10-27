package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtestutil "github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// requireStakingPoolsConsistent checks the staking module account invariant:
// the bonded pool holds exactly the tokens of bonded validators, and the
// not-bonded pool those of the other validators plus unbonding delegations.
func requireStakingPoolsConsistent(t *testing.T, f *fixture, ctx sdk.Context) {
	t.Helper()
	bondedTokens, notBondedTokens := math.ZeroInt(), math.ZeroInt()
	vals, err := f.stakingKeeper.GetAllValidators(ctx)
	require.NoError(t, err)
	for _, v := range vals {
		if v.IsBonded() {
			bondedTokens = bondedTokens.Add(v.Tokens)
		} else {
			notBondedTokens = notBondedTokens.Add(v.Tokens)
		}
	}
	require.NoError(t, f.stakingKeeper.IterateUnbondingDelegations(ctx, func(_ int64, ubd stakingtypes.UnbondingDelegation) bool {
		for _, e := range ubd.Entries {
			notBondedTokens = notBondedTokens.Add(e.Balance)
		}
		return false
	}))
	bondedPool := f.bankKeeper.GetBalance(ctx, f.stakingKeeper.GetBondedPool(ctx).GetAddress(), sdk.DefaultBondDenom)
	notBondedPool := f.bankKeeper.GetBalance(ctx, f.stakingKeeper.GetNotBondedPool(ctx).GetAddress(), sdk.DefaultBondDenom)
	require.Equal(t, bondedTokens, bondedPool.Amount, "bonded pool balance must equal bonded validators' tokens")
	require.Equal(t, notBondedTokens, notBondedPool.Amount, "not-bonded pool balance must equal non-bonded validators' tokens")
}

func createZeroCommissionValidator(t *testing.T, f *fixture, selfBond math.Int) {
	t.Helper()
	coins := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, selfBond))
	require.NoError(t, f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName, coins))
	require.NoError(t, f.bankKeeper.SendCoinsFromModuleToAccount(f.sdkCtx, distrtypes.ModuleName, sdk.AccAddress(f.valAddr), coins))
	tstaking := stakingtestutil.NewHelper(t, f.sdkCtx, f.stakingKeeper)
	tstaking.Commission = stakingtypes.NewCommissionRates(math.LegacyZeroDec(), math.LegacyNewDecWithPrec(5, 1), math.LegacyZeroDec())
	tstaking.CreateValidator(f.valAddr, valConsPk0, selfBond, true)
}

// TestAutoStakeToNonBondedValidator checks that auto-staked rewards of a
// validator that is not bonded (e.g. a deposit right after creation) land in
// the not-bonded pool, so staking can later bond the validator.
func TestAutoStakeToNonBondedValidator(t *testing.T) {
	f := initFixture(t)
	selfBond := f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 10)
	createZeroCommissionValidator(t, f, selfBond)

	val, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.Equal(t, stakingtypes.Unbonded, val.Status)

	reward := f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 50)
	rewardCoins := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, reward))
	require.NoError(t, f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName, rewardCoins))
	bondRemainder, err := f.distrKeeper.AllocateTokensToValidator(f.sdkCtx, val, sdk.NewDecCoinsFromCoins(rewardCoins...))
	require.NoError(t, err)
	require.True(t, bondRemainder.IsZero())
	requireStakingPoolsConsistent(t, f, f.sdkCtx)

	_, err = f.stakingKeeper.EndBlocker(f.sdkCtx)
	require.NoError(t, err)
	val, err = f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.Equal(t, stakingtypes.Bonded, val.Status)
	require.Equal(t, selfBond.Add(reward), val.Tokens)
	requireStakingPoolsConsistent(t, f, f.sdkCtx)
}

// TestAutoStakeToValidatorWithoutShares checks that a validator whose
// delegations were all removed, but which still receives rewards while in the
// last commit, does not end up with tokens nobody owns: its bond denom
// rewards go to the community pool and the validator is removed normally at
// the end of its unbonding period.
func TestAutoStakeToValidatorWithoutShares(t *testing.T) {
	f := initFixture(t)
	selfBond := f.stakingKeeper.TokensFromConsensusPower(f.sdkCtx, 10)
	createZeroCommissionValidator(t, f, selfBond)
	_, err := f.stakingKeeper.EndBlocker(f.sdkCtx)
	require.NoError(t, err)

	val, err := f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	_, _, err = f.stakingKeeper.Undelegate(f.sdkCtx, sdk.AccAddress(f.valAddr), f.valAddr, val.DelegatorShares)
	require.NoError(t, err)
	_, err = f.stakingKeeper.EndBlocker(f.sdkCtx)
	require.NoError(t, err)

	val, err = f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.Equal(t, stakingtypes.Unbonding, val.Status)
	require.True(t, val.DelegatorShares.IsZero())

	// Rewards allocated while the validator is still in the last commit.
	reward := math.NewInt(1000)
	rewardCoins := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, reward))
	require.NoError(t, f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName, rewardCoins))
	bondRemainder, err := f.distrKeeper.AllocateTokensToValidator(f.sdkCtx, val, sdk.NewDecCoinsFromCoins(rewardCoins...))
	require.NoError(t, err)
	require.Equal(t, sdk.NewDecCoinsFromCoins(rewardCoins...), bondRemainder)

	// A deposit to the same validator is routed to the community pool.
	depositor := sdk.AccAddress(PKS[1].Address())
	require.NoError(t, f.bankKeeper.MintCoins(f.sdkCtx, distrtypes.ModuleName, rewardCoins))
	require.NoError(t, f.bankKeeper.SendCoinsFromModuleToAccount(f.sdkCtx, distrtypes.ModuleName, depositor, rewardCoins))
	feePoolBefore, err := f.distrKeeper.FeePool.Get(f.sdkCtx)
	require.NoError(t, err)
	_, err = f.app.RunMsg(&distrtypes.MsgDepositValidatorRewardsPool{
		Depositor:        depositor.String(),
		ValidatorAddress: f.valAddr.String(),
		Amount:           rewardCoins,
	})
	require.NoError(t, err)
	feePoolAfter, err := f.distrKeeper.FeePool.Get(f.sdkCtx)
	require.NoError(t, err)
	require.Equal(t, feePoolBefore.CommunityPool.Add(sdk.NewDecCoinsFromCoins(rewardCoins...)...), feePoolAfter.CommunityPool)

	val, err = f.stakingKeeper.GetValidator(f.sdkCtx, f.valAddr)
	require.NoError(t, err)
	require.True(t, val.Tokens.IsZero())
	requireStakingPoolsConsistent(t, f, f.sdkCtx)

	ubdTime, err := f.stakingKeeper.UnbondingTime(f.sdkCtx)
	require.NoError(t, err)
	matured := f.sdkCtx.WithBlockTime(f.sdkCtx.BlockTime().Add(ubdTime + time.Second)).WithBlockHeight(f.sdkCtx.BlockHeight() + 10)
	_, err = f.stakingKeeper.EndBlocker(matured)
	require.NoError(t, err)
	_, err = f.stakingKeeper.GetValidator(matured, f.valAddr)
	require.ErrorIs(t, err, stakingtypes.ErrNoValidatorFound)
}
