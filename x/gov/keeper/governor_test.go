package keeper_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec/address"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	"github.com/cosmos/cosmos-sdk/x/gov"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestGovernor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	govKeeper, _, _, _, _, _, ctx := setupGovKeeper(t)
	addrs := simtestutil.CreateRandomAccounts(3)
	govAddrs := convertAddrsToGovAddrs(addrs)

	// Add 2 governors
	gov1Desc := v1.NewGovernorDescription("moniker1", "id1", "website1", "sec1", "detail1")
	gov1, err := v1.NewGovernor(govAddrs[0].String(), gov1Desc, time.Now().UTC())
	require.NoError(err)
	gov2Desc := v1.NewGovernorDescription("moniker2", "id2", "website2", "sec2", "detail2")
	gov2, err := v1.NewGovernor(govAddrs[1].String(), gov2Desc, time.Now().UTC())
	require.NoError(err)
	gov2.Status = v1.Inactive
	govKeeper.Governors.Set(ctx, gov1.GetAddress(), gov1)
	govKeeper.Governors.Set(ctx, gov2.GetAddress(), gov2)

	// Get gov1
	gov, err := govKeeper.Governors.Get(ctx, govAddrs[0])
	assert.NoError(err, "cant find gov1")
	assert.Equal(gov1, gov)

	// Get gov2
	gov, err = govKeeper.Governors.Get(ctx, govAddrs[1])
	assert.NoError(err, "cant find gov2")
	assert.Equal(gov2, gov)

	// Get all govs
	var govs []*v1.Governor
	err = govKeeper.Governors.Walk(ctx, nil, func(_ types.GovernorAddress, gov v1.Governor) (stop bool, err error) {
		govs = append(govs, &gov)
		return false, nil
	})
	require.NoError(err)
	if assert.Len(govs, 2, "expected 2 governors") {
		// Insert order is not preserved, order is related to the address which is
		// generated randomly, so the order of govs is random.
		for i := 0; i < 2; i++ {
			switch govs[i].GetAddress().String() {
			case gov1.GetAddress().String():
				assert.Equal(gov1, *govs[i])
			case gov2.GetAddress().String():
				assert.Equal(gov2, *govs[i])
			}
		}
	}

	// Get all active govs
	govs = nil
	err = govKeeper.Governors.Walk(ctx, nil, func(_ types.GovernorAddress, gov v1.Governor) (stop bool, err error) {
		if gov.IsActive() {
			govs = append(govs, &gov)
		}
		return false, nil
	})
	require.NoError(err)
	if assert.Len(govs, 1, "expected 1 active governor") {
		assert.Equal(gov1, *govs[0])
	}

	// Remove gov2
	err = govKeeper.Governors.Remove(ctx, govAddrs[1])
	require.NoError(err)
	_, err = govKeeper.Governors.Get(ctx, govAddrs[1])
	assert.ErrorIs(err, collections.ErrNotFound, "expected gov2 to be removed")

	// Get all govs after removal
	govs = nil
	err = govKeeper.Governors.Walk(ctx, nil, func(_ types.GovernorAddress, gov v1.Governor) (stop bool, err error) {
		govs = append(govs, &gov)
		return false, nil
	})
	require.NoError(err)
	if assert.Len(govs, 1, "expected 1 governor after removal") {
		assert.Equal(gov1, *govs[0])
	}
}

