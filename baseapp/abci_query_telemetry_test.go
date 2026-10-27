package baseapp_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/telemetry"
)

// TestQueryDoesNotCreateTelemetrySeriesFromQueryPath is a regression test for a
// memory-exhaustion DoS: BaseApp.Query used to key telemetry metrics on the
// attacker-controlled ABCI query path, so every unique path supplied via the
// unauthenticated CometBFT abci_query RPC created a new, never-evicted
// Prometheus counter/summary series (~75KB retained heap per unique path).
// Query telemetry must only use fixed metric keys.
func TestQueryDoesNotCreateTelemetrySeriesFromQueryPath(t *testing.T) {
	// Enable telemetry with a Prometheus sink, mirroring a node configured
	// with telemetry (prometheus retention) enabled. telemetry.New installs
	// the go-metrics global sink and registers the Prometheus sink with the
	// default Prometheus registry.
	_, err := telemetry.New(telemetry.Config{
		Enabled:                 true,
		ServiceName:             t.Name(),
		PrometheusRetentionTime: 600,
	})
	require.NoError(t, err)

	app := getQueryBaseapp(t)

	// Flood the query entry point with unique attacker-chosen paths, the same
	// way the unauthenticated abci_query RPC flood would.
	for i := 0; i < 100; i++ {
		_, err := app.Query(context.Background(), &abci.RequestQuery{
			Path:   fmt.Sprintf("/custom/uniquequery%d/somekey", i),
			Height: 1,
			Data:   []byte("value"),
		})
		require.NoError(t, err)
	}

	// Gather all registered Prometheus series: none of them may embed the
	// attacker-controlled path bytes in the metric identity.
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	tainted := []string{}
	for _, mf := range families {
		name, help := mf.GetName(), mf.GetHelp()
		for i := 0; i < 100; i++ {
			marker := fmt.Sprintf("uniquequery%d", i)
			if strings.Contains(name, marker) || strings.Contains(help, marker) {
				tainted = append(tainted, name)
				break
			}
		}
	}
	require.Empty(t, tainted, "telemetry series keyed by attacker-controlled query path still created: %v", tainted)
}
