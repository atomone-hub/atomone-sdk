package types

import (
	"encoding/json"
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec"
)

// NewGenesisState returns a new genesis state for the module.
func NewGenesisState(
	params Params,
	state State,
) *GenesisState {
	return &GenesisState{
		Params: params,
		State:  state,
	}
}

// ValidateBasic performs basic validation of the genesis state data returning an
// error for any failed validation criteria.
func (gs *GenesisState) ValidateBasic() error {
	if err := gs.Params.ValidateBasic(); err != nil {
		return err
	}
	if err := gs.State.ValidateBasic(); err != nil {
		return err
	}
	// The sliding window in the state must have exactly one slot per block in
	// the parameter window. InitGenesis relies on this invariant, so enforce it
	// here too, otherwise a mismatched genesis passes `genesis validate` and
	// then halts every node during InitChain.
	if gs.Params.Window != uint64(len(gs.State.Window)) {
		return fmt.Errorf("params.window (%d) does not match state.window length (%d)",
			gs.Params.Window, len(gs.State.Window))
	}
	return nil
}

// GetGenesisStateFromAppState returns x/dynamicfee GenesisState given raw application
// genesis state.
func GetGenesisStateFromAppState(cdc codec.Codec, appState map[string]json.RawMessage) GenesisState {
	var gs GenesisState
	cdc.MustUnmarshalJSON(appState[ModuleName], &gs)
	return gs
}
