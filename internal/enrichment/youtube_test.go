package enrichment

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/security"
)

// The table covers every URL shape the mobile apps and the share sheet produce,
// and the lookalikes that must not be mistaken for one. A lookalike falling
// through to the HTML path is the safe outcome: those hosts are fetched and
// parsed as before, so nothing is lost by being strict here.
func TestYouTubeVideoID(t *testing.T) {
	cases := map[string]struct {
		rawURL string
		want   string
		wantOK bool
	}{
		"watch": {
			rawURL: "https://www.youtube.com/watch?v=8D2CzTH8YoY",
			want:   "8D2CzTH8YoY",
			wantOK: true,
		},
		"watch with tracking params": {
			rawURL: "https://www.youtube.com/watch?v=h-Dezsc0oQ4&si=zKZLyX-_0wRJr2-r",
			want:   "h-Dezsc0oQ4",
			wantOK: true,
		},
		"watch without www": {
			rawURL: "https://youtube.com/watch?v=8D2CzTH8YoY",
			want:   "8D2CzTH8YoY",
			wantOK: true,
		},
		"live": {
			rawURL: "https://youtube.com/live/rFZHOHl-L8A?si=5-EjlgAJwzl11bgH",
			want:   "rFZHOHl-L8A",
			wantOK: true,
		},
		"shorts": {
			rawURL: "https://www.youtube.com/shorts/rFZHOHl-L8A",
			want:   "rFZHOHl-L8A",
			wantOK: true,
		},
		"embed": {
			rawURL: "https://www.youtube.com/embed/rFZHOHl-L8A",
			want:   "rFZHOHl-L8A",
			wantOK: true,
		},
		"short link": {
			rawURL: "https://youtu.be/rFZHOHl-L8A",
			want:   "rFZHOHl-L8A",
			wantOK: true,
		},
		"short link with params": {
			rawURL: "https://youtu.be/rFZHOHl-L8A?si=abc",
			want:   "rFZHOHl-L8A",
			wantOK: true,
		},
		"mobile host": {
			rawURL: "https://m.youtube.com/watch?v=rFZHOHl-L8A",
			want:   "rFZHOHl-L8A",
			wantOK: true,
		},
		"nocookie host": {
			rawURL: "https://www.youtube-nocookie.com/embed/rFZHOHl-L8A",
			want:   "rFZHOHl-L8A",
			wantOK: true,
		},
		"uppercase host": {
			rawURL: "https://WWW.YouTube.com/watch?v=rFZHOHl-L8A",
			want:   "rFZHOHl-L8A",
			wantOK: true,
		},

		// A channel, a playlist and a search address no single video.
		"channel": {
			rawURL: "https://www.youtube.com/@RickAstleyYT",
		},
		"playlist": {
			rawURL: "https://www.youtube.com/playlist?list=PLlaN88a7y2_plecYoJxvRFTLHVbIVAOoc",
		},
		"search": {
			rawURL: "https://www.youtube.com/results?search_query=lofi",
		},
		"home": {
			rawURL: "https://www.youtube.com/",
		},

		// An id of the wrong shape is refused rather than sent to oEmbed.
		"watch without v": {
			rawURL: "https://www.youtube.com/watch",
		},
		"watch with short v": {
			rawURL: "https://www.youtube.com/watch?v=short",
		},
		"watch with long v": {
			rawURL: "https://www.youtube.com/watch?v=8D2CzTH8YoYextra",
		},
		"watch with invalid characters": {
			rawURL: "https://www.youtube.com/watch?v=8D2CzTH8Yo!",
		},

		// Lookalikes fall outside the map, so they keep the HTML path.
		"lookalike suffix": {
			rawURL: "https://youtube.com.evil.example/watch?v=rFZHOHl-L8A",
		},
		"lookalike prefix": {
			rawURL: "https://notyoutube.com/watch?v=rFZHOHl-L8A",
		},
		"other host": {
			rawURL: "https://example.com/watch?v=rFZHOHl-L8A",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			parsed, err := url.Parse(tc.rawURL)
			require.NoError(t, err)

			got, ok := youtubeVideoID(parsed)

			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

// serveOEmbed starts a server that answers like YouTube's oEmbed endpoint.
//
// It records the url parameter it was asked about, so a test can assert that the
// video id from the submitted URL is the one that was requested.
func serveOEmbed(
	t *testing.T,
	body string,
	status int,
	requested *string,
) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if requested != nil {
				*requested = r.URL.Query().Get("url")
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}),
	)
	t.Cleanup(server.Close)

	return server
}

// newOEmbedTestEnricher points the YouTube path at a local server.
//
// The client is plain rather than guarded because httptest binds loopback, which
// the guarded client correctly refuses. That is the same seam newTestEnricher
// uses, and the guarded composition is verified in internal/security.
func newOEmbedTestEnricher(endpoint string) *enricher {
	policy := security.OutboundFetchPolicy{
		MaxResponseBytes: security.OutboundFetchMaxResponseBytes,
	}

	e := NewEnricher(&http.Client{}, policy)
	e.youtubeOEmbedURL = endpoint

	return e
}

const oembedResponse = `{
	"title": "lofi hip hop radio 📚 beats to relax/study to",
	"author_name": "Lofi Girl",
	"thumbnail_url": "https://i.ytimg.com/vi/rFZHOHl-L8A/hqdefault.jpg",
	"provider_name": "YouTube",
	"type": "video"
}`

