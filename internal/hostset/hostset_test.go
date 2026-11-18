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

func TestPlatformMap_ContainsAndPlatformFor(t *testing.T) {
	platforms := NewPlatformMap(map[string]string{
		"instagram.com":      "Instagram",
		"WWW.Instagram.com.": "Instagram",
		"facebook.com":       "Facebook",
	})

	require.True(t, platforms.Contains("instagram.com"))
	require.True(t, platforms.Contains("WWW.INSTAGRAM.COM."))

	platform, ok := platforms.PlatformFor("instagram.com")
	require.True(t, ok)
	require.Equal(t, "Instagram", platform)

	platform, ok = platforms.PlatformFor("facebook.com")
	require.True(t, ok)
	require.Equal(t, "Facebook", platform)

	// Exact-host matching, the same as Set.
	require.False(t, platforms.Contains("m.instagram.com"))
	require.False(t, platforms.Contains("notinstagram.com"))

	_, ok = platforms.PlatformFor("example.com")
	require.False(t, ok)
}

// A half-configured entry cannot be used on its own, so it is dropped rather than
// stored with a missing half.
func TestNewPlatformMap_DropsIncompleteEntries(t *testing.T) {
	platforms := NewPlatformMap(map[string]string{
		"":              "Instagram",
		"facebook.com":  "",
		"instagram.com": "Instagram",
	})

	require.Len(t, platforms, 1)
	require.True(t, platforms.Contains("instagram.com"))
}

func TestPlatformMap_EmptyMatchesNothing(t *testing.T) {
	require.False(t, NewPlatformMap(nil).Contains("instagram.com"))
	require.False(t, NewPlatformMap(map[string]string{}).Contains("instagram.com"))
}

func TestPlatformMap_HostsAndEntries(t *testing.T) {
	platforms := NewPlatformMap(map[string]string{
		"www.instagram.com": "Instagram",
		"instagram.com":     "Instagram",
		"facebook.com":      "Facebook",
	})

	require.Equal(
		t,
		[]string{"facebook.com", "instagram.com", "www.instagram.com"},
		platforms.Hosts(),
	)
	require.Equal(
		t,
		[]string{
			"facebook.com=Facebook",
			"instagram.com=Instagram",
			"www.instagram.com=Instagram",
		},
		platforms.Entries(),
	)
}
