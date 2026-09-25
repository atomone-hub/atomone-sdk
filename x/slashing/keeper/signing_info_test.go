package keeper_test

import (
	"time"

	"go.uber.org/mock/gomock"

	"cosmossdk.io/core/comet"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/testutil"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func (s *KeeperTestSuite) TestValidatorSigningInfo() {
	ctx, keeper := s.ctx, s.slashingKeeper
	require := s.Require()

	// No validator backs this consensus address; address resolution falls back
	// to the address itself.
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	signingInfo := slashingtypes.NewValidatorSigningInfo(
		consAddr,
		ctx.BlockHeight(),
		int64(3),
		time.Unix(2, 0),
		false,
		int64(10),
	)

	// set the validator signing information
	keeper.SetValidatorSigningInfo(ctx, consAddr, signingInfo)

	require.True(keeper.HasValidatorSigningInfo(ctx, consAddr))
	info, err := keeper.GetValidatorSigningInfo(ctx, consAddr)
	require.NoError(err)
	require.Equal(info.StartHeight, ctx.BlockHeight())
	require.Equal(info.IndexOffset, int64(3))
	require.Equal(info.JailedUntil, time.Unix(2, 0).UTC())
	require.Equal(info.MissedBlocksCounter, int64(10))

	var signingInfos []slashingtypes.ValidatorSigningInfo

	keeper.IterateValidatorSigningInfos(ctx, func(consAddr sdk.ConsAddress, info slashingtypes.ValidatorSigningInfo) (stop bool) {
		signingInfos = append(signingInfos, info)
		return false
	})

	require.Equal(signingInfos[0].Address, signingInfo.Address)

	// test Tombstone
	err = keeper.Tombstone(ctx, consAddr)
	require.NoError(err)
	require.True(keeper.IsTombstoned(ctx, consAddr))

	// test JailUntil
	jailTime := time.Now().Add(time.Hour).UTC()
	keeper.JailUntil(ctx, consAddr, jailTime)
	sInfo, _ := keeper.GetValidatorSigningInfo(ctx, consAddr)
	require.Equal(sInfo.JailedUntil, jailTime)
}

func (s *KeeperTestSuite) TestValidatorMissedBlockBitmap_SmallWindow() {
	ctx, keeper := s.ctx, s.slashingKeeper
	require := s.Require()

	for _, window := range []int64{100, 32_000} {
		params := testutil.TestParams()
		params.SignedBlocksWindow = window
		require.NoError(keeper.SetParams(ctx, params))

		// validator misses all blocks in the window
		var valIdxOffset int64
		for valIdxOffset < params.SignedBlocksWindow {
			idx := valIdxOffset % params.SignedBlocksWindow
			err := keeper.SetMissedBlockBitmapValue(ctx, consAddr, idx, true)
			require.NoError(err)

			missed, err := keeper.GetMissedBlockBitmapValue(ctx, consAddr, idx)
			require.NoError(err)
			require.True(missed)

			valIdxOffset++
		}

		// validator should have missed all blocks
		missedBlocks, err := keeper.GetValidatorMissedBlocks(ctx, consAddr)
		require.NoError(err)
		require.Len(missedBlocks, int(params.SignedBlocksWindow))

		// sign next block, which rolls the missed block bitmap
		idx := valIdxOffset % params.SignedBlocksWindow
		err = keeper.SetMissedBlockBitmapValue(ctx, consAddr, idx, false)
		require.NoError(err)

		missed, err := keeper.GetMissedBlockBitmapValue(ctx, consAddr, idx)
		require.NoError(err)
		require.False(missed)

		// validator should have missed all blocks except the last one
		missedBlocks, err = keeper.GetValidatorMissedBlocks(ctx, consAddr)
		require.NoError(err)
		require.Len(missedBlocks, int(params.SignedBlocksWindow)-1)

		// if the validator rotated its key, there will be different consKeys and a mapping will be added in the state
		consAddr1 := sdk.ConsAddress("addr1_______________")
		s.stakingKeeper.EXPECT().ValidatorIdentifier(gomock.Any(), consAddr1).Return(consAddr, nil).AnyTimes()

		missedBlocks, err = keeper.GetValidatorMissedBlocks(ctx, consAddr1)
		require.NoError(err)
		require.Len(missedBlocks, int(params.SignedBlocksWindow)-1)
	}
}

