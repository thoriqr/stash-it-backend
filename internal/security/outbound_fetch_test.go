package security

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsPubliclyRoutable_AllowsPublicAddresses(t *testing.T) {
	addresses := []string{
		// Ordinary public IPv4.
		"1.1.1.1",
		"8.8.8.8",
		"93.184.216.34",
		"172.32.0.1",  // first address above RFC 1918 172.16.0.0/12
		"192.169.0.1", // first address above RFC 1918 192.168.0.0/16
		// Ordinary public IPv6.
		"2606:4700:4700::1111",
		"2a00:1450:4001:80f::200e",
		"2000::", // first address of the global unicast block
		"3fff::", // last address of the global unicast block
	}

	for _, address := range addresses {
		t.Run(address, func(t *testing.T) {
			addr, err := netip.ParseAddr(address)
			require.NoError(t, err)

			require.True(t, isPubliclyRoutable(addr))
		})
	}
}

func TestIsPubliclyRoutable_BlocksUnsafeAddresses(t *testing.T) {
	// Each entry names the category it covers, so that a range added to
	// specialPurposePrefixes without a corresponding case here is visible.
	addresses := map[string]string{
		// Loopback.
		"127.0.0.1":              "loopback",
		"127.1.2.3":              "loopback, whole 127.0.0.0/8",
		"::1":                    "loopback",
		"::ffff:127.0.0.1":       "IPv4 mapped loopback, needs Unmap",
		"::ffff:10.0.0.1":        "IPv4 mapped RFC 1918, needs Unmap",
		"::ffff:169.254.169.254": "IPv4 mapped metadata address, needs Unmap",
		"0:0:0:0:0:0:0:1":        "loopback, fully expanded form",

		// Unspecified.
		"0.0.0.0": "unspecified",
		"::":      "unspecified",

		// RFC 1918 and IPv6 unique local, including both edges of each block.
		"10.0.0.1":        "RFC 1918, first address of 10.0.0.0/8",
		"10.255.255.255":  "RFC 1918, last address of 10.0.0.0/8",
		"172.16.0.0":      "RFC 1918, first address of 172.16.0.0/12",
		"172.31.255.255":  "RFC 1918, last address of 172.16.0.0/12",
		"192.168.0.1":     "RFC 1918",
		"192.168.255.255": "RFC 1918, last address of 192.168.0.0/16",
		"fd00::1":         "IPv6 unique local",
		"fc00::1":         "IPv6 unique local, first address of fc00::/7",
		"fdff::1":         "IPv6 unique local, last address of fc00::/7",

		// Link local, which is where the metadata server lives.
		"169.254.169.254": "link local, GCP and GCE metadata server",
		"169.254.169.253": "link local, GCE DNS resolver",
		"169.254.0.1":     "link local, whole 169.254.0.0/16",
		"fe80::1":         "link local",

		// Zoned, which is only meaningful on the local link.
		"fe80::1%eth0": "zoned",

		// Multicast and broadcast.
		"224.0.0.1":       "multicast",
		"239.255.255.250": "multicast, former mDNS",
		"255.255.255.255": "multicast and broadcast",
		"ff02::1":         "link local multicast",
		"ff01::1":         "interface local multicast",

		// Special purpose IPv4 that netip treats as ordinary global unicast.
		"0.1.2.3":         "special purpose 0.0.0.0/8",
		"100.64.0.1":      "special purpose, carrier grade NAT",
		"100.127.255.255": "special purpose, last address of the carrier grade NAT block",
		"192.0.0.1":       "special purpose, IETF protocol assignments",
		"192.0.2.1":       "special purpose, TEST-NET-1",
		"192.88.99.1":     "special purpose, 6to4 relay anycast",
		"198.18.0.1":      "special purpose, benchmarking",
		"198.51.100.1":    "special purpose, TEST-NET-2",
		"203.0.113.1":     "special purpose, TEST-NET-3",
		"239.255.255.254": "special purpose, last address of the reserved block",

		// Special purpose IPv6.
		"100::1":          "special purpose, discard only",
		"2001::1":         "special purpose, IETF protocol assignments",
		"2001:db8::1":     "special purpose, documentation",
		"2002::1":         "special purpose, 6to4, embeds an IPv4 destination",
		"64:ff9b::1":      "special purpose, NAT64, embeds an IPv4 destination",
		"64:ff9b::7f00:1": "special purpose, NAT64 to 127.0.0.1",
		"fd20:ce::254":    "special purpose, GCP IPv6 metadata server",
	}

	for address, reason := range addresses {
		t.Run(address+" ("+reason+")", func(t *testing.T) {
			addr, err := netip.ParseAddr(address)
			require.NoError(t, err)

			require.False(t, isPubliclyRoutable(addr))
		})
	}
}

