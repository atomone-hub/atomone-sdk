package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// InitGenesis initializes default parameters and the keeper's address to
// pubkey map.
func (keeper Keeper) InitGenesis(ctx sdk.Context, stakingKeeper types.StakingKeeper, data *types.GenesisState) error {
	err := stakingKeeper.IterateValidators(ctx, func(_ int64, validator stakingtypes.ValidatorI) bool {
		consPk, err := validator.ConsPubKey()
		if err != nil {
			panic(err)
		}

		if err := keeper.AddPubkey(ctx, consPk); err != nil {
			panic(err)
		}
		return false
	})
	if err != nil {
		return err
	}

	for _, info := range data.SigningInfos {
		address, err := keeper.sk.ConsensusAddressCodec().StringToBytes(info.Address)
		if err != nil {
			return err
		}
		if err := keeper.SetValidatorSigningInfo(ctx, address, info.ValidatorSigningInfo); err != nil {
			return err
		}
	}

	for _, array := range data.MissedBlocks {
		address, err := keeper.sk.ConsensusAddressCodec().StringToBytes(array.Address)
		if err != nil {
			return err
		}

		for _, missed := range array.MissedBlocks {
			if err := keeper.SetMissedBlockBitmapValue(ctx, address, missed.Index, missed.Missed); err != nil {
				return err
			}
		}
	}

	return keeper.SetParams(ctx, data.Params)
}

// bitmapExportGroup tracks the signing-info records sharing one physical
// missed-block bitmap: the validator's current consensus address plus the
// retained records for rotated-away keys. Only one record's genesis entry
// carries the bitmap, so tooling summing missed blocks across genesis entries
// does not double-count downtime per historical rotation.
type bitmapExportGroup struct {
	holder    sdk.ConsAddress // address the bitmap is keyed under
	first     sdk.ConsAddress // first record seen for this group (fallback)
	live      sdk.ConsAddress // record matching the validator's current key
	hasHolder bool            // whether the holder itself has a record
}

// exportAddr returns the record whose genesis entry carries the bitmap. The
// live record wins: after re-import (which does not restore the rotation
// maps) liveness tracking reads the bitmap under the validator's current
// consensus address. Without a live record, the holder carries it.
func (g *bitmapExportGroup) exportAddr() sdk.ConsAddress {
	switch {
	case g.live != nil:
		return g.live
	case g.hasHolder:
		return g.holder
	default:
		return g.first
	}
}

// ExportGenesis writes the current store values
// to a genesis file, which can be imported again
// with InitGenesis
func (keeper Keeper) ExportGenesis(ctx sdk.Context) (data *types.GenesisState) {
	params, err := keeper.GetParams(ctx)
	if err != nil {
		panic(err)
	}

	signingInfos := make([]types.SigningInfo, 0)
	missedBlocks := make([]types.ValidatorMissedBlocks, 0)

	groups := make(map[string]*bitmapExportGroup)  // keyed by bitmap holder
	groupOf := make(map[string]*bitmapExportGroup) // record address -> its group
	addresses := make([]sdk.ConsAddress, 0)        // store order, keeps export deterministic

	keeper.IterateValidatorSigningInfos(ctx, func(address sdk.ConsAddress, info types.ValidatorSigningInfo) (stop bool) {
		signingInfos = append(signingInfos, types.SigningInfo{
			Address:              address.String(),
			ValidatorSigningInfo: info,
		})

		group, err := keeper.bitmapGroupFor(ctx, groups, address)
		if err != nil {
			panic(err)
		}
		groupOf[string(address)] = group
		addresses = append(addresses, address)
		return false
	})

	for _, address := range addresses {
		missedBlocks = append(missedBlocks, keeper.missedBlocksEntry(ctx, address, groupOf[string(address)]))
	}

	return types.NewGenesisState(params, signingInfos, missedBlocks)
}

// bitmapGroupFor returns the export group for a signing-info record address,
// registering it if needed. Records that resolve, via the rotation identifier
// map, to the same bitmap holder share one group.
func (keeper Keeper) bitmapGroupFor(ctx context.Context, groups map[string]*bitmapExportGroup, address sdk.ConsAddress) (*bitmapExportGroup, error) {
	holder, err := keeper.getPreviousConsKey(ctx, address)
	if err != nil {
		return nil, err
	}

	group, ok := groups[string(holder)]
	if !ok {
		group = &bitmapExportGroup{holder: holder, first: address}
		groups[string(holder)] = group
	}
	if address.Equals(holder) {
		group.hasHolder = true
	}
	if keeper.isCurrentValidatorConsAddr(ctx, address) {
		group.live = address
	}

	return group, nil
}

// missedBlocksEntry builds the genesis missed-blocks entry for one record: the
// shared bitmap data when this record's entry carries it, otherwise empty.
func (keeper Keeper) missedBlocksEntry(ctx context.Context, address sdk.ConsAddress, group *bitmapExportGroup) types.ValidatorMissedBlocks {
	entry := types.ValidatorMissedBlocks{
		Address:      address.String(),
		MissedBlocks: []types.MissedBlock{},
	}
	if !address.Equals(group.exportAddr()) {
		return entry
	}

	localMissedBlocks, err := keeper.GetValidatorMissedBlocks(ctx, address)
	if err != nil {
		panic(err)
	}
	entry.MissedBlocks = localMissedBlocks
	return entry
}
