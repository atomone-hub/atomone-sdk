package distribution

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/suite"

	"cosmossdk.io/math"
	"cosmossdk.io/simapp"

	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	"github.com/cosmos/cosmos-sdk/testutil/network"
	sdk "github.com/cosmos/cosmos-sdk/types"
	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/cosmos/cosmos-sdk/types/query"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
)

type GRPCQueryTestSuite struct {
	suite.Suite

	cfg     network.Config
	network *network.Network
}

func (s *GRPCQueryTestSuite) SetupSuite() {
	s.T().Log("setting up e2e test suite")

	cfg := network.DefaultConfig(simapp.NewTestNetworkFixture)
	cfg.NumValidators = 1
	s.cfg = cfg

	// Seed fee_collector with non-bond denom so non-bond rewards appear in F1.
	// These are swept (along with minted bond denom) on the first allocation at block 2.
	genesisState := s.cfg.GenesisState
	var bankData banktypes.GenesisState
	s.Require().NoError(s.cfg.Codec.UnmarshalJSON(genesisState[banktypes.ModuleName], &bankData))

	feeCollectorAddr := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	photonCoins := sdk.NewCoins(sdk.NewCoin("photon", math.NewInt(1000)))
	bankData.Balances = append(bankData.Balances, banktypes.Balance{
		Address: feeCollectorAddr.String(),
		Coins:   photonCoins,
	})

	bankDataBz, err := s.cfg.Codec.MarshalJSON(&bankData)
	s.Require().NoError(err)
	genesisState[banktypes.ModuleName] = bankDataBz
	s.cfg.GenesisState = genesisState

	s.network, err = network.New(s.T(), s.T().TempDir(), s.cfg)
	s.Require().NoError(err)

	s.Require().NoError(s.network.WaitForNextBlock())
}

// TearDownSuite cleans up the curret test network after _each_ test.
func (s *GRPCQueryTestSuite) TearDownSuite() {
	s.T().Log("tearing down e2e test suite1")
	s.network.Cleanup()
}

