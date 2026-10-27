package types

import (
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// ValidateInputOutputs validates that each respective input and output is
// valid and that the sum of inputs is equal to the sum of outputs.
func ValidateInputOutputs(input Input, outputs []Output) error {
	if err := input.ValidateBasic(); err != nil {
		return err
	}
	totalIn := input.Coins

	// Output.ValidateBasic guarantees each output's coins are positive, sorted
	// and unique, so the per-denom sum stays positive. This must stay linear in
	// the number of coins: it runs unmetered in FinalizeBlock.
	m := sdk.NewMapCoins(nil)
	for _, out := range outputs {
		if err := out.ValidateBasic(); err != nil {
			return err
		}

		m.Add(out.Coins...)
	}
	totalOut := m.ToCoins()

	// make sure inputs and outputs match
	if !totalIn.Equal(totalOut) {
		return ErrInputOutputMismatch
	}

	return nil
}

// ValidateBasic - validate transaction input
func (in Input) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(in.Address); err != nil {
		return sdkerrors.ErrInvalidAddress.Wrapf("invalid input address: %s", err)
	}

	if !in.Coins.IsValid() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidCoins, in.Coins.String())
	}

	if !in.Coins.IsAllPositive() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidCoins, in.Coins.String())
	}

	return nil
}

// NewInput - create a transaction input, used with MsgMultiSend
func NewInput(addr sdk.AccAddress, coins sdk.Coins) Input {
	return Input{
		Address: addr.String(),
		Coins:   coins,
	}
}

// ValidateBasic - validate transaction output
func (out Output) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(out.Address); err != nil {
		return sdkerrors.ErrInvalidAddress.Wrapf("invalid output address: %s", err)
	}

	if !out.Coins.IsValid() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidCoins, out.Coins.String())
	}

	if !out.Coins.IsAllPositive() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidCoins, out.Coins.String())
	}

	return nil
}

// NewOutput - create a transaction output, used with MsgMultiSend
func NewOutput(addr sdk.AccAddress, coins sdk.Coins) Output {
	return Output{
		Address: addr.String(),
		Coins:   coins,
	}
}