func TestEditGovernorDescriptionMerge(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	govKeeper, _, _, _, _, _, ctx := setupGovKeeper(t)

	// Create a governor with all description fields
	govAddr := convertAddrsToGovAddrs(simtestutil.CreateRandomAccounts(1))[0]
	govDesc := v1.NewGovernorDescription(
		"original-moniker",
		"original-identity",
		"original-website",
		"original-security-contact",
		"original-details",
	)
	gov, err := v1.NewGovernor(govAddr.String(), govDesc, time.Now().UTC())
	require.NoError(err)
	err = govKeeper.Governors.Set(ctx, govAddr, gov)
	require.NoError(err)

	// Edit only the moniker, preserving other fields using DoNotModifyDesc
	editedDesc := v1.NewGovernorDescription(
		"updated-moniker",
		stakingtypes.DoNotModifyDesc,
		stakingtypes.DoNotModifyDesc,
		stakingtypes.DoNotModifyDesc,
		stakingtypes.DoNotModifyDesc,
	)

	// Use the UpdateDescription method directly (which EditGovernor should call)
	updatedDesc, err := gov.Description.UpdateDescription(editedDesc)
	require.NoError(err)

	// Verify that only moniker changed
	assert.Equal("updated-moniker", updatedDesc.Moniker)
	assert.Equal("original-identity", updatedDesc.Identity)
	assert.Equal("original-website", updatedDesc.Website)
	assert.Equal("original-security-contact", updatedDesc.SecurityContact)
	assert.Equal("original-details", updatedDesc.Details)

	// Update the governor
	gov.Description = updatedDesc
	err = govKeeper.Governors.Set(ctx, govAddr, gov)
	require.NoError(err)

	// Retrieve and verify
	retrievedGov, err := govKeeper.Governors.Get(ctx, govAddr)
	require.NoError(err)
	assert.Equal("updated-moniker", retrievedGov.Description.Moniker)
	assert.Equal("original-identity", retrievedGov.Description.Identity)
	assert.Equal("original-website", retrievedGov.Description.Website)
	assert.Equal("original-security-contact", retrievedGov.Description.SecurityContact)
	assert.Equal("original-details", retrievedGov.Description.Details)
}

func TestValidateGovernorMinSelfDelegation(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(*fixture) (v1.Governor, []sdk.ValAddress)
		selfDelegation bool
		valDelegations []stakingtypes.Delegation
		expectedPanic  bool
		expectedValid  bool
	}{
		{
			name: "inactive governor",
			setup: func(s *fixture) (v1.Governor, []sdk.ValAddress) {
				return s.inactiveGovernor, nil
			},
			expectedPanic: false,
			expectedValid: false,
		},
		{
			name: "active governor w/o self delegation w/o validator delegation",
			setup: func(s *fixture) (v1.Governor, []sdk.ValAddress) {
				return s.activeGovernors[0], nil
			},
			expectedPanic: true,
			expectedValid: false,
		},
		{
			name: "active governor w self delegation w/o validator delegation",
			setup: func(s *fixture) (v1.Governor, []sdk.ValAddress) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				err := s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr)
				require.NoError(s.t, err)
				return s.activeGovernors[0], nil
			},
			expectedPanic: false,
			expectedValid: false,
		},
		{
			name: "active governor w self delegation w not enough validator delegation",
			setup: func(s *fixture) (v1.Governor, []sdk.ValAddress) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				err := s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr)
				require.NoError(s.t, err)
				s.delegate(delAddr, s.valAddrs[0], 1)
				return s.activeGovernors[0], nil
			},
			expectedPanic: false,
			expectedValid: false,
		},
		{
			name: "active governor w self delegation w enough validator delegation",
			setup: func(s *fixture) (v1.Governor, []sdk.ValAddress) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				err := s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr)
				require.NoError(s.t, err)
				s.delegate(delAddr, s.valAddrs[0], v1.DefaultMinGovernorSelfDelegation.Int64())
				return s.activeGovernors[0], nil
			},
			expectedPanic: false,
			expectedValid: true,
		},
		{
			name: "active governor meets threshold by sum across two validators, excluding one drops below",
			setup: func(s *fixture) (v1.Governor, []sdk.ValAddress) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				require.NoError(s.t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				half := v1.DefaultMinGovernorSelfDelegation.QuoRaw(2).Int64()
				s.delegate(delAddr, s.valAddrs[0], half)
				s.delegate(delAddr, s.valAddrs[1], half)
				return s.activeGovernors[0], []sdk.ValAddress{s.valAddrs[0]}
			},
			expectedPanic: false,
			expectedValid: false,
		},
		{
			name: "active governor meets threshold, excluding a validator they don't delegate to is a no-op",
			setup: func(s *fixture) (v1.Governor, []sdk.ValAddress) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				require.NoError(s.t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				s.delegate(delAddr, s.valAddrs[0], v1.DefaultMinGovernorSelfDelegation.Int64())
				return s.activeGovernors[0], []sdk.ValAddress{s.valAddrs[1]}
			},
			expectedPanic: false,
			expectedValid: true,
		},
		{
			name: "active governor meets threshold by remaining delegation alone, excluding the smaller one keeps active",
			setup: func(s *fixture) (v1.Governor, []sdk.ValAddress) {
				govAddr := s.activeGovernors[0].GetAddress()
				delAddr := sdk.AccAddress(govAddr)
				require.NoError(s.t, s.keeper.DelegateToGovernor(s.ctx, delAddr, govAddr))
				s.delegate(delAddr, s.valAddrs[0], 1)
				s.delegate(delAddr, s.valAddrs[1], v1.DefaultMinGovernorSelfDelegation.Int64())
				return s.activeGovernors[0], []sdk.ValAddress{s.valAddrs[0]}
			},
			expectedPanic: false,
			expectedValid: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			govKeeper, accKeeper, bankKeeper, stakingKeeper, distrKeeper, _, ctx := setupGovKeeper(t, mockAccountKeeperExpectations)
			mocks := mocks{
				accKeeper:          accKeeper,
				bankKeeper:         bankKeeper,
				stakingKeeper:      stakingKeeper,
				distributionKeeper: distrKeeper,
			}
			s := newFixture(t, ctx, 2, 2, 2, govKeeper, mocks)
			governor, exclusions := tt.setup(s)

			excludeSet := map[string]struct{}{}
			for _, valAddr := range exclusions {
				excludeSet[valAddr.String()] = struct{}{}
			}

			if tt.expectedPanic {
				assert.Panics(t, func() {
					govKeeper.ValidateGovernorMinSelfDelegationWithExclusions(ctx, governor, excludeSet)
				})
			} else {
				valid := govKeeper.ValidateGovernorMinSelfDelegationWithExclusions(ctx, governor, excludeSet)

				assert.Equal(t, tt.expectedValid, valid, "return of ValidateGovernorMinSelfDelegationWithExclusions")
			}
		})
	}
}

