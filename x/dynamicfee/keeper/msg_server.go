package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/dynamicfee/types"
)

var _ types.MsgServer = (*MsgServer)(nil)

// MsgServer is the server API for x/dynamicfee Msg service.
type MsgServer struct {
	k *Keeper
}

// NewMsgServer returns the MsgServer implementation.
func NewMsgServer(k *Keeper) types.MsgServer {
	return &MsgServer{k}
}

// Params defines a method that updates the module's parameters. The signer of the message must
// be the module authority.
func (ms MsgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if msg.Authority != ms.k.GetAuthority() {
		return nil, fmt.Errorf("invalid authority to execute message")
	}

	// Bound the Window before it is persisted and used to allocate a new
	// State: NewState allocates window * 8 bytes during proposal execution
	// and the EndBlocker scans the whole window each block with no gas
	// meter, so an oversized window lets a governance proposal halt every
	// validator (OOM / block-time explosion). Params.ValidateBasic bounds
	// Window, but the tx-level check can be bypassed by direct handler
	// calls, so enforce the bound here too.
	if msg.Params.Window > types.MaxWindow {
		return nil, fmt.Errorf("window cannot exceed %d, got %d", types.MaxWindow, msg.Params.Window)
	}

	gotParams, err := ms.k.GetParams(ctx)
	if err != nil {
		return nil, fmt.Errorf("error getting params: %w", err)
	}

	// if going from disabled -> enabled, set enabled height
	if !gotParams.Enabled && msg.Params.Enabled {
		ms.k.SetEnabledHeight(ctx, ctx.BlockHeight())
	}

	params := msg.Params
	if err := ms.k.SetParams(ctx, params); err != nil {
		return nil, fmt.Errorf("error setting params: %w", err)
	}

	newState := types.NewState(params.Window, params.MinBaseGasPrice, params.MinLearningRate)
	if err := ms.k.SetState(ctx, newState); err != nil {
		return nil, fmt.Errorf("error setting state: %w", err)
	}

	return &types.MsgUpdateParamsResponse{}, nil
}
