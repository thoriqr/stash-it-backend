package enrichment

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/security"
)

// newTestEnricher builds an Enricher over an httptest server's URL.
//
// The client here is a plain one rather than security.NewGuardedHTTPClient,
// because httptest binds 127.0.0.1 and the guarded client correctly refuses
// loopback. This is the seam the constructor exists to provide: enrichment
// never builds a client, so a test may supply one that permits loopback.
//
// The SSRF policy is verified separately, in internal/security, and its
// composition with a real server is verified below in
// TestEnrich_WithGuardedClientRefusesLoopback.
func newTestEnricher(t *testing.T, rawURL string) *enricher {
	t.Helper()

	return NewEnricher(
		&http.Client{},
		security.OutboundFetchPolicy{
			MaxResponseBytes: security.OutboundFetchMaxResponseBytes,
		},
	)
}

const testPage = `<!doctype html>
<html>
<head>
	<title>Document Title</title>
	<meta property="og:title" content="Open Graph Title">
	<meta property="og:image" content="https://example.com/og.png">
	<meta name="application-name" content="Example App">
</head>
<body><p>Body</p></body>
</html>`

// serveHTML starts a server returning body as an HTML page.
func serveHTML(t *testing.T, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, body)
		}),
	)
	t.Cleanup(server.Close)

	return server
}

func TestEnrich_Success(t *testing.T) {
	server := serveHTML(t, testPage)

	metadata, err := newTestEnricher(t, server.URL).
		Enrich(context.Background(), server.URL)

	require.NoError(t, err)
	requireMetadata(t, metadata, "title", "Open Graph Title")
	requireMetadata(t, metadata, "platform", "Example App")
	requireMetadata(t, metadata, "image", "https://example.com/og.png")
}

func TestEnrich_SendsAnHTMLAcceptHeader(t *testing.T) {
	var gotAccept string

	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAccept = r.Header.Get("Accept")

			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, testPage)
		}),
	)
	defer server.Close()

	_, err := newTestEnricher(t, server.URL).
		Enrich(context.Background(), server.URL)

	require.NoError(t, err)
	require.Contains(t, gotAccept, "text/html")
	require.Contains(t, gotAccept, "application/xhtml+xml")
}

func TestEnrich_UsesTheInjectedClient(t *testing.T) {
	// Proves enrichment does not construct its own transport. The injected
	// client's transport is the only one that can be reached, so a request
	// arriving here is the request enrichment made.
	stub := &recordingTransport{}

	client := &http.Client{Transport: stub}

	enricher := NewEnricher(
		client,
		security.OutboundFetchPolicy{
			MaxResponseBytes: security.OutboundFetchMaxResponseBytes,
		},
	)

	metadata, err := enricher.Enrich(
		context.Background(),
		"https://example.com/articles/1",
	)

	require.NoError(t, err)
	requireMetadata(t, metadata, "title", "Open Graph Title")

	require.Len(t, stub.requests, 1)

	request := stub.requests[0]
	require.Equal(t, http.MethodGet, request.Method)
	require.Equal(t, "https://example.com/articles/1", request.URL.String())
	require.Contains(t, request.Header.Get("Accept"), "text/html")

	// Enrichment must not set its own User-Agent. The guarded client owns that,
	// and a second layer here would make the caller that builds the client look
	// ineffective.
	require.Empty(t, request.Header.Get("User-Agent"))

	// It must also leave compression to the transport, or the body would arrive
	// compressed and never parse.
	require.Empty(t, request.Header.Get("Accept-Encoding"))
}

func TestEnrich_ClosesTheResponseBody(t *testing.T) {
	stub := &recordingTransport{
		body: &trackedBody{
			Reader: strings.NewReader(testPage),
		},
	}

	enricher := NewEnricher(
		&http.Client{Transport: stub},
		security.OutboundFetchPolicy{
			MaxResponseBytes: security.OutboundFetchMaxResponseBytes,
		},
	)

	_, err := enricher.Enrich(context.Background(), "https://example.com/")

	require.NoError(t, err)
	require.True(t, stub.body.closed, "the response body must always be closed")
}

