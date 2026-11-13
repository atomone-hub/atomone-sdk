package v2_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/mint"
	"github.com/cosmos/cosmos-sdk/x/mint/exported"
	v2 "github.com/cosmos/cosmos-sdk/x/mint/migrations/v2"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

type mockSubspace struct {
	ps types.Params
}

func newMockSubspace(ps types.Params) mockSubspace {
	return mockSubspace{ps: ps}
}

func (ms mockSubspace) GetParamSet(ctx sdk.Context, ps exported.ParamSet) {
	*ps.(*types.Params) = ms.ps
}

func TestMigrate(t *testing.T) {
	encCfg := moduletestutil.MakeTestEncodingConfig(mint.AppModuleBasic{})
	cdc := encCfg.Codec

	// The x/params key table has no max_block_elapsed entry, so the params read
	// from the legacy subspace carry it as zero.
	v1Params := types.DefaultParams()
	v1Params.MaxBlockElapsed = 0

	tests := []struct {
		name   string
		params func() types.Params
		expErr string
	}{
		{
			name:   "params from x/params",
			params: func() types.Params { return v1Params },
		},
		{
			name: "blank mint denom",
			params: func() types.Params {
				p := v1Params
				p.MintDenom = " "
				return p
			},
			expErr: "mint denom cannot be blank",
		},
		{
			name: "zero goal bonded",
			params: func() types.Params {
				p := v1Params
				p.GoalBonded = math.LegacyZeroDec()
				return p
			},
			expErr: "goal bonded must be positive",
		},
		{
			name: "max inflation below min inflation",
			params: func() types.Params {
				p := v1Params
				p.InflationMax = math.LegacyNewDecWithPrec(5, 2)
				return p
			},
			expErr: "max inflation (0.050000000000000000) must be greater than or equal to min inflation (0.070000000000000000)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storeKey := storetypes.NewKVStoreKey(v2.ModuleName)
			tKey := storetypes.NewTransientStoreKey("transient_test")
			ctx := testutil.DefaultContext(storeKey, tKey)
			kvStoreService := runtime.NewKVStoreService(storeKey)
			store := kvStoreService.OpenKVStore(ctx)

			legacySubspace := newMockSubspace(tc.params())
			err := v2.Migrate(ctx, store, legacySubspace, cdc)

			bz, getErr := store.Get(v2.ParamsKey)
			require.NoError(t, getErr)
			if tc.expErr != "" {
				require.ErrorContains(t, err, tc.expErr)
				require.Nil(t, bz)
				return
			}
			require.NoError(t, err)

			var res types.Params
			require.NoError(t, cdc.Unmarshal(bz, &res))
			require.Equal(t, legacySubspace.ps, res)
		})
	}
}
