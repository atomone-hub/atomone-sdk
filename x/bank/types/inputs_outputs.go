package types

import (
	"sort"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

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

	// Accumulate the output total per denom instead of calling Coins.Add once
	// per output: Add re-inserts and re-sorts the entire accumulated set on
	// every call, making this loop O(M² log M) in the number of distinct output
	// denoms, and none of that work is gas-metered — a single MsgMultiSend
	// with ~12k distinct-denom outputs (fits in one 1 MB tx) burned minutes of
	// unmetered CPU per block. Output.ValidateBasic guarantees each output's
	// coins are positive, sorted and unique, so sums stay positive.
	amountsByDenom := make(map[string]math.Int)
	denoms := make([]string, 0)
	for _, out := range outputs {
		if err := out.ValidateBasic(); err != nil {
			return err
		}

		for _, coin := range out.Coins {
			if prev, ok := amountsByDenom[coin.Denom]; ok {
				amountsByDenom[coin.Denom] = prev.Add(coin.Amount)
			} else {
				amountsByDenom[coin.Denom] = coin.Amount
				denoms = append(denoms, coin.Denom)
			}
		}
	}

	sort.Strings(denoms)
	totalOut := make(sdk.Coins, len(denoms))
	for i, denom := range denoms {
		totalOut[i] = sdk.NewCoin(denom, amountsByDenom[denom])
	}

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
