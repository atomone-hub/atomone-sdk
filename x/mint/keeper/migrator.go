package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/mint/exported"
	v2 "github.com/cosmos/cosmos-sdk/x/mint/migrations/v2"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

// Migrator is a struct for handling in-place state migrations.
type Migrator struct {
	keeper         Keeper
	legacySubspace exported.Subspace
}

// NewMigrator returns Migrator instance for the state migration.
func NewMigrator(k Keeper, ss exported.Subspace) Migrator {
	return Migrator{
		keeper:         k,
		legacySubspace: ss,
	}
}

// Migrate1to2 migrates the x/mint module state from the consensus version 1 to
// version 2. Specifically, it takes the parameters that are currently stored
// and managed by the x/params modules and stores them directly into the x/mint
// module state.
func (m Migrator) Migrate1to2(ctx sdk.Context) error {
	return v2.Migrate(ctx, m.keeper.storeService.OpenKVStore(ctx), m.legacySubspace, m.keeper.cdc)
}

// Migrate2to3 migrates the x/mint module state from the consensus version 2 to
// version 3. It sets max_block_elapsed to its default and re-stores the params,
// which also drops the bytes of the reserved field 6 from the stored encoding.
func (m Migrator) Migrate2to3(ctx sdk.Context) error {
	params, err := m.keeper.Params.Get(ctx)
	if err != nil {
		return err
	}
	if params.MaxBlockElapsed == 0 {
		params.MaxBlockElapsed = types.DefaultMaxBlockElapsed
	}
	if err := params.Validate(); err != nil {
		return err
	}

	return m.keeper.Params.Set(ctx, params)
}
