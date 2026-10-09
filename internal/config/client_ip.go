package config

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// ClientIPSource declares where the client address comes from.
//
// It is a declaration rather than a default because the deployment is not
// decided, and the two deployment shapes need genuinely different handling. An
// application reached directly knows its caller by the socket; an application
// behind a load balancer or reverse proxy sees only that hop, and its real caller
// is in a header that anybody can write. Inferring one from the other produces a
// limiter that either trusts a client-controlled value or groups every client
// together, and neither failure is visible in the responses.
//
// Naming the source forces the choice to be made where it can be reviewed, and
// makes the proxy settings that follow from it a consequence rather than a
// separate decision that can disagree with it.
type ClientIPSource string

const (
	// ClientIPSourcePeer means clients connect directly and the address of the
	// socket is the client's own.
	//
	// No forwarding header is read in this mode, whatever the request contains.
	// A deployment behind a proxy that leaves this set will see every client as
	// the proxy, which puts them all in one counter; that is a misconfiguration
	// with a visible symptom rather than a silent one.
	ClientIPSourcePeer ClientIPSource = "peer"

	// ClientIPSourceProxy means clients reach the application through a proxy and
	// the real address is carried in a forwarding header.
	//
	// It requires an explicit list of the proxies that are allowed to set that
	// header, because the header is only meaningful when something in front of
	// the application is known to have written it.
	ClientIPSourceProxy ClientIPSource = "proxy"
)

// defaultTrustedProxyHeader is the header a proxy mode reads.
//
// It is the near-universal name for the chain, and the alternative would be to
// make an operator spell it out for a setting that almost always has one right
// answer. A deployment using something else sets it explicitly.
const defaultTrustedProxyHeader = "X-Forwarded-For"

// ProxySettings is the framework-level view of the declared client IP source.
//
// It is a plain struct rather than a framework type so that `internal/config`
// stays free of a web framework. The composition root maps it onto whatever it
// runs, which is the one place that should know both.
type ProxySettings struct {
	// TrustProxy enables reading the client address from a forwarding header
	// instead of the socket. It is false unless a proxy source was declared and
	// its proxies were listed, so nothing about header trust is implicit.
	TrustProxy bool

	// Proxies is the allowlist of proxy addresses or CIDR ranges whose forwarded
	// values may be believed. It is empty whenever TrustProxy is false.
	Proxies []string

	// ProxyHeader names the header carrying the forwarded chain. It is empty
	// whenever TrustProxy is false, which is what makes a header present in a
	// request mean nothing at all.
	ProxyHeader string

	// EnableIPValidation makes the framework parse and validate the forwarded
	// chain rather than handing back its raw contents.
	//
	// It is not an optional refinement. Without it a trusted-proxy read returns
	// the header's bytes verbatim, so a client behind a proxy whose chain is
	// "203.0.113.7, 198.51.100.4" would be keyed on that whole string: a
	// different key for every chain it chose to send, and therefore a different
	// budget for every chain it chose to send. It only ever has to be on when
	// TrustProxy is on.
	EnableIPValidation bool
}

// loadClientIP reads and validates the client IP configuration.
//
// Every rejection here is a configuration that would otherwise produce a limiter
// that cannot be trusted, and each one is refused at startup rather than
// discovered by the first caller it affects.
func loadClientIP() (ClientIPSource, []string, string, error) {
	source := ClientIPSource(strings.ToLower(strings.TrimSpace(
		os.Getenv("CLIENT_IP_SOURCE"),
	)))

	if source == "" {
		source = ClientIPSourcePeer
	}

	switch source {
	case ClientIPSourcePeer, ClientIPSourceProxy:
	default:
		return "", nil, "", fmt.Errorf(
			"CLIENT_IP_SOURCE must be %q or %q, got %q",
			ClientIPSourcePeer,
			ClientIPSourceProxy,
			source,
		)
	}

	proxies := splitList(os.Getenv("TRUSTED_PROXIES"))

	header := strings.TrimSpace(os.Getenv("TRUSTED_PROXY_HEADER"))

	if source == ClientIPSourcePeer {
		// Trusting a forwarded address while declaring there is no proxy is a
		// contradiction, and which half to honour is not a question worth guessing
		// at. Neither half is dangerous on its own here, because both are ignored
		// unless the mode says otherwise, so the mistake is refused rather than
		// resolved.
		if len(proxies) > 0 {
			return "", nil, "", fmt.Errorf(
				"TRUSTED_PROXIES is set but CLIENT_IP_SOURCE is %q; "+
					"set CLIENT_IP_SOURCE=%q to use them",
				ClientIPSourcePeer,
				ClientIPSourceProxy,
			)
		}

		if header != "" {
			return "", nil, "", fmt.Errorf(
				"TRUSTED_PROXY_HEADER is set but CLIENT_IP_SOURCE is %q; "+
					"set CLIENT_IP_SOURCE=%q to use it",
				ClientIPSourcePeer,
				ClientIPSourceProxy,
			)
		}

		return source, nil, "", nil
	}

	if len(proxies) == 0 {
		return "", nil, "", fmt.Errorf(
			"CLIENT_IP_SOURCE=%q requires TRUSTED_PROXIES listing the proxies "+
				"allowed to set %s; refusing to believe a forwarded address from "+
				"any source",
			ClientIPSourceProxy,
			defaultTrustedProxyHeader,
		)
	}

	for _, proxy := range proxies {
		if err := validateProxyEntry(proxy); err != nil {
			return "", nil, "", err
		}
	}

	if header == "" {
		header = defaultTrustedProxyHeader
	}

	return source, proxies, header, nil
}

