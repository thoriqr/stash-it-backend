package config

import (
	"fmt"
	"os"
	"strings"
)

// loadOEmbedSkipHosts reads OEMBED_SKIP_HOSTS into exact, normalized hostnames.
//
// The value is optional. Each entry is trimmed, lowercased and stripped of one
// trailing dot, so a host written the way it appears in a browser still matches
// the hostname parsed from a URL. Empty entries are ignored.
//
// A wildcard, port, path or scheme is rejected rather than silently stored,
// because an entry that cannot match would look configured and do nothing. The
// list is intentionally exact-host only: a host the operator did not name keeps
// its registry endpoint.
func loadOEmbedSkipHosts() ([]string, error) {
	entries := splitList(os.Getenv("OEMBED_SKIP_HOSTS"))

	hosts := make([]string, 0, len(entries))

	for _, entry := range entries {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(entry), "."))
		if host == "" {
			continue
		}

		if strings.ContainsAny(host, "/:@* \t") {
			return nil, fmt.Errorf(
				"OEMBED_SKIP_HOSTS entry %q is not an exact hostname",
				entry,
			)
		}

		hosts = append(hosts, host)
	}

	return hosts, nil
}
