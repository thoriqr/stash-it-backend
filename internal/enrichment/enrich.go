package enrichment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html/charset"

	"github.com/thoriqr/stash-it-backend/internal/security"
)

// Enricher fetches a saved item's page and extracts metadata from it.
//
// It is the whole of the enrichment capability: it takes a URL and returns either
// metadata or a classified failure. It does not know what a saved item is, does
// not touch the database, and does not decide what should be stored or how.
//
// Both callers use this one interface. The background worker will hand it a saved
// item's URL and persist whatever comes back; the synchronous endpoint will do the
// same for one item on demand. Neither can diverge, because there is nothing to
// diverge from.
type Enricher interface {
	Enrich(
		ctx context.Context,
		rawURL string,
	) (Metadata, error)
}

// enricher is the implementation.
type enricher struct {
	// genericClient carries the generic protocol and timeout policy.
	genericClient *http.Client

	// tokopediaClient carries the Tokopedia protocol and timeout policy.
	tokopediaClient *http.Client

	// policy supplies the response body limit. It is the generic policy, whose
	// size limit is shared by both transports.
	policy security.OutboundFetchPolicy

	// youtubeOEmbedURL is the endpoint YouTube metadata is read from. It is a
	// field so a test can point it at a local server; production always leaves it
	// at youtubeOEmbedEndpoint.
	youtubeOEmbedURL string

	// providerResolver maps a URL to a provider's oEmbed endpoint. It may be nil,
	// which is the no-registry configuration: known hosts such as YouTube still
	// use oEmbed, and everything else goes straight to HTML.
	providerResolver ProviderResolver

	// oembedSkipHosts are exact, normalized hostnames for which no oEmbed
	// endpoint is called, including built-in and discovery endpoints. The HTML
	// path still runs. An empty map means no skips.
	oembedSkipHosts map[string]struct{}
}

// enrichAcceptHeader asks for HTML and accepts a little more, so that an XHTML
// page is not turned away on a technicality.
//
// The low q values are what keep an origin from answering with a preview image
// when the extension guessed wrong, which would produce a response this package
// then refuses to parse. The wildcard exists only so that a server answering with
// something unlabelled can still be sniffed.
const enrichAcceptHeader = "text/html,application/xhtml+xml;q=0.9," +
	"text/plain;q=0.5,*/*;q=0.1"

// NewEnricher builds an Enricher that uses one guarded HTTP client for every host.
//
// client must come from security.NewGuardedHTTPClient. It is the seam used by tests
// and by any caller that does not distinguish hosts; production wires the two
// distinct transports with NewEnricherWithTransports.
//
// policy is needed only for the response body limit, which is applied here rather
// than by the client because a client cannot bound a body it does not read. It
// must be the same policy the client was built with.
func NewEnricher(
	client *http.Client,
	policy security.OutboundFetchPolicy,
) *enricher {
	return NewEnricherWithTransports(client, client, policy)
}

// NewEnricherWithTransports builds an Enricher with distinct guarded clients for
// the generic hosts and for Tokopedia.
//
// Both clients must come from security.NewGuardedHTTPClient: the SSRF policy lives
// in each client's dialer, so a client from anywhere else has none of it, and
// enrichment cannot detect that for itself.
//
// The client is selected once, from the original submitted URL's hostname, and is
// then used for the whole request: redirect hops and the header-profile fallback
// cannot move it to the other client. That is what keeps a redirect from a
// Tokopedia short link on the Tokopedia transport.
//
// policy supplies the response body limit used by reader. It must be the policy the
// generic client was built with; the size limit is the same on both transports.
func NewEnricherWithTransports(
	genericClient *http.Client,
	tokopediaClient *http.Client,
	policy security.OutboundFetchPolicy,
) *enricher {
	return NewEnricherWithResolver(
		genericClient,
		tokopediaClient,
		policy,
		nil,
	)
}

// NewEnricherWithResolver builds an Enricher that also consults a provider
// registry before falling back to HTML.
//
// resolver may be nil. A nil resolver is not a degraded mode for the hosts this
// package already knows: YouTube is still read through oEmbed, because that is a
// property of the host and not of the registry. Everything else goes straight to
// the HTML path, which is exactly the behaviour before a registry existed.
//
// A non-nil resolver is consulted for every other host. It must be safe for
// concurrent use, because one Enricher is shared by every request in a process.
func NewEnricherWithResolver(
	genericClient *http.Client,
	tokopediaClient *http.Client,
	policy security.OutboundFetchPolicy,
	resolver ProviderResolver,
) *enricher {
	return &enricher{
		genericClient:    genericClient,
		tokopediaClient:  tokopediaClient,
		policy:           policy,
		youtubeOEmbedURL: youtubeOEmbedEndpoint,
		providerResolver: resolver,
	}
}

