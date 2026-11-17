package config

import (
	"fmt"
	"os"
	"strings"
)

// loadOEmbedSkipHosts reads OEMBED_SKIP_HOSTS.
//
// It is optional. The hosts it names never have their oEmbed endpoint called;
// the HTML path still runs.
func loadOEmbedSkipHosts() ([]string, error) {
	return loadHostList("OEMBED_SKIP_HOSTS")
}

// loadClientEnrichmentHosts reads CLIENT_ENRICHMENT_HOSTS.
//
// It is optional. The hosts it names are fetched and parsed by the client
// instead of the server: a save is not queued for the worker, and the enrich
// endpoint accepts the metadata the client extracted rather than fetching.
func loadClientEnrichmentHosts() ([]string, error) {
	return loadHostList("CLIENT_ENRICHMENT_HOSTS")
}

// loadHostList reads a comma-separated list of exact hostnames from one
// environment variable.
//
// Each entry is trimmed, lowercased and stripped of one trailing dot, so a host
// written the way it appears in a browser still matches the hostname parsed from
// a URL. Empty entries are ignored.
//
// A wildcard, port, path, scheme or whitespace inside an entry is rejected
// rather than silently stored, because an entry that cannot match would look
// configured and do nothing.
func loadHostList(name string) ([]string, error) {
	entries := splitList(os.Getenv(name))

	hosts := make([]string, 0, len(entries))

	for _, entry := range entries {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(entry), "."))
		if host == "" {
			continue
		}

		if strings.ContainsAny(host, "/:@* \t") {
			return nil, fmt.Errorf(
				"%s entry %q is not an exact hostname",
				name,
				entry,
			)
		}

		hosts = append(hosts, host)
	}

	return hosts, nil
}
