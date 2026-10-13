package tx_test

import (
	"context"
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
)

// mockCometRPC satisfies client.CometRPC while only implementing Block.
type mockCometRPC struct {
	client.CometRPC
	block *coretypes.ResultBlock
}

func (m mockCometRPC) Block(_ context.Context, _ *int64) (*coretypes.ResultBlock, error) {
	return m.block, nil
}

// TestGetBlockWithTxsHugeLimit is a regression test for the fatal-OOM issue in
// GetBlockWithTxs: the pagination limit was used directly as the slice
// capacity of the response tx slice. A single unauthenticated query with a
// huge limit (e.g. 1<<35 => 256 GiB of []*txtypes.Tx backing array) caused
// `fatal error: runtime: out of memory`, which no recover middleware can catch,
// crashing any node serving gRPC/API queries.
func TestGetBlockWithTxsHugeLimit(t *testing.T) {
	registry := types.NewInterfaceRegistry()
	protoCodec := codec.NewProtoCodec(registry)
	txConfig := authtx.NewTxConfig(protoCodec, authtx.DefaultSignModes)

	// Build a block containing a single validly-encoded proto tx (an
	// ADR-027-conformant empty TxRaw decodes without error).
	rawTx := &txtypes.TxRaw{BodyBytes: []byte{}, AuthInfoBytes: []byte{}}
	txBytes, err := rawTx.Marshal()
	require.NoError(t, err)
	block := &cmttypes.Block{
		Header: cmttypes.Header{Height: 1},
		Data:   cmttypes.Data{Txs: cmttypes.Txs{cmttypes.Tx(txBytes)}},
	}

	clientCtx := client.Context{}.
		WithTxConfig(txConfig).
		WithClient(mockCometRPC{block: &coretypes.ResultBlock{Block: block}})

	svc := authtx.NewTxServer(clientCtx, nil, registry)
	sdkCtx := sdk.NewContext(nil, cmtproto.Header{Height: 2}, false, nil)

	// Before the fix this request makes the Go runtime fatally OOM while
	// allocating the response slice. After the fix, the pre-allocation is
	// clamped to the block's tx count.
	resp, err := svc.GetBlockWithTxs(sdkCtx, &txtypes.GetBlockWithTxsRequest{
		Height:     1,
		Pagination: &query.PageRequest{Limit: 1 << 35},
	})
	require.NoError(t, err)
	require.Len(t, resp.Txs, 1)
	require.Equal(t, uint64(1), resp.Pagination.Total)
}