func (s *KeeperTestSuite) TestPerformConsensusPubKeyUpdate() {
	ctx, slashingKeeper := s.ctx, s.slashingKeeper

	require := s.Require()

	pks := simtestutil.CreateTestPubKeys(500)

	oldConsAddr := sdk.ConsAddress(pks[0].Address())
	newConsAddr := sdk.ConsAddress(pks[1].Address())

	newInfo := slashingtypes.NewValidatorSigningInfo(
		newConsAddr,
		4,
		0,
		time.Unix(2, 0).UTC(),
		false,
		int64(10),
	)

	err := slashingKeeper.SetValidatorSigningInfo(ctx, oldConsAddr, newInfo)
	require.NoError(err)

	s.stakingKeeper.EXPECT().ValidatorIdentifier(gomock.Any(), oldConsAddr).Return(oldConsAddr, nil).AnyTimes()
	err = slashingKeeper.SetMissedBlockBitmapValue(ctx, oldConsAddr, 10, true)
	require.NoError(err)

	err = slashingKeeper.Hooks().AfterConsensusPubKeyUpdate(ctx, pks[0], pks[1], sdk.Coin{})
	require.NoError(err)

	// check pubkey relation is set properly
	savedPubKey, err := slashingKeeper.GetPubkey(ctx, newConsAddr.Bytes())
	require.NoError(err)
	require.Equal(savedPubKey, pks[1])

	// check validator's SigningInfo is set properly with new consensus pubkey
	signingInfo, err := slashingKeeper.GetValidatorSigningInfo(ctx, newConsAddr)
	require.NoError(err)
	require.Equal(signingInfo, newInfo)

	// missed blocks map corresponds only to the old cons key, as there is an identifier added to get the missed blocks using the new cons key
	missedBlocks, err := slashingKeeper.GetValidatorMissedBlocks(ctx, oldConsAddr)
	require.NoError(err)

	require.Len(missedBlocks, 1)
}

// TestHandleValidatorSignature_AfterConsKeyRotation is a regression test for the
// chain-halt bug where HandleValidatorSignature received a stale (pre-rotation)
// consensus address from CometBFT last-commit info, looked up signing info
// directly under that address, and returned ErrNoSigningInfoFound because
// performConsensusPubKeyUpdate had already migrated the record to the new
// consensus address.
func (s *KeeperTestSuite) TestHandleValidatorSignature_AfterConsKeyRotation() {
	ctx, keeper := s.ctx, s.slashingKeeper
	require := s.Require()

	oldPk := ed25519.GenPrivKey().PubKey()
	newPk := ed25519.GenPrivKey().PubKey()
	oldConsAddr := sdk.ConsAddress(oldPk.Address())
	newConsAddr := sdk.ConsAddress(newPk.Address())

	// Seed signing info under the old address (pre-rotation state).
	original := slashingtypes.NewValidatorSigningInfo(
		oldConsAddr, ctx.BlockHeight(), int64(0), time.Unix(0, 0), false, int64(0),
	)
	require.NoError(keeper.SetValidatorSigningInfo(ctx, oldConsAddr, original))

	// Migrate signing info + bitmap to the new address. Mirrors what the
	// AfterConsensusPubKeyUpdate hook does during staking EndBlock.
	require.NoError(keeper.Hooks().AfterConsensusPubKeyUpdate(ctx, oldPk, newPk, sdk.NewInt64Coin("uatom", 0)))

	// The live signing-info record was migrated to the new address, and the
	// old-address record is retained (frozen) so old-key equivocation evidence
	// still finds signing info there.
	retained, err := keeper.GetValidatorSigningInfo(ctx, oldConsAddr)
	require.NoError(err)
	require.Equal(oldConsAddr.String(), retained.Address)
	migrated, err := keeper.GetValidatorSigningInfo(ctx, newConsAddr)
	require.NoError(err)
	require.Equal(newConsAddr.String(), migrated.Address)

	// Build a validator carrying the *new* pubkey. In production this is what
	// staking returns via GetValidatorByConsAddr's old-to-new fallback.
	newPkAny, err := codectypes.NewAnyWithValue(newPk)
	require.NoError(err)
	val := stakingtypes.Validator{ConsensusPubkey: newPkAny}

	// Expect ValidatorByConsAddr to be called with the stale old address (this
	// is what CometBFT last-commit info still reports during ValidatorUpdateDelay).
	s.stakingKeeper.EXPECT().
		ValidatorByConsAddr(gomock.Any(), oldConsAddr).
		Return(val, nil).
		Times(1)

	// Before the fix, this call returned ErrNoSigningInfoFound and aborted the
	// slashing BeginBlocker. After the fix, it succeeds.
	require.NoError(keeper.HandleValidatorSignature(
		ctx, oldPk.Address(), int64(1), comet.BlockIDFlagCommit,
	))
}
