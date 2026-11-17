package hostset

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSet_Contains(t *testing.T) {
	set := New([]string{"instagram.com", "WWW.Instagram.com."})

	require.True(t, set.Contains("instagram.com"))
	require.True(t, set.Contains("www.instagram.com"))
	require.True(t, set.Contains("WWW.INSTAGRAM.COM."))

	// Exact-host matching: a subdomain or lookalike is not the same host.
	require.False(t, set.Contains("m.instagram.com"))
	require.False(t, set.Contains("notinstagram.com"))
	require.False(t, set.Contains("instagram.com.evil.example"))
	require.False(t, set.Contains(""))
}

func TestSet_EmptyMatchesNothing(t *testing.T) {
	require.False(t, New(nil).Contains("instagram.com"))
	require.False(t, New([]string{}).Contains("instagram.com"))
}

func TestNew_IgnoresEmptyEntries(t *testing.T) {
	set := New([]string{"", "  ", "instagram.com"})

	require.Len(t, set, 1)
	require.True(t, set.Contains("instagram.com"))
}