func TestIsPubliclyRoutable_BlocksInvalidAddress(t *testing.T) {
	// The zero Addr is what a failed parse leaves behind. Treating it as allowed
	// would turn any parse mistake into an open connection.
	require.False(t, isPubliclyRoutable(netip.Addr{}))
}

func TestGuardedDialControl_AllowsPublicAddress(t *testing.T) {
	addresses := []string{
		"93.184.216.34:80",
		"93.184.216.34:443",
		"1.1.1.1:443",
		"[2606:4700:4700::1111]:443",
	}

	for _, address := range addresses {
		t.Run(address, func(t *testing.T) {
			err := guardedDialControl(
				context.Background(),
				"tcp",
				address,
				nil,
			)

			require.NoError(t, err)
		})
	}
}

func TestGuardedDialControl_BlocksUnsafeAddress(t *testing.T) {
	addresses := []string{
		"127.0.0.1:80",
		"127.0.0.1:443",
		"[::1]:443",
		"10.0.0.1:443",
		"192.168.1.1:80",
		"169.254.169.254:80", // GCP metadata server
		"[fd20:ce::254]:80",  // GCP IPv6 metadata server
		"[::ffff:127.0.0.1]:80",
		"[fe80::1%eth0]:80",
	}

	for _, address := range addresses {
		t.Run(address, func(t *testing.T) {
			err := guardedDialControl(
				context.Background(),
				"tcp",
				address,
				nil,
			)

			require.ErrorIs(t, err, ErrOutboundAddressBlocked)
		})
	}
}

func TestGuardedDialControl_BlocksDisallowedPort(t *testing.T) {
	// A public address is used so that the port check is exercised on its own and
	// the port is confirmed to be the reason for the refusal.
	ports := []uint16{
		0,
		22,    // ssh
		25,    // smtp
		3306,  // mysql
		5432,  // postgres
		6379,  // redis
		8080,  // the API's own port
		9200,  // elasticsearch
		27017, // mongodb
		65535, // last port
	}

	for _, port := range ports {
		t.Run(strconv.Itoa(int(port)), func(t *testing.T) {
			address := netip.AddrPortFrom(
				netip.MustParseAddr("93.184.216.34"),
				port,
			)

			err := guardedDialControl(
				context.Background(),
				"tcp",
				address.String(),
				nil,
			)

			require.ErrorIs(t, err, ErrOutboundAddressBlocked)
			require.Contains(t, err.Error(), "port")
		})
	}
}

func TestGuardedDialControl_BlocksNonIPAddress(t *testing.T) {
	// The dialer resolves before the control function runs, so a name here means
	// the transport is not doing what this policy assumes. It must be refused
	// rather than connected to on trust.
	addresses := []string{
		"example.com:443",
		"localhost:80",
		"",
		"93.184.216.34",       // no port
		"93.184.216.34:https", // named port
		"93.184.216.34:99999", // port out of range
	}

	for _, address := range addresses {
		t.Run(address, func(t *testing.T) {
			err := guardedDialControl(
				context.Background(),
				"tcp",
				address,
				nil,
			)

			require.ErrorIs(t, err, ErrOutboundAddressBlocked)
		})
	}
}