func TestEnrich_ClosesTheBodyOnFailure(t *testing.T) {
	// A non-success status returns before the body is read, and the deferred
	// close still has to run.
	stub := &recordingTransport{
		statusCode: http.StatusInternalServerError,
		body: &trackedBody{
			Reader: strings.NewReader("error"),
		},
	}

	enricher := NewEnricher(
		&http.Client{Transport: stub},
		security.OutboundFetchPolicy{
			MaxResponseBytes: security.OutboundFetchMaxResponseBytes,
		},
	)

	_, err := enricher.Enrich(context.Background(), "https://example.com/")

	require.ErrorIs(t, err, ErrUnexpectedStatus)
	require.True(t, stub.body.closed, "the response body must be closed on failure")
}

// Every non-success status is a failure with the same sentinel. What differs is
// the classification, and it differs for exactly one reason: whether the origin
// could plausibly answer differently later.
func TestEnrich_NonSuccessStatus(t *testing.T) {
	cases := []struct {
		status int
		kind   FailureKind
	}{
		// Terminal client statuses describe the resource itself, so asking again
		// produces the same answer.
		{http.StatusBadRequest, FailureContent},
		{http.StatusUnauthorized, FailureContent},
		{http.StatusForbidden, FailureContent},
		{http.StatusNotFound, FailureContent},
		{http.StatusGone, FailureContent},

		// Rate limiting and server-side errors describe the origin at that
		// moment, which is the case a bounded retry exists for.
		{http.StatusTooManyRequests, FailureFetch},
		{http.StatusInternalServerError, FailureFetch},
		{http.StatusBadGateway, FailureFetch},
		{http.StatusServiceUnavailable, FailureFetch},
		{http.StatusGatewayTimeout, FailureFetch},
	}

	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, testPage)
				}),
			)
			defer server.Close()

			metadata, err := newTestEnricher(t, server.URL).
				Enrich(context.Background(), server.URL)

			require.ErrorIs(t, err, ErrUnexpectedStatus)
			require.Equal(t, tc.kind, Kind(err))

			// A non-success status is a failure, not an empty result. Returning
			// the parsed body here would report an unenrichable page as enriched.
			// This holds for both groups: retryability changes when the attempt
			// happens again, never whether it is a failure.
			require.True(t, metadata.IsEmpty())
		})
	}
}

func TestEnrich_NonHTMLContentType(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{"png", "image/png", "\x89PNG\r\n\x1a\nbinary"},
		{"jpeg", "image/jpeg", "\xff\xd8\xff\xe0binary"},
		{"pdf", "application/pdf", "%PDF-1.7\nbinary"},
		{"zip", "application/zip", "PK\x03\x04binary"},
		{"json", "application/json", `{"not":"html"}`},
		{"css", "text/css", "body { color: red; }"},
		{"plain text", "text/plain", "just some words"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", tt.contentType)
					_, _ = io.WriteString(w, tt.body)
				}),
			)
			defer server.Close()

			metadata, err := newTestEnricher(t, server.URL).
				Enrich(context.Background(), server.URL)

			require.ErrorIs(t, err, ErrUnsupportedContentType)
			require.Equal(t, FailureContent, Kind(err))
			require.True(t, metadata.IsEmpty())
		})
	}
}

func TestEnrich_MissingContentTypeIsSniffed(t *testing.T) {
	t.Run("an HTML body with no content type is accepted", func(t *testing.T) {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// No Content-Type at all. Go will sniff and set one, so the
				// header is explicitly suppressed below.
				w.Header()["Content-Type"] = nil
				_, _ = io.WriteString(w, testPage)
			}),
		)
		defer server.Close()

		metadata, err := newTestEnricher(t, server.URL).
			Enrich(context.Background(), server.URL)

		require.NoError(t, err)
		requireMetadata(t, metadata, "title", "Open Graph Title")
	})

	t.Run("a body with no content type and no HTML is refused", func(t *testing.T) {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = nil
				_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03, 0xff, 0xfe})
			}),
		)
		defer server.Close()

		_, err := newTestEnricher(t, server.URL).
			Enrich(context.Background(), server.URL)

		require.ErrorIs(t, err, ErrUnsupportedContentType)
	})

	t.Run("an HTML body mislabelled as octet-stream is sniffed", func(t *testing.T) {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = io.WriteString(w, testPage)
			}),
		)
		defer server.Close()

		metadata, err := newTestEnricher(t, server.URL).
			Enrich(context.Background(), server.URL)

		require.NoError(t, err)
		requireMetadata(t, metadata, "title", "Open Graph Title")
	})
}