// SetOEmbedSkipHosts configures exact hostnames for which no oEmbed endpoint is
// called. The HTML path still runs, so a skipped host is read the same way as one
// the registry does not know.
//
// Matching is by exact normalized hostname, with no suffix or substring match.
// The set is written before the enricher is shared, so it is read-only afterward.
// An empty list clears any previous configuration.
func (e *enricher) SetOEmbedSkipHosts(hosts []string) {
	if len(hosts) == 0 {
		e.oembedSkipHosts = nil

		return
	}

	skipped := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		normalized := normalizeHostname(host)
		if normalized != "" {
			skipped[normalized] = struct{}{}
		}
	}

	e.oembedSkipHosts = skipped
}

// skipsOEmbed reports whether the host's oEmbed endpoints are disabled.
func (e *enricher) skipsOEmbed(hostname string) bool {
	if len(e.oembedSkipHosts) == 0 {
		return false
	}

	_, skipped := e.oembedSkipHosts[normalizeHostname(hostname)]

	return skipped
}

// clientFor returns the guarded client for a transport selection.
func (e *enricher) clientFor(transport transportKind) *http.Client {
	if transport == tokopediaTransport {
		return e.tokopediaClient
	}

	return e.genericClient
}

// Enrich fetches rawURL and extracts metadata from the response.
//
// A page that was reached but declares nothing usable returns an empty Metadata
// and a nil error, because "this page told us nothing" is a successful
// enrichment whose result happens to be empty. Only a failure to obtain or use a
// page produces an error, and every error is a *Failure carrying a Kind, so a
// caller can tell a retryable problem from a permanent one.
//
// The request profile and the HTTP client are both chosen from the original URL's
// hostname. A host with a verified profile is tried first and the generic profile
// is tried once more if that attempt fails for a reason the headers can change, so
// the generic fallback stays reachable without becoming the universal default. The
// chosen client carries every attempt and every redirect hop: the fallback changes
// headers only, never the transport.
//
// There is no SSRF decision anywhere in this function. The scheme, the port and
// the resolved address are all enforced by the injected client, in its dialer,
// and re-checking them here would create a second place to keep in step with the
// first. A caller that supplies an unguarded client has not weakened enrichment;
// it has simply not wired it to the policy, and that mistake is visible in the
// composition rather than hidden behind a second check.
func (e *enricher) Enrich(
	ctx context.Context,
	rawURL string,
) (Metadata, error) {
	// The URL is parsed rather than validated, purely so that a malformed value
	// is reported as a bad URL instead of surfacing as a transport error, and so
	// the hostname for profile selection comes from the URL the caller supplied.
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Metadata{}, newFailure(
			FailureFetch,
			fmt.Errorf("%w: %w", ErrInvalidURL, err),
		)
	}

	hostname := parsed.Hostname()

	// The transport is selected once from the original host and then reused for the
	// whole request, so neither a redirect hop nor the header fallback can move the
	// fetch to a different client.
	client := e.clientFor(transportFor(hostname))

	// oEmbed is asked first when a provider is known. It is a few kilobytes
	// instead of a whole page, and for some hosts it is the only answer that is
	// not a bot challenge. A result with no usable field is not treated as a
	// success, so the HTML path still gets its turn.
	endpoint, hasOEmbed := e.resolveOEmbedEndpoint(ctx, parsed)

	var (
		oembedFailure  error
		oembedMetadata Metadata
	)

	if hasOEmbed {
		metadata, failure := e.enrichWithOEmbed(
			ctx,
			client,
			parsed,
			rawURL,
			endpoint,
			true,
		)

		metadata = dropDuplicateDescription(metadata)
		metadata = dropGenericTitle(parsed, metadata)

		switch {
		case failure != nil:
			if endpoint.SkipHTMLFallback {
				return Metadata{}, failure
			}

			oembedFailure = failure

		case endpoint.SkipHTMLFallback:
			// The page would tell us nothing the endpoint did not already say,
			// so whatever the endpoint returned is the result.
			return metadata, nil

		case metadata.Title != nil && metadata.Platform != nil:
			return metadata, nil

		default:
			// The endpoint answered, but not with enough to store on its own.
			// Keep what it did say so the HTML result can be completed with it.
			oembedMetadata = metadata
		}
	}

	profiles := requestProfilesFor(hostname)

	var lastFailure error

	for index, profile := range profiles {
		metadata, discoveryURL, failure := e.enrichWithProfile(
			ctx,
			client,
			rawURL,
			profile,
		)
		if failure == nil {
			metadata = e.enrichWithDiscovery(
				ctx,
				client,
				parsed,
				rawURL,
				metadata,
				discoveryURL,
			)
			metadata = mergeMetadata(metadata, oembedMetadata)
			metadata = dropDuplicateDescription(metadata)

			return dropGenericTitle(parsed, metadata), nil
		}

		lastFailure = failure

		// Stop after the last profile, and never fall back for a failure the
		// headers cannot change. See shouldFallBackToGeneric.
		if !shouldFallBackToGeneric(failure) || index == len(profiles)-1 {
			break
		}
	}

	return Metadata{}, combineFailures(lastFailure, oembedFailure)
}

