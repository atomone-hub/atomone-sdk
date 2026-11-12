package keeper_test

import (
	"google.golang.org/protobuf/encoding/protowire"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/mint/exported"
	"github.com/cosmos/cosmos-sdk/x/mint/keeper"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

// legacySubspace returns the params as x/params holds them, without
// max_block_elapsed.
type legacySubspace struct{}

func (legacySubspace) GetParamSet(_ sdk.Context, ps exported.ParamSet) {
	p := types.DefaultParams()
	p.MaxBlockElapsed = 0
	*ps.(*types.Params) = p
}

func (s *IntegrationTestSuite) TestMigrate1to3() {
	m := keeper.NewMigrator(s.mintKeeper, legacySubspace{})
	s.Require().NoError(m.Migrate1to2(s.ctx))
	s.Require().NoError(m.Migrate2to3(s.ctx))

	got, err := s.mintKeeper.Params.Get(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(types.DefaultParams(), got)
}

func (s *IntegrationTestSuite) TestMigrate2to3SetsMaxBlockElapsedAndDropsReservedField() {
	params := types.DefaultParams()
	bz, err := s.cdc.Marshal(&params)
	s.Require().NoError(err)

	// Params as stored by consensus version 2: no max_block_elapsed (field 7),
	// and the varint field 6 (blocks_per_year) present.
	v2 := params
	v2.MaxBlockElapsed = 0
	legacy, err := s.cdc.Marshal(&v2)
	s.Require().NoError(err)
	legacy = protowire.AppendTag(legacy, 6, protowire.VarintType)
	legacy = protowire.AppendVarint(legacy, 4_360_000)
	s.Require().NotEqual(bz, legacy)

	store := s.storeService.OpenKVStore(s.ctx)
	s.Require().NoError(store.Set(types.ParamsKey, legacy))

	got, err := s.mintKeeper.Params.Get(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(v2, got)

	s.Require().NoError(keeper.NewMigrator(s.mintKeeper, nil).Migrate2to3(s.ctx))

	raw, err := store.Get(types.ParamsKey)
	s.Require().NoError(err)
	s.Require().Equal(bz, raw)

	got, err = s.mintKeeper.Params.Get(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(params, got)
	s.Require().Equal(types.DefaultMaxBlockElapsed, got.MaxBlockElapsed)
}
