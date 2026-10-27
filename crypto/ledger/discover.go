package ledger

import (
	"errors"
	"fmt"
	"runtime"
)

// discoverCosmosApp connects to the first Ledger device and returns a client
// for the Cosmos app, which must be open on it.
func discoverCosmosApp() (SECP256K1, error) {
	if !hidSupported {
		return nil, fmt.Errorf("no Ledger support on %s", runtime.GOOS)
	}
	dev, err := connectHID()
	if err != nil {
		return nil, err
	}
	app, err := openApp(dev)
	if err != nil {
		_ = dev.Close()
		return nil, err
	}
	return app, nil
}

// openApp returns a client for the Cosmos app open on dev.
func openApp(dev device) (SECP256K1, error) {
	app, err := openCosmosApp(dev)
	switch {
	case err == nil:
		return app, nil
	case errors.Is(err, errNotCosmosApp):
		return nil, errors.New("open the Cosmos app on the Ledger device")
	default:
		return nil, err
	}
}