func TestValidateOutboundURL_Allows(t *testing.T) {
	urls := []string{
		"http://example.com",
		"https://example.com",
		"https://example.com/",
		"http://example.com:80",
		"https://example.com:443",
		"HTTPS://EXAMPLE.COM",      // the scheme is case insensitive
		"https://example.com:443/", // explicit default port
		"  https://example.com  ",  // surrounding whitespace is trimmed
		"https://sub.domain.example.co.uk/a/b?c=d#e",
		"https://example.com.",   // fully qualified, trailing dot
		"https://93.184.216.34",  // public literal
		"https://[2606:4700::1]", // public IPv6 literal
	}

	for _, rawURL := range urls {
		t.Run(rawURL, func(t *testing.T) {
			parsed, err := ValidateOutboundURL(rawURL)

			require.NoError(t, err)
			require.NotNil(t, parsed)
			require.NotEmpty(t, parsed.Hostname())
		})
	}
}

func TestValidateOutboundURL_RejectsUnsupportedScheme(t *testing.T) {
	urls := []string{
		"file:///etc/passwd",
		"gopher://example.com/",
		"dict://example.com:11211/",
		"ftp://example.com/file",
		"javascript:alert(1)",
		"data:text/html,<h1>x</h1>",
		"example.com",   // no scheme
		"//example.com", // scheme relative
		"",
	}

	for _, rawURL := range urls {
		t.Run(rawURL, func(t *testing.T) {
			_, err := ValidateOutboundURL(rawURL)

			require.ErrorIs(t, err, ErrOutboundSchemeNotAllowed)
		})
	}
}

func TestValidateOutboundURL_RejectsMissingHost(t *testing.T) {
	urls := []string{
		"http://",
		"https://",
		"http:///path",
		"https:///path",
	}

	for _, rawURL := range urls {
		t.Run(rawURL, func(t *testing.T) {
			_, err := ValidateOutboundURL(rawURL)

			require.ErrorIs(t, err, ErrOutboundHostMissing)
		})
	}
}

func TestValidateOutboundURL_RejectsDisallowedPort(t *testing.T) {
	urls := []string{
		"http://example.com:22",
		"http://example.com:6379",
		"https://example.com:5432",
		"http://example.com:8080",
		"http://example.com:0",
		"http://example.com:65535",
	}

	for _, rawURL := range urls {
		t.Run(rawURL, func(t *testing.T) {
			_, err := ValidateOutboundURL(rawURL)

			require.ErrorIs(t, err, ErrOutboundPortNotAllowed)
		})
	}
}

func TestValidateOutboundURL_RejectsUserinfo(t *testing.T) {
	urls := []string{
		"http://user:pass@example.com/",
		"http://user@example.com/",
		// The disguise. The credential looks like the host to anything reading
		// the wrong part of the URL, while the real destination is example.com
		// and the "user" is the target.
		"http://169.254.169.254@example.com/",
		"http://127.0.0.1@example.com/",
	}

	for _, rawURL := range urls {
		t.Run(rawURL, func(t *testing.T) {
			_, err := ValidateOutboundURL(rawURL)

			require.ErrorIs(t, err, ErrOutboundUserInfoNotAllowed)
		})
	}
}

func TestValidateOutboundURL_RejectsPrivateHostNames(t *testing.T) {
	urls := []string{
		"http://localhost/",
		"http://localhost:8080/admin",
		"http://LOCALHOST/",
		"http://localhost./",
		"http://localhost.localdomain/",
		"http://api.localhost/",
		"http://db.local/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://something.internal/",
		"http://DB.INTERNAL/",
	}

	for _, rawURL := range urls {
		t.Run(rawURL, func(t *testing.T) {
			_, err := ValidateOutboundURL(rawURL)

			require.ErrorIs(t, err, ErrOutboundHostNotAllowed)
		})
	}
}

