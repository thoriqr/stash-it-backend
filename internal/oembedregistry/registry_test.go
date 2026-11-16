package oembedregistry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/enrichment"
)

type memoryCache struct {
	mu     sync.Mutex
	values map[string][]byte
	gets   int
	sets   int
}

func newMemoryCache() *memoryCache {
	return &memoryCache{values: make(map[string][]byte)}
}

func (c *memoryCache) Get(
	_ context.Context,
	key string,
) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.gets++

	value, ok := c.values[key]
	if !ok {
		return nil, ErrCacheMiss
	}

	return value, nil
}

func (c *memoryCache) Set(
	_ context.Context,
	key string,
	value []byte,
	_ time.Duration,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sets++
	c.values[key] = append([]byte(nil), value...)

	return nil
}

func serveRegistry(
	t *testing.T,
	body string,
	hits *int,
) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			*hits++

			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}),
	)
	t.Cleanup(server.Close)

	return server
}

func resolverFor(
	providersURL string,
	cache Cache,
) *Resolver {
	resolver := New(&http.Client{}, cache, zap.NewNop())
	resolver.providersURL = providersURL

	return resolver
}

func resolve(
	t *testing.T,
	resolver *Resolver,
	rawURL string,
) (enrichment.OEmbedEndpoint, bool) {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)

	return resolver.Resolve(context.Background(), parsed)
}

const tiktokRegistry = `[
	{
		"provider_name": "TikTok",
		"provider_url": "http://www.tiktok.com/",
		"endpoints": [
			{
				"schemes": [
					"https://www.tiktok.com/*",
					"https://www.tiktok.com/*/video/*"
				],
				"url": "https://www.tiktok.com/oembed"
			}
		]
	}
]`

func TestResolver_MatchesTikTok(t *testing.T) {
	hits := 0
	server := serveRegistry(t, tiktokRegistry, &hits)

	endpoint, ok := resolve(
		t,
		resolverFor(server.URL, newMemoryCache()),
		"https://www.tiktok.com/@creator/video/7106594312292453675",
	)

	require.True(t, ok)
	require.Equal(t, "TikTok", endpoint.ProviderName)
	require.Equal(t, "https://www.tiktok.com/oembed", endpoint.EndpointURL)
	require.Equal(t, 1, hits)
}

// TikTok's short-link hosts are not in the provider list. They are aliases for
// the TikTok entry, so they resolve without a registry fetch.
func TestResolver_MatchesTikTokShortHosts(t *testing.T) {
	hits := 0
	server := serveRegistry(t, tiktokRegistry, &hits)
	resolver := resolverFor(server.URL, newMemoryCache())

	for _, rawURL := range []string{
		"https://vm.tiktok.com/ZSpb9Fp4P/",
		"https://vt.tiktok.com/ZSpb9Fp4P/",
		"https://m.tiktok.com/@creator/video/1",
		"https://tiktok.com/@creator/video/1",
	} {
		endpoint, ok := resolve(t, resolver, rawURL)

		require.True(t, ok, rawURL)
		require.Equal(t, "TikTok", endpoint.ProviderName, rawURL)
		require.Equal(
			t,
			"https://www.tiktok.com/oembed",
			endpoint.EndpointURL,
			rawURL,
		)
	}

	require.Equal(t, 0, hits, "aliases should not fetch the registry")
}

// A provider with a catch-all pattern and specific patterns must select the
// specific one. This is the case that keeps a Facebook post off the page
// endpoint.
func TestResolver_PrefersTheMostSpecificPattern(t *testing.T) {
	const registry = `[
		{
			"provider_name": "Facebook",
			"endpoints": [
				{
					"schemes": ["https://www.facebook.com/*"],
					"url": "https://graph.facebook.com/v16.0/oembed_page"
				},
				{
					"schemes": ["https://www.facebook.com/*/posts/*"],
					"url": "https://graph.facebook.com/v16.0/oembed_post"
				},
				{
					"schemes": ["https://www.facebook.com/*/videos/*"],
					"url": "https://graph.facebook.com/v16.0/oembed_video"
				}
			]
		}
	]`

	hits := 0
	server := serveRegistry(t, registry, &hits)
	resolver := resolverFor(server.URL, newMemoryCache())

	cases := map[string]string{
		"https://www.facebook.com/creator/posts/123":  "https://graph.facebook.com/v16.0/oembed_post",
		"https://www.facebook.com/creator/videos/123": "https://graph.facebook.com/v16.0/oembed_video",
		"https://www.facebook.com/creator":            "https://graph.facebook.com/v16.0/oembed_page",
	}

	for rawURL, expected := range cases {
		endpoint, ok := resolve(t, resolver, rawURL)

		require.True(t, ok, rawURL)
		require.Equal(t, expected, endpoint.EndpointURL, rawURL)
	}
}

func TestResolver_UsesTheSharedCache(t *testing.T) {
	hits := 0
	server := serveRegistry(t, tiktokRegistry, &hits)
	cache := newMemoryCache()

	first := resolverFor(server.URL, cache)

	_, ok := resolve(t, first, "https://www.tiktok.com/@creator/video/1")
	require.True(t, ok)
	require.Equal(t, 1, hits)

	_, ok = resolve(t, first, "https://www.tiktok.com/@creator/video/2")
	require.True(t, ok)
	require.Equal(t, 1, hits, "the in-memory copy should be reused")

	second := resolverFor(server.URL, cache)

	_, ok = resolve(t, second, "https://www.tiktok.com/@creator/video/3")
	require.True(t, ok)
	require.Equal(t, 1, hits, "the shared cache should avoid another fetch")
	require.Equal(t, 1, cache.sets)
}

