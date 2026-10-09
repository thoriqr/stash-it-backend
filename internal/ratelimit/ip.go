package ratelimit

import (
	"net"
	"net/netip"
	"strings"
)

// NormalizeIP canonicalizes a client address so that every spelling of one
// address lands on a single limiter identity.
//
// This is the counterpart of normalizing an email address before using it as a
// limiter subject, and it exists for the same reason. A subject that is a raw
// string is a subject an attacker can vary: `192.0.2.1`, `::ffff:192.0.2.1`,
// `[::ffff:192.0.2.1]`, `192.0.2.1:44320` and `192.0.2.1%eth0` are one client
// reached by several routes, and each spelling handed to the limiter is a
// different key holding a different budget. One client behind one NAT would then
// get one budget per spelling it happened to use.
//
// Unmapping is what makes the IPv4-mapped IPv6 case collapse: a proxy that
// reports the client as `::ffff:192.0.2.1` and a direct connection that reports
// `192.0.2.1` are the same client, and a deployment that switches between the two
// must not silently hand it two budgets.
//
// It returns false for anything that is not an address. A caller that cannot
// resolve who it is talking to should refuse the request rather than invent a
// subject, because every unresolvable request sharing one invented subject is a
// shared counter that blocks unrelated callers.
func NormalizeIP(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", false
	}

	// A peer address arrives as host:port. SplitHostPort fails on a bare address
	// and on a bare IPv6 literal, in which case the address is already what we
	// want, so a failure is not an error here.
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}

	// A link-local address may carry a zone. The zone says which interface, not
	// which client, so two requests from the same host through different
	// interfaces are one client.
	if zone := strings.IndexByte(addr, '%'); zone >= 0 {
		addr = addr[:zone]
	}

	parsed, err := netip.ParseAddr(addr)
	if err != nil {
		return "", false
	}

	return parsed.Unmap().String(), true
}

// IsUnspecifiedIP reports whether an address is the unspecified address, which
// means the peer was not actually identified.
//
// It is a real address, so it is a valid subject, and on a socket that never
// resolves it will be the address every such connection shares. It is reported
// separately because that outcome is worth an operator knowing about: a
// deployment seeing it means the client IP is not being resolved the way the
// deployment expects, and every client behind the unresolved hop is being counted
// as one.
func IsUnspecifiedIP(addr string) bool {
	parsed, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}

	return parsed.Unmap().IsUnspecified()
}