func TestValidateOutboundURL_RejectsUnsafeLiteralAddress(t *testing.T) {
	urls := []string{
		"http://127.0.0.1/",
		"http://127.0.0.1:80/",
		"https://10.0.0.1/",
		"http://192.168.1.1/",
		"http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token",
		"https://[::1]/",
		"https://[::ffff:127.0.0.1]/",
		"https://[fd20:ce::254]/",
		"http://0.0.0.0/",
		"http://[64:ff9b::7f00:1]/",
		// The address is reported rather than the port when both are wrong, so
		// that a private destination is never hidden behind a port complaint.
		"http://127.0.0.1:6379/",
		"http://10.0.0.1:5432/",
	}

	for _, rawURL := range urls {
		t.Run(rawURL, func(t *testing.T) {
			_, err := ValidateOutboundURL(rawURL)

			require.ErrorIs(t, err, ErrOutboundAddressBlocked)
		})
	}
}

// TestValidateOutboundURL_ReportsTheMostSpecificReason pins the order of the
// checks. Every one of these URLs is refused either way, but the reason a caller
// sees is what tells them whether the URL, the host, or the port was the problem.
func TestValidateOutboundURL_ReportsTheMostSpecificReason(t *testing.T) {
	tests := []struct {
		rawURL    string
		expected  error
		reasoning string
	}{
		{
			rawURL:    "http://localhost:8080/admin",
			expected:  ErrOutboundHostNotAllowed,
			reasoning: "a private host name is more informative than a bad port",
		},
		{
			rawURL:    "http://127.0.0.1:6379/",
			expected:  ErrOutboundAddressBlocked,
			reasoning: "a private literal address is more informative than a bad port",
		},
		{
			rawURL:    "http://example.com:6379/",
			expected:  ErrOutboundPortNotAllowed,
			reasoning: "the host is fine, so the port is the only problem",
		},
		{
			rawURL:    "http://user:pass@example.com:22/",
			expected:  ErrOutboundUserInfoNotAllowed,
			reasoning: "userinfo is refused before anything else is considered",
		},
		{
			rawURL:    "gopher://example.com:70/",
			expected:  ErrOutboundSchemeNotAllowed,
			reasoning: "the scheme is refused before anything else is considered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.rawURL, func(t *testing.T) {
			_, err := ValidateOutboundURL(tt.rawURL)

			require.ErrorIs(t, err, tt.expected, tt.reasoning)
		})
	}
}

func TestValidateOutboundURL_RejectsInvalidURL(t *testing.T) {
	urls := []string{
		"http://exa mple.com/",
		"http://example.com/%zz",
	}

	for _, rawURL := range urls {
		t.Run(rawURL, func(t *testing.T) {
			_, err := ValidateOutboundURL(rawURL)

			require.Error(t, err)
			require.NotErrorIs(t, err, ErrOutboundSchemeNotAllowed)
		})
	}
}

func TestValidateOutboundURL_DoesNotResolveHostNames(t *testing.T) {
	// A public looking host name is deliberately not resolved here. The early
	// check cannot see DNS and the dialer cannot report a clear error, so
	// resolving in both places would be duplicated work with no added safety.
	// Asserted so that the split is not quietly collapsed.
	parsed, err := ValidateOutboundURL("https://example.com/")

	require.NoError(t, err)
	require.Equal(t, "example.com", parsed.Hostname())
}

