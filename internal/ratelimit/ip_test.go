package ratelimit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Every spelling of one address has to land on one key. A subject that is a raw
// string is a subject a client can vary, and two spellings of one client would be
// two budgets for one client.
func TestNormalizeIP(t *testing.T) {
	cases := map[string]struct {
		addr    string
		want    string
		wantOK  bool
		whyThis string
	}{
		"an IPv4 address": {
			addr:    "192.0.2.1",
			want:    "192.0.2.1",
			wantOK:  true,
			whyThis: "the ordinary case",
		},
		"an IPv4 address with a port": {
			addr:    "192.0.2.1:54321",
			want:    "192.0.2.1",
			wantOK:  true,
			whyThis: "a peer address arrives with a port",
		},
		"surrounding whitespace": {
			addr:    "  192.0.2.1  ",
			want:    "192.0.2.1",
			wantOK:  true,
			whyThis: "a header value may be padded",
		},
		// This is the case that matters most. The same client reported through
		// an IPv4-mapped IPv6 address must not get a second budget.
		"an IPv4-mapped IPv6 address collapses to IPv4": {
			addr:    "::ffff:192.0.2.1",
			want:    "192.0.2.1",
			wantOK:  true,
			whyThis: "a proxy and a socket disagree about the same client",
		},
		"an IPv4-mapped IPv6 address with a port": {
			addr:    "[::ffff:192.0.2.1]:44321",
			want:    "192.0.2.1",
			wantOK:  true,
			whyThis: "the mapped form still reaches a socket with a port",
		},
		"an IPv6 address": {
			addr:    "2001:db8::1",
			want:    "2001:db8::1",
			wantOK:  true,
			whyThis: "the ordinary IPv6 case",
		},
		"an IPv6 address with a port": {
			addr:    "[2001:db8::1]:44321",
			want:    "2001:db8::1",
			wantOK:  true,
			whyThis: "a bracketed literal reaches a socket with a port",
		},
		// A zone names an interface, not a client. Two requests from one host
		// through two interfaces are one client.
		"a zoned IPv6 address drops the zone": {
			addr:    "fe80::1%eth0",
			want:    "fe80::1",
			wantOK:  true,
			whyThis: "the interface is not part of the client",
		},
		"a zoned and ported IPv6 address drops both": {
			addr:    "[fe80::1%eth0]:44321",
			want:    "fe80::1",
			wantOK:  true,
			whyThis: "a peer address arrives fully decorated",
		},
		"the unspecified address is a real address": {
			addr:    "0.0.0.0",
			want:    "0.0.0.0",
			wantOK:  true,
			whyThis: "it is a value, and refusing it here would hide which one",
		},
		"an empty address": {
			addr:   "",
			wantOK: false,
		},
		"whitespace only": {
			addr:   "   ",
			wantOK: false,
		},
		// A forwarded chain reaches the limiter only when the framework has been
		// configured to read one, and then it returns a single address. If one
		// ever arrived here it would be a configuration mistake, and inventing a
		// subject from it would silently merge two clients.
		"a forwarded chain": {
			addr:   "203.0.113.7, 198.51.100.4",
			wantOK: false,
		},
		"a host name": {
			addr:   "example.com",
			wantOK: false,
		},
		"not an address at all": {
			addr:   "<nil>",
			wantOK: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := NormalizeIP(tc.addr)

			require.Equal(t, tc.wantOK, ok, "why: %s", tc.whyThis)
			require.Equal(t, tc.want, got)
		})
	}
}

// The property the whole function exists for, stated directly rather than left to
// be inferred from the table above.
func TestNormalizeIP_EquivalentSpellingsShareOneIdentity(t *testing.T) {
	limiter := New(nil, []byte(testSecret))

	sameClient := []string{
		"192.0.2.1",
		"  192.0.2.1  ",
		"192.0.2.1:54321",
		"::ffff:192.0.2.1",
		"[::ffff:192.0.2.1]:44321",
	}

	want := limiter.key("pin-ip", "192.0.2.1")

	for _, spelling := range sameClient {
		t.Run(spelling, func(t *testing.T) {
			normalized, ok := NormalizeIP(spelling)
			require.True(t, ok)

			require.Equal(
				t,
				want,
				limiter.key("pin-ip", normalized),
				"one client must not get a budget per spelling of its address",
			)
		})
	}
}

// Two clients are two budgets, or one client could spend another's.
func TestNormalizeIP_DifferentClientsGetDifferentIdentities(t *testing.T) {
	limiter := New(nil, []byte(testSecret))

	different := []string{
		"192.0.2.1",
		"192.0.2.2",
		"198.51.100.1",
		"2001:db8::1",
		"2001:db8::2",
		"203.0.113.7",
	}

	seen := map[string]string{}

	for _, addr := range different {
		normalized, ok := NormalizeIP(addr)
		require.True(t, ok, "addr: %s", addr)

		key := limiter.key("pin-ip", normalized)

		previous, collided := seen[key]
		require.False(
			t,
			collided,
			"%s and %s share a budget", previous, addr,
		)

		seen[key] = addr
	}
}

func TestIsUnspecifiedIP(t *testing.T) {
	require.True(t, IsUnspecifiedIP("0.0.0.0"))
	require.True(t, IsUnspecifiedIP("::"))
	require.True(t, IsUnspecifiedIP("::ffff:0.0.0.0"))

	require.False(t, IsUnspecifiedIP("192.0.2.1"))
	require.False(t, IsUnspecifiedIP("2001:db8::1"))
	require.False(t, IsUnspecifiedIP(""))
	require.False(t, IsUnspecifiedIP("not-an-address"))
}