// TestInitGenesis_GovernorSelfDelegationNotDoubled verifies that after
// an export/import cycle, active governor self-delegations are not double-counted.
func TestInitGenesis_GovernorSelfDelegationNotDoubled(t *testing.T) {
	// Create a validator address that will alsoe the governor
	valAddr := sdk.ValAddress([]byte("validator001"))
	accAddr := sdk.AccAddress(valAddr)
	govAddr := types.GovernorAddress(accAddr)

	// Set up custom mock expectations that properly handle IterateDelegations
	expectations := func(ctx sdk.Context, m mocks) {
		// Account keeper expectations
		m.accKeeper.EXPECT().GetModuleAddress(types.ModuleName).Return(govAcct).AnyTimes()
		m.accKeeper.EXPECT().GetModuleAddress(disttypes.ModuleName).Return(distAcct).AnyTimes()
		m.accKeeper.EXPECT().GetModuleAccount(gomock.Any(), types.ModuleName).Return(authtypes.NewEmptyModuleAccount(types.ModuleName)).AnyTimes()
		m.accKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()

		// Bank keeper expectations
		m.bankKeeper.EXPECT().GetAllBalances(gomock.Any(), gomock.Any()).Return(sdk.Coins{}).AnyTimes()

		// Account keeper expectations for InitGenesis
		m.accKeeper.EXPECT().SetModuleAccount(gomock.Any(), gomock.Any()).AnyTimes()

		// Staking keeper expectations
		m.stakingKeeper.EXPECT().IterateBondedValidatorsByPower(gomock.Any(), gomock.Any()).AnyTimes()
		m.stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return("stake", nil).AnyTimes()
		m.stakingKeeper.EXPECT().GetValidator(gomock.Any(), gomock.Any()).Return(stakingtypes.Validator{
			OperatorAddress: valAddr.String(),
			Status:          stakingtypes.Bonded,
			Tokens:          math.NewInt(1000000000),
			DelegatorShares: math.LegacyNewDec(1000000000),
		}, nil).AnyTimes()
		m.stakingKeeper.EXPECT().GetDelegation(gomock.Any(), gomock.Any(), gomock.Any()).Return(stakingtypes.Delegation{
			DelegatorAddress: accAddr.String(),
			ValidatorAddress: valAddr.String(),
			Shares:           math.LegacyNewDec(1000000000),
		}, nil).AnyTimes()

		// This is the key mock - IterateDelegations must call the callback to set up shares
		m.stakingKeeper.EXPECT().IterateDelegations(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, voter sdk.AccAddress, fn func(index int64, d stakingtypes.DelegationI) bool) error {
				// Return one delegation for the validator (1000000000 shares)
				if voter.String() == accAddr.String() {
					d := stakingtypes.Delegation{
						DelegatorAddress: voter.String(),
						ValidatorAddress: valAddr.String(),
						Shares:           math.LegacyNewDec(1000000000),
					}
					fn(0, d)
				}
				return nil
			}).AnyTimes()
	}

	govKeeper, accKeeper, bankKeeper, _, _, _, ctx := setupGovKeeper(t, expectations)

	// Set up account for the governor (needed for InitGenesis)
	accKeeper.EXPECT().GetAccount(gomock.Any(), gomock.Any()).Return(
		authtypes.NewBaseAccountWithAddress(accAddr),
	).AnyTimes()

	// Create an active governor
	governor, err := v1.NewGovernor(govAddr.String(), v1.GovernorDescription{}, time.Now())
	require.NoError(t, err)
	err = govKeeper.Governors.Set(ctx, governor.GetAddress(), governor)
	require.NoError(t, err)

	// Delegate the governor to itself (self-delegation)
	err = govKeeper.DelegateToGovernor(ctx, accAddr, governor.GetAddress())
	require.NoError(t, err)

	// Verify initial shares are 1000000000 (from the staking delegation)
	initialShares, err := govKeeper.ValidatorSharesByGovernor.Get(ctx, collections.Join(governor.GetAddress(), valAddr))
	require.NoError(t, err)
	require.Equal(t, math.LegacyNewDec(1000000000), initialShares.Shares, "initial shares should be 1000000000")

	// Export genesis
	exportedState, err := gov.ExportGenesis(ctx, govKeeper)
	require.NoError(t, err)

	// Verify the exported state contains the governor and self-delegation
	require.Len(t, exportedState.Governors, 1)
	require.Len(t, exportedState.GovernanceDelegations, 1)
	require.Equal(t, accAddr.String(), exportedState.GovernanceDelegations[0].DelegatorAddress)
	require.Equal(t, governor.GetAddress().String(), exportedState.GovernanceDelegations[0].GovernorAddress)

	// Clear the state to simulate a fresh import
	err = govKeeper.ValidatorSharesByGovernor.Remove(ctx, collections.Join(governor.GetAddress(), valAddr))
	require.NoError(t, err)
	govKeeper.RemoveGovernanceDelegation(ctx, accAddr)
	err = govKeeper.Governors.Remove(ctx, governor.GetAddress())
	require.NoError(t, err)

	// Re-import the genesis state
	gov.InitGenesis(ctx, accKeeper, bankKeeper, govKeeper, exportedState)

	// Verify the governor was restored
	restoredGovernor, err := govKeeper.Governors.Get(ctx, governor.GetAddress())
	require.NoError(t, err)
	require.True(t, restoredGovernor.IsActive())

	// Verify the validator shares are NOT doubled (the bug would cause them to be 200)
	finalShares, err := govKeeper.ValidatorSharesByGovernor.Get(ctx, collections.Join(governor.GetAddress(), valAddr))
	require.NoError(t, err)
	require.Equal(t, math.LegacyNewDec(1000000000), finalShares.Shares,
		"shares should still be 100 after export/import, not doubled to 200")
}

