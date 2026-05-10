package v5

// Package v5 contains the distribution module's v4->v5 in-place store
// migration. The migration is invoked once at upgrade height by the module's
// configurator and converts pre-upgrade chain state to the post-upgrade
// shares-based F1 + auto-staked bond-denom-rewards model.
//
// The migration is deliberately limited to the F1 stores that have a
// schema-level semantic shift between v4 and v5. The reason is the change
// in storage interpretation:
//
//   - Pre-upgrade: ValidatorHistoricalRewards.CumulativeRewardRatio is
//     "rewards per token" (post-slash tokens at period close);
//     DelegatorStartingInfo.Stake is "tokens-from-shares at delegation init";
//     slash events scale stake at calculation time via (1 - fraction).
//
//   - Post-upgrade: CumulativeRewardRatio is "rewards per share";
//     DelegatorStartingInfo.Stake holds the delegator's share count;
//     no slash event scaling is applied. Bond-denom delegator rewards are
//     auto-staked into the bonded pool instead of going through F1.
//
// Reading old storage values under the new interpretation produces materially
// wrong numbers on chains that have been slashed (cumulative ratios are
// inflated relative to what shares-based math expects). To avoid that, the
// migration drains pending rewards under the legacy logic, then resets and
// rebuilds the F1 stores under the new one.
//
// The flow is:
//
//  1. Snapshot every (validator, delegation, accumulated commission,
//     historical period) record the migration needs before mutating the
//     stores.
//  2. For each validator currently in staking (in deterministic key order):
//     close its current period under legacy F1, pay out each delegation's
//     pending rewards (auto-staking the bond denom delegator portion),
//     wipe the F1 stores while preserving accumulatedCommission and
//     setting outstandingRewards to exactly that preserved commission,
//     re-seed period 0/1, and re-initialize the `DelegatorStartingInfo`
//     for each of its active delegations using the shares-based semantic.
//  3. For each orphan (an address with leftover F1 records but no matching
//     validator in staking): sweep all outstanding state to the community
//     pool — including accumulated commission, since there is no operator
//     to preserve it for — and wipe storage.
//
// `ValidatorSlashEvent` records are intentionally left in storage. They are
// no longer consumed by reward computation, but the public ValidatorSlashes
// gRPC endpoint still exposes them for historical / audit purposes.

