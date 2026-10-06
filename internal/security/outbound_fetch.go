package security

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// This file is the outbound fetch boundary: the single place where the server
// is allowed to make an HTTP request to a URL a user supplied.
//
// The threat is server side request forgery. A registered user is not a
// trusted caller: the URL is attacker chosen, and the server's network position
// is more privileged than the user's. So the destination has to be constrained
// even though the caller is authenticated.
//
// The design has one idea in it. The authoritative check runs inside the dialer,
// on the address the resolver actually produced, at the moment the connection is
// about to be made. Everything else is either an early rejection that produces a
// clearer error, or a limit on how much work a single fetch can cause.
//
// Validating the URL first and connecting afterwards has a gap in it: a host name
// can resolve to a public address when it is checked and to a private one when it
// is dialled. That is DNS rebinding, and no amount of checking the URL string can
// close it because the URL does not change. Checking inside the dialer closes it
// because there is no longer a gap between the check and the connection. The same
// hook also covers redirect hops and pooled connection reuse for free, since every
// connection the transport opens goes through it.
//
// Nothing here knows what the fetched bytes are used for. Keeping this package
// free of Fiber, of the queue, and of any feature means the HTTP path and the
// background worker cannot drift into using different protection.

// Outbound fetch limits.
//
// These live here rather than at the call site so that every caller fetches under
// the same limits. Enrichment code is expected to read them from
// DefaultOutboundFetchPolicy rather than inventing its own numbers.
const (
	// OutboundFetchTimeout bounds one whole request, including redirects and
	// reading the body. It is the outer bound that makes a slow origin a failure
	// instead of a held worker slot.
	OutboundFetchTimeout = 10 * time.Second

	// OutboundFetchResponseHeaderTimeout bounds the wait between finishing the
	// request write and receiving response headers. A stalled origin usually
	// fails here rather than at the overall timeout, so it is much shorter.
	OutboundFetchResponseHeaderTimeout = 5 * time.Second

	// OutboundFetchMaxRedirects bounds how many redirects are followed. Every hop
	// is revalidated by the dialer, but an unbounded chain is still a way to keep
	// a worker busy, so the count is capped well below the http.Client default.
	OutboundFetchMaxRedirects = 3

	// OutboundFetchMaxResponseBytes bounds how much of a response body is read.
	// Go's transport transparently decompresses a Content-Encoding: gzip response
	// before handing the body over, so applying the limit with a LimitReader on
	// the returned body also bounds a compressed response and a compression bomb.
	OutboundFetchMaxResponseBytes int64 = 2 << 20

	// OutboundFetchDialTimeout bounds establishing one connection, resolution
	// included.
	OutboundFetchDialTimeout = 5 * time.Second

	// OutboundFetchTLSHandshakeTimeout bounds the TLS handshake on its own, so a
	// stalled handshake does not consume the whole request timeout silently.
	OutboundFetchTLSHandshakeTimeout = 5 * time.Second

	// The keep alive and idle pool values are deliberately smaller than the
	// http.DefaultTransport ones. Enrichment bursts tend to hit one origin, and a
	// per host idle pool is what would otherwise accumulate.
	OutboundFetchKeepAlive       = 15 * time.Second
	OutboundFetchIdleConnTimeout = 30 * time.Second
	OutboundFetchMaxIdleConns    = 64
	OutboundFetchMaxIdlePerHost  = 2

	// DefaultOutboundUserAgent identifies this crawler honestly rather than
	// pretending to be a browser. The http.DefaultTransport agent,
	// "Go-http-client/1.1", is refused outright by a meaningful share of sites,
	// which would silently depress the enrichment success rate rather than
	// showing up as an error.
	DefaultOutboundUserAgent = "StashItEnrichmentBot/1.0"
)