// resolveOEmbedEndpoint returns the endpoint to ask about a URL.
//
// YouTube is kept as a built-in provider because its URL normalization is a
// property of the host rather than of the registry: a short link and a watch URL
// are the same video, and the endpoint should be asked about the canonical form.
// The endpoint itself is the same one the registry lists.
//
// Everything else goes through the injected resolver. A resolver that is absent or
// that knows nothing about the URL is not an error; it means the HTML path is the
// next thing to try. A host in the configured skip set returns no endpoint either,
// which is how an operator disables a provider that is not worth calling.
func (e *enricher) resolveOEmbedEndpoint(
	ctx context.Context,
	parsed *url.URL,
) (OEmbedEndpoint, bool) {
	if e.skipsOEmbed(parsed.Hostname()) {
		return OEmbedEndpoint{}, false
	}

	if _, ok := youtubeVideoID(parsed); ok {
		return OEmbedEndpoint{
			ProviderName:     "YouTube",
			EndpointURL:      e.youtubeOEmbedURL,
			SkipHTMLFallback: true,
		}, true
	}

	if e.providerResolver == nil {
		return OEmbedEndpoint{}, false
	}

	endpoint, ok := e.providerResolver.Resolve(ctx, parsed)
	if !ok || strings.TrimSpace(endpoint.EndpointURL) == "" {
		return OEmbedEndpoint{}, false
	}

	return endpoint, true
}

// enrichWithDiscovery follows a page's oEmbed discovery link when the HTML result
// is still missing something worth having.
//
// Discovery is a fallback, not a first choice: the page has already been fetched
// and its Open Graph and JSON-LD values are the primary result. Following the link
// costs one more request, so it is done only when the page left the title or the
// platform empty. A failure is ignored because the HTML result is still a valid
// successful enrichment.
func (e *enricher) enrichWithDiscovery(
	ctx context.Context,
	client *http.Client,
	parsed *url.URL,
	rawURL string,
	metadata Metadata,
	discoveryURL string,
) Metadata {
	if discoveryURL == "" {
		return metadata
	}

	if e.skipsOEmbed(parsed.Hostname()) {
		return metadata
	}

	if metadata.Title != nil && metadata.Platform != nil {
		return metadata
	}

	discovered, failure := e.enrichWithOEmbed(
		ctx,
		client,
		parsed,
		rawURL,
		OEmbedEndpoint{EndpointURL: discoveryURL},

		// The discovery link already names the page it belongs to, so its url
		// parameter is kept when it has one.
		false,
	)
	if failure != nil {
		return metadata
	}

	return mergeMetadata(metadata, discovered)
}

// mergeMetadata fills the empty fields of primary from fallback.
//
// The primary source is the one with the more authoritative answer for the field
// it supplied; a fallback never overwrites it. This is what makes one enrichment
// the union of both sources rather than a choice between them.
func mergeMetadata(primary, fallback Metadata) Metadata {
	if primary.Title == nil {
		primary.Title = fallback.Title
	}

	if primary.Platform == nil {
		primary.Platform = fallback.Platform
	}

	if primary.Description == nil {
		primary.Description = fallback.Description
	}

	if primary.ImageURL == nil {
		primary.ImageURL = fallback.ImageURL
	}

	if primary.CanonicalURL == nil {
		primary.CanonicalURL = fallback.CanonicalURL
	}

	if primary.SiteName == nil {
		primary.SiteName = fallback.SiteName
	}

	if primary.Author == nil {
		primary.Author = fallback.Author
	}

	if primary.Price == nil {
		primary.Price = fallback.Price
	}

	return primary
}