func TestEnrich_ContentTypeWithCharset(t *testing.T) {
	for _, contentType := range []string{
		"text/html",
		"text/html; charset=utf-8",
		"text/html;charset=UTF-8",
		"TEXT/HTML",
		"application/xhtml+xml",
	} {
		t.Run(contentType, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", contentType)
					_, _ = io.WriteString(w, testPage)
				}),
			)
			defer server.Close()

			metadata, err := newTestEnricher(t, server.URL).
				Enrich(context.Background(), server.URL)

			require.NoError(t, err)
			requireMetadata(t, metadata, "title", "Open Graph Title")
		})
	}
}

func TestEnrich_FetchFailure(t *testing.T) {
	t.Run("a refused connection", func(t *testing.T) {
		// Started then closed, so the port is almost certainly not listening.
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		url := server.URL
		server.Close()

		_, err := newTestEnricher(t, url).
			Enrich(context.Background(), url)

		require.ErrorIs(t, err, ErrFetchFailed)
		require.Equal(t, FailureFetch, Kind(err))
	})

	t.Run("a cancelled context", func(t *testing.T) {
		server := serveHTML(t, testPage)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := newTestEnricher(t, server.URL).Enrich(ctx, server.URL)

		require.ErrorIs(t, err, ErrFetchFailed)
		require.Equal(t, FailureFetch, Kind(err))
	})
}

func TestEnrich_InvalidURL(t *testing.T) {
	// URLs that url.Parse itself rejects. Whether a URL may be fetched is the
	// client's decision, not this package's, so only parse failures are asserted
	// here.
	cases := []struct {
		name   string
		rawURL string
	}{
		{"malformed percent escape", "http://example.com/%zz"},
		{"control character", "http://exa\x7fmple.com/"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			metadata, err := newTestEnricher(t, tt.rawURL).
				Enrich(context.Background(), tt.rawURL)

			require.ErrorIs(t, err, ErrInvalidURL)
			require.Equal(t, FailureFetch, Kind(err))
			require.True(t, metadata.IsEmpty())
		})
	}
}

func TestEnrich_URLWithNoHost(t *testing.T) {
	// These parse, so they reach the client, which is what refuses them. The
	// distinction matters: it is the client reporting, not this package, which is
	// the same reason the SSRF assertions live further down.
	for _, rawURL := range []string{
		"",
		"https://",
		"http:///path",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := newTestEnricher(t, rawURL).
				Enrich(context.Background(), rawURL)

			require.Error(t, err)
			require.ErrorIs(t, err, ErrFetchFailed)
			require.Equal(t, FailureFetch, Kind(err))
		})
	}
}

func TestEnrich_UnsupportedScheme(t *testing.T) {
	// The client's transport only speaks http and https, so a file URL cannot be
	// requested even though enrichment itself does not check the scheme.
	for _, rawURL := range []string{
		"file:///etc/passwd",
		"gopher://example.com/",
		"ftp://example.com/file",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := newTestEnricher(t, rawURL).
				Enrich(context.Background(), rawURL)

			require.Error(t, err)
			require.NotErrorIs(t, err, ErrUnsupportedContentType)
			require.Equal(t, FailureFetch, Kind(err))
		})
	}
}

func TestEnrich_GuardedClientRefusesAHostThatDoesNotResolve(t *testing.T) {
	// metadata.google.internal only resolves to the metadata address on GCP, so
	// what happens to it depends on where the test runs: inside GCP the dialer
	// refuses the resolved address, elsewhere the lookup fails first. Either way
	// it is refused, which is the property worth asserting.
	policy := security.DefaultOutboundFetchPolicy()

	enricher := NewEnricher(
		security.NewGuardedHTTPClient(policy),
		policy,
	)

	metadata, err := enricher.Enrich(
		context.Background(),
		"http://metadata.google.internal/computeMetadata/v1/",
	)

	require.Error(t, err)
	require.Equal(t, FailureFetch, Kind(err))
	require.True(t, metadata.IsEmpty())
}

