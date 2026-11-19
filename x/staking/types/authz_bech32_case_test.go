package types_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	coreheader "cosmossdk.io/core/header"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestStakeAuthorizationBech32CaseDenyBypass checks that Accept enforces its
// allow/deny lists on decoded address bytes: bech32 accepts an all-uppercase
// encoding of the same address, which is how the staking msg servers resolve
// validator addresses.
func TestStakeAuthorizationBech32CaseDenyBypass(t *testing.T) {
	key := storetypes.NewKVStoreKey(stakingtypes.StoreKey)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	ctx := testCtx.Ctx.WithHeaderInfo(coreheader.Info{})

	// the uppercase form resolves to the same address bytes
	uppercaseVal1 := strings.ToUpper(val1.String())
	bzCanonical, err := sdk.ValAddressFromBech32(val1.String())
	require.NoError(t, err)
	bzUppercase, err := sdk.ValAddressFromBech32(uppercaseVal1)
	require.NoError(t, err)
	require.Equal(t, bzCanonical, bzUppercase)

	// Deny-list grant: grantee may stake anywhere except val1.
	deniedAuth, err := stakingtypes.NewStakeAuthorization(nil, []sdk.ValAddress{val1}, stakingtypes.AuthorizationType_AUTHORIZATION_TYPE_DELEGATE, nil)
	require.NoError(t, err)

	// Canonical encoding of the denied validator is rejected.
	_, err = deniedAuth.Accept(ctx, stakingtypes.NewMsgDelegate(delAddr.String(), val1.String(), coin100))
	require.Error(t, err)

	// All-uppercase encoding of the same denied validator must also be rejected.
	_, err = deniedAuth.Accept(ctx, stakingtypes.NewMsgDelegate(delAddr.String(), uppercaseVal1, coin100))
	require.Error(t, err)

	// Same bypass shape for the other msg types Accept handles.
	undelAuth, err := stakingtypes.NewStakeAuthorization(nil, []sdk.ValAddress{val1}, stakingtypes.AuthorizationType_AUTHORIZATION_TYPE_UNDELEGATE, nil)
	require.NoError(t, err)
	_, err = undelAuth.Accept(ctx, stakingtypes.NewMsgUndelegate(delAddr.String(), uppercaseVal1, coin100))
	require.Error(t, err)

	redelAuth, err := stakingtypes.NewStakeAuthorization(nil, []sdk.ValAddress{val1}, stakingtypes.AuthorizationType_AUTHORIZATION_TYPE_REDELEGATE, nil)
	require.NoError(t, err)
	_, err = redelAuth.Accept(ctx, stakingtypes.NewMsgBeginRedelegate(delAddr.String(), val2.String(), uppercaseVal1, coin100))
	require.Error(t, err)

	cancelAuth, err := stakingtypes.NewStakeAuthorization(nil, []sdk.ValAddress{val1}, stakingtypes.AuthorizationType_AUTHORIZATION_TYPE_CANCEL_UNBONDING_DELEGATION, nil)
	require.NoError(t, err)
	_, err = cancelAuth.Accept(ctx, stakingtypes.NewMsgCancelUnbondingDelegation(delAddr.String(), uppercaseVal1, 1, coin100))
	require.Error(t, err)

	// Allow-list grant keeps accepting (also for the uppercase encoding of an
	// allowed validator, mirroring the msg servers' byte resolution).
	allowedAuth, err := stakingtypes.NewStakeAuthorization([]sdk.ValAddress{val1}, nil, stakingtypes.AuthorizationType_AUTHORIZATION_TYPE_DELEGATE, nil)
	require.NoError(t, err)
	resp, err := allowedAuth.Accept(ctx, stakingtypes.NewMsgDelegate(delAddr.String(), uppercaseVal1, coin100))
	require.NoError(t, err)
	require.True(t, resp.Accept)

	// A validator that is not in the allow list is still rejected, in either
	// encoding.
	_, err = allowedAuth.Accept(ctx, stakingtypes.NewMsgDelegate(delAddr.String(), strings.ToUpper(val2.String()), coin100))
	require.Error(t, err)
}
