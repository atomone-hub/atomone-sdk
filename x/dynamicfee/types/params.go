package types

import (
	fmt "fmt"

	"cosmossdk.io/math"
)

// NewParams instantiates a new EIP-1559 Params object. This params object is
// utilized to implement both the base EIP-1559 fee and AIMD EIP-1559 dynamic
// fee pricing implementations.
func NewParams(
	window uint64,
	alpha math.LegacyDec,
	beta math.LegacyDec,
	gamma math.LegacyDec,
	minBaseGasPrice math.LegacyDec,
	targetBlockUtilization math.LegacyDec,
	defaultMaxBlockGas uint64,
	minLearingRate math.LegacyDec,
	maxLearningRate math.LegacyDec,
	feeDenom string,
	enabled bool,
) Params {
	return Params{
		Alpha:                  alpha,
		Beta:                   beta,
		Gamma:                  gamma,
		MinBaseGasPrice:        minBaseGasPrice,
		TargetBlockUtilization: targetBlockUtilization,
		DefaultMaxBlockGas:     defaultMaxBlockGas,
		MinLearningRate:        minLearingRate,
		MaxLearningRate:        maxLearningRate,
		Window:                 window,
		FeeDenom:               feeDenom,
		Enabled:                enabled,
	}
}

// MaxWindow is the maximum allowed window size. The window drives both a
// heap allocation (window * 8 bytes) in NewState and an unmetered O(window)
// scan in the EndBlocker, so an unbounded window would let a governance
// proposal halt every validator with an oversized allocation or block-time
// explosion. It must be bounded.
const MaxWindow uint64 = 1_000_000 // 1M blocks (~27 days at 2.3s/block)

// maxDefaultMaxBlockGas is the maximum allowed DefaultMaxBlockGas, i.e.
// MaxInt64. It matches the type of consensus_params.block.max_gas (an
// int64 which GetMaxBlockGas converts to uint64 when it is set): a
// default above it has no enforceable equivalent gas limit, and it used
// to overflow the int64 conversions of the AIMD block gas math
// (GetTargetBlockGas), panicking the EndBlocker.
const maxDefaultMaxBlockGas uint64 = 1<<63 - 1

// ValidateBasic performs basic validation on the parameters.
func (p *Params) ValidateBasic() error {
	if p.Window == 0 {
		return fmt.Errorf("window cannot be zero")
	}

	if p.Window > MaxWindow {
		return fmt.Errorf("window cannot exceed %d, got %d", MaxWindow, p.Window)
	}

	if p.Alpha.IsNil() || p.Alpha.IsNegative() {
		return fmt.Errorf("alpha cannot be nil must be between [0, inf)")
	}

	if p.Beta.IsNil() || p.Beta.IsNegative() || p.Beta.GT(math.LegacyOneDec()) {
		return fmt.Errorf("beta cannot be nil and must be between [0, 1]")
	}

	if p.Gamma.IsNil() || p.Gamma.IsNegative() || p.Gamma.GT(math.LegacyMustNewDecFromStr("0.5")) {
		return fmt.Errorf("theta cannot be nil and must be between [0, 0.5]")
	}

	if p.MinBaseGasPrice.IsNil() || !p.MinBaseGasPrice.GTE(math.LegacyZeroDec()) {
		return fmt.Errorf("min base gas price cannot be nil and must be greater than or equal to zero")
	}

	if p.TargetBlockUtilization.IsNil() || !p.TargetBlockUtilization.IsPositive() || p.TargetBlockUtilization.GT(math.LegacyOneDec()) {
		return fmt.Errorf("target block utilization must be between (0, 1]")
	}

	if p.DefaultMaxBlockGas == 0 {
		return fmt.Errorf("default max block gas must be greater than 0")
	}

	if p.DefaultMaxBlockGas > maxDefaultMaxBlockGas {
		return fmt.Errorf("default max block gas must be less than or equal to %d", maxDefaultMaxBlockGas)
	}

	if p.MinLearningRate.IsNil() || p.MinLearningRate.IsNegative() {
		return fmt.Errorf("min learning rate cannot be negative or nil")
	}

	if p.MaxLearningRate.IsNil() || p.MaxLearningRate.IsNegative() {
		return fmt.Errorf("max learning rate cannot be negative or nil")
	}

	if p.MinLearningRate.GT(p.MaxLearningRate) {
		return fmt.Errorf("min learning rate cannot be greater than max learning rate")
	}

	if p.FeeDenom == "" {
		return fmt.Errorf("fee denom must be set")
	}

	return nil
}
