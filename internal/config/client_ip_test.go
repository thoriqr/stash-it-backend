package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// withClientIPEnv sets the three client IP variables for the duration of a test.
//
// They are set together because they are one decision. Setting only one of them
// is not a state worth testing on its own, and it is usually a mistake.
func withClientIPEnv(t *testing.T, source, proxies, header string) {
	t.Helper()

	if source != "" {
		t.Setenv("CLIENT_IP_SOURCE", source)
	}

	if proxies != "" {
		t.Setenv("TRUSTED_PROXIES", proxies)
	}

	if header != "" {
		t.Setenv("TRUSTED_PROXY_HEADER", header)
	}
}

// The default is a direct connection, which is the only default that cannot be
// spoofed. Everything about reading a header has to be asked for.
func TestLoadClientIP_DefaultsToThePeerAddress(t *testing.T) {
	source, proxies, header, err := loadClientIP()
	require.NoError(t, err)

	require.Equal(t, ClientIPSourcePeer, source)
	require.Empty(t, proxies)
	require.Empty(t, header)
}

// Peer mode reads no header at all, so the framework settings it produces leave
// every header-reading switch off.
func TestProxySettings_PeerModeTrustsNothing(t *testing.T) {
	settings := Config{ClientIPSource: ClientIPSourcePeer}.ProxySettings()

	require.False(t, settings.TrustProxy)
	require.Empty(t, settings.Proxies)
	require.Empty(t, settings.ProxyHeader)
	require.False(
		t,
		settings.EnableIPValidation,
		"with no header read there is nothing to validate",
	)
}

// A verified proxy produces exactly the settings that make reading its header
// safe, including the one that is not optional.
func TestProxySettings_ProxyModeEnablesValidation(t *testing.T) {
	settings := Config{
		ClientIPSource:     ClientIPSourceProxy,
		TrustedProxies:     []string{"10.0.0.1", "192.168.1.0/24"},
		TrustedProxyHeader: "X-Forwarded-For",
	}.ProxySettings()

	require.True(t, settings.TrustProxy)
	require.Equal(t, []string{"10.0.0.1", "192.168.1.0/24"}, settings.Proxies)
	require.Equal(t, "X-Forwarded-For", settings.ProxyHeader)

	// Without this the framework returns the header's raw bytes as the client
	// address, so it is not a preference.
	require.True(t, settings.EnableIPValidation)
}

// The allowlist is copied, so a caller holding the returned settings cannot
// change what the application believes by editing one slice.
func TestProxySettings_DoesNotAliasTheAllowlist(t *testing.T) {
	cfg := Config{
		ClientIPSource: ClientIPSourceProxy,
		TrustedProxies: []string{"10.0.0.1"},
	}

	settings := cfg.ProxySettings()
	settings.Proxies[0] = "0.0.0.0/0"

	require.Equal(t, []string{"10.0.0.1"}, cfg.TrustedProxies)
	require.Equal(t, []string{"0.0.0.0/0"}, settings.Proxies)
}

func TestLoadClientIP_ProxyModeRequiresAnAllowlist(t *testing.T) {
	withClientIPEnv(t, "proxy", "", "")

	_, _, _, err := loadClientIP()
	require.Error(
		t,
		err,
		"believing a forwarded address with no verified source is the failure this refuses",
	)
	require.Contains(t, err.Error(), "TRUSTED_PROXIES")
}

func TestLoadClientIP_ProxyModeDefaultsTheHeader(t *testing.T) {
	withClientIPEnv(t, "proxy", "10.0.0.1", "")

	source, proxies, header, err := loadClientIP()
	require.NoError(t, err)

	require.Equal(t, ClientIPSourceProxy, source)
	require.Equal(t, []string{"10.0.0.1"}, proxies)
	require.Equal(t, defaultTrustedProxyHeader, header)
}

func TestLoadClientIP_ProxyModeHonoursAnExplicitHeader(t *testing.T) {
	withClientIPEnv(t, "proxy", "10.0.0.1", "CF-Connecting-IP")

	_, _, header, err := loadClientIP()
	require.NoError(t, err)

	require.Equal(t, "CF-Connecting-IP", header)
}