func TestEnrich_YouTubeUsesOEmbed(t *testing.T) {
	var requested string

	server := serveOEmbed(t, oembedResponse, http.StatusOK, &requested)

	metadata, err := newOEmbedTestEnricher(server.URL).
		Enrich(context.Background(), "https://youtu.be/rFZHOHl-L8A")

	require.NoError(t, err)

	require.Equal(
		t,
		"https://www.youtube.com/watch?v=rFZHOHl-L8A",
		requested,
		"a short link must be resolved to the canonical watch URL",
	)

	require.NotNil(t, metadata.Title)
	require.Equal(
		t,
		"lofi hip hop radio 📚 beats to relax/study to",
		*metadata.Title,
	)

	require.NotNil(t, metadata.Platform)
	require.Equal(t, "YouTube", *metadata.Platform)

	require.NotNil(t, metadata.ImageURL)
	require.Equal(
		t,
		"https://i.ytimg.com/vi/rFZHOHl-L8A/hqdefault.jpg",
		*metadata.ImageURL,
	)

	require.NotNil(t, metadata.Author)
	require.Equal(t, "Lofi Girl", *metadata.Author)
}

// A response without provider_name still classifies the item, because the request
// already established which platform answered.
func TestEnrich_YouTubePlatformFallsBackWhenProviderNameMissing(t *testing.T) {
	server := serveOEmbed(
		t,
		`{"title":"A video","thumbnail_url":"https://i.ytimg.com/vi/x/hq.jpg"}`,
		http.StatusOK,
		nil,
	)

	metadata, err := newOEmbedTestEnricher(server.URL).
		Enrich(context.Background(), "https://www.youtube.com/watch?v=rFZHOHl-L8A")

	require.NoError(t, err)

	require.NotNil(t, metadata.Platform)
	require.Equal(t, "YouTube", *metadata.Platform)
}

// A private or deleted video answers 404, which no retry can change.
func TestEnrich_YouTubeUnavailableIsAPermanentFailure(t *testing.T) {
	server := serveOEmbed(t, `Not Found`, http.StatusNotFound, nil)

	_, err := newOEmbedTestEnricher(server.URL).
		Enrich(context.Background(), "https://youtu.be/rFZHOHl-L8A")

	require.Error(t, err)
	require.Equal(t, FailureContent, Kind(err))
	require.Equal(t, http.StatusNotFound, Status(err))
}

// A 5xx is a statement about the origin at that moment, so it stays retryable.
func TestEnrich_YouTubeServerErrorIsRetryable(t *testing.T) {
	server := serveOEmbed(t, `unavailable`, http.StatusServiceUnavailable, nil)

	_, err := newOEmbedTestEnricher(server.URL).
		Enrich(context.Background(), "https://youtu.be/rFZHOHl-L8A")

	require.Error(t, err)
	require.Equal(t, FailureFetch, Kind(err))
	require.Equal(t, http.StatusServiceUnavailable, Status(err))
}

// A body that is not the documented JSON is a permanent content failure rather
// than something to retry.
func TestEnrich_YouTubeMalformedBodyIsAPermanentFailure(t *testing.T) {
	server := serveOEmbed(t, `<html>nope</html>`, http.StatusOK, nil)

	_, err := newOEmbedTestEnricher(server.URL).
		Enrich(context.Background(), "https://youtu.be/rFZHOHl-L8A")

	require.Error(t, err)
	require.Equal(t, FailureContent, Kind(err))
}

// An empty title is a successful enrichment that found nothing, which is how this
// package treats every reached page that declared nothing usable.
func TestEnrich_YouTubeEmptyTitleIsNotAFailure(t *testing.T) {
	server := serveOEmbed(
		t,
		`{"title":"","provider_name":"YouTube"}`,
		http.StatusOK,
		nil,
	)

	metadata, err := newOEmbedTestEnricher(server.URL).
		Enrich(context.Background(), "https://youtu.be/rFZHOHl-L8A")

	require.NoError(t, err)
	require.Nil(t, metadata.Title)
	require.Nil(t, metadata.ImageURL)

	require.NotNil(t, metadata.Platform)
	require.Equal(t, "YouTube", *metadata.Platform)
}

// A channel URL is not a video, so it must still be fetched as HTML.
//
// The oEmbed endpoint is pointed at a server that fails the test if it is called,
// and the HTML client answers from a stub transport, so this asserts the routing
// decision without reaching the network.
func TestEnrich_YouTubeChannelKeepsTheHTMLPath(t *testing.T) {
	var oembedCalled bool

	oembed := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			oembedCalled = true

			w.WriteHeader(http.StatusInternalServerError)
		}),
	)
	t.Cleanup(oembed.Close)

	e := newOEmbedTestEnricher(oembed.URL)
	e.genericClient = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body: io.NopCloser(strings.NewReader(testPage)),
			}, nil
		}),
	}

	metadata, err := e.Enrich(
		context.Background(),
		"https://www.youtube.com/@RickAstleyYT",
	)

	require.NoError(t, err)
	require.False(t, oembedCalled, "a channel URL must not use oEmbed")

	require.NotNil(t, metadata.Title)
	require.Equal(t, "Open Graph Title", *metadata.Title)
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
