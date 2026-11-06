package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParamsValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(p *Params)
		expErr string
	}{
		{"default", func(*Params) {}, ""},
		{"zero max block elapsed", func(p *Params) { p.MaxBlockElapsed = 0 }, "max block elapsed must be positive: 0s"},
		{"negative max block elapsed", func(p *Params) { p.MaxBlockElapsed = -time.Second }, "max block elapsed must be positive: -1s"},
		{"one second max block elapsed", func(p *Params) { p.MaxBlockElapsed = time.Second }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := DefaultParams()
			tc.mutate(&params)

			err := params.Validate()
			if tc.expErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tc.expErr)
		})
	}
}