func TestNewGuardedHTTPClient_AppliesLimits(t *testing.T) {
	policy := DefaultOutboundFetchPolicy()
	client := NewGuardedHTTPClient(policy)

	require.Equal(t, policy.Timeout, client.Timeout)

	wrapped, ok := client.Transport.(*userAgentTransport)
	require.True(
		t,
		ok,
		"the transport must wrap the base transport to set a User-Agent",
	)
	require.Equal(t, policy.UserAgent, wrapped.userAgent)

	transport, ok := wrapped.next.(*http.Transport)
	require.True(t, ok)

	require.Equal(t, policy.ResponseHeaderTimeout, transport.ResponseHeaderTimeout)
	require.Equal(t, policy.TLSHandshakeTimeout, transport.TLSHandshakeTimeout)
	require.Equal(t, policy.MaxIdleConns, transport.MaxIdleConns)
	require.Equal(t, policy.MaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	require.Equal(t, policy.IdleConnTimeout, transport.IdleConnTimeout)
	require.NotNil(t, transport.DialContext)
	require.False(t, transport.DisableCompression)

	// A proxy would connect from its own process, which puts the whole dial time
	// policy out of reach.
	require.Nil(
		t,
		transport.Proxy,
		"a proxy would bypass the dialer policy entirely",
	)
}

func TestNewGuardedHTTPClient_LimitsRedirects(t *testing.T) {
	policy := DefaultOutboundFetchPolicy()
	client := NewGuardedHTTPClient(policy)

	newRequest := func() *http.Request {
		req, err := http.NewRequest(
			http.MethodGet,
			"https://example.com/",
			nil,
		)
		require.NoError(t, err)

		return req
	}

	// A chain of exactly MaxRedirects redirects is followed.
	err := client.CheckRedirect(
		newRequest(),
		make([]*http.Request, policy.MaxRedirects),
	)
	require.NoError(t, err)

	// One more is refused.
	err = client.CheckRedirect(
		newRequest(),
		make([]*http.Request, policy.MaxRedirects+1),
	)
	require.ErrorIs(t, err, ErrTooManyRedirects)
}

func TestNewGuardedHTTPClient_SetsUserAgent(t *testing.T) {
	stub := &stubRoundTripper{}

	transport := &userAgentTransport{
		userAgent: DefaultOutboundUserAgent,
		next:      stub,
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	require.Error(t, err)

	require.NotNil(t, stub.request)
	require.Equal(t, DefaultOutboundUserAgent, stub.request.Header.Get("User-Agent"))

	// The caller's request must not be modified, because a RoundTripper does not
	// own the request it is handed.
	require.Empty(t, req.Header.Get("User-Agent"))
}

func TestNewGuardedHTTPClient_RefusesLoopbackServer(t *testing.T) {
	// httptest binds 127.0.0.1 on an ephemeral port. Reaching it would mean the
	// dialer policy is not installed, so a successful fetch here is the failure.
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(
				"<html><head><title>secret</title></head></html>",
			))
		}),
	)
	defer server.Close()

	client := NewGuardedHTTPClient(DefaultOutboundFetchPolicy())

	resp, err := client.Get(server.URL)
	require.Error(t, err)
	require.Nil(t, resp)

	// The refusal must be recognisable as a policy refusal rather than a
	// transport failure, so that a caller can avoid retrying it.
	require.ErrorIs(t, err, ErrOutboundAddressBlocked)
}

func TestNewGuardedHTTPClient_RefusesUnsafeAddressOnAllowedPort(t *testing.T) {
	// In the loopback server test above the port check fires first, because an
	// ephemeral port is never 80 or 443. These assert the address check itself on
	// an allowed port, which is the case that matters for a service listening on
	// 443 on localhost or on the metadata address.
	addresses := []string{
		"127.0.0.1:80",
		"127.0.0.1:443",
		"169.254.169.254:80",
		"[fd20:ce::254]:80",
		"10.0.0.1:443",
	}

	for _, address := range addresses {
		t.Run(address, func(t *testing.T) {
			err := guardedDialControl(
				context.Background(),
				"tcp",
				address,
				nil,
			)

			require.ErrorIs(t, err, ErrOutboundAddressBlocked)
		})
	}
}

func TestNewGuardedHTTPClient_DoesNotUseDefaultTransport(t *testing.T) {
	// The guarantee this package exists to provide: the guarded client is not the
	// default one and is not built from it.
	client := NewGuardedHTTPClient(DefaultOutboundFetchPolicy())

	require.NotEqual(
		t,
		http.DefaultTransport,
		client.Transport,
		"the guarded client must not be the default transport",
	)

	wrapped, ok := client.Transport.(*userAgentTransport)
	require.True(t, ok)

	require.NotEqual(t, http.DefaultTransport, wrapped.next)
}