// TestInitGenesis_InactiveGovernorSelfDelegationIsProcessed verifies that
// self-delegations for INACTIVE governors are still processed in the second loop.
// This is important because inactive governors don't get DelegateToGovernor called
// in the first loop, so their delegations must be processed in the second loop.
func TestInitGenesis_InactiveGovernorSelfDelegationIsProcessed(t *testing.T) {
	// Create a validator address that will also be the governor
	valAddr := sdk.ValAddress([]byte("validator002"))
	accAddr := sdk.AccAddress(valAddr)
	govAddr := types.GovernorAddress(accAddr)

	// Set up custom mock expectations that properly handle IterateDelegations
	expectations := func(ctx sdk.Context, m mocks) {
		// Account keeper expectations
		m.accKeeper.EXPECT().GetModuleAddress(types.ModuleName).Return(govAcct).AnyTimes()
		m.accKeeper.EXPECT().GetModuleAddress(disttypes.ModuleName).Return(distAcct).AnyTimes()
		m.accKeeper.EXPECT().GetModuleAccount(gomock.Any(), types.ModuleName).Return(authtypes.NewEmptyModuleAccount(types.ModuleName)).AnyTimes()
		m.accKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()

		// Bank keeper expectations
		m.bankKeeper.EXPECT().GetAllBalances(gomock.Any(), gomock.Any()).Return(sdk.Coins{}).AnyTimes()

		// Account keeper expectations for InitGenesis
		m.accKeeper.EXPECT().SetModuleAccount(gomock.Any(), gomock.Any()).AnyTimes()

		// Staking keeper expectations
		m.stakingKeeper.EXPECT().IterateBondedValidatorsByPower(gomock.Any(), gomock.Any()).AnyTimes()
		m.stakingKeeper.EXPECT().BondDenom(gomock.Any()).Return("stake", nil).AnyTimes()
		m.stakingKeeper.EXPECT().GetValidator(gomock.Any(), gomock.Any()).Return(stakingtypes.Validator{
			OperatorAddress: valAddr.String(),
			Status:          stakingtypes.Bonded,
			Tokens:          math.NewInt(100),
			DelegatorShares: math.LegacyNewDec(100),
		}, nil).AnyTimes()
		m.stakingKeeper.EXPECT().GetDelegation(gomock.Any(), gomock.Any(), gomock.Any()).Return(stakingtypes.Delegation{
			DelegatorAddress: accAddr.String(),
			ValidatorAddress: valAddr.String(),
			Shares:           math.LegacyNewDec(100),
		}, nil).AnyTimes()

		// This is the key mock - IterateDelegations must call the callback to set up shares
		m.stakingKeeper.EXPECT().IterateDelegations(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, voter sdk.AccAddress, fn func(index int64, d stakingtypes.DelegationI) bool) error {
				// Return one delegation for the validator (100 shares)
				if voter.String() == accAddr.String() {
					d := stakingtypes.Delegation{
						DelegatorAddress: voter.String(),
						ValidatorAddress: valAddr.String(),
						Shares:           math.LegacyNewDec(100),
					}
					fn(0, d)
				}
				return nil
			}).AnyTimes()
	}

	govKeeper, accKeeper, bankKeeper, _, _, _, ctx := setupGovKeeper(t, expectations)

	// Set up account for the governor (needed for InitGenesis)
	accKeeper.EXPECT().GetAccount(gomock.Any(), gomock.Any()).Return(
		authtypes.NewBaseAccountWithAddress(accAddr),
	).AnyTimes()

	// Create an INACTIVE governor
	governor, err := v1.NewGovernor(govAddr.String(), v1.GovernorDescription{}, time.Now())
	require.NoError(t, err)
	governor.Status = v1.Inactive
	err = govKeeper.Governors.Set(ctx, governor.GetAddress(), governor)
	require.NoError(t, err)

	// Delegate the governor to itself (self-delegation)
	err = govKeeper.DelegateToGovernor(ctx, accAddr, governor.GetAddress())
	require.NoError(t, err)

	// Verify initial shares are 100
	initialShares, err := govKeeper.ValidatorSharesByGovernor.Get(ctx, collections.Join(governor.GetAddress(), valAddr))
	require.NoError(t, err)
	require.Equal(t, math.LegacyNewDec(100), initialShares.Shares, "initial shares should be 100")

	// Export genesis
	exportedState, err := gov.ExportGenesis(ctx, govKeeper)
	require.NoError(t, err)

	// Clear the state
	err = govKeeper.ValidatorSharesByGovernor.Remove(ctx, collections.Join(governor.GetAddress(), valAddr))
	require.NoError(t, err)
	govKeeper.RemoveGovernanceDelegation(ctx, accAddr)
	err = govKeeper.Governors.Remove(ctx, governor.GetAddress())
	require.NoError(t, err)

	// Re-import the genesis state
	gov.InitGenesis(ctx, accKeeper, bankKeeper, govKeeper, exportedState)

	// Verify the governor was restored (still inactive)
	restoredGovernor, err := govKeeper.Governors.Get(ctx, governor.GetAddress())
	require.NoError(t, err)
	require.False(t, restoredGovernor.IsActive())

	// Verify the shares are still 100 (not 0, and not 200)
	// For inactive governors, the self-delegation IS processed in the second loop
	finalShares, err := govKeeper.ValidatorSharesByGovernor.Get(ctx, collections.Join(governor.GetAddress(), valAddr))
	require.NoError(t, err)
	require.Equal(t, math.LegacyNewDec(100), finalShares.Shares,
		"shares should be 100 for inactive governor (self-delegation processed in second loop)")
}
