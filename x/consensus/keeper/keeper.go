package keeper

import (
	"context"
	"fmt"
	"slices"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/event"
	storetypes "cosmossdk.io/core/store"
	"cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/consensus/exported"
	"github.com/cosmos/cosmos-sdk/x/consensus/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
)

var StoreKey = "Consensus"

type Keeper struct {
	storeService storetypes.KVStoreService
	event        event.Service

	authority   string
	ParamsStore collections.Item[cmtproto.ConsensusParams]
}

var _ exported.ConsensusParamSetter = Keeper{}.ParamsStore

func NewKeeper(cdc codec.BinaryCodec, storeService storetypes.KVStoreService, authority string, em event.Service) Keeper {
	sb := collections.NewSchemaBuilder(storeService)
	return Keeper{
		storeService: storeService,
		authority:    authority,
		event:        em,
		ParamsStore:  collections.NewItem(sb, collections.NewPrefix("Consensus"), "params", codec.CollValue[cmtproto.ConsensusParams](cdc)),
	}
}

func (k *Keeper) GetAuthority() string {
	return k.authority
}

// Querier

var _ types.QueryServer = Keeper{}

// Params queries params of consensus module
func (k Keeper) Params(ctx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	params, err := k.ParamsStore.Get(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryParamsResponse{Params: &params}, nil
}

// MsgServer

var _ types.MsgServer = Keeper{}

func (k Keeper) UpdateParams(ctx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if k.GetAuthority() != msg.Authority {
		return nil, errors.Wrapf(govtypes.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.GetAuthority(), msg.Authority)
	}

	consensusParams, err := msg.ToProtoConsensusParams()
	if err != nil {
		return nil, err
	}

	paramsProto, err := k.ParamsStore.Get(ctx)
	if err != nil {
		return nil, err
	}

	// initialize version params with zero value if not set
	if paramsProto.Version == nil {
		paramsProto.Version = &cmtproto.VersionParams{}
	}

	params := cmttypes.ConsensusParamsFromProto(paramsProto)

	nextParams := params.Update(&consensusParams)

	// NOTE: do not override the version params
	// consensusParams.Version always has DefaultConsensusParams version: 0
	// AppVersion is updated by x/upgrade during migrations
	nextParams.Version = params.Version
	if err := nextParams.ValidateBasic(); err != nil {
		return nil, err
	}

	// SECURITY: Block.MaxGas of 0 must never be accepted. CometBFT's
	// ValidateBasic only rejects MaxGas < -1, so a proposal setting
	// MaxGas = 0 passes every existing check, but 0 is not a benign value:
	// CometBFT's mempool post-check (PostCheckMaxGas) enforces it as a block
	// gas budget of zero and permanently rejects every transaction (no
	// admissible tx has GasWanted == 0) from every node's mempool. Reversing
	// the parameter is itself a transaction, so this locks governance out of
	// the chain. Note the asymmetry: MaxBytes == 0 IS rejected by
	// ValidateBasic, and baseapp's block gas meter treats 0 as unlimited
	// (GetMaximumBlockGas), confirming 0 is the degenerate "unset" shape
	// rather than an intended configuration.
	if nextParams.Block.MaxGas == 0 {
		return nil, fmt.Errorf("Block.MaxGas cannot be 0: a zero block gas budget makes every node's mempool reject all transactions permanently; use -1 for unlimited or a positive limit")
	}

	// SECURITY: Validator.PubKeyTypes may only be extended, never shrunk.
	// CometBFT's ValidateBasic only checks that the list is non-empty and
	// contains known types, so an update that drops a currently allowed type
	// (e.g. ["ed25519"] -> ["secp256k1"]) passes every existing check but is
	// later rejected by CometBFT's validateValidatorUpdates for the active
	// validators, which panics in applyBlock and halts the whole chain
	// irrecoverably. Keep every type the current validators may be using.
	for _, t := range params.Validator.PubKeyTypes {
		if !slices.Contains(nextParams.Validator.PubKeyTypes, t) {
			return nil, fmt.Errorf("cannot remove pubkey type %q from Validator.PubKeyTypes: existing validators may still use it", t)
		}
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := params.ValidateUpdate(&consensusParams, sdkCtx.BlockHeader().Height); err != nil {
		return nil, err
	}

	if err := k.ParamsStore.Set(ctx, nextParams.ToProto()); err != nil {
		return nil, err
	}

	if err := k.event.EventManager(ctx).EmitKV(
		ctx,
		"update_consensus_params",
		event.Attribute{Key: "authority", Value: msg.Authority},
		event.Attribute{Key: "parameters", Value: consensusParams.String()}); err != nil {
		return nil, err
	}

	return &types.MsgUpdateParamsResponse{}, nil
}
