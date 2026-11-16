// Package oembedregistry resolves a URL to an oEmbed endpoint using the
// community-maintained provider list at https://oembed.com/providers.json.
//
// It is deliberately separate from internal/enrichment. Enrichment must not know
// about persistence, and this package both fetches a remote document and caches it
// in Redis. The enricher consumes the narrow ProviderResolver interface instead,
// so the two responsibilities stay in their own packages.
//
// The registry is a directory, not a metadata service. It supplies an endpoint for
// a URL pattern; the endpoint is still responsible for answering with the fields a
// provider chooses to publish. A provider that requires an access token, or whose
// endpoint is stale, simply fails that attempt and enrichment falls back to HTML.
package oembedregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/enrichment"
)

// Registry defaults.
const (
	// DefaultProvidersURL is the community provider list.
	DefaultProvidersURL = "https://oembed.com/providers.json"

	// cacheKey is versioned so a change to the parsed shape can invalidate the
	// old value instead of trying to read it with the new code.
	cacheKey = "oembed:providers:v1"

	// cacheTTL is how long a successful copy is considered current. The list
	// changes slowly, so a day is enough; there is deliberately no scheduler,
	// because a refresh only matters when enrichment actually runs.
	cacheTTL = 24 * time.Hour

	// failureRetry is how long a failed fetch is remembered in memory. Without it
	// every enrichment would retry a registry that is already known to be down.
	failureRetry = time.Minute

	// maxRegistryBytes bounds the provider document. The real file is under 200
	// KB; this leaves room without letting a misdirected fetch stream something
	// large.
	maxRegistryBytes int64 = 1 << 20

	// fetchTimeout bounds one registry fetch independently of the client policy,
	// so a slow registry cannot hold a refresh for the whole outbound timeout.
	fetchTimeout = 15 * time.Second
)

// ErrCacheMiss reports that the registry cache has no value.
var ErrCacheMiss = errors.New("oembed provider cache miss")

// tiktokAliasHosts are short-link and mobile hosts TikTok uses that the provider
// list does not include. They are aliases for the TikTok entry, not a separate
// provider: the platform still comes from provider_name in the response.
//
// www.tiktok.com is deliberately absent. The registry lists it, and leaving it
// there keeps the provider list authoritative for the canonical host.
var tiktokAliasHosts = map[string]struct{}{
	"tiktok.com":    {},
	"m.tiktok.com":  {},
	"vm.tiktok.com": {},
	"vt.tiktok.com": {},
}

const (
	tiktokAliasProvider = "TikTok"
	tiktokAliasEndpoint = "https://www.tiktok.com/oembed"
)

// aliasEndpoint resolves a host the provider list does not describe.
func aliasEndpoint(parsed *url.URL) (enrichment.OEmbedEndpoint, bool) {
	if parsed == nil {
		return enrichment.OEmbedEndpoint{}, false
	}

	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if _, ok := tiktokAliasHosts[host]; !ok {
		return enrichment.OEmbedEndpoint{}, false
	}

	return enrichment.OEmbedEndpoint{
		ProviderName: tiktokAliasProvider,
		EndpointURL:  tiktokAliasEndpoint,
	}, true
}

// Cache is the shared copy of the provider document.
//
// It is an interface rather than a Redis client so tests do not need Redis, and so
// the resolver can degrade to fetching directly when the cache is unavailable.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// Resolver resolves URLs against the provider list.
//
// It is safe for concurrent use. The provider list is loaded lazily, kept in
// memory for cacheTTL, and shared through the cache so a process restart does not
// force a fetch.
type Resolver struct {
	client *http.Client
	cache  Cache
	log    *zap.Logger

	// providersURL is a field so a test can point it at a local server.
	providersURL string

	mu         sync.Mutex
	loaded     []compiledProvider
	loadedAt   time.Time
	hasLoaded  bool
	retryAfter time.Time
}

// New builds a resolver.
//
// client must be the guarded outbound client. The registry URL is fixed rather
// than user supplied, but sending it through the same dialer and limits as every
// other fetch is the least surprising choice and keeps a second HTTP policy from
// appearing in the codebase.
//
// cache may be nil, which makes every refresh a direct fetch after the in-memory
// copy expires.
func New(
	client *http.Client,
	cache Cache,
	log *zap.Logger,
) *Resolver {
	return &Resolver{
		client:       client,
		cache:        cache,
		log:          log,
		providersURL: DefaultProvidersURL,
	}
}