// Errors returned by this package. They are sentinels so that a caller can tell a
// policy rejection from a genuine network failure and treat them differently,
// which matters because a rejection must never be retried and a timeout usually
// should be.
var (
	// ErrOutboundURLInvalid is returned when the URL cannot be parsed at all.
	ErrOutboundURLInvalid = errors.New("outbound url is invalid")

	// ErrOutboundSchemeNotAllowed is returned for any scheme other than http or
	// https. This is what keeps file, gopher and dict schemes out, and it also
	// rejects a schemeless value that a caller might have expected to default to
	// http.
	ErrOutboundSchemeNotAllowed = errors.New(
		"outbound url scheme is not allowed",
	)

	// ErrOutboundHostMissing is returned when the URL has no host to connect to.
	ErrOutboundHostMissing = errors.New(
		"outbound url must include a host",
	)

	// ErrOutboundPortNotAllowed is returned by the early URL check for a port
	// other than 80 or 443.
	ErrOutboundPortNotAllowed = errors.New(
		"outbound url port is not allowed",
	)

	// ErrOutboundUserInfoNotAllowed is returned when the URL carries a userinfo
	// component. There is never a reason to send credentials to an origin the
	// user chose, and the component is a classic way to disguise the real host
	// from anything that inspects the wrong part of the URL.
	ErrOutboundUserInfoNotAllowed = errors.New(
		"outbound url must not carry userinfo",
	)

	// ErrOutboundHostNotAllowed is returned for host names that can only resolve
	// inside the caller's own network, such as localhost and .internal.
	ErrOutboundHostNotAllowed = errors.New(
		"outbound url host is not a public internet host",
	)

	// ErrOutboundAddressBlocked is returned when the destination address, after
	// resolution, is not a public internet address, or the dial port is not
	// allowed. This is the error the dialer produces, so it is what a rejection
	// from the security boundary looks like.
	ErrOutboundAddressBlocked = errors.New(
		"outbound address is not a public internet destination",
	)

	// ErrTooManyRedirects is returned when a response chain exceeds
	// OutboundFetchMaxRedirects.
	ErrTooManyRedirects = errors.New("too many redirects")
)

// specialPurposePrefixes are ranges that net/netip does not report as loopback,
// link local, multicast, unspecified or private, but that a server must still
// never dial.
//
// The IPv4 entries are IANA special-purpose registry blocks that otherwise look
// ordinary: the "this host" range, carrier grade NAT, the IETF protocol
// assignments, the three TEST-NET documentation ranges, benchmarking, the 6to4
// relay anycast block, and the reserved range that ends at the broadcast address.
//
// The IPv6 entries matter for two distinct reasons. 64:ff9b::/96 and 2002::/16
// are translation prefixes that embed an IPv4 destination, so a public looking
// IPv6 literal can still reach an internal IPv4 host on the right network. The
// remaining IPv6 entries are special-purpose ranges that are globally routable in
// appearance only.
var specialPurposePrefixes = []netip.Prefix{
	// IPv4 special purpose.
	netip.MustParsePrefix("0.0.0.0/8"),       // "this host" on the old class A model
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast, deprecated
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, includes broadcast

	// IPv6 special purpose and address translation.
	netip.MustParsePrefix("100::/64"),      // discard only
	netip.MustParsePrefix("2001::/23"),     // IETF protocol assignments, Teredo
	netip.MustParsePrefix("2001:db8::/32"), // documentation
	netip.MustParsePrefix("2002::/16"),     // 6to4, embeds an IPv4 destination
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64, embeds an IPv4 destination

	// The GCP IPv6 metadata server. It already falls inside the fc00::/7
	// unique local range that Addr.IsPrivate covers, and is listed explicitly so
	// that the one address most worth rejecting keeps being rejected even if the
	// range checks are ever reordered.
	netip.MustParsePrefix("fd20:ce::254/128"),
}

// blockedHostSuffixes are host name suffixes that can only ever mean something
// inside the caller's own network.
//
// They are rejected by name so that the resolver is never asked about them and
// the rejection is reported as a bad URL. This is a usability measure and not the
// security boundary. A host name that resolves to a private address passes
// ValidateOutboundURL and is refused by the dialer, which is what makes DNS
// rebinding ineffective.
var blockedHostSuffixes = []string{
	".localhost", // RFC 6761 special use
	".local",     // RFC 6762 multicast DNS
	".internal",  // ICANN reserved for private use
}