import (
	"context"
	"sort"

	"cosmossdk.io/collections"
	addresscodec "cosmossdk.io/core/address"
	storetypes "cosmossdk.io/core/store"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	dstrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// bankKeeper enumerates the bank methods needed by the migration.
type bankKeeper interface {
	SendCoinsFromModuleToModule(ctx context.Context, sender, recipient string, amt sdk.Coins) error
	SendCoinsFromModuleToAccount(ctx context.Context, sender string, recipient sdk.AccAddress, amt sdk.Coins) error
}

// stakingKeeper enumerates the staking methods needed by the migration.
type stakingKeeper interface {
	BondDenom(ctx context.Context) (string, error)
	AddValidatorTokens(ctx context.Context, valAddr sdk.ValAddress, tokensToAdd math.Int) error
	ValidatorAddressCodec() addresscodec.Codec
	IterateValidators(ctx context.Context, fn func(int64, stakingtypes.ValidatorI) bool) error
	Validator(ctx context.Context, valAddr sdk.ValAddress) (stakingtypes.ValidatorI, error)
	Delegation(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) (stakingtypes.DelegationI, error)
}

// Migrator gives the migration access to the distribution keeper's storage
// surface. The real Keeper satisfies this interface implicitly. Slash events
// are not on the interface — they are read directly via the storeService /
// cdc passed to MigrateStore, so the keeper does not have to expose a
// dedicated public iterator just for migration use.
type Migrator interface {
	IterateDelegatorStartingInfos(ctx context.Context, fn func(val sdk.ValAddress, del sdk.AccAddress, info dstrtypes.DelegatorStartingInfo) (stop bool))
	IterateValidatorAccumulatedCommissions(ctx context.Context, fn func(val sdk.ValAddress, commission dstrtypes.ValidatorAccumulatedCommission) (stop bool))
	IterateValidatorHistoricalRewards(ctx context.Context, fn func(val sdk.ValAddress, period uint64, rewards dstrtypes.ValidatorHistoricalRewards) (stop bool))

	GetValidatorCurrentRewards(ctx context.Context, val sdk.ValAddress) (dstrtypes.ValidatorCurrentRewards, error)
	GetValidatorHistoricalRewards(ctx context.Context, val sdk.ValAddress, period uint64) (dstrtypes.ValidatorHistoricalRewards, error)
	GetValidatorOutstandingRewards(ctx context.Context, val sdk.ValAddress) (dstrtypes.ValidatorOutstandingRewards, error)
	GetDelegatorWithdrawAddr(ctx context.Context, delAddr sdk.AccAddress) (sdk.AccAddress, error)

	SetValidatorCurrentRewards(ctx context.Context, val sdk.ValAddress, rewards dstrtypes.ValidatorCurrentRewards) error
	SetValidatorHistoricalRewards(ctx context.Context, val sdk.ValAddress, period uint64, rewards dstrtypes.ValidatorHistoricalRewards) error
	SetValidatorOutstandingRewards(ctx context.Context, val sdk.ValAddress, rewards dstrtypes.ValidatorOutstandingRewards) error
	SetValidatorAccumulatedCommission(ctx context.Context, val sdk.ValAddress, commission dstrtypes.ValidatorAccumulatedCommission) error
	SetDelegatorStartingInfo(ctx context.Context, val sdk.ValAddress, del sdk.AccAddress, info dstrtypes.DelegatorStartingInfo) error

	DeleteValidatorHistoricalReward(ctx context.Context, val sdk.ValAddress, period uint64) error
	DeleteDelegatorStartingInfo(ctx context.Context, val sdk.ValAddress, del sdk.AccAddress) error
	DeleteValidatorAccumulatedCommission(ctx context.Context, val sdk.ValAddress) error
	DeleteValidatorOutstandingRewards(ctx context.Context, val sdk.ValAddress) error
	DeleteValidatorCurrentRewards(ctx context.Context, val sdk.ValAddress) error
}

// MigrateStore performs the v4->v5 in-place upgrade.
//
// storeService and cdc are needed to iterate the pre-upgrade ValidatorSlashEvent
// records directly: that data is only consumed by the migration's legacy
// reward computation and is otherwise unreachable from the post-upgrade
// keeper API, which deliberately does not expose a public slash-event
// iterator.
func MigrateStore(
	ctx sdk.Context,
	dk Migrator,
	storeService storetypes.KVStoreService,
	cdc codec.BinaryCodec,
	feePool collections.Item[dstrtypes.FeePool],
	bk bankKeeper,
	sk stakingKeeper,
) error {
	bondDenom, err := sk.BondDenom(ctx)
	if err != nil {
		return err
	}

	fp, err := feePool.Get(ctx)
	if err != nil {
		return err
	}

	// Snapshot every store entry the migration needs BEFORE mutating anything.
	// Each helper performs a single-pass iteration over its store; later
	// phases consume the snapshots without re-iterating.
	snap := newStateSnapshot(ctx, dk)

	// Process each currently-live validator: drain rewards, wipe F1, re-seed,
	// re-init delegations. IterateValidators yields validators in store-key
	// order so the operation is deterministic across nodes.
	live := map[string]struct{}{}
	if err := sk.IterateValidators(ctx, func(_ int64, val stakingtypes.ValidatorI) bool {
		valBz, errStr := sk.ValidatorAddressCodec().StringToBytes(val.GetOperator())
		if errStr != nil {
			err = errStr
			return true
		}
		live[string(valBz)] = struct{}{}
		err = processValidator(ctx, dk, storeService, cdc, bk, sk, &fp, snap, valBz, val, bondDenom)
		return err != nil
	}); err != nil {
		return err
	}
	if err != nil {
		return err
	}

	// Process orphans: addresses with leftover F1 records but no matching
	// validator in staking. Determinism guaranteed by sorting the candidate
	// keys.
	for _, valStr := range orphanKeys(snap, live) {
		if err := wipeOrphan(ctx, dk, &fp, snap, sdk.ValAddress(valStr)); err != nil {
			return err
		}
	}

	return feePool.Set(ctx, fp)
}

// stateSnapshot collects every pre-mutation read the migration needs, so we
// don't re-iterate during processing or risk reading the partially-modified
// stores. All maps are keyed by string(validator-address-bytes).
type stateSnapshot struct {
	delegations map[string][]delegationEntry
	commissions map[string]sdk.DecCoins
	periods     map[string][]uint64
}

// delegationEntry pairs a delegator address with their pre-migration F1
// starting info. The validator address is implicit in the map key.
type delegationEntry struct {
	delAddr sdk.AccAddress
	info    dstrtypes.DelegatorStartingInfo
}

func newStateSnapshot(ctx context.Context, dk Migrator) *stateSnapshot {
	s := &stateSnapshot{
		delegations: map[string][]delegationEntry{},
		commissions: map[string]sdk.DecCoins{},
		periods:     map[string][]uint64{},
	}
	dk.IterateDelegatorStartingInfos(ctx, func(va sdk.ValAddress, da sdk.AccAddress, info dstrtypes.DelegatorStartingInfo) bool {
		s.delegations[string(va)] = append(s.delegations[string(va)], delegationEntry{da, info})
		return false
	})
	dk.IterateValidatorAccumulatedCommissions(ctx, func(va sdk.ValAddress, c dstrtypes.ValidatorAccumulatedCommission) bool {
		if !c.Commission.IsZero() {
			s.commissions[string(va)] = c.Commission
		}
		return false
	})
	dk.IterateValidatorHistoricalRewards(ctx, func(va sdk.ValAddress, period uint64, _ dstrtypes.ValidatorHistoricalRewards) bool {
		s.periods[string(va)] = append(s.periods[string(va)], period)
		return false
	})
	return s
}

// processValidator runs the per-validator flow: drain delegator rewards,
// sweep dust, wipe F1 storage, re-seed period 0/1, and re-initialize
// startingInfos for every delegation under this validator. Commission
// is **not** drained or otherwise touched by the migration.
// ValidatorAccumulatedCommission has no schema-level semantic shift
// between v4 and v5 (unlike CumulativeRewardRatio and
// DelegatorStartingInfo.Stake).
func processValidator(
	ctx context.Context,
	dk Migrator,
	storeService storetypes.KVStoreService,
	cdc codec.BinaryCodec,
	bk bankKeeper,
	sk stakingKeeper,
	fp *dstrtypes.FeePool,
	snap *stateSnapshot,
	valBz []byte,
	val stakingtypes.ValidatorI,
	bondDenom string,
) error {
	valAddr := sdk.ValAddress(valBz)
	valKey := string(valBz)
	dels := snap.delegations[valKey]

	// 1. Close the current period under legacy F1 so pending rewards roll up
	//    at the pre-upgrade exchange rate. val.GetTokens() reflects post-slash,
	//    pre-auto-stake state — the same value the legacy keeper would have
	//    used.
	endingPeriod, err := legacyIncrementValidatorPeriod(ctx, dk, valAddr, val.GetTokens())
	if err != nil {
		return err
	}

	// 2. Pay out each delegation's pending rewards. Bond denom is auto-staked
	//    while non-bond is paid to the delegator's withdraw address.
	for _, d := range dels {
		rewards, err := legacyCalculateDelegationRewards(
			ctx, dk, storeService, cdc, valAddr,
			d.info.PreviousPeriod, endingPeriod,
			d.info.Stake, d.info.Height,
		)
		if err != nil {
			return err
		}
		if err := payOutRewards(ctx, dk, bk, sk, fp, valAddr, d.delAddr, rewards, bondDenom, true /* autoStakeBond */); err != nil {
			return err
		}
	}

	// 3. Sweep dust + wipe F1 storage, preserving accumulatedCommission
	//    untouched. OutstandingRewards is reset to exactly the preserved
	//    commission so the "module balance == sum of outstanding claims"
	//    invariant continues to hold post-migration.
	preservedCommission, _ := snap.commissions[valKey]
	if err := wipeF1PreservingCommission(ctx, dk, fp, snap, valAddr, preservedCommission); err != nil {
		return err
	}

	// 4. Re-seed F1 with a fresh period 0 / period 1 pair, mirroring the
	//    keeper's initializeValidator path. Commission and outstanding
	//    rewards were preserved/restored in step 3 and are not touched here.
	if err := seedFreshF1(ctx, dk, valAddr); err != nil {
		return err
	}

	// 5. Re-initialize DelegatorStartingInfo for each delegation under this
	//    validator, this time with shares-based semantics.
	for _, d := range dels {
		del, err := sk.Delegation(ctx, d.delAddr, valAddr)
		if err != nil || del == nil {
			// Stale entry (delegation removed mid-flight): drop it.
			if err := dk.DeleteDelegatorStartingInfo(ctx, valAddr, d.delAddr); err != nil {
				return err
			}
			continue
		}
		if err := bumpRefCount(ctx, dk, valAddr, 0); err != nil {
			return err
		}
		height := uint64(sdk.UnwrapSDKContext(ctx).BlockHeight())
		if err := dk.SetDelegatorStartingInfo(ctx, valAddr, d.delAddr, dstrtypes.NewDelegatorStartingInfo(0, del.GetShares(), height)); err != nil {
			return err
		}
	}
	return nil
}

// wipeOrphan handles an address that has leftover F1 records but no matching
// validator in the staking module. There is no operator to receive the
// accumulated commission of an orphaned record, so everything (including
// commission) is swept to the community pool and deleted. This is the
// terminal cleanup path; the live-validator path uses
// wipeF1PreservingCommission instead so operators keep the commission
// they are owed across the upgrade.
func wipeOrphan(
	ctx context.Context,
	dk Migrator,
	fp *dstrtypes.FeePool,
	snap *stateSnapshot,
	valAddr sdk.ValAddress,
) error {
	outstanding, err := dk.GetValidatorOutstandingRewards(ctx, valAddr)
	if err != nil {
		return err
	}
	if !outstanding.Rewards.IsZero() {
		fp.CommunityPool = fp.CommunityPool.Add(outstanding.Rewards...)
	}
	for _, period := range snap.periods[string(valAddr)] {
		if err := dk.DeleteValidatorHistoricalReward(ctx, valAddr, period); err != nil {
			return err
		}
	}
	if err := dk.DeleteValidatorCurrentRewards(ctx, valAddr); err != nil {
		return err
	}
	if err := dk.DeleteValidatorOutstandingRewards(ctx, valAddr); err != nil {
		return err
	}
	if err := dk.DeleteValidatorAccumulatedCommission(ctx, valAddr); err != nil {
		return err
	}
	for _, d := range snap.delegations[string(valAddr)] {
		if err := dk.DeleteDelegatorStartingInfo(ctx, valAddr, d.delAddr); err != nil {
			return err
		}
	}
	return nil
}

// wipeF1PreservingCommission deletes the F1 stores that have a schema-level
// semantic shift between v4 and v5 (historical and current rewards) and
// resets outstanding rewards to exactly the preserved commission, so the
// module-balance invariant ("module balance == sum of outstanding claims")
// continues to hold post-migration. AccumulatedCommission is not touched
// since it has no semantic shift between schemas.
//
// `preservedCommission` is the snapshot of accumulatedCommission taken in
// Phase 1; the migration's payout step intentionally never subtracts from
// it, so it equals the current on-disk value at the time this is called.
//
// Any outstanding rewards beyond the commission portion (i.e., the dust
// left over from rounding in the delegator-payout step) are swept to the
// community pool — that's the only thing actually deleted from the
// operator's perspective.
func wipeF1PreservingCommission(
	ctx context.Context,
	dk Migrator,
	fp *dstrtypes.FeePool,
	snap *stateSnapshot,
	valAddr sdk.ValAddress,
	preservedCommission sdk.DecCoins,
) error {
	outstanding, err := dk.GetValidatorOutstandingRewards(ctx, valAddr)
	if err != nil {
		return err
	}

	// Sweep dust = outstanding - preservedCommission.
	// outstanding may be slightly less than commission due to truncation
	// during delegator payouts, so use SafeSub-style logic via Sub: if it
	// would go negative on any denom we treat dust as zero for that denom.
	dust, hasNeg := outstanding.Rewards.SafeSub(preservedCommission)
	if hasNeg {
		// Should not happen under correct accounting; fall back to no
		// sweep rather than panicking on a defensive edge case
		dust = sdk.DecCoins{}
	}
	if !dust.IsZero() {
		fp.CommunityPool = fp.CommunityPool.Add(dust...)
	}

	for _, period := range snap.periods[string(valAddr)] {
		if err := dk.DeleteValidatorHistoricalReward(ctx, valAddr, period); err != nil {
			return err
		}
	}
	if err := dk.DeleteValidatorCurrentRewards(ctx, valAddr); err != nil {
		return err
	}
	// Reset outstanding to exactly the preserved commission. AccumulatedCommission
	// itself is left untouched on disk
	return dk.SetValidatorOutstandingRewards(ctx, valAddr,
		dstrtypes.ValidatorOutstandingRewards{Rewards: preservedCommission})
}

// seedFreshF1 writes the canonical "freshly created validator" F1 records
// for historical[0] and current period 1, mirroring
// keeper.initializeValidator. AccumulatedCommission and OutstandingRewards
// are deliberately not touched here — wipeF1PreservingCommission has
// already left them in the correct post-migration state.
func seedFreshF1(ctx context.Context, dk Migrator, valAddr sdk.ValAddress) error {
	if err := dk.SetValidatorHistoricalRewards(ctx, valAddr, 0, dstrtypes.NewValidatorHistoricalRewards(sdk.DecCoins{}, 1)); err != nil {
		return err
	}
	return dk.SetValidatorCurrentRewards(ctx, valAddr, dstrtypes.NewValidatorCurrentRewards(sdk.DecCoins{}, 1))
}

// bumpRefCount increments the historical record's reference count for the
// given period, mirroring keeper.incrementReferenceCount.
func bumpRefCount(ctx context.Context, dk Migrator, valAddr sdk.ValAddress, period uint64) error {
	h, err := dk.GetValidatorHistoricalRewards(ctx, valAddr, period)
	if err != nil {
		return err
	}
	h.ReferenceCount++
	return dk.SetValidatorHistoricalRewards(ctx, valAddr, period, h)
}

// orphanKeys returns the validator addresses present in any F1 store but
// absent from staking, sorted lexicographically for deterministic processing.
func orphanKeys(snap *stateSnapshot, live map[string]struct{}) []string {
	candidates := map[string]struct{}{}
	for k := range snap.delegations {
		if _, ok := live[k]; !ok {
			candidates[k] = struct{}{}
		}
	}
	for k := range snap.commissions {
		if _, ok := live[k]; !ok {
			candidates[k] = struct{}{}
		}
	}
	for k := range snap.periods {
		if _, ok := live[k]; !ok {
			candidates[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(candidates))
	for k := range candidates {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// payOutRewards routes a delegator's (or operator's) reward DecCoins after
// the migration has computed them under legacy F1.
//
// `autoStakeBond` controls how the bond denom portion is handled:
//
//   - true (delegator path): the bond denom integer is sent from the
//     distribution module to the bonded pool, and validator.Tokens is
//     incremented via AddValidatorTokens (no new shares). This carries
//     the new auto-staking semantic into the migration: pre-upgrade the
//     bond denom would have been claimed by the delegator, post-upgrade
//     it auto-compounds.
//
//   - false (commission path): bond denom
//     is paid out to the recipient just like any other denom, because
//     only delegator rewards are auto-staked — commission stays fully
//     claimable in F1.
//
// Bond denom decimal dust always goes to the community pool. The non-bond
// integer portion is always paid to the recipient's withdraw address;
// non-bond decimal dust always goes to the community pool. The validator's
// outstanding-rewards record is decremented by the full DecCoins amount we
// consumed so the module-account invariant continues to hold.
func payOutRewards(
	ctx context.Context,
	dk Migrator,
	bk bankKeeper,
	sk stakingKeeper,
	fp *dstrtypes.FeePool,
	valAddr sdk.ValAddress,
	recipient sdk.AccAddress,
	rewards sdk.DecCoins,
	bondDenom string,
	autoStakeBond bool,
) error {
	if rewards.IsZero() {
		return nil
	}

	withdrawAddr, err := dk.GetDelegatorWithdrawAddr(ctx, recipient)
	if err != nil {
		return err
	}

	// Split bond denom from the rest.
	bondDec := rewards.AmountOf(bondDenom)
	bondPortion := sdk.DecCoins{}
	if bondDec.IsPositive() {
		bondPortion = sdk.NewDecCoins(sdk.NewDecCoinFromDec(bondDenom, bondDec))
	}
	nonBond := rewards.Sub(bondPortion)

	// Bond denom integer: either auto-stake into the bonded pool or pay to
	// the recipient. Bond denom decimal dust always goes to the community pool.
	bondInt := bondDec.TruncateInt()
	if bondInt.IsPositive() {
		bondCoins := sdk.NewCoins(sdk.NewCoin(bondDenom, bondInt))
		if autoStakeBond {
			if err := bk.SendCoinsFromModuleToModule(ctx, dstrtypes.ModuleName, stakingtypes.BondedPoolName, bondCoins); err != nil {
				return err
			}
			if err := sk.AddValidatorTokens(ctx, valAddr, bondInt); err != nil {
				return err
			}
		} else if err := bk.SendCoinsFromModuleToAccount(ctx, dstrtypes.ModuleName, withdrawAddr, bondCoins); err != nil {
			return err
		}
	}
	if dust := bondDec.Sub(math.LegacyNewDecFromInt(bondInt)); dust.IsPositive() {
		fp.CommunityPool = fp.CommunityPool.Add(sdk.NewDecCoinFromDec(bondDenom, dust))
	}

	// Non-bond integer to recipient; non-bond decimal dust to the community pool.
	nonBondCoins, nonBondDust := nonBond.TruncateDecimal()
	if !nonBondCoins.IsZero() {
		if err := bk.SendCoinsFromModuleToAccount(ctx, dstrtypes.ModuleName, withdrawAddr, nonBondCoins); err != nil {
			return err
		}
	}
	fp.CommunityPool = fp.CommunityPool.Add(nonBondDust...)

	// Decrement outstanding rewards by the full DecCoins amount we consumed.
	outstanding, err := dk.GetValidatorOutstandingRewards(ctx, valAddr)
	if err != nil {
		return err
	}
	outstanding.Rewards = outstanding.Rewards.Sub(rewards)
	return dk.SetValidatorOutstandingRewards(ctx, valAddr, outstanding)
}