// validateProxyEntry rejects one entry that could not be a proxy address.
//
// A malformed entry is not ignored: an operator who mistyped a CIDR would
// otherwise end up with an allowlist that silently trusts nothing, and every
// client collapsed onto the proxy's address, with no error anywhere to explain it.
func validateProxyEntry(entry string) error {
	// A range is matched numerically against the peer's address rather than by
	// comparing text, so its spelling does not have to be canonical and is not
	// checked for that below.
	if _, _, err := net.ParseCIDR(entry); err == nil {
		return validateProxyNetwork(entry)
	}

	// Parsed with `net` rather than `net/netip` on purpose. This is the same
	// package the framework matches with, and the two do not render one address
	// the same way: `net.IP` renders an IPv4-mapped IPv6 address as its IPv4
	// form, while `netip.Addr` keeps the mapped form. Checking canonicality with
	// the wrong one would accept an entry that could never match.
	ip := net.ParseIP(entry)
	if ip == nil {
		return fmt.Errorf(
			"TRUSTED_PROXIES entry %q is not an IP address or CIDR range",
			entry,
		)
	}

	if ip.IsUnspecified() {
		return fmt.Errorf(
			"TRUSTED_PROXIES entry %q is the unspecified address, "+
				"which trusts every peer",
			entry,
		)
	}

	// A bare address is matched as text against the peer's canonical form, so it
	// has to be written exactly as that form. The framework keeps the configured
	// string as it was given and compares it to the incoming address rendered
	// canonically, so any other spelling is accepted here and then never matches
	// anything at all.
	//
	// That failure is silent and it is the worst one available: the peer is
	// never recognised as a proxy, the forwarded address is never read, and
	// every client through it resolves to the proxy's own address and shares one
	// counter. Refusing the spelling at startup turns this into a configuration
	// error an operator sees, rather than rate limiting imposed on unrelated
	// users because of how they wrote an address down.
	//
	// Long-form and uppercase IPv6, and IPv4 written in its IPv6-mapped form, are
	// all valid addresses that no peer ever presents in that spelling.
	if canonical := ip.String(); canonical != entry {
		return fmt.Errorf(
			"TRUSTED_PROXIES entry %q is not in its canonical form %q; "+
				"a proxy address is matched by its canonical text, so any "+
				"other spelling would silently never match",
			entry,
			canonical,
		)
	}

	return nil
}

// validateProxyNetwork rejects a CIDR range so broad it cannot be an allowlist.
//
// The floor is /8 for both families. Anything narrower than that is a supernet
// covering most of the internet, which cannot describe a set of proxies: the
// ranges anyone actually deploys a proxy in — RFC1918 space, IPv6 unique-local
// space — are all /8 or narrower, so this refuses configurations nobody intended
// without refusing any they might.
func validateProxyNetwork(entry string) error {
	_, network, err := net.ParseCIDR(entry)
	if err != nil {
		return fmt.Errorf(
			"TRUSTED_PROXIES entry %q is not a valid CIDR range",
			entry,
		)
	}

	const minimumPrefix = 8

	prefix, _ := network.Mask.Size()

	if prefix < minimumPrefix {
		return fmt.Errorf(
			"TRUSTED_PROXIES entry %q covers most of the internet; "+
				"list the proxies individually or use a range of /%d or narrower",
			entry,
			minimumPrefix,
		)
	}

	return nil
}

// ProxySettings returns the framework configuration implied by the declared
// client IP source.
//
// Everything about header trust is derived here rather than configured twice, so
// there is no combination of settings in which a header is read without a
// verified allowlist beside it.
func (c Config) ProxySettings() ProxySettings {
	if c.ClientIPSource != ClientIPSourceProxy || len(c.TrustedProxies) == 0 {
		return ProxySettings{}
	}

	// Copied rather than aliased: the caller may hold on to the returned
	// settings, and a shared slice would let one change what the other believes.
	proxies := make([]string, len(c.TrustedProxies))
	copy(proxies, c.TrustedProxies)

	return ProxySettings{
		TrustProxy:  true,
		Proxies:     proxies,
		ProxyHeader: c.TrustedProxyHeader,
		// EnableIPValidation is mandatory whenever a header is read. See the
		// field's own comment for what happens without it.
		EnableIPValidation: true,
	}
}

// splitList reads a comma-separated environment value into trimmed entries,
// dropping empty ones so a trailing comma is not an empty entry.
func splitList(value string) []string {
	var entries []string

	for _, part := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			entries = append(entries, trimmed)
		}
	}

	return entries
}
