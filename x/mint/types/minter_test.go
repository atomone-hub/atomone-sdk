package types

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestNextInflationRate(t *testing.T) {
	params := DefaultParams()
	year := time.Duration(NanosecondsPerYear)
	day := 24 * time.Hour

	// Governing Mechanism:
	//    inflationRateChangePerYear = (1 - BondedRatio/GoalBonded) * InflationRateChange
	//    step = inflationRateChangePerYear * elapsed / NanosecondsPerYear

	tests := []struct {
		name         string
		bondedRatio  math.LegacyDec
		elapsed      time.Duration
		expInflation math.LegacyDec
	}{
		{
			name:         "half the goal for a year",
			bondedRatio:  math.LegacyNewDecWithPrec(335, 3),
			elapsed:      year,
			expInflation: math.LegacyNewDecWithPrec(165, 3),
		},
		{
			name:         "half the goal for zero elapsed",
			bondedRatio:  math.LegacyNewDecWithPrec(335, 3),
			elapsed:      0,
			expInflation: math.LegacyNewDecWithPrec(10, 2),
		},
		{
			name:        "half the goal for a day",
			bondedRatio: math.LegacyNewDecWithPrec(335, 3),
			elapsed:     day,
			expInflation: math.LegacyNewDecWithPrec(10, 2).Add(
				math.LegacyNewDecWithPrec(65, 3).MulInt64(day.Nanoseconds()).QuoInt64(NanosecondsPerYear),
			),
		},
		{
			name:         "nothing bonded for a year clamps to max",
			bondedRatio:  math.LegacyZeroDec(),
			elapsed:      year,
			expInflation: params.InflationMax,
		},
		{
			name:         "twice the goal for a year clamps to min",
			bondedRatio:  math.LegacyNewDecWithPrec(134, 2),
			elapsed:      year,
			expInflation: params.InflationMin,
		},
		{
			name:         "at goal for a year",
			bondedRatio:  math.LegacyNewDecWithPrec(67, 2),
			elapsed:      year,
			expInflation: math.LegacyNewDecWithPrec(10, 2),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			minter := InitialMinter(math.LegacyNewDecWithPrec(10, 2))

			inflation := minter.NextInflationRate(params, tc.bondedRatio, tc.elapsed)

			require.True(t, inflation.Equal(tc.expInflation), "expected %s, got %s", tc.expInflation, inflation)
		})
	}
}

func TestBlockProvision(t *testing.T) {
	params := DefaultParams()
	minter := InitialMinter(math.LegacyNewDecWithPrec(10, 2))
	minter.AnnualProvisions = math.LegacyNewDec(31_557_600_000_000)
	year := time.Duration(NanosecondsPerYear)
	day := 24 * time.Hour

	tests := []struct {
		name      string
		elapsed   time.Duration
		expAmount int64
	}{
		{"zero", 0, 0},
		{"negative", -time.Second, 0},
		{"5.8 seconds", 5800 * time.Millisecond, 5_800_000},
		{"one minute", time.Minute, 60_000_000},
		{"default max block elapsed", DefaultMaxBlockElapsed, 8_000_000},
		{"one day", day, 86_400_000_000},
		{"one year", year, 31_557_600_000_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provision := minter.BlockProvision(params, tc.elapsed)

			require.Equal(t, sdk.NewCoin(params.MintDenom, math.NewInt(tc.expAmount)), provision)
		})
	}
}

func TestBlockProvisionSumsToAnnualProvisions(t *testing.T) {
	params := DefaultParams()
	minter := InitialMinter(math.LegacyNewDecWithPrec(10, 2))
	minter.AnnualProvisions = math.LegacyNewDec(31_557_600_000_000)
	r := rand.New(rand.NewSource(1))

	const minElapsed, maxElapsed = int64(4 * time.Second), int64(8 * time.Second)
	sum := math.ZeroInt()
	totalElapsedNs := int64(0)
	for totalElapsedNs < NanosecondsPerYear {
		elapsed := minElapsed + r.Int63n(maxElapsed-minElapsed+1)
		totalElapsedNs += elapsed
		sum = sum.Add(minter.BlockProvision(params, time.Duration(elapsed)).Amount)
	}

	expected := minter.AnnualProvisions.MulInt64(totalElapsedNs).QuoInt64(NanosecondsPerYear)
	diff := math.LegacyNewDecFromInt(sum).Sub(expected).Abs()
	require.True(t, diff.LTE(expected.QuoInt64(10_000)),
		"sum %s differs from expected %s by %s", sum, expected, diff)
}

// Benchmarking :)
// previously using math.Int operations:
// BenchmarkBlockProvision-4 5000000 220 ns/op
//
// using math.LegacyDec operations: (current implementation)
// BenchmarkBlockProvision-4 3000000 429 ns/op
func BenchmarkBlockProvision(b *testing.B) {
	b.ReportAllocs()
	minter := InitialMinter(math.LegacyNewDecWithPrec(1, 1))
	params := DefaultParams()

	s1 := rand.NewSource(100)
	r1 := rand.New(s1)
	minter.AnnualProvisions = math.LegacyNewDec(r1.Int63n(1000000))

	// run the BlockProvision function b.N times
	for n := 0; n < b.N; n++ {
		minter.BlockProvision(params, 6*time.Second)
	}
}

// Next inflation benchmarking
// BenchmarkNextInflation-4 1000000 1828 ns/op
func BenchmarkNextInflation(b *testing.B) {
	b.ReportAllocs()
	minter := InitialMinter(math.LegacyNewDecWithPrec(1, 1))
	params := DefaultParams()
	bondedRatio := math.LegacyNewDecWithPrec(1, 1)

	// run the NextInflationRate function b.N times
	for n := 0; n < b.N; n++ {
		minter.NextInflationRate(params, bondedRatio, 6*time.Second)
	}
}

// Next annual provisions benchmarking
// BenchmarkNextAnnualProvisions-4 5000000 251 ns/op
func BenchmarkNextAnnualProvisions(b *testing.B) {
	b.ReportAllocs()
	minter := InitialMinter(math.LegacyNewDecWithPrec(1, 1))
	params := DefaultParams()
	totalSupply := math.NewInt(100000000000000)

	// run the NextAnnualProvisions function b.N times
	for n := 0; n < b.N; n++ {
		minter.NextAnnualProvisions(params, totalSupply)
	}
}
