package gov_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/gov"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// TestInitGenesis_AllowsStrayModuleAccountFunds reproduces a restart-from-export
// panic: the gov module account is allowed to receive external funds (it is
// excluded from BlockedModuleAccountAddrs on the chain), so a stray send leaves
// the module balance strictly larger than the sum of deposit records. The live
// chain tolerates this (ModuleAccountInvariant uses IsAllGTE), but InitGenesis
// used to demand exact equality and panic on every node started from such an
// exported genesis. InitGenesis must accept balance >= totalDeposits.
func TestInitGenesis_AllowsStrayModuleAccountFunds(t *testing.T) {
	suite := createTestSuite(t)
	ctx := suite.App.BaseApp.NewContext(false)

	govModuleAddr := authtypes.NewModuleAddress(govtypes.ModuleName)

	// pick a funded non-module account
	var from sdk.AccAddress
	suite.AccountKeeper.IterateAccounts(ctx, func(acc sdk.AccountI) (stop bool) {
		if _, isModule := acc.(*authtypes.ModuleAccount); isModule {
			return false
		}
		if bal := suite.BankKeeper.GetAllBalances(ctx, acc.GetAddress()); !bal.IsZero() {
			from = acc.GetAddress()
			return true
		}
		return false
	})
	require.NotEmpty(t, from)
	bal := suite.BankKeeper.GetAllBalances(ctx, from)
	require.False(t, bal.IsZero())

	// Stray external funds credited to the gov module account, with no
	// matching deposit record — the persistent state a bank.MsgSend to the
	// unblocked gov module account produces on the live chain.
	require.NoError(t, suite.BankKeeper.SendCoins(ctx, from, govModuleAddr, bal))
	require.False(t, suite.BankKeeper.GetAllBalances(ctx, govModuleAddr).IsZero())

	require.NotPanics(t, func() {
		gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, v1.DefaultGenesisState())
	})
}

// TestInitGenesis_StillPanicsOnMissingDepositFunds guards the other direction:
// a module balance that does NOT cover the imported deposits is genuine
// export/import corruption and must still abort.
func TestInitGenesis_StillPanicsOnMissingDepositFunds(t *testing.T) {
	suite := createTestSuite(t)
	ctx := suite.App.BaseApp.NewContext(false)

	require.Panics(t, func() {
		gov.InitGenesis(ctx, suite.AccountKeeper, suite.BankKeeper, suite.GovKeeper, &v1.GenesisState{
			Deposits: v1.Deposits{
				{
					ProposalId: 1234,
					Depositor:  "me",
					Amount: sdk.Coins{
						sdk.NewCoin("stake", sdkmath.NewInt(1234)),
					},
				},
			},
		})
	})
}