func (s *GRPCQueryTestSuite) TestQueryParamsGRPC() {
	val := s.network.Validators[0]
	baseURL := val.APIAddress

	testCases := []struct {
		name     string
		url      string
		respType proto.Message
		expected proto.Message
	}{
		{
			"gRPC request params",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/params", baseURL),
			&types.QueryParamsResponse{},
			&types.QueryParamsResponse{
				Params: types.DefaultParams(),
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		resp, err := sdktestutil.GetRequest(tc.url)
		s.Run(tc.name, func() {
			s.Require().NoError(err)
			s.Require().NoError(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
			s.Require().Equal(tc.expected, tc.respType)
		})
	}
}

func (s *GRPCQueryTestSuite) TestQueryValidatorDistributionInfoGRPC() {
	val := s.network.Validators[0]
	baseURL := val.APIAddress

	testCases := []struct {
		name     string
		url      string
		expErr   bool
		respType proto.Message
	}{
		{
			"gRPC request with wrong validator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s", baseURL, "wrongAddress"),
			true,
			&types.QueryValidatorDistributionInfoResponse{},
		},
		{
			"gRPC request with valid validator address ",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s", baseURL, val.ValAddress.String()),
			false,
			&types.QueryValidatorDistributionInfoResponse{},
		},
	}

	for _, tc := range testCases {
		tc := tc
		resp, err := sdktestutil.GetRequest(tc.url)
		s.Run(tc.name, func() {
			if tc.expErr {
				s.Require().Error(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
			} else {
				s.Require().NoError(err)
				s.Require().NoError(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
			}
		})
	}
}

func (s *GRPCQueryTestSuite) TestQueryOutstandingRewardsGRPC() {
	val := s.network.Validators[0]
	baseURL := val.APIAddress

	// Bond denom delegator rewards are auto-staked. Outstanding = commission only: 9.8stake.
	// Non-bond photon flows through F1 normally:
	// 1000photon genesis fee_collector -> 980photon outstanding (490 commission + 490 delegator).
	rewards, err := sdk.ParseDecCoins("980photon,9.8stake")
	s.Require().NoError(err)

	testCases := []struct {
		name     string
		url      string
		headers  map[string]string
		expErr   bool
		respType proto.Message
		expected proto.Message
	}{
		{
			"gRPC request params with wrong validator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s/outstanding_rewards", baseURL, "wrongAddress"),
			map[string]string{},
			true,
			&types.QueryValidatorOutstandingRewardsResponse{},
			&types.QueryValidatorOutstandingRewardsResponse{},
		},
		{
			"gRPC request params valid address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s/outstanding_rewards", baseURL, val.ValAddress.String()),
			map[string]string{
				grpctypes.GRPCBlockHeightHeader: "2",
			},
			false,
			&types.QueryValidatorOutstandingRewardsResponse{},
			&types.QueryValidatorOutstandingRewardsResponse{
				Rewards: types.ValidatorOutstandingRewards{
					Rewards: rewards,
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		resp, err := sdktestutil.GetRequestWithHeaders(tc.url, tc.headers)
		s.Run(tc.name, func() {
			if tc.expErr {
				s.Require().Error(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
			} else {
				s.Require().NoError(err)
				s.Require().NoError(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
				s.Require().Equal(tc.expected.String(), tc.respType.String())
			}
		})
	}
}

func (s *GRPCQueryTestSuite) TestQueryValidatorCommissionGRPC() {
	val := s.network.Validators[0]
	baseURL := val.APIAddress

	commission, err := sdk.ParseDecCoins("490photon,9.8stake")
	s.Require().NoError(err)

	testCases := []struct {
		name     string
		url      string
		headers  map[string]string
		expErr   bool
		respType proto.Message
		expected proto.Message
	}{
		{
			"gRPC request params with wrong validator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s/commission", baseURL, "wrongAddress"),
			map[string]string{},
			true,
			&types.QueryValidatorCommissionResponse{},
			&types.QueryValidatorCommissionResponse{},
		},
		{
			"gRPC request params valid address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s/commission", baseURL, val.ValAddress.String()),
			map[string]string{
				grpctypes.GRPCBlockHeightHeader: "2",
			},
			false,
			&types.QueryValidatorCommissionResponse{},
			&types.QueryValidatorCommissionResponse{
				Commission: types.ValidatorAccumulatedCommission{
					Commission: commission,
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		resp, err := sdktestutil.GetRequestWithHeaders(tc.url, tc.headers)
		s.Run(tc.name, func() {
			if tc.expErr {
				s.Require().Error(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
			} else {
				s.Require().NoError(err)
				s.Require().NoError(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
				s.Require().Equal(tc.expected.String(), tc.respType.String())
			}
		})
	}
}

func (s *GRPCQueryTestSuite) TestQuerySlashesGRPC() {
	val := s.network.Validators[0]
	baseURL := val.APIAddress

	testCases := []struct {
		name     string
		url      string
		expErr   bool
		respType proto.Message
		expected proto.Message
	}{
		{
			"invalid validator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s/slashes", baseURL, ""),
			true,
			&types.QueryValidatorSlashesResponse{},
			nil,
		},
		{
			"invalid start height",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s/slashes?starting_height=%s&ending_height=%s", baseURL, val.ValAddress.String(), "-1", "3"),
			true,
			&types.QueryValidatorSlashesResponse{},
			nil,
		},
		{
			"invalid start height",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s/slashes?starting_height=%s&ending_height=%s", baseURL, val.ValAddress.String(), "1", "-3"),
			true,
			&types.QueryValidatorSlashesResponse{},
			nil,
		},
		{
			"valid request get slashes",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/validators/%s/slashes?starting_height=%s&ending_height=%s", baseURL, val.ValAddress.String(), "1", "3"),
			false,
			&types.QueryValidatorSlashesResponse{},
			&types.QueryValidatorSlashesResponse{
				Pagination: &query.PageResponse{},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		resp, err := sdktestutil.GetRequest(tc.url)

		s.Run(tc.name, func() {
			if tc.expErr {
				s.Require().Error(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
			} else {
				s.Require().NoError(err)
				s.Require().NoError(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
				s.Require().Equal(tc.expected.String(), tc.respType.String())
			}
		})
	}
}

func (s *GRPCQueryTestSuite) TestQueryDelegatorRewardsGRPC() {
	val := s.network.Validators[0]
	baseURL := val.APIAddress

	// Bond denom delegator rewards are auto-staked; only non-bond photon is claimable.
	// 1000photon genesis fee_collector -> 490photon delegator share after community tax
	// and 50% commission split.
	rewards, err := sdk.ParseDecCoins("490photon")
	s.Require().NoError(err)

	testCases := []struct {
		name     string
		url      string
		headers  map[string]string
		expErr   bool
		respType proto.Message
		expected proto.Message
	}{
		{
			"wrong delegator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/rewards", baseURL, "wrongDelegatorAddress"),
			map[string]string{},
			true,
			&types.QueryDelegationTotalRewardsResponse{},
			nil,
		},
		{
			"valid request",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/rewards", baseURL, val.Address.String()),
			map[string]string{
				grpctypes.GRPCBlockHeightHeader: "2",
			},
			false,
			&types.QueryDelegationTotalRewardsResponse{},
			&types.QueryDelegationTotalRewardsResponse{
				Rewards: []types.DelegationDelegatorReward{
					types.NewDelegationDelegatorReward(val.ValAddress.String(), rewards),
				},
				Total: rewards,
			},
		},
		{
			"wrong validator address(specific validator rewards)",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/rewards/%s", baseURL, val.Address.String(), "wrongValAddress"),
			map[string]string{},
			true,
			&types.QueryDelegationTotalRewardsResponse{},
			nil,
		},
		{
			"valid request(specific validator rewards)",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/rewards/%s", baseURL, val.Address.String(), val.ValAddress.String()),
			map[string]string{
				grpctypes.GRPCBlockHeightHeader: "2",
			},
			false,
			&types.QueryDelegationRewardsResponse{},
			&types.QueryDelegationRewardsResponse{
				Rewards: rewards,
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		resp, err := sdktestutil.GetRequestWithHeaders(tc.url, tc.headers)

		s.Run(tc.name, func() {
			if tc.expErr {
				s.Require().Error(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
			} else {
				s.Require().NoError(err)
				s.Require().NoError(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
				s.Require().Equal(tc.expected.String(), tc.respType.String())
			}
		})
	}
}

func (s *GRPCQueryTestSuite) TestQueryDelegatorValidatorsGRPC() {
	val := s.network.Validators[0]
	baseURL := val.APIAddress

	testCases := []struct {
		name     string
		url      string
		expErr   bool
		respType proto.Message
		expected proto.Message
	}{
		{
			"empty delegator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/validators", baseURL, ""),
			true,
			&types.QueryDelegatorValidatorsResponse{},
			nil,
		},
		{
			"wrong delegator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/validators", baseURL, "wrongDelegatorAddress"),
			true,
			&types.QueryDelegatorValidatorsResponse{},
			nil,
		},
		{
			"valid request",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/validators", baseURL, val.Address.String()),
			false,
			&types.QueryDelegatorValidatorsResponse{},
			&types.QueryDelegatorValidatorsResponse{
				Validators: []string{val.ValAddress.String()},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		resp, err := sdktestutil.GetRequest(tc.url)

		s.Run(tc.name, func() {
			if tc.expErr {
				s.Require().Error(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
			} else {
				s.Require().NoError(err)
				s.Require().NoError(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
				s.Require().Equal(tc.expected.String(), tc.respType.String())
			}
		})
	}
}

func (s *GRPCQueryTestSuite) TestQueryWithdrawAddressGRPC() {
	val := s.network.Validators[0]
	baseURL := val.APIAddress

	testCases := []struct {
		name     string
		url      string
		expErr   bool
		respType proto.Message
		expected proto.Message
	}{
		{
			"empty delegator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/withdraw_address", baseURL, ""),
			true,
			&types.QueryDelegatorWithdrawAddressResponse{},
			nil,
		},
		{
			"wrong delegator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/withdraw_address", baseURL, "wrongDelegatorAddress"),
			true,
			&types.QueryDelegatorWithdrawAddressResponse{},
			nil,
		},
		{
			"valid request",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/delegators/%s/withdraw_address", baseURL, val.Address.String()),
			false,
			&types.QueryDelegatorWithdrawAddressResponse{},
			&types.QueryDelegatorWithdrawAddressResponse{
				WithdrawAddress: val.Address.String(),
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		resp, err := sdktestutil.GetRequest(tc.url)

		s.Run(tc.name, func() {
			if tc.expErr {
				s.Require().Error(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
			} else {
				s.Require().NoError(err)
				s.Require().NoError(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
				s.Require().Equal(tc.expected.String(), tc.respType.String())
			}
		})
	}
}

func (s *GRPCQueryTestSuite) TestQueryValidatorCommunityPoolGRPC() {
	val := s.network.Validators[0]
	baseURL := val.APIAddress

	// Community pool receives: 2% community tax (0.4stake) + decimal truncation dust
	// from auto-staking the delegator bond denom (9.8stake -> 9 tokens staked, 0.8stake dust)
	// so in total 1.2stake. Plus the 2% community tax on 1000photon genesis fee = 20photon.
	communityPool, err := sdk.ParseDecCoins("20photon,1.2stake")
	s.Require().NoError(err)

	testCases := []struct {
		name     string
		url      string
		headers  map[string]string
		expErr   bool
		respType proto.Message
		expected proto.Message
	}{
		{
			"gRPC request params with wrong validator address",
			fmt.Sprintf("%s/cosmos/distribution/v1beta1/community_pool", baseURL),
			map[string]string{
				grpctypes.GRPCBlockHeightHeader: "2",
			},
			false,
			&types.QueryCommunityPoolResponse{},
			&types.QueryCommunityPoolResponse{
				Pool: communityPool,
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		resp, err := sdktestutil.GetRequestWithHeaders(tc.url, tc.headers)

		s.Run(tc.name, func() {
			if tc.expErr {
				s.Require().Error(err)
			} else {
				s.Require().NoError(err)
				s.Require().NoError(val.ClientCtx.Codec.UnmarshalJSON(resp, tc.respType))
				s.Require().Equal(tc.expected.String(), tc.respType.String())
			}
		})
	}
}