// TestEnrich_WithGuardedClientRefusesInternalTargets is the SSRF test that
// belongs to this package.
//
// Enrichment holds no SSRF logic of its own, so the property worth asserting is
// that the guarded client, composed the way production composes it, refuses every
// kind of internal destination. If someone later wires an Enricher to an
// unguarded client, this is the test that would have failed first.
func TestEnrich_WithGuardedClientRefusesInternalTargets(t *testing.T) {
	server := serveHTML(t, testPage)
	defer server.Close()

	policy := security.DefaultOutboundFetchPolicy()

	targets := []struct {
		name   string
		rawURL string
	}{
		{"a loopback server", server.URL},
		{"loopback ipv4 literal", "http://127.0.0.1/"},
		{"loopback ipv6 literal", "http://[::1]/"},
		{"an ipv4 mapped loopback", "http://[::ffff:127.0.0.1]/"},
		{"a private address", "http://10.0.0.1/"},
		{"a link local address", "http://169.254.169.254/computeMetadata/v1/"},
		{"the ipv6 metadata address", "http://[fd20:ce::254]/"},
		{"redis on loopback", "http://127.0.0.1:6379/"},
		{"postgres on a private address", "http://10.0.0.1:5432/"},
		{"localhost by name", "http://localhost:8080/"},
	}

	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			enricher := NewEnricher(
				security.NewGuardedHTTPClient(policy),
				policy,
			)

			metadata, err := enricher.Enrich(
				context.Background(),
				target.rawURL,
			)

			require.Error(t, err)

			// Every one of these is refused by the dialer, so the sentinel is
			// the security one rather than anything enrichment invented.
			require.ErrorIs(t, err, security.ErrOutboundAddressBlocked)
			require.Equal(t, FailureFetch, Kind(err))
			require.True(t, metadata.IsEmpty())
		})
	}
}

func TestEnrich_Redirect(t *testing.T) {
	t.Run("a redirect is followed and metadata resolves against the final URL", func(t *testing.T) {
		var mux *http.ServeMux

		mux = http.NewServeMux()
		mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/articles/final", http.StatusFound)
		})
		mux.HandleFunc("/articles/final", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")

			// A relative image resolves against /articles/, not against /old.
			_, _ = io.WriteString(w, `<html><head>
				<meta property="og:image" content="og.png">
				<meta property="og:title" content="After Redirect">
			</head></html>`)
		})

		server := httptest.NewServer(mux)
		defer server.Close()

		metadata, err := newTestEnricher(t, server.URL+"/old").
			Enrich(context.Background(), server.URL+"/old")

		require.NoError(t, err)
		requireMetadata(t, metadata, "title", "After Redirect")
		requireMetadata(
			t,
			metadata,
			"image",
			server.URL+"/articles/og.png",
		)
	})

	t.Run("a redirect chain beyond the limit fails", func(t *testing.T) {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/loop", http.StatusFound)
			}),
		)
		defer server.Close()

		policy := security.DefaultOutboundFetchPolicy()

		// The redirect cap belongs to the client, so the test drives a client
		// carrying the same cap the guarded client sets. The guarded client
		// cannot be used here because httptest is on loopback.
		client := &http.Client{
			CheckRedirect: func(
				req *http.Request,
				via []*http.Request,
			) error {
				if len(via) > policy.MaxRedirects {
					return security.ErrTooManyRedirects
				}

				return nil
			},
		}

		enricher := NewEnricher(client, policy)

		_, err := enricher.Enrich(context.Background(), server.URL)

		require.ErrorIs(t, err, ErrFetchFailed)
		require.ErrorIs(t, err, security.ErrTooManyRedirects)
		require.Equal(t, FailureFetch, Kind(err))
	})
}

