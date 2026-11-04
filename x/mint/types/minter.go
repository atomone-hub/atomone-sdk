package types

import (
	"fmt"
	"time"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	// NanosecondsPerYear is the length of a Julian year (365.25 days) in
	// nanoseconds. Annual provisions and the annual inflation-rate change are
	// spread over this duration.
	NanosecondsPerYear int64 = 31_557_600_000_000_000

	// MaxBlockElapsed caps the time a single block can account for when
	// minting. It bounds what a block whose timestamp is far ahead of the
	// previous one can mint, whether after a halt or because the proposer set
	// the time ahead of the wall clock.
	MaxBlockElapsed = 8 * time.Second
)

// NewMinter returns a new Minter object with the given inflation and annual
// provisions values.
func NewMinter(inflation, annualProvisions math.LegacyDec) Minter {
	return Minter{
		Inflation:        inflation,
		AnnualProvisions: annualProvisions,
	}
}

// InitialMinter returns an initial Minter object with a given inflation value.
func InitialMinter(inflation math.LegacyDec) Minter {
	return NewMinter(
		inflation,
		math.LegacyNewDec(0),
	)
}

// DefaultInitialMinter returns a default initial Minter object for a new chain
// which uses an inflation rate of 13%.
func DefaultInitialMinter() Minter {
	return InitialMinter(
		math.LegacyNewDecWithPrec(13, 2),
	)
}

// ValidateMinter does a basic validation on minter.
func ValidateMinter(minter Minter) error {
	if minter.Inflation.IsNegative() {
		return fmt.Errorf("mint parameter Inflation should be positive, is %s",
			minter.Inflation.String())
	}
	return nil
}

// NextInflationRate returns the new inflation rate after elapsed time has
// passed since the previous block.
func (m Minter) NextInflationRate(params Params, bondedRatio math.LegacyDec, elapsed time.Duration) math.LegacyDec {
	// The target annual inflation rate is recalculated for each block. The inflation
	// is also subject to a rate change (positive or negative) depending on the
	// distance from the desired ratio (67%). The maximum rate change possible is
	// defined to be 13% per year, applied in proportion to the time elapsed since
	// the previous block, however the annual inflation is capped as between 7% and
	// 20%.

	// (1 - bondedRatio/GoalBonded) * InflationRateChange
	inflationRateChangePerYear := math.LegacyOneDec().
		Sub(bondedRatio.Quo(params.GoalBonded)).
		Mul(params.InflationRateChange)
	inflationRateChange := inflationRateChangePerYear.MulInt64(elapsed.Nanoseconds()).QuoInt64(NanosecondsPerYear)

	// adjust the new annual inflation for this next block
	inflation := m.Inflation.Add(inflationRateChange) // note inflationRateChange may be negative
	if inflation.GT(params.InflationMax) {
		inflation = params.InflationMax
	}
	if inflation.LT(params.InflationMin) {
		inflation = params.InflationMin
	}

	return inflation
}

// NextAnnualProvisions returns the annual provisions based on current total
// supply and inflation rate.
func (m Minter) NextAnnualProvisions(_ Params, totalSupply math.Int) math.LegacyDec {
	return m.Inflation.MulInt(totalSupply)
}

// BlockProvision returns the provisions for a block, the share of the annual
// provisions corresponding to the time elapsed since the previous block.
func (m Minter) BlockProvision(params Params, elapsed time.Duration) sdk.Coin {
	provisionAmt := m.AnnualProvisions.MulInt64(elapsed.Nanoseconds()).QuoInt64(NanosecondsPerYear)
	return sdk.NewCoin(params.MintDenom, provisionAmt.TruncateInt())
}
