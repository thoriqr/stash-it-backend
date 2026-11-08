package enrichment

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/security"
)

// scriptedResponse is one canned reply from a sequenceTransport.
type scriptedResponse struct {
	status   int
	body     string
	location string
}

// sequenceTransport returns its replies in order and records every request, so a
// test can assert both what was sent and how many attempts were made.
type sequenceTransport struct {
	responses []scriptedResponse
	requests  []*http.Request
}

func (s *sequenceTransport) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	s.requests = append(s.requests, req)

	index := len(s.requests) - 1
	if index >= len(s.responses) {
		index = len(s.responses) - 1
	}

	response := s.responses[index]

	status := response.status
	if status == 0 {
		status = http.StatusOK
	}

	header := http.Header{"Content-Type": []string{"text/html"}}
	if response.location != "" {
		header.Set("Location", response.location)
	}

	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(response.body)),
		Request:    req,
	}, nil
}

func newScriptedEnricher(transport http.RoundTripper) *enricher {
	return NewEnricher(
		&http.Client{Transport: transport},
		security.OutboundFetchPolicy{
			MaxResponseBytes: security.OutboundFetchMaxResponseBytes,
		},
	)
}

// A verified Tokopedia host is fetched with the Tokopedia profile, and the generic
// profile is not used because the first attempt succeeded.
func TestEnrich_TokopediaUsesVerifiedRequestProfile(t *testing.T) {
	stub := &recordingTransport{}

	metadata, err := newScriptedEnricher(stub).Enrich(
		context.Background(),
		"https://www.tokopedia.com/jullindo/product",
	)

	require.NoError(t, err)
	requireMetadata(t, metadata, "title", "Open Graph Title")

	require.Len(t, stub.requests, 1)
	require.Equal(t, "Mozilla/5.0", stub.requests[0].Header.Get("User-Agent"))
	require.Equal(t, "*/*", stub.requests[0].Header.Get("Accept"))
}

// An unknown host keeps the generic profile: no caller-supplied User-Agent and the
// HTML Accept header.
func TestEnrich_UnknownHostUsesGenericProfile(t *testing.T) {
	stub := &recordingTransport{}

	_, err := newScriptedEnricher(stub).Enrich(
		context.Background(),
		"https://example.com/articles/1",
	)

	require.NoError(t, err)
	require.Len(t, stub.requests, 1)
	require.Empty(t, stub.requests[0].Header.Get("User-Agent"))
	require.Contains(t, stub.requests[0].Header.Get("Accept"), "text/html")
}

// A host-specific attempt that fails on content is retried once with the generic
// profile, and the fallback's success is returned normally.
func TestEnrich_FallsBackToGenericOnContentFailure(t *testing.T) {
	stub := &sequenceTransport{
		responses: []scriptedResponse{
			{status: http.StatusForbidden},
			{status: http.StatusOK, body: testPage},
		},
	}

	metadata, err := newScriptedEnricher(stub).Enrich(
		context.Background(),
		"https://www.tokopedia.com/jullindo/product",
	)

	require.NoError(t, err)
	requireMetadata(t, metadata, "title", "Open Graph Title")

	require.Len(t, stub.requests, 2)
	require.Equal(t, "Mozilla/5.0", stub.requests[0].Header.Get("User-Agent"))
	require.Equal(t, "*/*", stub.requests[0].Header.Get("Accept"))

	// The fallback is the generic profile.
	require.Empty(t, stub.requests[1].Header.Get("User-Agent"))
	require.Contains(t, stub.requests[1].Header.Get("Accept"), "text/html")
}

// A server error is a fetch failure and is left to the worker's retry; no second
// request is made.
func TestEnrich_DoesNotFallBackOnFetchFailure(t *testing.T) {
	stub := &sequenceTransport{
		responses: []scriptedResponse{
			{status: http.StatusInternalServerError},
			{status: http.StatusOK, body: testPage},
		},
	}

	_, err := newScriptedEnricher(stub).Enrich(
		context.Background(),
		"https://www.tokopedia.com/jullindo/product",
	)

	require.ErrorIs(t, err, ErrUnexpectedStatus)
	require.Equal(t, FailureFetch, Kind(err))
	require.Len(t, stub.requests, 1)
}

// Rate limiting is also a fetch failure and does not trigger an immediate generic
// request.
func TestEnrich_DoesNotFallBackOnRateLimit(t *testing.T) {
	stub := &sequenceTransport{
		responses: []scriptedResponse{
			{status: http.StatusTooManyRequests},
			{status: http.StatusOK, body: testPage},
		},
	}

	_, err := newScriptedEnricher(stub).Enrich(
		context.Background(),
		"https://www.tokopedia.com/jullindo/product",
	)

	require.ErrorIs(t, err, ErrUnexpectedStatus)
	require.Equal(t, FailureFetch, Kind(err))
	require.Len(t, stub.requests, 1)
}

