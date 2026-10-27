package types_test

import (
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/bank/types"
)

// buildMultiSend constructs the attacker shape: one input mirroring the
// outputs, and m outputs each carrying one distinct denom with amount 1.
func buildMultiSend(m int) (types.Input, []types.Output) {
	addr := sdk.AccAddress(make([]byte, 20))

	coins := make(sdk.Coins, m)
	outputs := make([]types.Output, m)
	for i := 0; i < m; i++ {
		coin := sdk.NewInt64Coin(fmt.Sprintf("aa%06d", i), 1)
		coins[i] = coin
		outputs[i] = types.NewOutput(addr, sdk.Coins{coin})
	}
	coins = coins.Sort()
	return types.NewInput(addr, coins), outputs
}

// TestValidateInputOutputsLinearScaling guards against reintroducing a
// per-output Coins.Add accumulation: before the fix, summing M distinct-denom
// outputs cost O(M² log M) in unmetered CPU (~2.2s at M=2000 on the
// reproducing machine, minutes for a 1 MB MsgMultiSend), reached twice per tx
// from the MultiSend handler and InputOutputCoins. Scaling is asserted on
// allocation counts (a stable proxy for CPU) rather than wall-clock time.
func TestValidateInputOutputsLinearScaling(t *testing.T) {
	allocsFor := func(n int) float64 {
		input, outputs := buildMultiSend(n)
		return testing.AllocsPerRun(3, func() {
			if err := types.ValidateInputOutputs(input, outputs); err != nil {
				t.Fatal(err)
			}
		})
	}

	small := allocsFor(200)
	large := allocsFor(1000)

	// A linear implementation allocates ~5x more for 1000 outputs than for
	// 200; the quadratic Coins.Add implementation allocates ~25x more. 8x
	// leaves headroom against noise while failing any super-linear
	// accumulation.
	if large >= small*8 {
		t.Fatalf("allocation growth from 200 to 1000 outputs is super-linear (%g -> %g, %.2fx)",
			small, large, large/small)
	}
}

// TestValidateInputOutputsSemantics checks that the linear accumulation keeps
// accept/reject behavior unchanged.
func TestValidateInputOutputsSemantics(t *testing.T) {
	addr := sdk.AccAddress(make([]byte, 20))

	// The same denom spread over multiple outputs must be aggregated.
	input := types.NewInput(addr, sdk.NewCoins(sdk.NewInt64Coin("uatom", 3), sdk.NewInt64Coin("stake", 1)))
	outputs := []types.Output{
		types.NewOutput(addr, sdk.NewCoins(sdk.NewInt64Coin("uatom", 1))),
		types.NewOutput(addr, sdk.NewCoins(sdk.NewInt64Coin("uatom", 2), sdk.NewInt64Coin("stake", 1))),
	}
	if err := types.ValidateInputOutputs(input, outputs); err != nil {
		t.Fatalf("aggregated outputs should match input total: %v", err)
	}

	// Mismatched totals must still be rejected.
	badInput := types.NewInput(addr, sdk.NewCoins(sdk.NewInt64Coin("uatom", 4)))
	if err := types.ValidateInputOutputs(badInput, outputs); err == nil {
		t.Fatal("mismatched input/output totals were accepted")
	}

	// An invalid output must be rejected before the total check.
	badOutputs := append([]types.Output{}, outputs...)
	badOutputs[0].Address = "not-an-address"
	if err := types.ValidateInputOutputs(input, badOutputs); err == nil {
		t.Fatal("invalid output address was accepted")
	}
}
