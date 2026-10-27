package v1

import (
	"math/big"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// maxSafePercChangeProduct bounds the product amount * percChange in
// GetNewMinDeposit. The LegacyDec valid range caps the dec value at ~1.16e77
// (internal integer of 316 bits); staying 7 orders of magnitude below it keeps
// the multiplication from ever panicking with an Int overflow, whatever params
// are stored on chain.
var maxSafePercChangeProduct = math.LegacyNewDecFromBigInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(70), nil))

// clampPercChange returns percChange capped to the largest magnitude for which
// amount * percChange stays inside the LegacyDec valid range.
//
// The throttler targets (TargetActiveProposals, TargetProposals) are only
// validated as "> 0", so a passed params-update proposal can produce a
// percChange of up to ~-1.9e19 in the EndBlocker's decrease branch; combined
// with a large stored deposit this overflows LegacyDec.Mul and panics every
// validator in FinalizeBlock on every block. A negative clamped product
// truncates below the floor and is floored by the existing logic in
// GetNewMinDeposit.
func clampPercChange(amount math.Int, percChange math.LegacyDec) math.LegacyDec {
	if amount.IsNil() || percChange.IsNil() {
		return math.LegacyOneDec()
	}

	amtAbs := amount.Abs()
	if amtAbs.IsZero() {
		// amount * percChange is zero for any percChange; nothing to clamp.
		return percChange
	}

	maxPerc := maxSafePercChangeProduct.Quo(amtAbs.ToLegacyDec())
	if percChange.Abs().LTE(maxPerc) {
		return percChange
	}
	if percChange.IsNegative() {
		return maxPerc.Neg()
	}
	return maxPerc
}

func GetNewMinDeposit(minDepositFloor, lastMinDeposit sdk.Coins, percChange math.LegacyDec) sdk.Coins {
	newMinDeposit := sdk.Coins{}
	minDepositFloorDenomsSeen := make(map[string]bool)
	for _, lastMinDepositCoin := range lastMinDeposit {
		minDepositFloorCoinAmt := minDepositFloor.AmountOf(lastMinDepositCoin.Denom)
		if minDepositFloorCoinAmt.IsZero() {
			// minDepositFloor was changed since last update,
			// and this coin was removed.
			// reflect this also in the current min initial deposit,
			// i.e. remove this coin
			continue
		}
		minDepositFloorDenomsSeen[lastMinDepositCoin.Denom] = true
		// clamp percChange relative to the coin amount so the product can
		// never leave the LegacyDec valid range (see clampPercChange)
		minDepositCoinAmt := lastMinDepositCoin.Amount.ToLegacyDec().
			Mul(clampPercChange(lastMinDepositCoin.Amount, percChange)).TruncateInt()
		if minDepositCoinAmt.LT(minDepositFloorCoinAmt) {
			newMinDeposit = append(newMinDeposit, sdk.NewCoin(lastMinDepositCoin.Denom, minDepositFloorCoinAmt))
		} else {
			newMinDeposit = append(newMinDeposit, sdk.NewCoin(lastMinDepositCoin.Denom, minDepositCoinAmt))
		}
	}

	// make sure any new denoms in minDepositFloor are added to minDeposit
	for _, minDepositFloorCoin := range minDepositFloor {
		if _, seen := minDepositFloorDenomsSeen[minDepositFloorCoin.Denom]; !seen {
			// clamp percChange relative to the coin amount so the product can
			// never leave the LegacyDec valid range (see clampPercChange)
			minDepositCoinAmt := minDepositFloorCoin.Amount.ToLegacyDec().
				Mul(clampPercChange(minDepositFloorCoin.Amount, percChange)).TruncateInt()
			if minDepositCoinAmt.LT(minDepositFloorCoin.Amount) {
				newMinDeposit = append(newMinDeposit, minDepositFloorCoin)
			} else {
				newMinDeposit = append(newMinDeposit, sdk.NewCoin(minDepositFloorCoin.Denom, minDepositCoinAmt))
			}
		}
	}

	return sdk.NewCoins(newMinDeposit...)
}