// combineFailures reports the failure of a whole attempt after both sources were
// tried.
//
// A retryable failure from either source wins, because the question the retry
// asks is whether a later attempt could succeed, and one retryable cause is
// enough to make the answer yes. The status is kept when either source saw one,
// so a log line still says what the origin answered.
func combineFailures(primary, secondary error) error {
	if primary == nil {
		return secondary
	}

	if secondary == nil {
		return primary
	}

	kind := Kind(primary)
	if Kind(secondary) == FailureFetch {
		kind = FailureFetch
	}

	status := Status(primary)
	if status == 0 {
		status = Status(secondary)
	}

	return &Failure{
		Kind:       kind,
		StatusCode: status,
		Err:        errors.Join(primary, secondary),
	}
}

// enrichWithProfile performs one fetch-and-extract attempt with the given profile.
//
// client is the one chosen for the original host, so the header fallback stays on
// the same transport.
//
// It returns the selected metadata plus the absolute oEmbed discovery endpoint the
// page declared, or an empty string when it declared none. The discovery link is
// not followed here: whether a second request is worth making belongs to the
// caller, which knows what the page already supplied.
//
// The response body is closed here, after it has been read, because reader hands
// back a stream that is still bound to it. Pairing the close with the whole read
// in one function is what keeps every attempt from leaking a body.
func (e *enricher) enrichWithProfile(
	ctx context.Context,
	client *http.Client,
	rawURL string,
	profile requestProfile,
) (Metadata, string, error) {
	response, err := e.do(ctx, client, rawURL, profile)
	if err != nil {
		return Metadata{}, "", err
	}

	defer response.Body.Close()

	body, finalURL, err := e.reader(response)
	if err != nil {
		return Metadata{}, "", err
	}

	// TikTok answers its not-found route with a success status, so the status
	// classification above cannot see it. The page would otherwise be stored as a
	// generic "TikTok" title with no platform.
	if isTikTokNotFoundURL(finalURL) {
		return Metadata{}, "", newFailure(FailureContent, ErrNotFoundPage)
	}

	doc, err := extract(body, finalURL)
	if err != nil {
		// The tokenizer has no failure of its own to report: the only error it
		// returns is one from the reader underneath it. A body that stopped
		// arriving is a network event, so it stays retryable rather than being
		// recorded as a permanent statement about the page.
		return Metadata{}, "", newFailure(
			FailureFetch,
			fmt.Errorf("%w: %w", ErrParseFailed, err),
		)
	}

	metadata := selectMetadata(doc, finalURL)
	metadata = applyInstagramTitle(finalURL, metadata)
	metadata = applyTikTokUniversalData(doc, finalURL, metadata)
	metadata = applyAmazonProductImage(doc, finalURL, metadata)
	metadata = dropDuplicateDescription(metadata)
	metadata = dropGenericTitle(finalURL, metadata)

	return metadata,
		resolveOEmbedDiscovery(doc, finalURL),
		nil
}

// do performs the request and returns the response with its body unread.
//
// It deliberately does not close the body. The caller reads it and closes it.
func (e *enricher) do(
	ctx context.Context,
	client *http.Client,
	rawURL string,
	profile requestProfile,
) (
	*http.Response,
	error,
) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		rawURL,
		nil,
	)
	if err != nil {
		return nil, newFailure(
			FailureFetch,
			fmt.Errorf("%w: %w", ErrInvalidURL, err),
		)
	}

	request.Header.Set("Accept", profile.accept)

	// Only a verified host-specific profile supplies a User-Agent. When it does
	// not, the guarded client's transport applies its default, which is the honest
	// crawler agent. Enrichment does not invent one here.
	if profile.userAgent != "" {
		request.Header.Set("User-Agent", profile.userAgent)
	}

	// The language header is what keeps the stored text from depending on where
	// the worker runs; see enrichAcceptLanguage.
	if profile.acceptLanguage != "" {
		request.Header.Set("Accept-Language", profile.acceptLanguage)
	}

	// No Accept-Encoding is set on purpose. The guarded client's transport adds
	// gzip and decodes the response transparently, and doing that by hand would
	// mean parsing a compressed body as if it were HTML.
	response, err := client.Do(request)
	if err != nil {
		return nil, newFailure(
			requestFailureKind(err),
			fmt.Errorf("%w: %w", ErrFetchFailed, err),
		)
	}

	return response, nil
}

