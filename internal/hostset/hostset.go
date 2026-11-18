// Package hostset matches a URL hostname against a configured allowlist.
//
// It is a leaf utility with no dependencies, used by the features that need to
// answer "is this host one the operator listed". The list itself is loaded from
// the environment by internal/config; this package only normalizes and compares.
package hostset

import (
	"sort"
	"strings"
)

// Set is an exact-hostname allowlist.
//
// Matching is exact, never by suffix or substring, so a lookalike such as
// notinstagram.com cannot match instagram.com. An empty set matches nothing,
// which is how "the feature is off" is expressed.
type Set map[string]struct{}

// New builds a Set from configured hostnames.
//
// Each entry is trimmed, lowercased and stripped of one trailing dot, which is
// the same normalization the hostname parsed from a URL receives. Empty entries
// are ignored.
func New(hosts []string) Set {
	if len(hosts) == 0 {
		return nil
	}

	set := make(Set, len(hosts))

	for _, host := range hosts {
		if normalized := Normalize(host); normalized != "" {
			set[normalized] = struct{}{}
		}
	}

	return set
}

// Contains reports whether host is in the set.
func (s Set) Contains(host string) bool {
	if len(s) == 0 {
		return false
	}

	_, ok := s[Normalize(host)]

	return ok
}

// Normalize lowercases a host and removes one trailing dot.
//
// A fully qualified name such as "WWW.Example.com." therefore compares equal to
// "www.example.com". Nothing else is changed: a subdomain is a different host.
func Normalize(host string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(host), ".")

	return strings.ToLower(trimmed)
}

// PlatformMap is an exact-hostname allowlist that also names the platform each
// host belongs to.
//
// It exists because the two answers are always needed together: a save has to
// know that the client owns this host, and the item it stores has to carry the
// platform automatic organization will file it under. Keeping them in one value
// means a host cannot be added with the allowlist and forgotten in the platform
// mapping, which would leave every item from it in Unsorted.
//
// Matching is exact, exactly as Set: no suffix, no wildcard.
type PlatformMap map[string]string

// NewPlatformMap builds a PlatformMap from host-to-platform entries.
//
// Each host is normalized the way Set normalizes one. An entry with an empty host
// or an empty platform is skipped, because neither half can be used on its own:
// a host with no platform would complete an item and never file it, and a platform
// with no host names no source.
func NewPlatformMap(entries map[string]string) PlatformMap {
	if len(entries) == 0 {
		return nil
	}

	platforms := make(PlatformMap, len(entries))

	for host, platform := range entries {
		normalizedHost := Normalize(host)
		normalizedPlatform := strings.TrimSpace(platform)

		if normalizedHost == "" || normalizedPlatform == "" {
			continue
		}

		platforms[normalizedHost] = normalizedPlatform
	}

	if len(platforms) == 0 {
		return nil
	}

	return platforms
}

// Contains reports whether host is one the client fetches itself.
func (m PlatformMap) Contains(host string) bool {
	if len(m) == 0 {
		return false
	}

	_, ok := m[Normalize(host)]

	return ok
}

// PlatformFor returns the platform configured for a host.
//
// The second return value reports whether the host is in the map at all, which is
// what distinguishes "a client-owned host whose platform has not been named" from
// "a host the server enriches itself". The two need different handling, so the
// caller is not given a bare empty string.
func (m PlatformMap) PlatformFor(host string) (string, bool) {
	if len(m) == 0 {
		return "", false
	}

	platform, ok := m[Normalize(host)]

	return platform, ok
}

// Hosts lists the configured hostnames in sorted order.
//
// It exists for startup logs and for an error message that has to say which
// hosts are known. The map itself has no order, and log output that reorders
// between starts is hard to compare.
func (m PlatformMap) Hosts() []string {
	if len(m) == 0 {
		return nil
	}

	hosts := make([]string, 0, len(m))
	for host := range m {
		hosts = append(hosts, host)
	}

	sort.Strings(hosts)

	return hosts
}

// Entries lists each configured host with its platform as `host=Platform`, sorted
// by host.
//
// This is the shape the configuration is written in, so a startup log reads back
// the setting that was loaded rather than a reduced view of it: a host whose
// platform mapping is wrong is visible there instead of only in the collection it
// later creates.
func (m PlatformMap) Entries() []string {
	if len(m) == 0 {
		return nil
	}

	entries := make([]string, 0, len(m))
	for _, host := range m.Hosts() {
		entries = append(entries, host+"="+m[host])
	}

	return entries
}
