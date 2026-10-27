package v1

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestGetNewMinDepositClampsPercChange(t *testing.T) {
	hugeAmount, ok := math.NewIntFromString("10000000000000000000000000000000000000000000000000000000000") // 1e58
	require.True(t, ok)

	tests := []struct {
		name  string
		floor sdk.Coins
		last  sdk.Coins
		perc  math.LegacyDec
		want  sdk.Coins
	}{
		{
			// The EndBlocker's decrease branch with a uint64-max throttler
			// target produces percChange ~ -1.9e19; with a 1e58 stored deposit
			// this panicked with "Int overflow" before the clamp.
			name:  "huge negative percChange stays at floor",
			floor: sdk.NewCoins(sdk.NewCoin("stake", hugeAmount)),
			last:  sdk.NewCoins(sdk.NewCoin("stake", hugeAmount)),
			perc:  math.LegacyNewDecFromBigInt(new(big.Int).Neg(new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil))),
			want:  sdk.NewCoins(sdk.NewCoin("stake", hugeAmount)),
		},
		{
			name:  "huge positive percChange is clamped to the max safe product",
			floor: sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
			last:  sdk.NewCoins(sdk.NewCoin("stake", hugeAmount)),
			perc:  math.LegacyNewDecFromBigInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)),
			want: sdk.NewCoins(sdk.NewCoin("stake", math.NewIntFromBigInt(
				new(big.Int).Exp(big.NewInt(10), big.NewInt(70), nil)))), // 1e58 * (1e70/1e58) = 1e70
		},
		{
			name:  "normal percChange is untouched",
			floor: sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
			last:  sdk.NewCoins(sdk.NewInt64Coin("stake", 2000)),
			perc:  math.LegacyNewDecWithPrec(5, 1),
			want:  sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetNewMinDeposit(tt.floor, tt.last, tt.perc)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGetNewMinDeposit(t *testing.T) {
	type args struct {
		minDepositFloor sdk.Coins
		lastMinDeposit  sdk.Coins
		percChange      math.LegacyDec
	}
	tests := []struct {
		name string
		args args
		want sdk.Coins
	}{
		{
			"minDepositFloor = lastMinDeposit, percChange = 1, no change",
			args{
				minDepositFloor: sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
				lastMinDeposit:  sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
				percChange:      math.LegacyOneDec(),
			},
			sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
		},
		{
			"minDepositFloor > lastMinDeposit, percChange = 0.5, minDepositFloor",
			args{
				minDepositFloor: sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
				lastMinDeposit:  sdk.NewCoins(sdk.NewInt64Coin("stake", 500)),
				percChange:      math.LegacyNewDecWithPrec(5, 1),
			},
			sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
		},
		{
			"minDepositFloor*2 = lastMinDeposit, percChange = 0.5, minDepositFloor",
			args{
				minDepositFloor: sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
				lastMinDeposit:  sdk.NewCoins(sdk.NewInt64Coin("stake", 2000)),
				percChange:      math.LegacyNewDecWithPrec(5, 1),
			},
			sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
		},
		{
			"minDepositFloor != lastMinDeposit, percChange = 2, minDepositFloor*2",
			args{
				minDepositFloor: sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
				lastMinDeposit:  sdk.NewCoins(sdk.NewInt64Coin("xxx", 1000)),
				percChange:      math.LegacyNewDec(2),
			},
			sdk.NewCoins(sdk.NewInt64Coin("stake", 2000)),
		},
		{
			"minDepositFloor denoms in lastMinDeposit, percChange = 0.5, minDepositFloor",
			args{
				minDepositFloor: sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
				lastMinDeposit:  sdk.NewCoins(sdk.NewInt64Coin("stake", 2000), sdk.NewInt64Coin("xxx", 1000)),
				percChange:      math.LegacyNewDecWithPrec(5, 1),
			},
			sdk.NewCoins(sdk.NewInt64Coin("stake", 1000)),
		},
		{
			"minDepositFloor not in lastMinDeposit, percChange = 2, minDepositFloor*2",
			args{
				minDepositFloor: sdk.NewCoins(sdk.NewInt64Coin("stake", 1000), sdk.NewInt64Coin("xxx", 1000)),
				lastMinDeposit:  sdk.NewCoins(sdk.NewInt64Coin("yyy", 2000)),
				percChange:      math.LegacyNewDec(2),
			},
			sdk.NewCoins(sdk.NewInt64Coin("stake", 2000), sdk.NewInt64Coin("xxx", 2000)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetNewMinDeposit(tt.args.minDepositFloor, tt.args.lastMinDeposit, tt.args.percChange)
			require.Equal(t, tt.want, got)
		})
	}
}