// requestFailureKind classifies a failed request by whether another attempt could
// produce a different answer.
//
// A refusal by the outbound policy and a redirect chain that exceeded its limit
// are statements about the request itself: the same URL resolves to the same
// blocked destination, and the same chain has the same hops. Asking again spends
// queue time and network on a certainty, so both are permanent.
//
// Everything else the transport reports describes the network at that moment —
// DNS, the connection, TLS, a timeout, a stream reset — and stays retryable.
func requestFailureKind(err error) FailureKind {
	if errors.Is(err, security.ErrOutboundAddressBlocked) ||
		errors.Is(err, security.ErrTooManyRedirects) {
		return FailureContent
	}

	return FailureFetch
}

// reader validates the response and returns a reader positioned at the start of
// the document body.
//
// The body is bounded by the policy's response size limit, before and after any
// transcoding, and is never fully buffered, so a large page costs a bounded
// amount of memory rather than whatever the origin decides to send.
func (e *enricher) reader(
	response *http.Response,
) (
	io.Reader,
	*url.URL,
	error,
) {
	contentType := response.Header.Get("Content-Type")

	// A non-success status is a real answer from the origin. It is not a page to
	// parse, so it is a failure rather than an empty result. Whether that failure
	// is worth another attempt depends on which status it is, not on the fact
	// that a status came back at all.
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		failure := newFailure(
			statusFailureKind(response.StatusCode),
			fmt.Errorf(
				"%w: status %d",
				ErrUnexpectedStatus,
				response.StatusCode,
			),
		)

		// Kept as a field as well as in the message, so a caller can log the
		// status without reading the text.
		failure.StatusCode = response.StatusCode

		return nil, nil, failure
	}

	limited := e.policy.LimitOutboundBody(response.Body)

	body, err := prepareHTMLBody(limited, contentType)
	if err != nil {
		// A refused content type is a permanent content failure, but a body that
		// could not be read has already been classified as a fetch failure by
		// prepareHTMLBody, and that classification is the one that survives.
		return nil, nil, wrapFailure(FailureContent, err)
	}

	// The tokenizer assumes UTF-8. A page that declares another encoding, or that
	// prescans as one, is transcoded first so its metadata is read as text rather
	// than as mojibake.
	//
	// DetermineEncoding passes valid UTF-8 through unchanged, including a page
	// that never declared a charset, so this does not alter pages that were
	// already correct.
	decoded, err := charset.NewReader(body, contentType)
	if err != nil {
		// NewReader reports io.EOF only when its preview read received no bytes
		// at all, which is an empty response body rather than a failed read. An
		// empty document is a successful enrichment that found nothing, so it
		// keeps the meaning it had before transcoding was added.
		if errors.Is(err, io.EOF) {
			return bytes.NewReader(nil), responseURL(response), nil
		}

		// Any other error is the preview read failing, which is the same network
		// failure as any other body read.
		return nil, nil, newFailure(
			FailureFetch,
			fmt.Errorf("%w: %w", ErrFetchFailed, err),
		)
	}

	// A legacy 8-bit encoding can expand when transcoded, so the decoded stream
	// is bounded again. For UTF-8 this is a no-op: the decoded stream is exactly
	// what the body limit above already applied to.
	return e.policy.LimitOutboundBody(decoded), responseURL(response), nil
}

// statusFailureKind classifies a non-success status by whether the origin could
// plausibly answer differently later.
//
// The two groups are separated because they have different consequences for a
// caller that retries. A terminal client status (401, 403, 404, 410) and a
// redirect that was not followed describe the resource itself, and asking again
// produces the same answer. Rate limiting (429) and a server-side error (5xx)
// describe the origin at that moment: they are the cases where a later attempt
// genuinely can succeed, so they are classified as fetch failures and stay
// eligible for a bounded retry.
//
// Every non-success status remains a failure either way. Only the classification
// differs, and it decides retrying, never what is recorded.
func statusFailureKind(statusCode int) FailureKind {
	if statusCode == http.StatusTooManyRequests ||
		statusCode >= http.StatusInternalServerError {
		return FailureFetch
	}

	return FailureContent
}

// responseURL returns the URL the response actually came from.
//
// After a redirect that is not the requested URL, and it is the one relative
// metadata URLs are relative to. The response's own request is authoritative,
// because the transport rewrites it on every hop.
func responseURL(response *http.Response) *url.URL {
	if response.Request != nil && response.Request.URL != nil {
		return response.Request.URL
	}

	return nil
}

