// Package hostset matches a URL hostname against a configured allowlist.
//
// It is a leaf utility with no dependencies, used by the features that need to
// answer "is this host one the operator listed". The list itself is loaded from
// the environment by internal/config; this package only normalizes and compares.
package hostset

import "strings"

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
