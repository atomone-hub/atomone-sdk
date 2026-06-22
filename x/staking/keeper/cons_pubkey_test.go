package keeper_test

import (
	"go.uber.org/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/testutil"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

func (s *KeeperTestSuite) TestConsPubKeyRotationHistory() {
	stakingKeeper, ctx := s.stakingKeeper, s.ctx

	_, addrVals := createValAddrs(2)

	val := testutil.NewValidator(s.T(), addrVals[0], PKs[0])
	valTokens := stakingKeeper.TokensFromConsensusPower(ctx, 10)
	val, issuedShares := val.AddTokensFromDel(valTokens)
	s.Require().Equal(valTokens, issuedShares.RoundInt())

	s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), types.NotBondedPoolName, types.BondedPoolName, gomock.Any()).AnyTimes()
	_ = keeper.TestingUpdateValidator(stakingKeeper, ctx, val, true)
	val0AccAddr := sdk.AccAddress(addrVals[0].Bytes())
	selfDelegation := types.NewDelegation(val0AccAddr.String(), addrVals[0].String(), issuedShares)

	err := stakingKeeper.SetDelegation(ctx, selfDelegation)
	s.Require().NoError(err)

	validators, err := stakingKeeper.GetAllValidators(ctx)
	s.Require().NoError(err)
	s.Require().Len(validators, 1)

	validator := validators[0]
	valAddr, err := sdk.ValAddressFromBech32(validator.OperatorAddress)
	s.Require().NoError(err)

	historyObjects, err := stakingKeeper.GetValidatorConsPubKeyRotationHistory(ctx, valAddr)
	s.Require().NoError(err)
	s.Require().Len(historyObjects, 0)

	blockHistory, err := stakingKeeper.GetBlockConsPubKeyRotationHistory(ctx)
	s.Require().NoError(err)
	s.Require().Len(blockHistory, 0)
}

func (s *KeeperTestSuite) setValidators(n int) {
	stakingKeeper, ctx := s.stakingKeeper, s.ctx

	_, addrVals := createValAddrs(n)

	for i := 0; i < n; i++ {
		val := testutil.NewValidator(s.T(), addrVals[i], PKs[i])
		valTokens := stakingKeeper.TokensFromConsensusPower(ctx, 10)
		val, issuedShares := val.AddTokensFromDel(valTokens)
		s.Require().Equal(valTokens, issuedShares.RoundInt())

		s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), types.NotBondedPoolName, types.BondedPoolName, gomock.Any()).AnyTimes()
		_ = keeper.TestingUpdateValidator(stakingKeeper, ctx, val, true)
		val0AccAddr := sdk.AccAddress(addrVals[i].Bytes())
		selfDelegation := types.NewDelegation(val0AccAddr.String(), addrVals[i].String(), issuedShares)
		err := stakingKeeper.SetDelegation(ctx, selfDelegation)
		s.Require().NoError(err)

		err = stakingKeeper.SetValidatorByConsAddr(ctx, val)
		s.Require().NoError(err)
	}

	validators, err := stakingKeeper.GetAllValidators(ctx)
	s.Require().NoError(err)
	s.Require().Len(validators, n)
}
