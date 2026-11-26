package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A bit array decoded from a transaction can carry fewer Elems than its
// ExtraBitsStored implies. Count() then exceeds what Elems can hold, and
// indexing must not read or write outside the slice.
func TestCompactBitArrayElemsShorterThanCount(t *testing.T) {
	bA := &CompactBitArray{ExtraBitsStored: 10}
	require.Equal(t, 2, bA.Count())

	require.NotPanics(t, func() {
		require.False(t, bA.GetIndex(0))
		require.False(t, bA.GetIndex(1))
		require.False(t, bA.SetIndex(0, true))
		require.False(t, bA.SetIndex(1, true))
	})
}

func TestCompactBitArrayValidate(t *testing.T) {
	tests := []struct {
		name   string
		bA     *CompactBitArray
		errMsg string
	}{
		{"nil", nil, ""},
		{"empty", &CompactBitArray{}, ""},
		{"whole bytes", &CompactBitArray{Elems: []byte{0xC0, 0x00}}, ""},
		{"partial last byte", &CompactBitArray{ExtraBitsStored: 3, Elems: []byte{0xA0}}, ""},
		{"extra bits of 8", &CompactBitArray{ExtraBitsStored: 8, Elems: []byte{0xC0}}, "lower than 8"},
		{"truncated elems", &CompactBitArray{ExtraBitsStored: 16, Elems: []byte{0xC0}}, "lower than 8"},
		{"extra bits without elems", &CompactBitArray{ExtraBitsStored: 3}, "no elements"},
		{"unused bits set", &CompactBitArray{ExtraBitsStored: 3, Elems: []byte{0xBF}}, "unused bits"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.bA.Validate()
			if tc.errMsg == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.errMsg)
		})
	}

	for n := 1; n <= 64; n++ {
		require.NoError(t, NewCompactBitArray(n).Validate(), "size %d", n)
	}
}
