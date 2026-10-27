package keeper_test

import (
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
)

// TestAfterValidatorBondedClearsInheritedTombstone guards against a re-created
// validator (same consensus key, previous validator removed) inheriting the
// removed validator's tombstone: validator removal deletes only the
// addr-pubkey relation, the signing-info record survives, and the key can be
// re-registered afterwards. A carried-over tombstone would make the evidence
// handler ignore all double-sign evidence for the new validator
// (IsTombstoned early-return), giving it permanent double-sign immunity.
func TestAfterValidatorBondedClearsInheritedTombstone(t *testing.T) {
	s := new(KeeperTestSuite)
	s.SetT(t)
	s.SetupTest()

	ctx, keeper := s.ctx, s.slashingKeeper
	require := s.Require()

	// The staking consensus-address index no longer contains the removed
	// validator, so signing-info lookups fall back to the raw cons address.
	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), consAddr).Return(nil, nil).AnyTimes()

	// Leftover record of a previous, tombstoned (double-sign) and removed
	// validator that used this consensus key.
	leftover := slashingtypes.NewValidatorSigningInfo(consAddr, 10, 4, time.Now().Add(time.Hour), true, 3)
	require.NoError(keeper.SetValidatorSigningInfo(ctx, consAddr, leftover))
	require.True(keeper.IsTombstoned(ctx, consAddr))

	// A new validator registers the same consensus key and bonds.
	require.NoError(keeper.Hooks().AfterValidatorBonded(ctx, consAddr, sdk.ValAddress(consAddr.Bytes())))

	// The tombstone must not carry over: double-sign evidence against the new
	// validator is processed instead of being ignored by the evidence handler.
	require.False(keeper.IsTombstoned(ctx, consAddr))

	info, err := keeper.GetValidatorSigningInfo(ctx, consAddr)
	require.NoError(err)
	require.False(info.Tombstoned)

	// Fields whose carry-over is deliberate for the same-validator unjail
	// path must still be preserved.
	require.Equal(ctx.BlockHeight(), info.StartHeight)
	require.Equal(int64(4), info.IndexOffset)
	require.Equal(int64(3), info.MissedBlocksCounter)
	require.True(info.JailedUntil.Equal(leftover.JailedUntil))
}

// TestAfterValidatorBondedClearsStaleMissedBlockBitmap guards against a
// re-created validator pairing a fresh MissedBlocksCounter with the previous
// validator's stale missed-block bitmap. Consensus-key rotation migrates the
// signing-info record to the new key and deletes the record under the initial
// key while the bitmap stays keyed under the initial address; once that key is
// re-adopted, the stale set bits would decrement the fresh counter below zero
// as the new validator signs and freeze it below the
// MissedBlocksCounter > maxMissed jail threshold — permanent downtime-jail and
// downtime-slash evasion.
func TestAfterValidatorBondedClearsStaleMissedBlockBitmap(t *testing.T) {
	s := new(KeeperTestSuite)
	s.SetT(t)
	s.SetupTest()

	ctx, keeper := s.ctx, s.slashingKeeper
	require := s.Require()

	s.stakingKeeper.EXPECT().ValidatorByConsAddr(gomock.Any(), consAddr).Return(nil, nil).AnyTimes()

	// Stale bitmap of a previous validator that rotated away from this key
	// (rotation deletes the record but leaves the bitmap under the initial
	// consensus address): no signing-info record exists, only the bitmap.
	for i := int64(0); i < 5; i++ {
		require.NoError(keeper.SetMissedBlockBitmapValue(ctx, consAddr, i, true))
	}
	missed, err := keeper.GetValidatorMissedBlocks(ctx, consAddr)
	require.NoError(err)
	require.Len(missed, 5)

	// A new validator bonds on the initial key; no record exists, so a fresh
	// one is created — it must start paired with an empty bitmap.
	require.NoError(keeper.Hooks().AfterValidatorBonded(ctx, consAddr, sdk.ValAddress(consAddr.Bytes())))

	info, err := keeper.GetValidatorSigningInfo(ctx, consAddr)
	require.NoError(err)
	require.Equal(int64(0), info.MissedBlocksCounter)

	missed, err = keeper.GetValidatorMissedBlocks(ctx, consAddr)
	require.NoError(err)
	require.Empty(missed)
}
