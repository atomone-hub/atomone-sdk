package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
)

// InitGenesis initializes new authz genesis
func (k Keeper) InitGenesis(ctx sdk.Context, data *authz.GenesisState) {
	now := ctx.BlockTime()
	for _, entry := range data.Authorization {
		// ignore expired authorizations. The skip condition must be the exact
		// complement of the invariant enforced by SaveGrant (authz.NewGrant
		// requires expiration.After(blockTime)), otherwise an entry whose
		// expiration equals the genesis block time reaches SaveGrant, fails
		// validation and panics during InitChain. Such a grant is unusable on
		// every subsequent block anyway, so treat it as expired.
		if entry.Expiration != nil && !entry.Expiration.After(now) {
			continue
		}

		grantee, err := k.authKeeper.AddressCodec().StringToBytes(entry.Grantee)
		if err != nil {
			panic(err)
		}
		granter, err := k.authKeeper.AddressCodec().StringToBytes(entry.Granter)
		if err != nil {
			panic(err)
		}

		a, ok := entry.Authorization.GetCachedValue().(authz.Authorization)
		if !ok {
			panic("expected authorization")
		}

		err = k.SaveGrant(ctx, grantee, granter, a, entry.Expiration)
		if err != nil {
			panic(err)
		}
	}
}

// ExportGenesis returns a GenesisState for a given context.
func (k Keeper) ExportGenesis(ctx sdk.Context) *authz.GenesisState {
	var entries []authz.GrantAuthorization
	k.IterateGrants(ctx, func(granter, grantee sdk.AccAddress, grant authz.Grant) bool {
		entries = append(entries, authz.GrantAuthorization{
			Granter:       granter.String(),
			Grantee:       grantee.String(),
			Expiration:    grant.Expiration,
			Authorization: grant.Authorization,
		})
		return false
	})

	return authz.NewGenesisState(entries)
}
