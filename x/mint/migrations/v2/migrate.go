package v2

import (
	"errors"
	"fmt"
	"strings"

	storetypes "cosmossdk.io/core/store"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/mint/exported"
	"github.com/cosmos/cosmos-sdk/x/mint/types"
)

const (
	ModuleName = "mint"
)

var ParamsKey = []byte{0x01}

// Migrate migrates the x/mint module state from the consensus version 1 to
// version 2. Specifically, it takes the parameters that are currently stored
// and managed by the x/params modules and stores them directly into the x/mint
// module state.
func Migrate(
	ctx sdk.Context,
	store storetypes.KVStore,
	legacySubspace exported.Subspace,
	cdc codec.BinaryCodec,
) error {
	var currParams types.Params
	legacySubspace.GetParamSet(ctx, &currParams)

	if err := validateParams(currParams); err != nil {
		return err
	}

	bz := cdc.MustMarshal(&currParams)
	return store.Set(ParamsKey, bz)
}

// validateParams validates the params read from x/params against the rules of
// consensus version 2. It does not use types.Params.Validate, which also
// requires the fields added by later versions, such as max_block_elapsed, that
// the x/params key table does not carry.
func validateParams(p types.Params) error {
	if strings.TrimSpace(p.MintDenom) == "" {
		return errors.New("mint denom cannot be blank")
	}
	if err := sdk.ValidateDenom(p.MintDenom); err != nil {
		return err
	}
	if err := validateFraction("inflation rate change", p.InflationRateChange); err != nil {
		return err
	}
	if err := validateFraction("max inflation", p.InflationMax); err != nil {
		return err
	}
	if err := validateFraction("min inflation", p.InflationMin); err != nil {
		return err
	}
	if err := validateFraction("goal bonded", p.GoalBonded); err != nil {
		return err
	}
	if p.GoalBonded.IsZero() {
		return fmt.Errorf("goal bonded must be positive: %s", p.GoalBonded)
	}
	if p.InflationMax.LT(p.InflationMin) {
		return fmt.Errorf(
			"max inflation (%s) must be greater than or equal to min inflation (%s)",
			p.InflationMax, p.InflationMin,
		)
	}

	return nil
}

// validateFraction checks that v is set and within [0, 1].
func validateFraction(name string, v math.LegacyDec) error {
	if v.IsNil() {
		return fmt.Errorf("%s cannot be nil: %s", name, v)
	}
	if v.IsNegative() {
		return fmt.Errorf("%s cannot be negative: %s", name, v)
	}
	if v.GT(math.LegacyOneDec()) {
		return fmt.Errorf("%s too large: %s", name, v)
	}

	return nil
}
