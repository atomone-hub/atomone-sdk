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
