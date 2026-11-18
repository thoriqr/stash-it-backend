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
//
// Each entry is `host=Platform`, and the platform half is required. A host
// without one would let a save complete an item that automatic organization can
// never file, so the two are configured together rather than in separate
// variables that can drift apart.
func loadClientEnrichmentHosts() (map[string]string, error) {
	return loadPlatformMap("CLIENT_ENRICHMENT_HOSTS")
}

// loadPlatformMap reads a comma-separated host=platform list from one environment
// variable.
//
// The host is normalized the way every other host setting is: trimmed, lowercased
// and stripped of one trailing dot. The platform is trimmed but otherwise kept
// literally, because it becomes a collection's display name and its spelling is
// the operator's choice.
//
// Both halves are required and a malformed entry is a startup error rather than a
// setting that silently matches nothing. A wildcard, port, path, scheme or
// whitespace in the host, or a second `=` in the entry, is rejected outright.
func loadPlatformMap(name string) (map[string]string, error) {
	entries := splitList(os.Getenv(name))

	if len(entries) == 0 {
		return nil, nil
	}

	platforms := make(map[string]string, len(entries))

	for _, entry := range entries {
		hostPart, platformPart, found := strings.Cut(entry, "=")
		if !found {
			return nil, fmt.Errorf(
				"%s entry %q must be host=Platform",
				name,
				entry,
			)
		}

		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostPart), "."))
		platform := strings.TrimSpace(platformPart)

		if host == "" {
			return nil, fmt.Errorf(
				"%s entry %q has no hostname",
				name,
				entry,
			)
		}

		if strings.ContainsAny(host, "/:@* \t") {
			return nil, fmt.Errorf(
				"%s entry %q is not an exact hostname",
				name,
				entry,
			)
		}

		if platform == "" {
			return nil, fmt.Errorf(
				"%s entry %q has no platform",
				name,
				entry,
			)
		}

		if strings.Contains(platform, "=") {
			return nil, fmt.Errorf(
				"%s entry %q has more than one =",
				name,
				entry,
			)
		}

		platforms[host] = platform
	}

	return platforms, nil
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
