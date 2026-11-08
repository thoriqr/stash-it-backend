package enrichment

import (
	"errors"
	"strings"

	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Request profiles.
//
// A profile is the set of request headers that differ between hosts. It is
// deliberately narrow: nothing here changes the outbound security policy, the
// client, the redirect handling or the limits, all of which live in
// internal/security and are identical for every host. A profile only chooses the
// Accept and User-Agent headers, which is exactly what some sites gate their
// product responses on.
//
// Selection uses the hostname parsed from the original submitted URL, never a
// redirect destination. Followed hops are still validated by the guarded client,
// but they cannot switch which profile a fetch uses.

// requestProfile is the header set used for one class of hosts.
type requestProfile struct {
	// accept is sent as the Accept header.
	accept string

	// userAgent, when non-empty, is sent as the User-Agent header. An empty value
	// means the request carries none and the guarded client's transport applies
	// its configured default, which is the honest crawler agent.
	userAgent string
}

var (
	// genericRequestProfile is the fallback for every host without a verified,
	// host-specific profile. It asks for HTML and leaves the User-Agent to the
	// guarded client's default.
	genericRequestProfile = requestProfile{
		accept:    enrichAcceptHeader,
		userAgent: "",
	}

	// tokopediaRequestProfile is the profile verified against a real product page
	// and a local worker run. It is applied only to the exact hostnames in
	// marketplaceRequestProfiles, never globally.
	tokopediaRequestProfile = requestProfile{
		accept:    "*/*",
		userAgent: "Mozilla/5.0",
	}
)

// marketplaceRequestProfiles maps an exact, normalized hostname to the profile
// its requests use.
//
// The key is a complete hostname compared by exact equality. There is deliberately
// no suffix or substring matching, so lookalikes such as tokopedia.com.evil.example,
// notokopedia.com or an unsupported subdomain such as m.tokopedia.com do not
// match and fall back to the generic profile.
var marketplaceRequestProfiles = map[string]requestProfile{
	"tokopedia.com":     tokopediaRequestProfile,
	"www.tokopedia.com": tokopediaRequestProfile,
}

// normalizeHostname lowercases a hostname and drops one trailing dot, so a fully
// qualified name such as "WWW.Tokopedia.com." matches the same entry as
// "www.tokopedia.com".
func normalizeHostname(hostname string) string {
	trimmed := strings.TrimSpace(hostname)
	trimmed = strings.TrimSuffix(trimmed, ".")

	return strings.ToLower(trimmed)
}

// requestProfilesFor returns the profiles to try for a hostname, in order.
//
// An unknown host gets only the generic profile. A host with a verified profile
// gets that profile first and the generic profile second, which is what leaves the
// generic fallback reachable without making it the default.
func requestProfilesFor(hostname string) []requestProfile {
	if profile, ok := marketplaceRequestProfiles[normalizeHostname(hostname)]; ok {
		return []requestProfile{profile, genericRequestProfile}
	}

	return []requestProfile{genericRequestProfile}
}

// shouldFallBackToGeneric reports whether a failed attempt is worth retrying once
// with the generic profile.
//
// Only a content failure qualifies, because only content failures are plausibly
// caused by the headers: a terminal client status (401, 403, 404, 410) or a body
// that was not HTML. Everything else is left alone. A fetch failure is a network
// event, a 429 or 5xx is a "not right now" answer, and the worker's own retry
// already exists for those. A refused destination or an over-long redirect chain
// is a property of the URL, so a different User-Agent cannot change it.
func shouldFallBackToGeneric(err error) bool {
	if Kind(err) != FailureContent {
		return false
	}

	if errors.Is(err, security.ErrOutboundAddressBlocked) ||
		errors.Is(err, security.ErrTooManyRedirects) {
		return false
	}

	return true
}