func TestEnrich_WithGuardedClientRefusesLoopback(t *testing.T) {
	// The composition that matters in production: a real guarded client wired
	// into a real enricher refuses a loopback target. This is what proves the
	// two pieces fit, rather than merely that each compiles.
	server := serveHTML(t, testPage)
	defer server.Close()

	policy := security.DefaultOutboundFetchPolicy()

	enricher := NewEnricher(
		security.NewGuardedHTTPClient(policy),
		policy,
	)

	metadata, err := enricher.Enrich(context.Background(), server.URL)

	require.Error(t, err)
	require.ErrorIs(t, err, security.ErrOutboundAddressBlocked)
	require.True(t, metadata.IsEmpty())
}

func TestEnrich_ResponseSizeIsBounded(t *testing.T) {
	// A page larger than the limit must be truncated rather than buffered whole,
	// and truncation must not turn into an error. Anything past the limit is not
	// seen, so a title placed there is not extracted.
	padding := strings.Repeat("<!-- padding -->", 200)

	page := `<html><head>` +
		`<meta property="og:title" content="Before The Limit">` +
		padding +
		`<meta property="og:description" content="Past The Limit">` +
		`</head></html>`

	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, page)
		}),
	)
	defer server.Close()

	enricher := NewEnricher(
		&http.Client{},
		security.OutboundFetchPolicy{
			MaxResponseBytes: 256,
		},
	)

	metadata, err := enricher.Enrich(context.Background(), server.URL)

	// Truncation is not a failure. A page cut short still yielded a title.
	require.NoError(t, err)
	requireMetadata(t, metadata, "title", "Before The Limit")
	requireNoMetadata(t, metadata, "description")
}

func TestEnrich_MissingMetadataIsNotAFailure(t *testing.T) {
	cases := []struct {
		name string
		page string
	}{
		{"no metadata at all", `<html><body><p>Nothing</p></body></html>`},
		{"only a body", `<html><body><h1>Heading</h1></body></html>`},
		{"only whitespace metadata", `<html><head>
			<meta property="og:title" content="   ">
			<meta name="description" content="">
		</head></html>`},
		{"malformed but parseable", `<html><head><div><p>Broken`},
		{"an empty document", ``},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := serveHTML(t, tt.page)

			metadata, err := newTestEnricher(t, server.URL).
				Enrich(context.Background(), server.URL)

			// None of these is an error. "The page told us nothing" is a
			// successful enrichment with an empty result.
			require.NoError(t, err)
			require.True(t, metadata.IsEmpty())
		})
	}
}

func TestEnrich_MalformedJSONLDOverHTTP(t *testing.T) {
	server := serveHTML(t, `<html><head>
		<meta property="og:title" content="Survives">
		<script type="application/ld+json">{ broken }</script>
	</head></html>`)

	metadata, err := newTestEnricher(t, server.URL).
		Enrich(context.Background(), server.URL)

	require.NoError(t, err)
	requireMetadata(t, metadata, "title", "Survives")
}

func TestKind(t *testing.T) {
	t.Run("an error from this package keeps its kind", func(t *testing.T) {
		err := newFailure(FailureContent, ErrUnexpectedStatus)

		require.Equal(t, FailureContent, Kind(err))
	})

	t.Run("a wrapped error keeps the innermost kind", func(t *testing.T) {
		inner := newFailure(FailureContent, ErrUnsupportedContentType)
		wrapped := wrapFailure(FailureParse, inner)

		require.Equal(t, FailureContent, Kind(wrapped))
	})

	t.Run("a foreign error defaults to fetch", func(t *testing.T) {
		// The conservative default: suggesting a retry when there is no reason
		// to is cheaper than losing a recoverable failure.
		require.Equal(
			t,
			FailureFetch,
			Kind(errors.New("something else")),
		)
	})

	t.Run("no error has no kind", func(t *testing.T) {
		require.Equal(t, FailureKind(""), Kind(nil))
	})

	t.Run("wrapFailure passes nil through", func(t *testing.T) {
		require.NoError(t, wrapFailure(FailureParse, nil))
	})
}

