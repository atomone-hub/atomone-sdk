package gov_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/collections"
	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

func TestImportExportQueues_ErrorUnconsistentState(t *testing.T) {
	suite := createTestSuite(t)
	app := suite.App
	ctx := app.BaseApp.NewContext(false)
	require.Panics(t, func() {
		gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, &v1.GenesisState{
			Deposits: v1.Deposits{
				{
					ProposalId: 1234,
					Depositor:  "me",
					Amount: sdk.Coins{
						sdk.NewCoin(
							"stake",
							sdkmath.NewInt(1234),
						),
					},
				},
			},
		})
	})
	gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, v1.DefaultGenesisState())
	genState, err := gov.ExportGenesis(ctx, suite.GovKeeper)
	require.NoError(t, err)

	// Compare core fields (LastMinDeposit and LastMinInitialDeposit are dynamic and set during InitGenesis)
	expected := v1.DefaultGenesisState()
	require.Equal(t, expected.StartingProposalId, genState.StartingProposalId)
	require.Equal(t, expected.Params, genState.Params)
	require.Equal(t, expected.Constitution, genState.Constitution)
	require.Equal(t, expected.ParticipationEma, genState.ParticipationEma)
	require.Equal(t, expected.ConstitutionAmendmentParticipationEma, genState.ConstitutionAmendmentParticipationEma)
	require.Equal(t, expected.LawParticipationEma, genState.LawParticipationEma)
	require.Empty(t, genState.Deposits)
	require.Empty(t, genState.Votes)
	require.Empty(t, genState.Proposals)

	// Verify that dynamic deposit values were initialized
	require.NotNil(t, genState.LastMinDeposit)
	require.NotNil(t, genState.LastMinInitialDeposit)
	require.NotEmpty(t, genState.LastMinDeposit.Value)
	require.NotEmpty(t, genState.LastMinInitialDeposit.Value)
}

// TestExportGenesis_PreservesMinDepositTimes verifies that the throttler state
// is exported as stored: the exported times are the ones of the last update, not
// the block time of the (possibly time-less) export context, and they survive an
// import.
func TestExportGenesis_PreservesMinDepositTimes(t *testing.T) {
	suite := createTestSuite(t)
	ctx := suite.App.BaseApp.NewContext(false)
	gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, v1.DefaultGenesisState())

	depositTime := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	initialDepositTime := depositTime.Add(time.Hour)
	require.NoError(t, suite.GovKeeper.LastMinDeposit.Set(ctx, v1.LastMinDeposit{
		Value: sdk.NewCoins(sdk.NewInt64Coin("stake", 618)),
		Time:  &depositTime,
	}))
	require.NoError(t, suite.GovKeeper.LastMinInitialDeposit.Set(ctx, v1.LastMinDeposit{
		Value: sdk.NewCoins(sdk.NewInt64Coin("stake", 185)),
		Time:  &initialDepositTime,
	}))

	// export at a block time different from the stored times
	exportCtx := ctx.WithBlockTime(depositTime.Add(48 * time.Hour))
	genState, err := gov.ExportGenesis(exportCtx, suite.GovKeeper)
	require.NoError(t, err)
	require.Equal(t, sdk.NewCoins(sdk.NewInt64Coin("stake", 618)), sdk.Coins(genState.LastMinDeposit.Value))
	require.Equal(t, sdk.NewCoins(sdk.NewInt64Coin("stake", 185)), sdk.Coins(genState.LastMinInitialDeposit.Value))
	require.Equal(t, depositTime, *genState.LastMinDeposit.Time)
	require.Equal(t, initialDepositTime, *genState.LastMinInitialDeposit.Time)

	// import into a clean state and export again
	suite2 := createTestSuite(t)
	ctx2 := suite2.App.BaseApp.NewContext(false)
	gov.InitGenesis(ctx2, suite2.AccountKeeper, suite2.BankKeeper, suite2.GovKeeper, genState)
	genState2, err := gov.ExportGenesis(ctx2.WithBlockTime(depositTime.Add(72*time.Hour)), suite2.GovKeeper)
	require.NoError(t, err)
	require.Equal(t, genState.LastMinDeposit, genState2.LastMinDeposit)
	require.Equal(t, genState.LastMinInitialDeposit, genState2.LastMinInitialDeposit)
}

// TestInitGenesis_GovernorDelegationToOtherGovernorPanics verifies that an
// active governor cannot delegate to a different governor.
func TestInitGenesis_GovernorDelegationToOtherGovernorPanics(t *testing.T) {
	suite := createTestSuite(t)
	app := suite.App
	ctx := app.BaseApp.NewContext(false)

	// Create two governors
	governor1PubKey := pubkeys[0]
	governor2PubKey := pubkeys[1]

	governor1AccAddr := sdk.AccAddress(governor1PubKey.Address())
	governor2AccAddr := sdk.AccAddress(governor2PubKey.Address())

	gov1Acc := suite.AccountKeeper.NewAccountWithAddress(ctx, governor1AccAddr)
	suite.AccountKeeper.SetAccount(ctx, gov1Acc)

	gov2Acc := suite.AccountKeeper.NewAccountWithAddress(ctx, governor2AccAddr)
	suite.AccountKeeper.SetAccount(ctx, gov2Acc)

	govAddr1 := types.GovernorAddress(governor1AccAddr)
	govAddr2 := types.GovernorAddress(governor2AccAddr)
	now := time.Now().UTC()

	governor1, err := v1.NewGovernor(govAddr1.String(), v1.NewGovernorDescription("test-gov-1", "", "", "", ""), now)
	require.NoError(t, err)
	governor2, err := v1.NewGovernor(govAddr2.String(), v1.NewGovernorDescription("test-gov-2", "", "", "", ""), now)
	require.NoError(t, err)

	defaultState := v1.DefaultGenesisState()
	defaultState.Params.MinGovernorSelfDelegation = "0"
	defaultState.Governors = []*v1.Governor{&governor1, &governor2}
	// Governor 1 attempts to delegate to Governor 2 — must panic
	defaultState.GovernanceDelegations = []*v1.GovernanceDelegation{
		{
			DelegatorAddress: governor1AccAddr.String(),
			GovernorAddress:  govAddr2.String(),
		},
	}

	require.Panics(t, func() {
		gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, defaultState)
	})
}