func TestResolver_MalformedRegistryIsRemembered(t *testing.T) {
	hits := 0
	server := serveRegistry(t, `not json`, &hits)
	resolver := resolverFor(server.URL, newMemoryCache())

	_, ok := resolve(t, resolver, "https://www.tiktok.com/@creator/video/1")
	require.False(t, ok)
	require.Equal(t, 1, hits)

	_, ok = resolve(t, resolver, "https://www.tiktok.com/@creator/video/2")
	require.False(t, ok)
	require.Equal(t, 1, hits, "a failed fetch should not be retried on every call")
}

func TestResolver_SkipsUnusableEndpoints(t *testing.T) {
	cases := map[string]string{
		"xml only": `[
			{"provider_name": "Old", "endpoints": [
				{"schemes": ["https://old.example/*"], "url": "https://old.example/oembed", "formats": ["xml"]}
			]}
		]`,

		"empty endpoint": `[
			{"provider_name": "Bad", "endpoints": [
				{"schemes": ["https://bad.example/*"], "url": ""}
			]}
		]`,

		"non http scheme": `[
			{"provider_name": "Spotify", "endpoints": [
				{"schemes": ["spotify:*"], "url": "https://open.spotify.com/oembed"}
			]}
		]`,
	}

	for name, registry := range cases {
		t.Run(name, func(t *testing.T) {
			hits := 0
			server := serveRegistry(t, registry, &hits)

			_, ok := resolve(
				t,
				resolverFor(server.URL, newMemoryCache()),
				"https://old.example/page",
			)

			require.False(t, ok)
		})
	}
}

func TestResolver_ReplacesTheFormatPlaceholder(t *testing.T) {
	const registry = `[
		{
			"provider_name": "Example",
			"endpoints": [
				{
					"schemes": ["https://example.com/*"],
					"url": "https://example.com/oembed.{format}"
				}
			]
		}
	]`

	hits := 0
	server := serveRegistry(t, registry, &hits)

	endpoint, ok := resolve(
		t,
		resolverFor(server.URL, newMemoryCache()),
		"https://example.com/page",
	)

	require.True(t, ok)
	require.Equal(t, "https://example.com/oembed.json", endpoint.EndpointURL)
}

func TestResolver_TreatsHTTPAndHTTPSAsEquivalent(t *testing.T) {
	const registry = `[
		{
			"provider_name": "Example",
			"endpoints": [
				{
					"schemes": ["http://example.com/*"],
					"url": "https://example.com/oembed"
				}
			]
		}
	]`

	hits := 0
	server := serveRegistry(t, registry, &hits)

	endpoint, ok := resolve(
		t,
		resolverFor(server.URL, newMemoryCache()),
		"https://example.com/page",
	)

	require.True(t, ok)
	require.Equal(t, "https://example.com/oembed", endpoint.EndpointURL)
}

func TestResolver_IgnoresTrackingParametersForExactPatterns(t *testing.T) {
	const registry = `[
		{
			"provider_name": "Example",
			"endpoints": [
				{
					"schemes": ["https://example.com/page"],
					"url": "https://example.com/oembed"
				}
			]
		}
	]`

	hits := 0
	server := serveRegistry(t, registry, &hits)

	endpoint, ok := resolve(
		t,
		resolverFor(server.URL, newMemoryCache()),
		"https://example.com/page?utm_source=share",
	)

	require.True(t, ok)
	require.Equal(t, "https://example.com/oembed", endpoint.EndpointURL)
}

func TestResolver_MatchesPatternsThatNameAQueryParameter(t *testing.T) {
	const registry = `[
		{
			"provider_name": "Example",
			"endpoints": [
				{
					"schemes": ["https://example.com/photo?fbid=*"],
					"url": "https://example.com/oembed"
				}
			]
		}
	]`

	hits := 0
	server := serveRegistry(t, registry, &hits)
	resolver := resolverFor(server.URL, newMemoryCache())

	_, ok := resolve(
		t,
		resolver,
		"https://example.com/photo?fbid=123&utm_source=share",
	)
	require.True(t, ok)

	_, ok = resolve(t, resolver, "https://example.com/photo?other=1")
	require.False(t, ok)
}

func TestResolver_MatchesAnEmptyPath(t *testing.T) {
	hits := 0
	server := serveRegistry(t, tiktokRegistry, &hits)

	endpoint, ok := resolve(
		t,
		resolverFor(server.URL, newMemoryCache()),
		"https://www.tiktok.com",
	)

	require.True(t, ok)
	require.Equal(t, "https://www.tiktok.com/oembed", endpoint.EndpointURL)
}

func TestResolver_UnknownURLIsNotAnError(t *testing.T) {
	hits := 0
	server := serveRegistry(t, tiktokRegistry, &hits)

	_, ok := resolve(
		t,
		resolverFor(server.URL, newMemoryCache()),
		"https://unknown.example/page",
	)

	require.False(t, ok)
}