func TestDefaultOutboundFetchPolicy(t *testing.T) {
	policy := DefaultOutboundFetchPolicy()

	require.Equal(t, DefaultOutboundUserAgent, policy.UserAgent)
	require.Equal(t, OutboundFetchTimeout, policy.Timeout)
	require.Equal(
		t,
		OutboundFetchResponseHeaderTimeout,
		policy.ResponseHeaderTimeout,
	)
	require.Equal(t, OutboundFetchMaxRedirects, policy.MaxRedirects)
	require.Equal(t, OutboundFetchMaxResponseBytes, policy.MaxResponseBytes)
	require.Equal(t, OutboundFetchDialTimeout, policy.DialTimeout)
	require.Equal(
		t,
		OutboundFetchTLSHandshakeTimeout,
		policy.TLSHandshakeTimeout,
	)
	require.Equal(t, OutboundFetchKeepAlive, policy.KeepAlive)
	require.Equal(t, OutboundFetchIdleConnTimeout, policy.IdleConnTimeout)
	require.Equal(t, OutboundFetchMaxIdleConns, policy.MaxIdleConns)
	require.Equal(t, OutboundFetchMaxIdlePerHost, policy.MaxIdleConnsPerHost)
}

func TestDefaultOutboundFetchPolicy_HasNoZeroLimit(t *testing.T) {
	// A limit that reaches zero silently removes the protection it stands for, so
	// the defaults are asserted rather than left to review.
	policy := DefaultOutboundFetchPolicy()

	require.Positive(t, policy.Timeout)
	require.Positive(t, policy.ResponseHeaderTimeout)
	require.Positive(t, policy.MaxRedirects)
	require.Positive(t, policy.MaxResponseBytes)
	require.Positive(t, policy.DialTimeout)
	require.Positive(t, policy.TLSHandshakeTimeout)
	require.Positive(t, policy.MaxIdleConns)
	require.Positive(t, policy.MaxIdleConnsPerHost)
	require.NotZero(t, policy.KeepAlive)
	require.NotZero(t, policy.IdleConnTimeout)
	require.NotEmpty(t, policy.UserAgent)

	// The header wait must be the tighter of the two request limits, or a stalled
	// origin would occupy a worker slot for the whole outer timeout.
	require.Less(t, policy.ResponseHeaderTimeout, policy.Timeout)
	require.Less(t, policy.DialTimeout, policy.Timeout)
	require.Less(t, policy.TLSHandshakeTimeout, policy.Timeout)
}

func TestAllowedPorts(t *testing.T) {
	// The port allowlist is a security control rather than configuration, so it
	// is asserted. Widening it is a deliberate decision.
	require.Contains(t, allowedPorts, uint16(80))
	require.Contains(t, allowedPorts, uint16(443))
	require.Len(t, allowedPorts, 2)
}

func TestOutboundFetchPolicy_LimitOutboundBody(t *testing.T) {
	policy := OutboundFetchPolicy{MaxResponseBytes: 16}

	t.Run("a short body is read whole", func(t *testing.T) {
		body := policy.LimitOutboundBody(strings.NewReader("short"))

		read, err := io.ReadAll(body)
		require.NoError(t, err)
		require.Equal(t, "short", string(read))
	})

	t.Run("a long body is truncated", func(t *testing.T) {
		body := policy.LimitOutboundBody(
			strings.NewReader(strings.Repeat("a", 1024)),
		)

		read, err := io.ReadAll(body)
		require.NoError(t, err)
		require.Len(t, read, 16)
	})
}

// stubRoundTripper records the request it is given and always fails, so a test can
// inspect what a wrapping RoundTripper produced without touching the network.
type stubRoundTripper struct {
	request *http.Request
}

func (s *stubRoundTripper) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	s.request = req

	return nil, errors.New("stub round tripper")
}