// allowedPorts are the only ports a fetch may connect to.
//
// Restricting the port set is the highest leverage control in this file. It is
// what closes access to a co-located Redis, a Cloud SQL instance, and any other
// service on a non-standard port, none of which have to be recognised as private
// addresses to be kept out of reach.
var allowedPorts = map[uint16]struct{}{
	80:  {},
	443: {},
}

// OutboundFetchPolicy is the complete set of limits applied to a server side
// fetch of a user supplied URL.
//
// The zero value is deliberately not usable. Start from
// DefaultOutboundFetchPolicy and change only what is needed, so that no caller
// can accidentally drop a limit by declaring a partial struct. The security
// relevant decisions, the allowed schemes, the allowed ports and the address
// policy, are not fields here on purpose: they are not tunable per caller, and
// making them tunable is how an SSRF guard gets turned off by accident.
type OutboundFetchPolicy struct {
	// UserAgent is sent on every fetch. It must identify the crawler rather than
	// impersonate a browser.
	UserAgent string

	// Timeout bounds one whole request including redirects and body reads.
	Timeout time.Duration

	// ResponseHeaderTimeout bounds the wait for response headers.
	ResponseHeaderTimeout time.Duration

	// MaxRedirects is how many redirects may be followed before the chain is
	// refused.
	MaxRedirects int

	// MaxResponseBytes is how much of a response body may be read.
	MaxResponseBytes int64

	// DialTimeout bounds establishing one connection.
	DialTimeout time.Duration

	// TLSHandshakeTimeout bounds the TLS handshake on its own.
	TLSHandshakeTimeout time.Duration

	// KeepAlive is the interval between TCP keepalives on an idle connection.
	KeepAlive time.Duration

	// IdleConnTimeout is how long an idle pooled connection is kept.
	IdleConnTimeout time.Duration

	// MaxIdleConns bounds the whole idle pool.
	MaxIdleConns int

	// MaxIdleConnsPerHost bounds the idle pool for a single origin, which is the
	// one that matters when enrichment bursts hit one site.
	MaxIdleConnsPerHost int
}

// DefaultOutboundFetchPolicy returns the limits documented on the constants in
// this file.
func DefaultOutboundFetchPolicy() OutboundFetchPolicy {
	return OutboundFetchPolicy{
		UserAgent:             DefaultOutboundUserAgent,
		Timeout:               OutboundFetchTimeout,
		ResponseHeaderTimeout: OutboundFetchResponseHeaderTimeout,
		MaxRedirects:          OutboundFetchMaxRedirects,
		MaxResponseBytes:      OutboundFetchMaxResponseBytes,
		DialTimeout:           OutboundFetchDialTimeout,
		TLSHandshakeTimeout:   OutboundFetchTLSHandshakeTimeout,
		KeepAlive:             OutboundFetchKeepAlive,
		IdleConnTimeout:       OutboundFetchIdleConnTimeout,
		MaxIdleConns:          OutboundFetchMaxIdleConns,
		MaxIdleConnsPerHost:   OutboundFetchMaxIdlePerHost,
	}
}

// LimitOutboundBody wraps r so that at most MaxResponseBytes can be read from
// it.
//
// The limit is applied to the stream the caller receives, which for a compressed
// response is the decompressed one, so it bounds a compression bomb as well as an
// oversized page.
//
// Reaching the limit ends the read exactly as a finished body does, so a caller
// that needs to notice truncation should compare the bytes read against the limit
// rather than expecting an error.
func (p OutboundFetchPolicy) LimitOutboundBody(r io.Reader) io.Reader {
	return io.LimitReader(r, p.MaxResponseBytes)
}

