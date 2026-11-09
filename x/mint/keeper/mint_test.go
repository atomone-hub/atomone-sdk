package keeper_test

import (
	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/cosmos/cosmos-sdk/x/mint"
	"github.com/cosmos/cosmos-sdk/x/mint/keeper"
	minttestutil "github.com/cosmos/cosmos-sdk/x/mint/testutil"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

func (s *IntegrationTestSuite) TestWithMintFnOverridesDefault() {
	encCfg := moduletestutil.MakeTestEncodingConfig(mint.AppModuleBasic{})
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(s.T(), key, storetypes.NewTransientStoreKey("transient_test"))

	ctrl := gomock.NewController(s.T())
	accountKeeper := minttestutil.NewMockAccountKeeper(ctrl)
	accountKeeper.EXPECT().GetModuleAddress(types.ModuleName).Return(sdk.AccAddress{})

	called := false
	custom := func(ctx sdk.Context, k *keeper.Keeper) error {
		called = true
		return nil
	}

	k := keeper.NewKeeper(
		encCfg.Codec,
		runtime.NewKVStoreService(key),
		minttestutil.NewMockStakingKeeper(ctrl),
		accountKeeper,
		minttestutil.NewMockBankKeeper(ctrl),
		authtypes.FeeCollectorName,
		authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		keeper.WithMintFn(custom),
	)

	s.Require().NoError(k.MintFn(testCtx.Ctx))
	s.Require().True(called)
	s.Require().True(k.HasCustomMintFn())
	s.Require().False(s.mintKeeper.HasCustomMintFn())
}

func (s *IntegrationTestSuite) TestWithMintFnRejectsNil() {
	s.Require().PanicsWithValue("x/mint: WithMintFn requires a non-nil mint function", func() {
		keeper.WithMintFn(nil)
	})
}

func (s *IntegrationTestSuite) TestDefaultMintFnMintsBlockProvision() {
	s.Require().NoError(s.mintKeeper.Minter.Set(s.ctx, types.Minter{
		Inflation:        math.LegacyNewDecWithPrec(10, 2),
		AnnualProvisions: math.LegacyZeroDec(),
	}))

	supply := math.NewInt(1_000_000_000_000)
	s.stakingKeeper.EXPECT().StakingTokenSupply(gomock.Any()).Return(supply, nil)
	s.stakingKeeper.EXPECT().BondedRatio(gomock.Any()).Return(math.LegacyNewDecWithPrec(67, 2), nil)

	// 10% of the supply per year, spread over blocksPerYear and truncated.
	blocksPerYear := uint64(60 * 60 * 8766 / 5)
	annualProvisions := math.NewInt(100_000_000_000)
	minted := sdk.NewCoins(sdk.NewCoin("stake", annualProvisions.Quo(math.NewIntFromUint64(blocksPerYear))))
	s.bankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, minted).Return(nil)
	s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), types.ModuleName, authtypes.FeeCollectorName, minted).Return(nil)

	s.Require().NoError(s.mintKeeper.MintFn(s.ctx))

	minter, err := s.mintKeeper.Minter.Get(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(math.LegacyNewDecFromInt(annualProvisions), minter.AnnualProvisions)
	s.Require().Equal(math.LegacyNewDecWithPrec(10, 2), minter.Inflation)
}