// Resolve returns the endpoint that can answer for parsed, or false when no
// provider lists it.
//
// A registry that cannot be read is not an error to the caller: false sends the
// item down the HTML path, which is still a valid enrichment. The failure is
// logged by the resolver and then remembered for failureRetry.
func (r *Resolver) Resolve(
	ctx context.Context,
	parsed *url.URL,
) (enrichment.OEmbedEndpoint, bool) {
	if parsed == nil {
		return enrichment.OEmbedEndpoint{}, false
	}

	if endpoint, ok := aliasEndpoint(parsed); ok {
		return endpoint, true
	}

	providers := r.load(ctx)
	if len(providers) == 0 {
		return enrichment.OEmbedEndpoint{}, false
	}

	// A pattern that names a query parameter needs the full URL; every other
	// pattern is matched without the query, so a tracking parameter added by a
	// share sheet does not hide an otherwise exact pattern.
	withoutQuery := matchTarget(parsed, false)
	withQuery := matchTarget(parsed, true)

	// Providers are sorted most specific first. Returning the first match is what
	// keeps a Facebook post URL on oembed_post instead of the generic oembed_page
	// pattern that also matches it.
	for _, provider := range providers {
		target := withoutQuery
		if provider.requiresQuery {
			target = withQuery
		}

		if provider.pattern.MatchString(target) {
			return enrichment.OEmbedEndpoint{
				ProviderName: provider.providerName,
				EndpointURL:  provider.endpointURL,
			}, true
		}
	}

	return enrichment.OEmbedEndpoint{}, false
}

// load returns the current provider list, refreshing it when it is stale.
func (r *Resolver) load(ctx context.Context) []compiledProvider {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	if r.hasLoaded && now.Sub(r.loadedAt) < cacheTTL {
		return r.loaded
	}

	if !r.retryAfter.IsZero() && now.Before(r.retryAfter) {
		return r.loaded
	}

	if r.cache != nil {
		raw, err := r.cache.Get(ctx, cacheKey)
		switch {
		case err == nil && len(raw) > 0:
			providers, compileErr := compile(raw)
			if compileErr == nil {
				r.loaded = providers
				r.loadedAt = now
				r.hasLoaded = true
				r.retryAfter = time.Time{}

				return r.loaded
			}

			r.warn("oembed provider cache is not usable", compileErr)

		case err != nil && !errors.Is(err, ErrCacheMiss):
			r.warn("oembed provider cache read failed", err)
		}
	}

	raw, err := r.fetch(ctx)
	if err != nil {
		r.retryAfter = now.Add(failureRetry)
		r.warn("oembed provider registry fetch failed", err)

		return r.loaded
	}

	providers, err := compile(raw)
	if err != nil {
		r.retryAfter = now.Add(failureRetry)
		r.warn("oembed provider registry is not usable", err)

		return r.loaded
	}

	if r.cache != nil {
		if err := r.cache.Set(ctx, cacheKey, raw, cacheTTL); err != nil {
			r.warn("oembed provider cache write failed", err)
		}
	}

	r.loaded = providers
	r.loadedAt = now
	r.hasLoaded = true
	r.retryAfter = time.Time{}

	return r.loaded
}

// fetch reads the provider document.
func (r *Resolver) fetch(ctx context.Context) ([]byte, error) {
	if r.client == nil {
		return nil, errors.New("oembed registry has no http client")
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		r.providersURL,
		nil,
	)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Accept", "application/json")

	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"oembed providers: status %d",
			response.StatusCode,
		)
	}

	body, err := io.ReadAll(io.LimitReader(
		response.Body,
		maxRegistryBytes,
	))
	if err != nil {
		return nil, err
	}

	return body, nil
}

func (r *Resolver) warn(message string, err error) {
	if r.log != nil {
		r.log.Warn(message, zap.Error(err))
	}
}

// providerDocument is one entry in the provider list.
type providerDocument struct {
	ProviderName string             `json:"provider_name"`
	Endpoints    []endpointDocument `json:"endpoints"`
}

// endpointDocument is one endpoint of a provider.
type endpointDocument struct {
	Schemes []string `json:"schemes"`
	URL     string   `json:"url"`
	Formats []string `json:"formats"`
}

// compiledProvider is one URL pattern and the endpoint it selects.
type compiledProvider struct {
	providerName  string
	endpointURL   string
	pattern       *regexp.Regexp
	specificity   int
	requiresQuery bool
}