// NewGuardedHTTPClient builds the only http.Client this project should use to
// fetch a user supplied URL.
//
// Enrichment code must take the returned client as a constructor argument, must
// never build its own, and must never fall back to http.DefaultClient. That is the
// point of the function: there should be no code path to the network that has not
// passed this policy.
func NewGuardedHTTPClient(policy OutboundFetchPolicy) *http.Client {
	dialer := &net.Dialer{
		Timeout:   policy.DialTimeout,
		KeepAlive: policy.KeepAlive,

		// The security boundary. See guardedDialControl.
		ControlContext: guardedDialControl,
	}

	transport := &http.Transport{
		// Proxy is nil rather than http.ProxyFromEnvironment, on purpose. A proxy
		// performs the connection from its own process, which puts this entire
		// policy out of reach and would turn any HTTP_PROXY in the environment
		// into a bypass.
		Proxy: nil,

		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   policy.TLSHandshakeTimeout,
		ResponseHeaderTimeout: policy.ResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,

		MaxIdleConns:        policy.MaxIdleConns,
		MaxIdleConnsPerHost: policy.MaxIdleConnsPerHost,
		IdleConnTimeout:     policy.IdleConnTimeout,

		DisableCompression: false,
		ForceAttemptHTTP2:  true,
	}

	return &http.Client{
		Transport: &userAgentTransport{
			userAgent: policy.UserAgent,
			next:      transport,
		},
		Timeout: policy.Timeout,
		CheckRedirect: func(
			req *http.Request,
			via []*http.Request,
		) error {
			// via holds the requests already sent, oldest first, so its length is
			// the number of redirects already followed. Comparing against
			// MaxRedirects this way allows exactly MaxRedirects redirects and
			// refuses the next one.
			//
			// The hop is not re-checked for scheme or port here because it does
			// not need to be: the dialer validates the address and the port of
			// every connection this client opens, including this one.
			if len(via) > policy.MaxRedirects {
				return ErrTooManyRedirects
			}

			return nil
		},
	}
}

// guardedDialControl refuses a connection whose resolved destination is not a
// public internet address on an allowed port.
//
// This is the security boundary for outbound fetches, and it is a dialer control
// function rather than a request check for one reason: by the time it runs, the
// resolver has already produced the address and the connection has not been made
// yet. That is the only moment at which a rebinding attack cannot apply, because
// the value being checked is the value being connected to.
//
// It also covers cases that a per-request check would miss. Every redirect hop
// opens a new connection through this same hook, so a redirect to an internal
// address is refused without any redirect specific code. A pooled connection
// reused across requests goes through it too. And the port is checked here rather
// than only on the URL, because a redirect can change the port.
//
// The raw connection is not inspected and the context is not consulted: by the
// time a control function runs the decision to connect has already been made, and
// all this can do is refuse.
func guardedDialControl(
	_ context.Context,
	_ string,
	address string,
	_ syscall.RawConn,
) error {
	// The dialer has already resolved the host by now, so anything that is not
	// an ip:port pair here is not the address this policy understands, and is
	// refused rather than connected to on trust.
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf(
			"%w: dial address %q is not an ip:port pair",
			ErrOutboundAddressBlocked,
			address,
		)
	}

	if _, allowed := allowedPorts[addrPort.Port()]; !allowed {
		return fmt.Errorf(
			"%w: port %d is not allowed",
			ErrOutboundAddressBlocked,
			addrPort.Port(),
		)
	}

	if !isPubliclyRoutable(addrPort.Addr()) {
		return fmt.Errorf(
			"%w: %s",
			ErrOutboundAddressBlocked,
			addrPort.Addr(),
		)
	}

	return nil
}

