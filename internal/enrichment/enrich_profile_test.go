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
	status int
	body   string
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

	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
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