// htmlMediaTypes are the declared types that are accepted without sniffing.
//
// The list is deliberately short. application/xml and text/xml are not in it even
// though a server may label XHTML that way, because those types are at least as
// likely to be a feed or a data document, and refusing them is safer than
// guessing. A page served with an unusual type can still succeed through
// sniffing, which is how a mislabelled HTML page is not lost.
var htmlMediaTypes = map[string]struct{}{
	"text/html":             {},
	"application/xhtml+xml": {},
}

// prepareHTMLBody confirms the response is worth parsing as HTML and returns a
// reader positioned at the start of the document.
//
// A declared HTML type is taken at its word. Everything else, including an absent
// type, is sniffed, because a meaningful share of pages are served with no
// content type at all or with something generic, and refusing those would lose
// real pages. A declared type that is definitely something else, such as an image
// or a PDF, is refused outright without reading the body beyond the sniff window.
//
// The body is not fully read here. The returned reader is still live and is read
// by the caller, which is why the close belongs to the caller of this function.
//
// Sniffing reads ahead and then hands the bytes back, so the parser still sees
// the whole document.
func prepareHTMLBody(
	body io.Reader,
	contentType string,
) (
	io.Reader,
	error,
) {
	mediaType := parseMediaType(contentType)

	if _, isHTML := htmlMediaTypes[mediaType]; isHTML {
		return body, nil
	}

	// A body that cannot be read is a network failure rather than a statement
	// about the page, so it carries the fetch classification from here and is not
	// relabelled as a content failure by the caller.
	prefix, err := readSniffPrefix(body)
	if err != nil {
		return nil, newFailure(
			FailureFetch,
			fmt.Errorf(
				"%w: %w",
				ErrFetchFailed,
				err,
			),
		)
	}

	// The sniffed bytes are put back unconditionally, so the decision made here
	// never changes what the parser reads.
	restored := io.MultiReader(bytes.NewReader(prefix), body)

	if isHTMLPrefix(prefix) {
		return restored, nil
	}

	return nil, fmt.Errorf(
		"%w: content type %q",
		ErrUnsupportedContentType,
		mediaType,
	)
}

// parseMediaType reduces a Content-Type header to its lowercased type with any
// parameters such as a charset removed.
func parseMediaType(contentType string) string {
	if strings.TrimSpace(contentType) == "" {
		return ""
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// A malformed header is treated as absent rather than as a refusal, so
		// that it falls through to sniffing instead of being trusted or obeyed.
		trimmed := strings.TrimSpace(contentType)

		if index := strings.Index(trimmed, ";"); index >= 0 {
			trimmed = strings.TrimSpace(trimmed[:index])
		}

		return strings.ToLower(trimmed)
	}

	return strings.ToLower(mediaType)
}

// sniffLength is how much of the body is examined when the declared type does not
// settle the question.
//
// It is small on purpose. Every marker looked for appears at the very start of
// any real document, so reading further would buffer bytes for no gain.
const sniffLength = 512

// readSniffPrefix reads up to sniffLength bytes from the start of body.
//
// A short read is not an error: a document smaller than the sniff window is
// simply sniffed in full.
func readSniffPrefix(
	body io.Reader,
) (
	[]byte,
	error,
) {
	buffer := make([]byte, sniffLength)

	read, err := io.ReadFull(body, buffer)
	if err != nil &&
		!errors.Is(err, io.EOF) &&
		!errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}

	return buffer[:read], nil
}

// htmlPrefixMarkers are the openings a document cannot avoid.
//
// Checking for the document structure rather than for a leading "<" is what
// separates HTML from a binary payload that happens to start with a byte that
// renders as a bracket.
var htmlPrefixMarkers = []string{
	"<!doctype html",
	"<html",
	"<head",
	"<body",
	"<title",
	"<meta",
	"<script",
	"<?xml",
}

// isHTMLPrefix reports whether the sniffed prefix looks like an HTML document.
func isHTMLPrefix(prefix []byte) bool {
	if len(prefix) == 0 {
		return false
	}

	// A NUL byte means binary. HTML is not permitted to contain one, so this is
	// a reliable and cheap signal that the body is not text.
	for _, b := range prefix {
		if b == 0 {
			return false
		}
	}

	lowered := strings.ToLower(string(prefix))

	for _, marker := range htmlPrefixMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}

	return false
}