// compile turns the provider document into a specificity-ordered matcher.
//
// Endpoints without a usable JSON form are skipped rather than attempted: an
// enrichment has no XML parser, and calling an endpoint that will not answer in
// the expected format only delays the HTML fallback.
func compile(raw []byte) ([]compiledProvider, error) {
	var providers []providerDocument
	if err := json.Unmarshal(raw, &providers); err != nil {
		return nil, err
	}

	compiled := make([]compiledProvider, 0, len(providers))

	for _, provider := range providers {
		for _, endpoint := range provider.Endpoints {
			if !supportsJSON(endpoint.Formats) {
				continue
			}

			endpointURL, ok := normalizeEndpoint(endpoint.URL)
			if !ok {
				continue
			}

			for _, scheme := range endpoint.Schemes {
				pattern, ok := compileScheme(scheme)
				if !ok {
					continue
				}

				compiled = append(compiled, compiledProvider{
					providerName:  provider.ProviderName,
					endpointURL:   endpointURL,
					pattern:       pattern,
					specificity:   schemeSpecificity(scheme),
					requiresQuery: strings.Contains(scheme, "?"),
				})
			}
		}
	}

	sort.SliceStable(compiled, func(i, j int) bool {
		return compiled[i].specificity > compiled[j].specificity
	})

	return compiled, nil
}

// supportsJSON reports whether an endpoint can answer in JSON.
//
// An absent list is treated as JSON because most of the registry omits the field
// and JSON is the specification's default.
func supportsJSON(formats []string) bool {
	if len(formats) == 0 {
		return true
	}

	for _, format := range formats {
		if strings.EqualFold(strings.TrimSpace(format), "json") {
			return true
		}
	}

	return false
}

// normalizeEndpoint completes an endpoint template and rejects one that is not a
// plain http or https URL.
func normalizeEndpoint(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}

	trimmed = strings.ReplaceAll(trimmed, "{format}", "json")

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}

	if parsed.Host == "" {
		return "", false
	}

	return parsed.String(), true
}

// compileScheme turns a registry URL pattern into a matcher.
//
// Registry patterns are globs: '*' matches any run of characters, including
// slashes and a query string. Anything that is not an http or https pattern is
// skipped, because enrichment only fetches those schemes.
//
// The scheme is treated as interchangeable. Some entries list only the http form
// while the URL a person saved is https, and the two forms name the same page. The
// host and path are not normalized beyond that: an exact host is still required,
// so a lookalike domain cannot match.
func compileScheme(raw string) (*regexp.Regexp, bool) {
	pattern := strings.TrimSpace(raw)
	lowered := strings.ToLower(pattern)

	var rest string

	switch {
	case strings.HasPrefix(lowered, "https://"):
		rest = pattern[len("https://"):]

	case strings.HasPrefix(lowered, "http://"):
		rest = pattern[len("http://"):]

	default:
		return nil, false
	}

	escaped := regexp.QuoteMeta(rest)
	escaped = strings.ReplaceAll(escaped, `\*`, `.*`)

	compiled, err := regexp.Compile("(?i)^https?://" + escaped + "$")
	if err != nil {
		return nil, false
	}

	return compiled, true
}

// schemeSpecificity scores a pattern so the most specific one wins.
//
// The score is the amount of literal text plus bonuses for an exact host and for
// a pattern that names a query parameter. It does not need to be a perfect
// measure; it only has to put a post or video pattern above the provider's
// catch-all pattern, which is the case that decides whether the right endpoint is
// asked.
func schemeSpecificity(pattern string) int {
	score := len(strings.ReplaceAll(pattern, "*", ""))

	host := pattern
	if index := strings.Index(host, "://"); index >= 0 {
		host = host[index+3:]
	}

	if index := strings.IndexAny(host, "/?#"); index >= 0 {
		host = host[:index]
	}

	if !strings.Contains(host, "*") {
		score += 1000
	}

	if strings.Contains(pattern, "?") {
		score += 500
	}

	return score
}

// matchTarget renders a URL for pattern matching.
//
// The fragment is dropped because no registry pattern describes one, and an empty
// path is rendered as "/" so a pattern ending in "/*" still matches a bare host.
// The query is included only when the pattern names one, so an exact path pattern
// is not defeated by a tracking parameter.
func matchTarget(
	parsed *url.URL,
	includeQuery bool,
) string {
	copied := *parsed
	copied.Fragment = ""
	copied.RawFragment = ""

	if !includeQuery {
		copied.RawQuery = ""
		copied.ForceQuery = false
	}

	if copied.Path == "" {
		copied.Path = "/"
		copied.RawPath = ""
	}

	return copied.String()
}

// RedisCache is the shared copy of the provider document.
type RedisCache struct {
	client *redis.Client
}

// NewRedisCache wraps a Redis client.
func NewRedisCache(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

// Get reads the cached document.
func (c *RedisCache) Get(
	ctx context.Context,
	key string,
) ([]byte, error) {
	if c == nil || c.client == nil {
		return nil, ErrCacheMiss
	}

	value, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrCacheMiss
	}

	return value, err
}

// Set stores the document for ttl.
func (c *RedisCache) Set(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) error {
	if c == nil || c.client == nil {
		return nil
	}

	return c.client.Set(ctx, key, value, ttl).Err()
}
