# Cosmos SDK for **AtomOne**

Fork of Cosmos SDK **v0.50.x** for **AtomOne**.

## Differences from the Cosmos SDK

This fork is based on Cosmos SDK **v0.50.x**. Its changes fall into two groups:
features developed specifically for AtomOne, and features backported from later or
related Cosmos SDK work that the v0.50.x line does not include. See the
[CHANGELOG.md](CHANGELOG.md) for the complete, versioned list.

### AtomOne-specific changes

- **Governors (`x/gov`).** A new class of participants — *governors* — can hold
  delegated voting power. Users may self-elect as governors (subject to a minimum
  self-delegation) and delegate their voting power to one, decoupling governance
  participation from staking. Upstreamed from the AtomOne governance module. See the
  [`x/gov` README](x/gov/README.md).
- **Dynamic governance parameters (`x/gov`).** The minimum deposit, minimum initial
  deposit, and quorum adjust dynamically to network activity.
- **Removed `MsgCancelProposal`** from `x/gov`.
- **Nakamoto Bonus (`x/distribution`, ADR-004).** Block rewards are split into a
  stake-proportional part and a fixed bonus shared equally across validators, to
  incentivize decentralization; the bonus coefficient is recomputed each epoch. This
  replaces the proposer-reward parameters (`base_proposer_reward`,
  `bonus_proposer_reward`), which were removed. See the
  [`x/distribution` README](x/distribution/README.md).
- **Validator commission updates (`x/staking`).** Existing validator commissions are
  updated when the commission parameters change.
- **Legacy global account number (`x/auth`).** Support for querying the legacy global
  account number in historical state.
- **Custom ABCI query router (`baseapp`).** Re-added for custom ABCI queries — the
  Cosmos SDK removed it.
- **Mono `go.mod`.** Non-forked `cosmossdk.io/*` modules were removed from the
  repository; only the forked SDK and the `cosmossdk.io/x/upgrade` module are
  maintained here. Everything else is consumed from upstream releases.

### Backported from later/related Cosmos SDK work

These are absent from the v0.50.x base but originate in newer or related Cosmos
SDK code:

- **`x/epochs` module.** A generalized epoch/timer system that lets modules hook into
  periodic execution (used by the Nakamoto Bonus). Backported from the upstream
  lineage (`cosmossdk.io/x/epochs`, originally from Osmosis). See the
  [`x/epochs` README](x/epochs/README.md).
- **App version connected to consensus params (`baseapp`).** `SetProtocolVersion` is
  renamed to `SetAppVersion` and stores the app version in baseapp's consensus
  `ParamStore`. Backport of Cosmos SDK #16244 (with parts of #23622 and
  #21508).
- **Consensus key rotation (`x/staking`, ADR-016).** Validators can rotate their
  consensus key via `MsgRotateConsPubKey`. Backport of the Cosmos SDK ADR-016 feature.
  See [ADR-016](docs/architecture/adr-016-validator-consensus-key-rotation.md).
- **Grant-pruning limits (`x/authz`).** At most 200 expired grants pruned per block,
  plus a `PruneExpiredGrants` message. Backport of Cosmos SDK #18737.
