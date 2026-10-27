package keeper_test

import (
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttime "github.com/cometbft/cometbft/types/time"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec/address"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtestutil "github.com/cosmos/cosmos-sdk/x/staking/testutil"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestValidatorUpdatesNoDuplicateOnRotationAndPowerChange reproduces the
// chain-halting scenario where a validator rotates its consensus key
// (MsgRotateConsPubKey) in the same block in which its consensus power
// changes (a delegation). The staking EndBlocker used to emit the
// power-change update under the validator's OLD consensus key in addition to
// the rotation pair ({old key, 0}, {new key, power}), so the old key appeared
// twice in the returned ValidatorUpdates. CometBFT rejects validator updates
// containing a duplicate pubkey ("duplicate entry in %v"), which makes every
// node panic in finalizeCommit and permanently halt on the same block.
func TestValidatorUpdatesNoDuplicateOnRotationAndPowerChange(t *testing.T) {
	key := storetypes.NewKVStoreKey(stakingtypes.StoreKey)
	storeService := runtime.NewKVStoreService(key)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	ctx := testCtx.Ctx.WithBlockHeader(cmtproto.Header{Time: cmttime.Now()})
	encCfg := moduletestutil.MakeTestEncodingConfig()

	ctrl := gomock.NewController(t)
	accountKeeper := stakingtestutil.NewMockAccountKeeper(ctrl)
	accountKeeper.EXPECT().GetModuleAddress(stakingtypes.BondedPoolName).Return(bondedAcc.GetAddress())
	accountKeeper.EXPECT().GetModuleAddress(stakingtypes.NotBondedPoolName).Return(notBondedAcc.GetAddress())
	accountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()

	bankKeeper := stakingtestutil.NewMockBankKeeper(ctrl)

	sk := stakingkeeper.NewKeeper(
		encCfg.Codec,
		storeService,
		accountKeeper,
		bankKeeper,
		authtypes.NewModuleAddress(govtypes.ModuleName).String(),
		address.NewBech32Codec("cosmosvaloper"),
		address.NewBech32Codec("cosmosvalcons"),
	)
	require.NoError(t, sk.SetParams(ctx, stakingtypes.DefaultParams()))

	stakingtypes.RegisterInterfaces(encCfg.InterfaceRegistry)
	msgServer := stakingkeeper.NewMsgServerImpl(sk)
	sk.SetDistributionKeeper(&mockDistributionKeeper{})

	// Create one bonded validator with a self-delegation (mirrors setValidators).
	addrDels, addrVals := createValAddrs(1)
	val := stakingtestutil.NewValidator(t, addrVals[0], PKs[0])
	valTokens := sk.TokensFromConsensusPower(ctx, 10)
	val, issuedShares := val.AddTokensFromDel(valTokens)

	bankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), stakingtypes.NotBondedPoolName, stakingtypes.BondedPoolName, gomock.Any()).AnyTimes()
	_ = stakingkeeper.TestingUpdateValidator(sk, ctx, val, true)

	selfDelegation := stakingtypes.NewDelegation(addrDels[0].String(), addrVals[0].String(), issuedShares)
	require.NoError(t, sk.SetDelegation(ctx, selfDelegation))
	require.NoError(t, sk.SetValidatorByConsAddr(ctx, val))

	validators, err := sk.GetAllValidators(ctx)
	require.NoError(t, err)
	require.Len(t, validators, 1)
	validator := validators[0]

	oldPk, ok := validator.ConsensusPubkey.GetCachedValue().(cryptotypes.PubKey)
	require.True(t, ok)
	newPk := PKs[444]

	oldCmtPk, err := cryptocodec.ToCmtProtoPublicKey(oldPk)
	require.NoError(t, err)
	newCmtPk, err := cryptocodec.ToCmtProtoPublicKey(newPk)
	require.NoError(t, err)

	// Run one EndBlocker so that the validator's power is recorded in the
	// last validator power index, as in steady-state operation.
	_, err = sk.EndBlocker(ctx)
	require.NoError(t, err)

	// Same block: rotate the consensus key ...
	rotateMsg, err := stakingtypes.NewMsgRotateConsPubKey(validator.GetOperator(), newPk)
	require.NoError(t, err)
	_, err = msgServer.RotateConsPubKey(ctx, rotateMsg)
	require.NoError(t, err)

	// ... and change the validator's power with an additional delegation.
	delegated := sk.TokensFromConsensusPower(ctx, 10)
	_, err = sk.Delegate(ctx, addrDels[0], delegated, stakingtypes.Unbonded, validator, false)
	require.NoError(t, err)

	updatedVal, err := sk.GetValidator(ctx, addrVals[0])
	require.NoError(t, err)
	expPower := updatedVal.ConsensusPower(sk.PowerReduction(ctx))

	updates, err := sk.EndBlocker(ctx)
	require.NoError(t, err)

	// No consensus pubkey may appear more than once in the update list;
	// CometBFT rejects duplicate entries, halting the chain.
	seen := make(map[string]struct{})
	var oldPower, newPower int64 = -1, -1
	for _, u := range updates {
		pkKey := u.PubKey.String()
		require.NotContainsf(t, seen, pkKey, "duplicate validator update for pubkey %s", pkKey)
		seen[pkKey] = struct{}{}

		switch pkKey {
		case oldCmtPk.String():
			oldPower = u.Power
		case newCmtPk.String():
			newPower = u.Power
		}
	}

	// The old key must be retired and the new key must carry the current power.
	require.Equal(t, int64(0), oldPower, "old consensus key must be removed from the validator set")
	require.Equal(t, expPower, newPower, "new consensus key must carry the validator's current power")
}
