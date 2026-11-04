package keeper_test

import (
	"time"

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

func (s *IntegrationTestSuite) TestDefaultMintFn() {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t0Plus100s := t0.Add(100 * time.Second)
	stakingSupply := math.NewInt(315_576_000_000_000)
	goalBonded := math.LegacyNewDecWithPrec(67, 2)
	inflation := math.LegacyNewDecWithPrec(10, 2)

	// With the bonded ratio at half the goal, inflation moves up by 6.5% per
	// year, and a block whose elapsed time exceeds the cap moves it by the step
	// for the capped time only.
	cappedNs := types.DefaultMaxBlockElapsed.Nanoseconds()
	cappedInflation := inflation.Add(math.LegacyNewDecWithPrec(65, 3).MulInt64(cappedNs).QuoInt64(types.NanosecondsPerYear))
	cappedAnnualProvisions := cappedInflation.MulInt(stakingSupply)
	cappedMinted := cappedAnnualProvisions.MulInt64(cappedNs).QuoInt64(types.NanosecondsPerYear).TruncateInt()
	dur := func(d time.Duration) *time.Duration { return &d }

	tests := []struct {
		name            string
		previous        *time.Time
		blockTime       time.Time
		bondedRatio     math.LegacyDec
		maxBlockElapsed *time.Duration
		expErr          string
		expInflation    math.LegacyDec
		expMinted       math.Int
		expElapsed      string
		expEventAttrs   map[string]string
	}{
		{
			name:         "nil previous block time records the reference and mints nothing",
			previous:     nil,
			blockTime:    t0,
			bondedRatio:  goalBonded,
			expInflation: inflation,
			expMinted:    math.ZeroInt(),
		},
		{
			name:         "six seconds",
			previous:     &t0,
			blockTime:    t0.Add(6 * time.Second),
			bondedRatio:  goalBonded,
			expInflation: inflation,
			expMinted:    math.NewInt(6_000_000),
			expElapsed:   "6000000000",
			expEventAttrs: map[string]string{
				types.AttributeKeyBondedRatio:      "0.670000000000000000",
				types.AttributeKeyInflation:        "0.100000000000000000",
				types.AttributeKeyAnnualProvisions: "31557600000000.000000000000000000",
				sdk.AttributeKeyAmount:             "6000000",
				types.AttributeKeyElapsed:          "6000000000",
			},
		},
		{
			name:         "zero elapsed",
			previous:     &t0,
			blockTime:    t0,
			bondedRatio:  goalBonded,
			expInflation: inflation,
			expMinted:    math.ZeroInt(),
			expElapsed:   "0",
		},
		{
			name:         "elapsed above cap",
			previous:     &t0,
			blockTime:    t0.Add(10 * time.Minute),
			bondedRatio:  goalBonded,
			expInflation: inflation,
			expMinted:    math.NewInt(8_000_000),
			expElapsed:   "8000000000",
		},
		{
			name:         "elapsed above cap moves inflation by the capped step",
			previous:     &t0,
			blockTime:    t0.Add(10 * time.Minute),
			bondedRatio:  math.LegacyNewDecWithPrec(335, 3),
			expInflation: cappedInflation,
			expMinted:    cappedMinted,
			expElapsed:   "8000000000",
		},
		{
			name:            "cap from params",
			previous:        &t0,
			blockTime:       t0.Add(6 * time.Second),
			bondedRatio:     goalBonded,
			maxBlockElapsed: dur(3 * time.Second),
			expInflation:    inflation,
			expMinted:       math.NewInt(3_000_000),
			expElapsed:      "3000000000",
		},
		{
			name:            "zero max block elapsed is an error",
			previous:        &t0,
			blockTime:       t0.Add(6 * time.Second),
			bondedRatio:     goalBonded,
			maxBlockElapsed: dur(0),
			expErr:          "invalid max_block_elapsed parameter",
		},
		{
			name:        "block time before the reference is an error",
			previous:    &t0Plus100s,
			blockTime:   t0,
			bondedRatio: goalBonded,
			expErr:      "is before the previous block time",
		},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.maxBlockElapsed != nil {
				params, err := s.mintKeeper.Params.Get(s.ctx)
				s.Require().NoError(err)
				params.MaxBlockElapsed = *tc.maxBlockElapsed
				s.Require().NoError(s.mintKeeper.Params.Set(s.ctx, params))
			}
			s.Require().NoError(s.mintKeeper.Minter.Set(s.ctx, types.Minter{
				Inflation:         inflation,
				AnnualProvisions:  math.LegacyZeroDec(),
				PreviousBlockTime: tc.previous,
			}))

			s.stakingKeeper.EXPECT().StakingTokenSupply(gomock.Any()).Return(stakingSupply, nil).AnyTimes()
			s.stakingKeeper.EXPECT().BondedRatio(gomock.Any()).Return(tc.bondedRatio, nil).AnyTimes()

			// Without a reference point, and on an error, nothing is minted and
			// no bank call is made.
			if tc.previous != nil && tc.expErr == "" {
				minted := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, tc.expMinted))
				if tc.expMinted.IsPositive() {
					s.bankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, minted).Return(nil)
				}
				s.bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), types.ModuleName, authtypes.FeeCollectorName, minted).Return(nil)
			}

			ctx := s.ctx.WithBlockTime(tc.blockTime).WithEventManager(sdk.NewEventManager())
			err := s.mintKeeper.MintFn(ctx)

			var mintEvents []sdk.Event
			for _, event := range ctx.EventManager().Events() {
				if event.Type == types.EventTypeMint {
					mintEvents = append(mintEvents, event)
				}
			}
			minter, getErr := s.mintKeeper.Minter.Get(s.ctx)
			s.Require().NoError(getErr)

			if tc.expErr != "" {
				s.Require().ErrorContains(err, tc.expErr)
				s.Require().Empty(mintEvents)
				s.Require().NotNil(minter.PreviousBlockTime)
				s.Require().Equal(*tc.previous, *minter.PreviousBlockTime)
				s.Require().Equal(math.LegacyZeroDec(), minter.AnnualProvisions)
				return
			}
			s.Require().NoError(err)

			expAnnualProvisions := tc.expInflation.MulInt(stakingSupply)
			s.Require().NotNil(minter.PreviousBlockTime)
			s.Require().Equal(tc.blockTime, *minter.PreviousBlockTime)
			s.Require().Equal(tc.expInflation, minter.Inflation)
			s.Require().Equal(expAnnualProvisions, minter.AnnualProvisions)

			if tc.previous == nil {
				s.Require().Empty(mintEvents)
				return
			}

			s.Require().Len(mintEvents, 1)
			expAttributes := map[string]string{
				types.AttributeKeyBondedRatio:      tc.bondedRatio.String(),
				types.AttributeKeyInflation:        tc.expInflation.String(),
				types.AttributeKeyAnnualProvisions: expAnnualProvisions.String(),
				sdk.AttributeKeyAmount:             tc.expMinted.String(),
				types.AttributeKeyElapsed:          tc.expElapsed,
			}
			s.Require().Len(mintEvents[0].Attributes, len(expAttributes))
			for key, value := range expAttributes {
				attr, ok := mintEvents[0].GetAttribute(key)
				s.Require().True(ok, key)
				s.Require().Equal(value, attr.Value, key)
			}
			for key, value := range tc.expEventAttrs {
				attr, ok := mintEvents[0].GetAttribute(key)
				s.Require().True(ok, key)
				s.Require().Equal(value, attr.Value, key)
			}
		})
	}
}
