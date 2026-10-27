//go:build !test_ledger_mock

package ledger

// Ledger support needs neither CGO nor a build tag: discoverCosmosApp reaches
// the device through a pure-Go HID backend.
func init() {
	options.discoverLedger = discoverCosmosApp

	initOptionsDefault()
}
