package keeper

import (
	"errors"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// ApplyConstitutionAmendment applies the amendment as a patch against the current constitution
// and returns the updated constitution. If the amendment cannot be applied cleanly, an error is returned.
func (keeper Keeper) ApplyConstitutionAmendment(ctx sdk.Context, amendment string) (updatedConstitution string, err error) {
	if amendment == "" {
		return "", types.ErrInvalidConstitutionAmendment.Wrap("amendment cannot be empty")
	}

	currentConstitution, err := keeper.Constitution.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return "", err
	}

	updatedConstitution, err = types.ApplyUnifiedDiff(currentConstitution, amendment)
	if err != nil {
		return "", types.ErrInvalidConstitutionAmendment.Wrapf("failed to apply amendment: %v", err)
	}

	// The constitution is re-read, re-applied and re-stored in full on every
	// amendment message, and this code runs in the gov EndBlocker where no
	// gas meter bounds the work. Reject growth past MaxConstitutionLength so
	// per-message cost and cumulative growth across proposals stay bounded;
	// shrinking amendments are always allowed so a constitution that is
	// already over the cap (e.g. from a genesis predating it) can still be
	// amended back under it.
	if len(updatedConstitution) > v1.MaxConstitutionLength && len(updatedConstitution) > len(currentConstitution) {
		return "", types.ErrInvalidConstitutionAmendment.Wrapf("resulting constitution exceeds maximum size of %d bytes", v1.MaxConstitutionLength)
	}

	return updatedConstitution, nil
}