// TestInitGenesis_InactiveGovernorDelegationToOtherGovernor verifies that an
// inactive governor can delegate to a different governor.
func TestInitGenesis_InactiveGovernorDelegationToOtherGovernor(t *testing.T) {
	suite := createTestSuite(t)
	app := suite.App
	ctx := app.BaseApp.NewContext(false)

	governor1PubKey := pubkeys[0]
	governor2PubKey := pubkeys[1]

	governor1AccAddr := sdk.AccAddress(governor1PubKey.Address())
	governor2AccAddr := sdk.AccAddress(governor2PubKey.Address())

	gov1Acc := suite.AccountKeeper.NewAccountWithAddress(ctx, governor1AccAddr)
	suite.AccountKeeper.SetAccount(ctx, gov1Acc)

	gov2Acc := suite.AccountKeeper.NewAccountWithAddress(ctx, governor2AccAddr)
	suite.AccountKeeper.SetAccount(ctx, gov2Acc)

	govAddr1 := types.GovernorAddress(governor1AccAddr)
	govAddr2 := types.GovernorAddress(governor2AccAddr)
	now := time.Now().UTC()

	governor1, err := v1.NewGovernor(govAddr1.String(), v1.NewGovernorDescription("test-gov-1", "", "", "", ""), now)
	require.NoError(t, err)
	governor1.Status = v1.Inactive // make governor 1 inactive

	governor2, err := v1.NewGovernor(govAddr2.String(), v1.NewGovernorDescription("test-gov-2", "", "", "", ""), now)
	require.NoError(t, err)

	defaultState := v1.DefaultGenesisState()
	defaultState.Params.MinGovernorSelfDelegation = "0"
	defaultState.Governors = []*v1.Governor{&governor1, &governor2}
	// Inactive governor 1 delegates to active governor 2 — must not panic
	defaultState.GovernanceDelegations = []*v1.GovernanceDelegation{
		{
			DelegatorAddress: governor1AccAddr.String(),
			GovernorAddress:  govAddr2.String(),
		},
	}

	require.NotPanics(t, func() {
		gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, defaultState)
	})
}

// TestInitGenesis_NonGovernorDelegation verifies that a non-governor account
// can delegate to a governor.
func TestInitGenesis_NonGovernorDelegation(t *testing.T) {
	suite := createTestSuite(t)
	app := suite.App
	ctx := app.BaseApp.NewContext(false)

	// Create two accounts: one will be a governor, the other a plain delegator
	governorPubKey := pubkeys[0]
	delegatorPubKey := pubkeys[1]

	governorAccAddr := sdk.AccAddress(governorPubKey.Address())
	delegatorAccAddr := sdk.AccAddress(delegatorPubKey.Address())

	// Register both accounts in the auth module
	govAcc := suite.AccountKeeper.NewAccountWithAddress(ctx, governorAccAddr)
	suite.AccountKeeper.SetAccount(ctx, govAcc)

	delAcc := suite.AccountKeeper.NewAccountWithAddress(ctx, delegatorAccAddr)
	suite.AccountKeeper.SetAccount(ctx, delAcc)

	govAddr := types.GovernorAddress(governorAccAddr)
	now := time.Now().UTC()

	governor, err := v1.NewGovernor(govAddr.String(), v1.NewGovernorDescription("test-gov", "", "", "", ""), now)
	require.NoError(t, err)

	defaultState := v1.DefaultGenesisState()
	defaultState.Params.MinGovernorSelfDelegation = "0"
	defaultState.Governors = []*v1.Governor{&governor}
	defaultState.GovernanceDelegations = []*v1.GovernanceDelegation{
		{
			DelegatorAddress: delegatorAccAddr.String(),
			GovernorAddress:  govAddr.String(),
		},
	}

	require.NotPanics(t, func() {
		gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, defaultState)
	})
}

func TestExportGenesis_ReturnsGovernanceDelegationWalkError(t *testing.T) {
	suite := createTestSuite(t)
	ctx := suite.App.BaseApp.NewContext(false)

	gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, v1.DefaultGenesisState())

	governorAddr := types.GovernorAddress(pubkeys[0].Address())
	governor, err := v1.NewGovernor(governorAddr.String(), v1.GovernorDescription{}, ctx.BlockTime())
	require.NoError(t, err)
	require.NoError(t, suite.GovKeeper.Governors.Set(ctx, governor.GetAddress(), governor))

	corruptKey, err := collections.EncodeKeyWithPrefix(
		types.GovernanceDelegationsByGovernorKeyPrefix,
		suite.GovKeeper.GovernanceDelegationsByGovernor.KeyCodec(),
		collections.Join(governor.GetAddress(), sdk.AccAddress(pubkeys[1].Address())),
	)
	require.NoError(t, err)

	storeKey := suite.App.UnsafeFindStoreKey(types.StoreKey)
	ctx.KVStore(storeKey).Set(corruptKey, []byte{0x01})

	_, err = gov.ExportGenesis(ctx, suite.GovKeeper)
	require.Error(t, err)
}
