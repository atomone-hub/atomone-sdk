# ADR 017: Ledger Support Without CGO

## Changelog

* 2026 Oct 19: Initial draft

## Context

`crypto/ledger` reached the Ledger Cosmos app through `cosmos/ledger-cosmos-go`, which uses `zondax/ledger-go` for the transport, which in turn uses `zondax/hid`, a CGO binding of hidapi. Ledger support therefore needed CGO, a C toolchain at build time and the `ledger` build tag, which the `Makefile` set through `LEDGER_ENABLED` after checking for `gcc`.

Binaries built with `CGO_ENABLED=0`, such as static release builds and most container images, had no Ledger support: `--ledger` keys failed with "support for ledger devices is not available in this executable". Whether a given binary of a chain supported Ledger devices depended on how it was built, which users can't tell in advance.

## Decision

Ledger support is always built in and needs neither CGO nor a build tag. `crypto/ledger` talks to the device through a pure-Go HID backend, [`rafaelmartins.com/p/usbhid`](https://pkg.go.dev/rafaelmartins.com/p/usbhid), and its own client for the Cosmos app, in place of `ledger-cosmos-go`, `ledger-go` and `zondax/hid`.

### Transport

* `crypto/ledger/hid.go` frames APDUs over 64-byte HID reports, as Ledger devices expect them: channel `0x0101`, tag `0x05`, a sequence number and, in the first report, the length of the APDU. It reassembles responses and reports status words other than `0x9000` as errors.
* One report is written per call: pure-Go HID backends refuse longer writes, and multi-report writes wedge the device on macOS with the hidapi binding.
* Empty and all-zero reports, which some devices (such as the Nano X on macOS) send at any point of a response, are skipped, as `ledger-go` does. Any other unexpected report is an error.
* `crypto/ledger/hid_usbhid.go` opens the Ledger's APDU interface (vendor `0x2c97`, usage page `0xffa0`, as opposed to its FIDO interface) on Linux, macOS and Windows. The device is opened exclusively, so that no other application, such as Ledger Wallet, exchanges APDUs with it during a signature. `hid_unsupported.go` returns an error on other platforms.

### Cosmos app client

* `crypto/ledger/cosmos.go` is a client for the Cosmos app, ported from `ledger-cosmos-go`: version check, address and public key retrieval, and signing in 48-byte chunks, with P2 as the sign mode (amino JSON or textual).
* Cosmos app versions 2.1.0 to 2.x are accepted. Older ones are rejected, as `ledger-cosmos-go` rejects them, and so is any later major version, which may change the protocol, until it is checked.
* The client implements `SECP256K1` and is unexported, so the package API is unchanged. Chains overriding discovery with `ledger.SetDiscoverLedger` are unaffected.
* A locked device (`0x5515`) is reported as such, rather than as a missing Cosmos app.

### Build tags

* The `ledger` build tag is removed: nothing depends on it any more. The `Makefile` no longer checks for `gcc` nor reads `LEDGER_ENABLED`.
* The mock device is selected with `test_ledger_mock` alone, for tests.

## Status

Accepted

## Consequences

### Positive

* Every build of a chain on this SDK, including `CGO_ENABLED=0` builds, supports Ledger devices on Linux, macOS and Windows.
* Building no longer needs a C toolchain for Ledger support, and cross-compiling is simpler.
* Fewer dependencies: `ledger-cosmos-go` and the `zondax` modules are dropped.

### Negative

* The SDK maintains its own APDU transport and Cosmos app client instead of relying on the ones Zondax maintains, and must follow changes to the Cosmos app protocol itself, such as a new major version.
* Platforms other than Linux, macOS and Windows have no Ledger support. hidapi supported some of them, but only in CGO builds, which the `Makefile` already disabled on OpenBSD.

### Neutral

* Build scripts setting `LEDGER_ENABLED` or the `ledger` tag keep working: both are ignored.

## References

* [#127](https://github.com/atomone-hub/atomone-sdk/pull/127)
* [`ledger-cosmos-go`](https://github.com/cosmos/ledger-cosmos-go), [`ledger-go`](https://github.com/Zondax/ledger-go)
* [Ledger Cosmos app APDU specification](https://github.com/cosmos/ledger-cosmos/blob/main/docs/APDUSPEC.md)