// Trusting a forwarded address while declaring there is no proxy is a
// contradiction. Refusing it beats guessing which half the operator meant.
func TestLoadClientIP_RejectsProxySettingsInPeerMode(t *testing.T) {
	t.Run("an allowlist", func(t *testing.T) {
		withClientIPEnv(t, "peer", "10.0.0.1", "")

		_, _, _, err := loadClientIP()
		require.Error(t, err)
		require.Contains(t, err.Error(), "TRUSTED_PROXIES")
	})

	t.Run("a header", func(t *testing.T) {
		withClientIPEnv(t, "peer", "", "X-Forwarded-For")

		_, _, _, err := loadClientIP()
		require.Error(t, err)
		require.Contains(t, err.Error(), "TRUSTED_PROXY_HEADER")
	})
}

func TestLoadClientIP_RejectsAnUnknownSource(t *testing.T) {
	withClientIPEnv(t, "cloud-run", "", "")

	_, _, _, err := loadClientIP()
	require.Error(t, err)
	require.Contains(t, err.Error(), "CLIENT_IP_SOURCE")
}

func TestLoadClientIP_AcceptsCaseAndSpacingInTheSource(t *testing.T) {
	withClientIPEnv(t, "  Proxy  ", "10.0.0.1", "")

	source, _, _, err := loadClientIP()
	require.NoError(t, err)

	require.Equal(t, ClientIPSourceProxy, source)
}

func TestLoadClientIP_SplitsAndTrimsAnAllowlist(t *testing.T) {
	withClientIPEnv(t, "proxy", " 10.0.0.1 , 192.168.1.0/24 ,, ", "")

	_, proxies, _, err := loadClientIP()
	require.NoError(t, err)

	require.Equal(t, []string{"10.0.0.1", "192.168.1.0/24"}, proxies)
}

// A bare proxy address is matched against the peer's canonical text, so a
// spelling no peer ever presents would configure an allowlist that silently
// matches nothing — leaving every client through the proxy resolved to the proxy
// and sharing one counter. It is refused here instead, where an operator sees it.
func TestLoadClientIP_RejectsNonCanonicalBareAddresses(t *testing.T) {
	for _, entry := range []string{
		// Long-form IPv6. Canonical is "2001:db8::1".
		"2001:0db8:0000:0000:0000:0000:0000:0001",
		// Uppercase IPv6. Canonical is "2001:db8::1".
		"2001:DB8::1",
		// Mixed case IPv6.
		"2001:DB8::1",
		// IPv4 in its IPv6-mapped form. A peer presents "10.0.0.1".
		"::ffff:10.0.0.1",
	} {
		t.Run(entry, func(t *testing.T) {
			withClientIPEnv(t, "proxy", entry, "")

			_, _, _, err := loadClientIP()

			require.Error(
				t,
				err,
				"%q would never match a peer and must be refused", entry,
			)
			require.Contains(t, err.Error(), "canonical")
		})
	}
}

// The refusal above must not be so eager that it turns away the forms a peer
// really does present.
func TestLoadClientIP_AcceptsCanonicalBareAddresses(t *testing.T) {
	for _, entry := range []string{
		"10.0.0.1",
		"192.168.1.1",
		"203.0.113.7",
		"2001:db8::1",
		"fd00::1",
		"::1",
	} {
		t.Run(entry, func(t *testing.T) {
			withClientIPEnv(t, "proxy", entry, "")

			_, proxies, _, err := loadClientIP()

			require.NoError(t, err, "entry: %s", entry)
			require.Equal(t, []string{entry}, proxies)
		})
	}
}

// A range is matched numerically rather than by text, so it must stay acceptable
// however it is spelled. Refusing ranges for a spelling reason would reject the
// only way to express an IPv6 proxy of a single address.
func TestLoadClientIP_RangesAreNotJudgedOnSpelling(t *testing.T) {
	for _, entry := range []string{
		"10.0.0.0/8",
		"192.168.0.0/16",
		"2001:0db8::/32",
		"2001:DB8::/32",
		"fd00::/8",
	} {
		t.Run(entry, func(t *testing.T) {
			withClientIPEnv(t, "proxy", entry, "")

			_, proxies, _, err := loadClientIP()

			require.NoError(t, err, "entry: %s", entry)
			require.Equal(t, []string{entry}, proxies)
		})
	}
}

