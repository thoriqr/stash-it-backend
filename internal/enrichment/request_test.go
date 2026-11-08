package enrichment

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/security"
)

func TestNormalizeHostname(t *testing.T) {
	cases := []struct {
		name     string
		host     string
		expected string
	}{
		{"already normalized", "www.tokopedia.com", "www.tokopedia.com"},
		{"upper case is lowered", "WWW.Tokopedia.COM", "www.tokopedia.com"},
		{"a trailing dot is removed", "www.tokopedia.com.", "www.tokopedia.com"},
		{"surrounding whitespace is trimmed", "  Tokopedia.com  ", "tokopedia.com"},
		{"empty stays empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, normalizeHostname(tc.host))
		})
	}
}

func TestRequestProfilesFor(t *testing.T) {
	cases := []struct {
		name       string
		host       string
		profiles   int
		wantFirst  requestProfile
		wantAccept string
	}{
		{
			name:       "www.tokopedia.com gets the verified profile first",
			host:       "www.tokopedia.com",
			profiles:   2,
			wantFirst:  tokopediaRequestProfile,
			wantAccept: "*/*",
		},
		{
			name:       "tokopedia.com gets the verified profile first",
			host:       "tokopedia.com",
			profiles:   2,
			wantFirst:  tokopediaRequestProfile,
			wantAccept: "*/*",
		},
		{
			name:       "casing is normalized before matching",
			host:       "WWW.TOKOPEDIA.COM",
			profiles:   2,
			wantFirst:  tokopediaRequestProfile,
			wantAccept: "*/*",
		},
		{
			name:       "a trailing dot is normalized before matching",
			host:       "www.tokopedia.com.",
			profiles:   2,
			wantFirst:  tokopediaRequestProfile,
			wantAccept: "*/*",
		},
		{
			name:      "an unknown host gets only the generic profile",
			host:      "example.com",
			profiles:  1,
			wantFirst: genericRequestProfile,
		},
		{
			name:      "shopee is on the generic profile",
			host:      "shopee.co.id",
			profiles:  1,
			wantFirst: genericRequestProfile,
		},
		{
			name:      "blibli is on the generic profile",
			host:      "www.blibli.com",
			profiles:  1,
			wantFirst: genericRequestProfile,
		},
		{
			name:      "tiktok shop is on the generic profile",
			host:      "shop.tiktok.com",
			profiles:  1,
			wantFirst: genericRequestProfile,
		},
		{
			name:      "a lookalike suffix does not match",
			host:      "tokopedia.com.evil.example",
			profiles:  1,
			wantFirst: genericRequestProfile,
		},
		{
			name:      "a prefixed lookalike does not match",
			host:      "notokopedia.com",
			profiles:  1,
			wantFirst: genericRequestProfile,
		},
		{
			name:      "a suffixed lookalike does not match",
			host:      "eviltokopedia.com",
			profiles:  1,
			wantFirst: genericRequestProfile,
		},
		{
			name:      "a concatenated lookalike does not match",
			host:      "wwwtokopedia.com",
			profiles:  1,
			wantFirst: genericRequestProfile,
		},
		{
			name:      "an unsupported subdomain falls back to generic",
			host:      "m.tokopedia.com",
			profiles:  1,
			wantFirst: genericRequestProfile,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profiles := requestProfilesFor(tc.host)

			require.Len(t, profiles, tc.profiles)
			require.Equal(t, tc.wantFirst, profiles[0])

			if tc.wantAccept != "" {
				require.Equal(t, tc.wantAccept, profiles[0].accept)
				require.Equal(t, "Mozilla/5.0", profiles[0].userAgent)

				// The generic profile must still follow, so the fallback stays
				// reachable.
				require.Equal(t, genericRequestProfile, profiles[1])
			}
		})
	}
}

func TestShouldFallBackToGeneric(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "a terminal content failure falls back",
			err:  newFailure(FailureContent, ErrUnexpectedStatus),
			want: true,
		},
		{
			name: "an unsupported content type falls back",
			err:  newFailure(FailureContent, ErrUnsupportedContentType),
			want: true,
		},
		{
			name: "a fetch failure does not fall back",
			err:  newFailure(FailureFetch, ErrFetchFailed),
			want: false,
		},
		{
			name: "a nil error does not fall back",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldFallBackToGeneric(tc.err))
		})
	}
}

// A refused destination and an over-long redirect chain are content failures in
// the retry classification, but they describe the URL rather than the headers, so
// a different User-Agent cannot change them and no fallback request is made.
func TestShouldFallBackToGeneric_DoesNotRetryDestinationFailures(t *testing.T) {
	blocked := newFailure(
		FailureContent,
		fmt.Errorf("%w: test", security.ErrOutboundAddressBlocked),
	)
	require.False(t, shouldFallBackToGeneric(blocked))

	tooManyRedirects := newFailure(
		FailureContent,
		fmt.Errorf("%w: test", security.ErrTooManyRedirects),
	)
	require.False(t, shouldFallBackToGeneric(tooManyRedirects))
}