func newTwoTransportEnricher(
	generic http.RoundTripper,
	tokopedia http.RoundTripper,
) *enricher {
	return NewEnricherWithTransports(
		&http.Client{Transport: generic},
		&http.Client{Transport: tokopedia},
		security.OutboundFetchPolicy{
			MaxResponseBytes: security.OutboundFetchMaxResponseBytes,
		},
	)
}

// The client is chosen from the original submitted hostname: an exact Tokopedia
// host uses the Tokopedia client, and everything else uses the generic one.
func TestEnrich_SelectsTransportByOriginalHostname(t *testing.T) {
	cases := []struct {
		name          string
		rawURL        string
		wantTokopedia bool
	}{
		{"tokopedia.com", "https://tokopedia.com/jullindo/product", true},
		{"www.tokopedia.com", "https://www.tokopedia.com/jullindo/product", true},
		{"the short-link host", "https://tk.tokopedia.com/abc", true},
		{"casing and a trailing dot", "https://WWW.TOKOPEDIA.COM./x", true},
		{"an unknown host", "https://example.com/article", false},
		{"shopee", "https://shopee.co.id/product/1/2", false},
		{"blibli", "https://www.blibli.com/p/product", false},
		{"tiktok shop", "https://shop.tiktok.com/view/product/1", false},
		{"a lookalike suffix", "https://tokopedia.com.evil.example/x", false},
		{"a prefixed lookalike", "https://notokopedia.com/x", false},
		{"an unsupported subdomain", "https://m.tokopedia.com/x", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			genericStub := &recordingTransport{}
			tokopediaStub := &recordingTransport{}

			_, err := newTwoTransportEnricher(genericStub, tokopediaStub).
				Enrich(context.Background(), tc.rawURL)
			require.NoError(t, err)

			if tc.wantTokopedia {
				require.Len(t, tokopediaStub.requests, 1)
				require.Empty(t, genericStub.requests)
			} else {
				require.Len(t, genericStub.requests, 1)
				require.Empty(t, tokopediaStub.requests)
			}
		})
	}
}

// A redirect from the short-link host to www.tokopedia.com is handled by the same
// client, so the whole chain stays on the Tokopedia transport rather than being
// re-selected from the redirect destination.
func TestEnrich_TokopediaRedirectStaysOnTokopediaTransport(t *testing.T) {
	tokopediaStub := &sequenceTransport{
		responses: []scriptedResponse{
			{
				status:   http.StatusFound,
				location: "https://www.tokopedia.com/jullindo/product",
			},
			{status: http.StatusOK, body: testPage},
		},
	}
	genericStub := &sequenceTransport{
		responses: []scriptedResponse{{status: http.StatusOK, body: testPage}},
	}

	metadata, err := newTwoTransportEnricher(genericStub, tokopediaStub).
		Enrich(context.Background(), "https://tk.tokopedia.com/abc")
	require.NoError(t, err)
	requireMetadata(t, metadata, "title", "Open Graph Title")

	require.Len(t, tokopediaStub.requests, 2)
	require.Empty(t, genericStub.requests, "a redirect must not switch transport")

	require.Equal(t, "tk.tokopedia.com", tokopediaStub.requests[0].URL.Hostname())
	require.Equal(t, "www.tokopedia.com", tokopediaStub.requests[1].URL.Hostname())
	require.Equal(
		t,
		"Mozilla/5.0",
		tokopediaStub.requests[0].Header.Get("User-Agent"),
	)
}

// A content failure on the Tokopedia profile falls back to the generic headers, but
// the retry stays on the Tokopedia client rather than moving to the generic one.
func TestEnrich_FallbackStaysOnTokopediaTransport(t *testing.T) {
	tokopediaStub := &sequenceTransport{
		responses: []scriptedResponse{
			{status: http.StatusForbidden},
			{status: http.StatusOK, body: testPage},
		},
	}
	genericStub := &sequenceTransport{
		responses: []scriptedResponse{{status: http.StatusOK, body: testPage}},
	}

	metadata, err := newTwoTransportEnricher(genericStub, tokopediaStub).
		Enrich(context.Background(), "https://www.tokopedia.com/jullindo/product")
	require.NoError(t, err)
	requireMetadata(t, metadata, "title", "Open Graph Title")

	require.Len(t, tokopediaStub.requests, 2)
	require.Empty(
		t,
		genericStub.requests,
		"the header fallback must not switch to the generic transport",
	)

	// The first attempt used the Tokopedia headers; the retry used the generic
	// headers on the same client.
	require.Equal(
		t,
		"Mozilla/5.0",
		tokopediaStub.requests[0].Header.Get("User-Agent"),
	)
	require.Equal(t, "*/*", tokopediaStub.requests[0].Header.Get("Accept"))
	require.Empty(t, tokopediaStub.requests[1].Header.Get("User-Agent"))
	require.Contains(
		t,
		tokopediaStub.requests[1].Header.Get("Accept"),
		"text/html",
	)
}