// ValidateOutboundURL parses a user supplied URL and rejects it unless it is a
// plain http or https URL on port 80 or 443 with no userinfo and a host that is
// not private by name.
//
// It additionally rejects a URL whose host is a literal address that is not
// publicly routable, so an obvious attempt is reported as a bad URL instead of
// surfacing later as a failed dial.
//
// This is an early check, for clear errors and to keep obviously bad input from
// reaching DNS. It is not the security boundary and it must not be treated as one.
// A host name that resolves to a private address passes here and is refused by
// the dialer; that division is the reason DNS rebinding does not work.
func ValidateOutboundURL(rawURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOutboundURLInvalid, err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf(
			"%w: %q",
			ErrOutboundSchemeNotAllowed,
			parsed.Scheme,
		)
	}

	// Userinfo is refused before the host is read. http://user:pass@host/ keeps
	// the real destination in Hostname and leaves Host carrying the credentials,
	// so anything inspecting the wrong field sees the wrong host, and there is
	// never a reason to send credentials to an origin the user chose.
	if parsed.User != nil {
		return nil, ErrOutboundUserInfoNotAllowed
	}

	// Hostname, never Host. See above.
	hostname := parsed.Hostname()
	if hostname == "" {
		return nil, ErrOutboundHostMissing
	}

	// The host checks run before the port check so that the most specific reason
	// is the one reported. "localhost:8080" is refused because localhost is never
	// reachable, which is more useful to say than "8080 is not an allowed port".
	// The order only affects the error; every one of these rejects the URL.
	if isBlockedHostName(hostname) {
		return nil, fmt.Errorf(
			"%w: %q",
			ErrOutboundHostNotAllowed,
			hostname,
		)
	}

	// A literal address is already resolved, so it can be judged here. A host
	// name cannot be, and is left entirely to the dialer.
	if addr, err := netip.ParseAddr(hostname); err == nil {
		if !isPubliclyRoutable(addr) {
			return nil, fmt.Errorf(
				"%w: %s",
				ErrOutboundAddressBlocked,
				addr,
			)
		}
	}

	// url.Parse has already rejected a non numeric port, so comparing the text is
	// enough and avoids parsing it a second time.
	if port := parsed.Port(); port != "" && !isAllowedPortString(port) {
		return nil, fmt.Errorf(
			"%w: %q",
			ErrOutboundPortNotAllowed,
			port,
		)
	}

	return parsed, nil
}

// isPubliclyRoutable reports whether addr is a destination a server should be
// willing to fetch from.
//
// The order of the checks matters. The zone is rejected first because a zoned
// address is only meaningful on the local link. Unmapping happens next because an
// IPv4 mapped IPv6 address is not loopback or link local to net/netip until it is
// reduced to its IPv4 form, so ::ffff:127.0.0.1 would otherwise be judged an
// ordinary address.
func isPubliclyRoutable(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}

	// A zone such as the one in fe80::1%eth0 only has meaning on the local link
	// and must never be dialled from a server.
	if addr.Zone() != "" {
		return false
	}

	addr = addr.Unmap()

	// IsGlobalUnicast already excludes unspecified, loopback, multicast and both
	// kinds of link local address. IsPrivate adds RFC 1918 and IPv6 unique local,
	// which net/netip treats as ordinary global unicast.
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}

	for _, prefix := range specialPurposePrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}

	return true
}

// isBlockedHostName reports whether a host name can only resolve inside the
// caller's own network.
//
// A fully qualified name with a trailing dot is reduced first, so "localhost."
// is recognised rather than slipping through as a distinct name.
func isBlockedHostName(host string) bool {
	normalized := strings.ToLower(strings.TrimSuffix(host, "."))

	// Names that are private but carry none of the suffixes below.
	switch normalized {
	case "localhost", "localhost.localdomain":
		return true
	}

	for _, suffix := range blockedHostSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}

	return false
}

// isAllowedPortString reports whether the textual port from a parsed URL is
// allowed.
//
// url.Parse only admits a numeric port, so comparing the text is exact and avoids
// parsing it a second time. An absent port is not passed here: the transport fills
// in 80 or 443 from the scheme, and the dialer checks the resulting port.
func isAllowedPortString(port string) bool {
	return port == "80" || port == "443"
}

// userAgentTransport applies the configured User-Agent to every request.
//
// It exists because http.Transport has nowhere to put one and http.Client sets no
// default header, so without this the default Go agent would go out instead.
type userAgentTransport struct {
	userAgent string
	next      http.RoundTripper
}

func (t *userAgentTransport) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	// The caller's request is not mutated. A RoundTripper must not modify the
	// request it is given, so the header is set on a clone.
	cloned := req.Clone(req.Context())
	cloned.Header.Set("User-Agent", t.userAgent)

	return t.next.RoundTrip(cloned)
}