// A malformed entry must not become an allowlist that silently trusts nothing,
// because that collapses every client onto the proxy's address with no error
// anywhere to explain it.
func TestLoadClientIP_RejectsAMalformedEntry(t *testing.T) {
	for _, entry := range []string{
		"not-an-address",
		"10.0.0.256",
		"10.0.0.1/33",
		"10.0.0.1/8/8",
		"10.0.0.1/99999999999999999999",
	} {
		t.Run(entry, func(t *testing.T) {
			withClientIPEnv(t, "proxy", entry, "")

			_, _, _, err := loadClientIP()
			require.Error(t, err, "entry: %s", entry)
		})
	}
}

// Trusting every peer is the same as no allowlist, except that it looks like one.
func TestLoadClientIP_RejectsBlanketTrust(t *testing.T) {
	for _, entry := range []string{
		"0.0.0.0/0",
		"::/0",
		// Wider than any real network, and still refused.
		"0.0.0.0/1",
		"::/1",
		// And the address form of the same idea.
		"0.0.0.0",
		"::",
	} {
		t.Run(entry, func(t *testing.T) {
			withClientIPEnv(t, "proxy", entry, "")

			_, _, _, err := loadClientIP()
			require.Error(
				t,
				err,
				"%s trusts every peer and must be refused", entry,
			)
		})
	}
}

// The ranges that are narrow enough to mean something must still be accepted, so
// the blanket-trust rule is not quietly refusing real configuration.
func TestLoadClientIP_AcceptsSpecificRanges(t *testing.T) {
	for _, entry := range []string{
		"10.0.0.1",
		"10.0.0.0/8",
		"192.168.0.0/16",
		"172.16.0.0/12",
		"2001:db8::1",
		"2001:db8::/32",
		"fd00::/8",
	} {
		t.Run(entry, func(t *testing.T) {
			withClientIPEnv(t, "proxy", entry, "")

			_, proxies, _, err := loadClientIP()
			require.NoError(t, err, "entry: %s", entry)
			require.Equal(t, []string{entry}, proxies)
		})
	}
}

// The whole point of Load is that a deployment which cannot resolve a client
// address refuses to start rather than starting with a limiter that cannot work.
//
// Load reads an environment file from the working directory, so each case gets a
// directory of its own with an empty one in it. Running in the repository would
// make the test depend on whatever the developer happens to have there.
func TestLoad_ClientIPFailureStopsStartup(t *testing.T) {
	withLoadableEnv(t)

	t.Setenv("CLIENT_IP_SOURCE", "proxy")
	t.Setenv("TRUSTED_PROXIES", "0.0.0.0/0")

	cfg, err := Load()
	require.Error(t, err)
	require.Equal(t, Config{}, cfg)
	require.Contains(t, err.Error(), "TRUSTED_PROXIES")
}

// A load that succeeds must produce a configuration the framework settings can be
// derived from without another decision being made anywhere.
func TestLoad_ClientIPSettingsSurviveIntoTheConfig(t *testing.T) {
	withLoadableEnv(t)

	t.Setenv("CLIENT_IP_SOURCE", "proxy")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.1,192.168.1.0/24")

	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, ClientIPSourceProxy, cfg.ClientIPSource)
	require.Equal(t, []string{"10.0.0.1", "192.168.1.0/24"}, cfg.TrustedProxies)

	settings := cfg.ProxySettings()
	require.True(t, settings.TrustProxy)
	require.Equal(t, "X-Forwarded-For", settings.ProxyHeader)
	require.True(t, settings.EnableIPValidation)
}

// withLoadableEnv gives Load the environment it requires, in a working directory
// of its own so the repository's own files are not involved.
func withLoadableEnv(t *testing.T) {
	t.Helper()

	t.Chdir(t.TempDir())
	require.NoError(
		t,
		os.WriteFile(".env.development", []byte{}, 0o600),
		"Load reads an environment file and cannot proceed without one",
	)

	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("VERIFICATION_CODE_SECRET", "secret")
	t.Setenv("ACCESS_TOKEN_SECRET", "secret")
	t.Setenv("GOOGLE_CLIENT_ID", "client")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
}