// The status classification is the boundary a retry policy reads, so it is
// asserted directly rather than only through Enrich. The rule it encodes is one
// question: could the origin plausibly answer differently later?
func TestStatusFailureKind(t *testing.T) {
	cases := []struct {
		status int
		kind   FailureKind
	}{
		{http.StatusBadRequest, FailureContent},
		{http.StatusUnauthorized, FailureContent},
		{http.StatusForbidden, FailureContent},
		{http.StatusNotFound, FailureContent},
		{http.StatusGone, FailureContent},
		{http.StatusUnsupportedMediaType, FailureContent},
		{http.StatusUnprocessableEntity, FailureContent},

		{http.StatusTooManyRequests, FailureFetch},
		{http.StatusInternalServerError, FailureFetch},
		{http.StatusNotImplemented, FailureFetch},
		{http.StatusBadGateway, FailureFetch},
		{http.StatusServiceUnavailable, FailureFetch},
		{http.StatusGatewayTimeout, FailureFetch},

		// A redirect that was not followed describes the request rather than the
		// origin's state, and it is not a "not right now" signal either.
		{http.StatusFound, FailureContent},
		{http.StatusPermanentRedirect, FailureContent},
	}

	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			require.Equal(t, tc.kind, statusFailureKind(tc.status))
		})
	}
}

// A body that could not be read is a network failure, not a statement about the
// page. Relabelling it as a content failure would make a mid-download
// disconnect permanent and lose the retry that would have recovered it. It is
// the one path where sniffing is involved, because a declared HTML type is
// returned unread and only sniffing has to touch the body.
func TestPrepareHTMLBody_ReadFailureIsAFetchFailure(t *testing.T) {
	body := &trackedBody{
		Reader: erroringReader{err: errors.New("connection reset by peer")},
	}

	_, err := prepareHTMLBody(body, "application/octet-stream")

	require.ErrorIs(t, err, ErrFetchFailed)
	require.Equal(
		t,
		FailureFetch,
		Kind(err),
		"an unreadable body must stay retryable",
	)

	// The classification the caller wraps it with must not override it, which is
	// what wrapFailure exists to guarantee.
	wrapped := wrapFailure(FailureContent, err)

	require.Equal(t, FailureFetch, Kind(wrapped))
}

// A refused content type stays a permanent content failure through the same
// wrapping the unreadable-body case goes through.
func TestPrepareHTMLBody_RefusedContentTypeIsContentFailure(t *testing.T) {
	body := &trackedBody{
		Reader: strings.NewReader("%PDF-1.7\nbinary"),
	}

	_, err := prepareHTMLBody(body, "application/pdf")

	require.ErrorIs(t, err, ErrUnsupportedContentType)

	wrapped := wrapFailure(FailureContent, err)

	require.Equal(t, FailureContent, Kind(wrapped))
}

// erroringReader fails every read, standing in for a connection that dropped
// partway through the body.
type erroringReader struct {
	err error
}

func (r erroringReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestFailureSentinelsAreDistinct(t *testing.T) {
	// A caller distinguishes failures by sentinel, so two sentinels sharing a
	// value would make two different failures indistinguishable.
	sentinels := []error{
		ErrInvalidURL,
		ErrFetchFailed,
		ErrParseFailed,
		ErrUnexpectedStatus,
		ErrUnsupportedContentType,
	}

	for i, first := range sentinels {
		for j, second := range sentinels {
			if i == j {
				continue
			}

			require.NotEqual(t, first.Error(), second.Error())
		}
	}
}

func TestFailurePreservesItsCause(t *testing.T) {
	// A failure with no cause in it is not diagnosable, so the cause is kept in
	// the chain. Rendering one to a user is the caller's decision, made through
	// its own error type rather than by reading this string.
	cause := errors.New("dial tcp 169.254.169.254:80: connection refused")

	err := wrapFailure(FailureFetch, cause)

	require.ErrorIs(t, err, cause)

	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, FailureFetch, failure.Kind)
}

// recordingTransport records every request and replies with a canned response.
type recordingTransport struct {
	requests   []*http.Request
	body       *trackedBody
	statusCode int
}

func (r *recordingTransport) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	r.requests = append(r.requests, req)

	status := r.statusCode
	if status == 0 {
		status = http.StatusOK
	}

	body := r.body
	if body == nil {
		body = &trackedBody{
			Reader: strings.NewReader(testPage),
		}
	}

	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{"text/html"},
		},
		Body:    body,
		Request: req,
	}, nil
}

// trackedBody records whether it was closed.
type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true

	return nil
}
