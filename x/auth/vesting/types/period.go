package types

import (
	"fmt"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Periods stores all vesting periods passed as part of a PeriodicVestingAccount
type Periods []Period

// Duration converts the period Length from seconds to a time.Duration
func (p Period) Duration() time.Duration {
	return time.Duration(p.Length) * time.Second
}

// TotalLength return the total length in seconds for a period
func (p Periods) TotalLength() int64 {
	var total int64
	for _, period := range p {
		total += period.Length
	}
	return total
}

// TotalDuration returns the total duration of the period
func (p Periods) TotalDuration() time.Duration {
	length := p.TotalLength()
	return time.Duration(length) * time.Second
}

// TotalAmount returns the sum of coins for the period.
//
// It accumulates per denom and sorts once at the end: Coins.Add re-inserts
// and re-sorts the entire accumulated set on every call, which makes summing
// N periods O(N² log N) in unmetered CPU.
func (p Periods) TotalAmount() sdk.Coins {
	totals := make(map[string]sdk.Coin, len(p))
	for _, period := range p {
		for _, coin := range period.Amount {
			if prev, ok := totals[coin.Denom]; ok {
				totals[coin.Denom] = sdk.NewCoin(coin.Denom, prev.Amount.Add(coin.Amount))
			} else {
				totals[coin.Denom] = coin
			}
		}
	}

	total := make(sdk.Coins, 0, len(totals))
	for _, coin := range totals {
		total = append(total, coin)
	}

	return total.Sort()
}

// String implements the fmt.Stringer interface
func (p Periods) String() string {
	periodsListString := make([]string, len(p))
	for _, period := range p {
		periodsListString = append(periodsListString, period.String())
	}

	return strings.TrimSpace(fmt.Sprintf(`Vesting Periods:
		%s`, strings.Join(periodsListString, ", ")))
}
