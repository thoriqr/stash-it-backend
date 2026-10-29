package enrichment

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/security"
)

// The logged form of a URL keeps what identifies the page and drops everything
// that could carry a credential or a tracking identifier.
func TestSanitizedURL(t *testing.T) {
	cases := []struct {
		name   string
		rawURL string
		want   string
	}{
		{
			name:   "a plain url is unchanged",
			rawURL: "https://example.com/articles/one",
			want:   "https://example.com/articles/one",
		},
		{
			name: "the query string is dropped",
			rawURL: "https://www.tokopedia.com/pexio/kursi" +
				"?t_id=1793310042302&t_st=1&t_pp=homepage",
			want: "https://www.tokopedia.com/pexio/kursi",
		},
		{
			name:   "the fragment is dropped",
			rawURL: "https://example.com/article#section-two",
			want:   "https://example.com/article",
		},
		{
			name:   "userinfo is dropped",
			rawURL: "https://user:secret@example.com/article",
			want:   "https://example.com/article",
		},
		{
			name:   "both are dropped together",
			rawURL: "https://user:secret@example.com/a?token=abc#frag",
			want:   "https://example.com/a",
		},
		{
			name:   "surrounding whitespace is trimmed",
			rawURL: "  https://example.com/a  ",
			want:   "https://example.com/a",
		},
		{
			name:   "an unparseable value is not echoed",
			rawURL: "https://example.com/%zz",
			want:   "<invalid>",
		},
		{
			name:   "an empty value stays empty",
			rawURL: "",
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, SanitizedURL(tc.rawURL))
		})
	}
}

// A failed request is the case the sanitizer exists for. net/http renders the
// failure as `Get "URL": cause` and masks only the password in that URL, so the
// user name, the query string and the fragment are all in the error text.
func TestSafeErrorMessage_TransportFailureCarriesNoURL(t *testing.T) {
	rawURL := "http://user:password@127.0.0.1/articles/one" +
		"?token=secret-token#section"

	policy := security.DefaultOutboundFetchPolicy()

	_, err := NewEnricher(
		security.NewGuardedHTTPClient(policy),
		policy,
	).Enrich(context.Background(), rawURL)

	require.Error(t, err)

	// The raw error really does carry the URL. This is what makes logging it
	// directly unsafe, and it keeps the sanitizer test honest.
	require.Contains(t, err.Error(), "token=secret-token")
	require.Contains(t, err.Error(), "#section")
	require.Contains(t, err.Error(), "user:")

	message := SafeErrorMessage(err)

	// The diagnostic part survives: what failed, and why.
	require.Contains(t, message, "fetching the page failed")
	require.Contains(
		t,
		message,
		"outbound address is not a public internet destination",
	)
	require.Contains(t, message, "dial tcp")

	// Nothing that identifies the user's request as a URL with parameters does.
	require.NotContains(t, message, "token=secret-token")
	require.NotContains(t, message, "#section")
	require.NotContains(t, message, "user:")
	require.NotContains(t, message, "password")
}

// The second place a URL can enter the error is the parse failure, which is also
// a url.Error. It is covered by the same rendering rule.
func TestSafeErrorMessage_InvalidURLCarriesNoURL(t *testing.T) {
	rawURL := "https://example.com/%zz?token=secret-token"

	_, err := NewEnricher(
		&http.Client{},
		security.DefaultOutboundFetchPolicy(),
	).Enrich(context.Background(), rawURL)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidURL)
	require.Contains(t, err.Error(), "token=secret-token")

	message := SafeErrorMessage(err)

	require.Contains(t, message, "url cannot be used")
	require.Contains(t, message, "invalid URL escape")
	require.NotContains(t, message, "example.com")
	require.NotContains(t, message, "token=secret-token")
}

// Every other failure is built from a sentinel, a status and a content type, so
// there is nothing to remove and the message is returned as it is.
func TestSafeErrorMessage_NonTransportErrorsAreUnchanged(t *testing.T) {
	statusFailure := newFailure(FailureContent, ErrUnexpectedStatus)
	statusFailure.StatusCode = 404

	require.Equal(
		t,
		ErrUnexpectedStatus.Error(),
		SafeErrorMessage(statusFailure),
	)
	require.Empty(t, SafeErrorMessage(nil))
	require.Equal(
		t,
		"something else",
		SafeErrorMessage(errors.New("something else")),
	)
}
