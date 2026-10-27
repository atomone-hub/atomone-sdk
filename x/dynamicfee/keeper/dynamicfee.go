package keeper

import (
	"context"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/dynamicfee/types"
)

// UpdateDynamicfee updates the base fee and learning rate based on the
// AIMD learning rate adjustment algorithm. Note that if the dynamic fee
// pricing is disabled, this function will return without updating the
// dynamic fee pricing. This is executed in EndBlock which allows the next
// block's base fee to be readily available for wallets to estimate gas prices.
func (k *Keeper) UpdateDynamicfee(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(sdkCtx)

	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	logger.Info(
		"updated the dynamic fee pricing",
		"params", params,
	)

	if !params.Enabled {
		return nil
	}

	maxBlockGas := k.GetMaxBlockGas(ctx, params)

	state, err := k.GetState(ctx)
	if err != nil {
		return err
	}

	// Make the current block's window slot authoritative by sourcing it from
	// the consensus block gas meter, which accounts for the gas of every
	// transaction charged to the block, including transactions whose messages
	// failed or ran out of gas. The per-transaction post handler only records
	// gas for successfully executed messages: its cached state write is
	// discarded when message execution fails, and it is skipped entirely when a
	// transaction runs out of gas (the panic unwinds before it runs). Relying on
	// that handler alone therefore lets failed transactions consume block space
	// without ever influencing the base gas price. Reading the block gas meter
	// here closes that accounting gap and keeps the dynamic fee window in sync
	// with the gas the block actually charged.
	if blockGasMeter := sdkCtx.BlockGasMeter(); blockGasMeter != nil {
		blockGas := blockGasMeter.GasConsumed()
		// Clamp to the module's max block gas so a block never records more than
		// full utilization. maxBlockGas is authoritative for the module's
		// accounting: when the consensus MaxGas is set it equals it, and when it
		// is 0/-1 (unbounded consensus, so the app uses an infinite block gas
		// meter that enforces nothing) it falls back to DefaultMaxBlockGas. The
		// clamp keeps utilization in [0, 1] and the AIMD math and window bounded
		// in that case, and also absorbs the block gas meter overshooting its
		// limit on the tx whose final consumption trips it (recovered as
		// out-of-gas) when consensus MaxGas is set.
		if blockGas > maxBlockGas {
			blockGas = maxBlockGas
		}
		state.Window[state.Index] = blockGas
	}

	// Update the learning rate based on the block gas seen in the
	// current block. This is the AIMD learning rate adjustment algorithm.
	newLR := state.UpdateLearningRate(params, maxBlockGas)

	// Update the base gas price based with the new learning rate.
	newBaseGasPrice := state.UpdateBaseGasPrice(logger, params, maxBlockGas)

	logger.Info(
		"updated the dynamic fee pricing",
		"height", sdkCtx.BlockHeight(),
		"new_base_gas_price", newBaseGasPrice,
		"new_learning_rate", newLR,
		"average_block_gas", state.GetAverageGas(maxBlockGas),
		"net_block_gas", state.GetNetGas(maxBlockGas, params),
	)

	// Increment the height of the state and set the new state.
	state.IncrementHeight()
	return k.SetState(ctx, state)
}

// GetMaxBlockGas returns the maximum gas of a block
// It returns the value obtained from ConsensusParams if
// it is different from 0 or -1, otherwise it returns
// DefaultMaxBlockGas
func (k *Keeper) GetMaxBlockGas(ctx context.Context, params types.Params) uint64 {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	maxBlockGas := sdkCtx.ConsensusParams().Block.GetMaxGas()
	if maxBlockGas == 0 || maxBlockGas == -1 {
		return params.DefaultMaxBlockGas
	}
	return uint64(maxBlockGas)
}

// GetBaseGasPrice returns the base fee from the dynamic fee pricing state.
func (k *Keeper) GetBaseGasPrice(ctx context.Context) (math.LegacyDec, error) {
	state, err := k.GetState(ctx)
	if err != nil {
		return math.LegacyDec{}, err
	}

	return state.BaseGasPrice, nil
}

// GetLearningRate returns the learning rate from the dynamic fee pricing state.
func (k *Keeper) GetLearningRate(ctx context.Context) (math.LegacyDec, error) {
	state, err := k.GetState(ctx)
	if err != nil {
		return math.LegacyDec{}, err
	}

	return state.LearningRate, nil
}

// GetMinGasPrice returns the minimum gas prices for given denom as
// sdk.DecCoins from the dynamic fee pricing state.
func (k *Keeper) GetMinGasPrice(ctx context.Context, denom string) (sdk.DecCoin, error) {
	baseGasPrice, err := k.GetBaseGasPrice(ctx)
	if err != nil {
		return sdk.DecCoin{}, err
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return sdk.DecCoin{}, err
	}

	var gasPrice sdk.DecCoin

	if params.FeeDenom == denom {
		gasPrice = sdk.NewDecCoinFromDec(params.FeeDenom, baseGasPrice)
	} else {
		gasPrice, err = k.ResolveToDenom(ctx, sdk.NewDecCoinFromDec(params.FeeDenom, baseGasPrice), denom)
		if err != nil {
			return sdk.DecCoin{}, err
		}
	}

	return gasPrice, nil
}

// GetMinGasPrices returns the minimum gas prices as sdk.DecCoins from the
// dynamic fee pricing state.
func (k *Keeper) GetMinGasPrices(ctx context.Context) (sdk.DecCoins, error) {
	baseGasPrice, err := k.GetBaseGasPrice(ctx)
	if err != nil {
		return sdk.NewDecCoins(), err
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return sdk.NewDecCoins(), err
	}

	minGasPrice := sdk.NewDecCoinFromDec(params.FeeDenom, baseGasPrice)
	minGasPrices := sdk.NewDecCoins(minGasPrice)

	extraDenoms, err := k.resolver.ExtraDenoms(ctx)
	if err != nil {
		return sdk.NewDecCoins(), err
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, denom := range extraDenoms {
		gasPrice, err := k.ResolveToDenom(ctx, minGasPrice, denom)
		if err != nil {
			k.Logger(sdkCtx).Info(
				"failed to convert gas price",
				"min gas price", minGasPrice,
				"denom", denom,
			)
			continue
		}
		minGasPrices = minGasPrices.Add(gasPrice)
	}

	return minGasPrices, nil
}
