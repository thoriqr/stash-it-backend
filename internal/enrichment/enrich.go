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
	client *http.Client
	policy security.OutboundFetchPolicy
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

// NewEnricher builds an Enricher around a guarded HTTP client.
//
// client must come from security.NewGuardedHTTPClient. That is not a convention
// this package asks nicely for: the SSRF policy lives in the client's dialer, so
// a client from anywhere else means the fetch has none of it, and enrichment
// cannot detect that for itself.
//
// policy is needed only for the response body limit, which is applied here rather
// than by the client because a client cannot bound a body it does not read. It
// must be the same policy the client was built with.
func NewEnricher(
	client *http.Client,
	policy security.OutboundFetchPolicy,
) *enricher {
	return &enricher{
		client: client,
		policy: policy,
	}
}

// Enrich fetches rawURL and extracts metadata from the response.
//
// A page that was reached but declares nothing usable returns an empty Metadata
// and a nil error, because "this page told us nothing" is a successful
// enrichment whose result happens to be empty. Only a failure to obtain or use a
// page produces an error, and every error is a *Failure carrying a Kind, so a
// caller can tell a retryable problem from a permanent one.
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
	// is reported as a bad URL instead of surfacing as a transport error.
	if _, err := url.Parse(strings.TrimSpace(rawURL)); err != nil {
		return Metadata{}, newFailure(
			FailureFetch,
			fmt.Errorf("%w: %w", ErrInvalidURL, err),
		)
	}

	response, err := e.do(ctx, rawURL)
	if err != nil {
		return Metadata{}, err
	}

	// The body is closed here and not inside do or reader, because both hand back
	// a reader that is still bound to it. A close deferred in a function that
	// returns such a reader runs before the caller reads it, and every page would
	// then arrive as "read on closed response body". The close is paired with the
	// whole read, which is the only pairing that is correct.
	defer response.Body.Close()

	body, finalURL, err := e.reader(response)
	if err != nil {
		return Metadata{}, err
	}

	doc, err := extract(body, finalURL)
	if err != nil {
		return Metadata{}, newFailure(
			FailureParse,
			fmt.Errorf("%w: %w", ErrParseFailed, err),
		)
	}

	return selectMetadata(doc, finalURL), nil
}

// do performs the request and returns the response with its body unread.
//
// It deliberately does not close the body. The caller reads it and closes it.
func (e *enricher) do(
	ctx context.Context,
	rawURL string,
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

	request.Header.Set("Accept", enrichAcceptHeader)

	// No Accept-Encoding is set on purpose. The guarded client's transport adds
	// gzip and decodes the response transparently, and doing that by hand would
	// mean parsing a compressed body as if it were HTML.
	response, err := e.client.Do(request)
	if err != nil {
		return nil, newFailure(
			FailureFetch,
			fmt.Errorf("%w: %w", ErrFetchFailed, err),
		)
	}

	return response, nil
}

// reader validates the response and returns a reader positioned at the start of
// the document body.
//
// The body is bounded by the policy's response size limit and never fully
// buffered, so a large page costs a bounded amount of memory rather than whatever
// the origin decides to send.
func (e *enricher) reader(
	response *http.Response,
) (
	io.Reader,
	*url.URL,
	error,
) {
	// A non-success status is a real answer from the origin. It is not a page to
	// parse, so it is a failure rather than an empty result. Whether that failure
	// is worth another attempt depends on which status it is, not on the fact
	// that a status came back at all.
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, newFailure(
			statusFailureKind(response.StatusCode),
			fmt.Errorf(
				"%w: status %d",
				ErrUnexpectedStatus,
				response.StatusCode,
			),
		)
	}

	limited := e.policy.LimitOutboundBody(response.Body)

	body, err := prepareHTMLBody(
		limited,
		response.Header.Get("Content-Type"),
	)
	if err != nil {
		// A refused content type is a permanent content failure, but a body that
		// could not be read has already been classified as a fetch failure by
		// prepareHTMLBody, and that classification is the one that survives.
		return nil, nil, wrapFailure(FailureContent, err)
	}

	return body, responseURL(response), nil
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
