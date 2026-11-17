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

type fakeProviderResolver struct {
	endpoint OEmbedEndpoint
	ok       bool
	calls    int
}

func (f *fakeProviderResolver) Resolve(
	_ context.Context,
	_ *url.URL,
) (OEmbedEndpoint, bool) {
	f.calls++

	return f.endpoint, f.ok
}

func newResolverEnricher(resolver ProviderResolver) *enricher {
	policy := security.OutboundFetchPolicy{
		MaxResponseBytes: security.OutboundFetchMaxResponseBytes,
	}

	return NewEnricherWithResolver(
		&http.Client{},
		&http.Client{},
		policy,
		resolver,
	)
}

// A host in the configured skip list is never asked for an endpoint, even when
// the resolver would match it.
func TestResolveOEmbedEndpoint_SkipsConfiguredHosts(t *testing.T) {
	resolver := &fakeProviderResolver{
		ok:       true,
		endpoint: OEmbedEndpoint{EndpointURL: "https://oembed.example/x"},
	}

	enricher := newResolverEnricher(resolver)
	enricher.SetOEmbedSkipHosts([]string{"WWW.Instagram.com."})

	parsed, err := url.Parse("https://www.instagram.com/reel/abc/")
	require.NoError(t, err)

	_, ok := enricher.resolveOEmbedEndpoint(context.Background(), parsed)

	require.False(t, ok)
	require.Zero(t, resolver.calls)
}

// A host that is not in the skip list keeps its normal registry endpoint.
func TestResolveOEmbedEndpoint_OtherHostsStillResolve(t *testing.T) {
	resolver := &fakeProviderResolver{
		ok:       true,
		endpoint: OEmbedEndpoint{EndpointURL: "https://oembed.example/x"},
	}

	enricher := newResolverEnricher(resolver)
	enricher.SetOEmbedSkipHosts([]string{"instagram.com"})

	parsed, err := url.Parse("https://example.com/post")
	require.NoError(t, err)

	endpoint, ok := enricher.resolveOEmbedEndpoint(context.Background(), parsed)

	require.True(t, ok)
	require.Equal(t, "https://oembed.example/x", endpoint.EndpointURL)
	require.Equal(t, 1, resolver.calls)
}

// The skip is an operator override, so it also applies to a built-in endpoint.
func TestResolveOEmbedEndpoint_SkipAppliesToBuiltInHosts(t *testing.T) {
	enricher := newResolverEnricher(nil)
	enricher.SetOEmbedSkipHosts([]string{"youtube.com", "www.youtube.com"})

	parsed, err := url.Parse("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	require.NoError(t, err)

	_, ok := enricher.resolveOEmbedEndpoint(context.Background(), parsed)

	require.False(t, ok)
}

// serveOEmbedPage serves an oEmbed endpoint and an HTML page from one server.
//
// Both handlers count their calls, which is what lets a test assert that the more
// expensive path was skipped.
func serveOEmbedPage(
	t *testing.T,
	endpointPath string,
	oembedStatus int,
	oembedBody string,
	pageStatus int,
	pageBody string,
	pageHits *int,
	oembedHits *int,
) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc(endpointPath, func(w http.ResponseWriter, _ *http.Request) {
		*oembedHits++

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(oembedStatus)
		_, _ = io.WriteString(w, oembedBody)
	})

	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) {
		*pageHits++

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(pageStatus)
		_, _ = io.WriteString(w, pageBody)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func TestEnrich_OEmbedFirst(t *testing.T) {
	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusOK,
		`{
			"title": "oEmbed Title",
			"provider_name": "Example",
			"thumbnail_url": "https://example.com/thumb.png",
			"author_name": "Jane",
			"description": "oEmbed description"
		}`,
		http.StatusOK,
		testPage,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			ProviderName: "Example",
			EndpointURL:  server.URL + "/oembed",
		},
	}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)

	require.Equal(t, "oEmbed Title", *metadata.Title)
	require.Equal(t, "Example", *metadata.Platform)
	require.Equal(t, "https://example.com/thumb.png", *metadata.ImageURL)
	require.Equal(t, "oEmbed description", *metadata.Description)
	require.Equal(t, "Jane", *metadata.Author)

	require.Equal(t, 1, oembedHits)
	require.Equal(t, 0, pageHits, "a full oEmbed result should skip the page")
}

// A localized provider_name from an oEmbed response is reduced to the brand, so
// the platform does not depend on the language the endpoint answered in.
func TestEnrich_OEmbedPlatformNameIsCanonicalized(t *testing.T) {
	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusOK,
		`{"title":"A title","provider_name":"Facebook 小组"}`,
		http.StatusOK,
		testPage,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/oembed",
		},
	}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "Facebook", *metadata.Platform)
}

// The oEmbed request carries the same language header, so a provider that
// localizes provider_name cannot make the platform depend on the worker's
// address.
func TestEnrich_OEmbedSendsAnAcceptLanguageHeader(t *testing.T) {
	var got string

	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Accept-Language")

			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(
				w,
				`{"title":"A title","provider_name":"Example"}`,
			)
		}),
	)
	t.Cleanup(server.Close)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/oembed",
		},
	}

	_, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		"https://example.com/page",
	)

	require.NoError(t, err)
	require.Equal(t, enrichAcceptLanguage, got)
}

func TestEnrich_OEmbedPhotoUsesURLAsImage(t *testing.T) {
	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusOK,
		`{
			"type": "photo",
			"title": "A photo",
			"url": "https://example.com/photo.jpg",
			"provider_name": "Flickr"
		}`,
		http.StatusOK,
		testPage,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/oembed",
		},
	}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "https://example.com/photo.jpg", *metadata.ImageURL)
	require.Equal(t, 0, pageHits)
}

// A provider that publishes only its name is not enough on its own. The platform
// is kept, but the page is still read for the title the endpoint did not supply.
func TestEnrich_OEmbedPlatformOnlyFallsBackToHTML(t *testing.T) {
	const page = `<html><head><title>Page Title</title></head></html>`

	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusOK,
		`{"provider_name":"TikTok"}`,
		http.StatusOK,
		page,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/oembed",
		},
	}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "Page Title", *metadata.Title)
	require.Equal(t, "TikTok", *metadata.Platform)
	require.Equal(t, 1, oembedHits)
	require.Equal(t, 1, pageHits)
}

// The Instagram case: the endpoint answers with the provider name only, while the
// page itself publishes the caption, description and cover as Open Graph.
func TestEnrich_PartialOEmbedIsCompletedFromHTML(t *testing.T) {
	const page = `<!doctype html>
	<html><head>
		<meta property="og:site_name" content="Instagram">
		<meta property="og:title" content="A real caption">
		<meta property="og:description" content="A real description">
		<meta property="og:image" content="https://example.com/photo.jpg">
	</head></html>`

	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusOK,
		`{"provider_name":"Instagram"}`,
		http.StatusOK,
		page,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/oembed",
		},
	}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "A real caption", *metadata.Title)
	require.Equal(t, "A real description", *metadata.Description)
	require.Equal(t, "https://example.com/photo.jpg", *metadata.ImageURL)
	require.Equal(t, "Instagram", *metadata.Platform)
	require.Equal(t, 1, pageHits)
}

const tiktokUniversalPage = `<!doctype html>
<html><head>
	<title>TikTok - Make Your Day</title>
	<script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">
	{"__DEFAULT_SCOPE__":{"webapp.video-detail":{"itemInfo":{"itemStruct":{
		"desc":"S42 catch #spacex #starship #space",
		"video":{"cover":"https://cdn.example/cover.jpg"}
	}}}}}
	</script>
</head></html>`

// TikTok publishes the caption and cover only in its state blob. The oEmbed
// response supplies the platform, and the page supplies the rest.
func TestEnrich_TikTokUniversalDataSuppliesTheTitle(t *testing.T) {
	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			ProviderName: "TikTok",
			EndpointURL:  "https://www.tiktok.com/oembed",
		},
	}

	enricher := newResolverEnricher(resolver)
	enricher.genericClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/oembed" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type": []string{"application/json"},
					},
					Body:    io.NopCloser(strings.NewReader(`{"provider_name":"TikTok"}`)),
					Request: r,
				}, nil
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body:    io.NopCloser(strings.NewReader(tiktokUniversalPage)),
				Request: r,
			}, nil
		}),
	}

	metadata, err := enricher.Enrich(
		context.Background(),
		"https://www.tiktok.com/@creator/video/123",
	)

	require.NoError(t, err)
	require.Equal(
		t,
		"S42 catch #spacex #starship #space",
		*metadata.Title,
	)
	require.Equal(t, "TikTok", *metadata.Platform)
	require.Equal(t, "https://cdn.example/cover.jpg", *metadata.ImageURL)
}

// An expired short link redirects to TikTok's not-found route with a 200. It is a
// content failure, not a successful enrichment with the shell title.
func TestEnrich_TikTokNotFoundPageIsAPermanentFailure(t *testing.T) {
	enricher := newResolverEnricher(&fakeProviderResolver{ok: false})

	enricher.genericClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body: io.NopCloser(strings.NewReader(
					`<html><head><title>TikTok</title></head></html>`,
				)),
				Request: r,
			}, nil
		}),
	}

	_, err := enricher.Enrich(
		context.Background(),
		"https://www.tiktok.com/404?fromUrl=/ZSpbx7bDc/",
	)

	require.Error(t, err)
	require.Equal(t, FailureContent, Kind(err))
	require.ErrorIs(t, err, ErrNotFoundPage)
}

func TestEnrich_EmptyOEmbedFallsBackToHTML(t *testing.T) {
	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusOK,
		`{}`,
		http.StatusOK,
		testPage,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/oembed",
		},
	}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "Open Graph Title", *metadata.Title)
	require.Equal(t, "Example App", *metadata.Platform)
	require.Equal(t, 1, oembedHits)
	require.Equal(t, 1, pageHits)
}

func TestEnrich_OEmbedFailureFallsBackToHTML(t *testing.T) {
	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusNotFound,
		`not found`,
		http.StatusOK,
		testPage,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/oembed",
		},
	}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "Open Graph Title", *metadata.Title)
	require.Equal(t, 1, oembedHits)
	require.Equal(t, 1, pageHits)
}

func TestEnrich_OEmbedAndHTMLFailuresStayRetryable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oembed", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/oembed",
		},
	}

	_, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.Error(t, err)
	require.Equal(t, FailureFetch, Kind(err))
	require.Equal(t, http.StatusInternalServerError, Status(err))
}

func TestEnrich_DiscoveryFillsTheMissingPlatform(t *testing.T) {
	const page = `<!doctype html>
	<html><head>
		<title>Page Title</title>
		<link rel="alternate" type="application/json+oembed" href="/oembed">
	</head></html>`

	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusOK,
		`{"provider_name":"Example","title":"oEmbed Title"}`,
		http.StatusOK,
		page,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{ok: false}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "Page Title", *metadata.Title)
	require.Equal(t, "Example", *metadata.Platform)
	require.Equal(t, 1, oembedHits)
}

func TestEnrich_DiscoveryIsSkippedWhenHTMLIsComplete(t *testing.T) {
	const page = `<!doctype html>
	<html><head>
		<title>Page Title</title>
		<meta name="application-name" content="Example App">
		<link rel="alternate" type="application/json+oembed" href="/oembed">
	</head></html>`

	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusOK,
		`{"provider_name":"Example"}`,
		http.StatusOK,
		page,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{ok: false}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "Page Title", *metadata.Title)
	require.Equal(t, "Example App", *metadata.Platform)
	require.Equal(t, 0, oembedHits)
}

func TestEnrich_DiscoveryFailureKeepsTheHTMLResult(t *testing.T) {
	const page = `<!doctype html>
	<html><head>
		<title>Page Title</title>
		<link rel="alternate" type="application/json+oembed" href="/oembed">
	</head></html>`

	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed",
		http.StatusNotFound,
		`not found`,
		http.StatusOK,
		page,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{ok: false}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "Page Title", *metadata.Title)
	require.Nil(t, metadata.Platform)
	require.Equal(t, 1, oembedHits)
}

func TestEnrich_OEmbedUsesTheFormatPlaceholder(t *testing.T) {
	var pageHits, oembedHits int

	server := serveOEmbedPage(
		t,
		"/oembed.json",
		http.StatusOK,
		`{"title":"Title","provider_name":"Example"}`,
		http.StatusOK,
		testPage,
		&pageHits,
		&oembedHits,
	)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/oembed.{format}",
		},
	}

	metadata, err := newResolverEnricher(resolver).Enrich(
		context.Background(),
		server.URL+"/page",
	)

	require.NoError(t, err)
	require.Equal(t, "Title", *metadata.Title)
	require.Equal(t, 1, oembedHits)
}

// YouTube is a built-in provider because its URL normalization is a property of
// the host. The registry must not be able to redirect it to a different endpoint.
func TestEnrich_YouTubeKeepsTheBuiltInProvider(t *testing.T) {
	server := serveOEmbed(t, oembedResponse, http.StatusOK, nil)

	resolver := &fakeProviderResolver{
		ok: true,
		endpoint: OEmbedEndpoint{
			EndpointURL: server.URL + "/wrong",
		},
	}

	enricher := newResolverEnricher(resolver)
	enricher.youtubeOEmbedURL = server.URL

	metadata, err := enricher.Enrich(
		context.Background(),
		"https://youtu.be/rFZHOHl-L8A",
	)

	require.NoError(t, err)
	require.Equal(t, 0, resolver.calls)
	require.Equal(t, "YouTube", *metadata.Platform)
}

func TestBuildOEmbedURL(t *testing.T) {
	t.Run("adds the target and format", func(t *testing.T) {
		got, err := buildOEmbedURL(
			"https://example.com/oembed",
			"https://example.com/page",
			true,
		)

		require.NoError(t, err)

		parsed, err := url.Parse(got)
		require.NoError(t, err)
		require.Equal(
			t,
			"https://example.com/page",
			parsed.Query().Get("url"),
		)
		require.Equal(t, "json", parsed.Query().Get("format"))
	})

	t.Run("overrides a target when asked", func(t *testing.T) {
		got, err := buildOEmbedURL(
			"https://example.com/oembed?url=https://example.com/original",
			"https://example.com/page",
			true,
		)

		require.NoError(t, err)

		parsed, err := url.Parse(got)
		require.NoError(t, err)
		require.Equal(
			t,
			"https://example.com/page",
			parsed.Query().Get("url"),
		)
	})

	t.Run("keeps a discovery link target", func(t *testing.T) {
		got, err := buildOEmbedURL(
			"https://example.com/oembed?url=https://example.com/original",
			"https://example.com/page",
			false,
		)

		require.NoError(t, err)

		parsed, err := url.Parse(got)
		require.NoError(t, err)
		require.Equal(
			t,
			"https://example.com/original",
			parsed.Query().Get("url"),
		)
	})

	t.Run("adds a target when a discovery link has none", func(t *testing.T) {
		got, err := buildOEmbedURL(
			"https://example.com/oembed?format=json",
			"https://example.com/page",
			false,
		)

		require.NoError(t, err)

		parsed, err := url.Parse(got)
		require.NoError(t, err)
		require.Equal(
			t,
			"https://example.com/page",
			parsed.Query().Get("url"),
		)
	})

	t.Run("replaces the format placeholder", func(t *testing.T) {
		got, err := buildOEmbedURL(
			"https://example.com/oembed.{format}",
			"https://example.com/page",
			true,
		)

		require.NoError(t, err)
		require.Contains(t, got, "/oembed.json")
	})

	t.Run("rejects a non http endpoint", func(t *testing.T) {
		_, err := buildOEmbedURL(
			"ftp://example.com/oembed",
			"https://example.com/page",
			true,
		)

		require.Error(t, err)
	})
}

func TestExtract_OEmbedDiscovery(t *testing.T) {
	const baseURL = "https://example.com/articles/1"

	discoveryFrom := func(document string) string {
		t.Helper()

		parsed, err := url.Parse(baseURL)
		require.NoError(t, err)

		doc, err := extract(strings.NewReader(document), parsed)
		require.NoError(t, err)

		return resolveOEmbedDiscovery(doc, parsed)
	}

	t.Run("absolute href", func(t *testing.T) {
		require.Equal(
			t,
			"https://example.com/oembed?url=https://example.com/articles/1",
			discoveryFrom(`<html><head>
				<link rel="alternate" type="application/json+oembed"
					href="https://example.com/oembed?url=https://example.com/articles/1">
			</head></html>`),
		)
	})

	t.Run("relative href", func(t *testing.T) {
		require.Equal(
			t,
			"https://example.com/oembed",
			discoveryFrom(`<html><head>
				<link rel="alternate" type="application/json+oembed" href="/oembed">
			</head></html>`),
		)
	})

	t.Run("type parameter is ignored", func(t *testing.T) {
		require.Equal(
			t,
			"https://example.com/oembed",
			discoveryFrom(`<html><head>
				<link rel="alternate" type="application/json+oembed; charset=utf-8" href="/oembed">
			</head></html>`),
		)
	})

	t.Run("xml discovery is ignored", func(t *testing.T) {
		require.Empty(t, discoveryFrom(`<html><head>
			<link rel="alternate" type="text/xml+oembed" href="/oembed">
		</head></html>`))
	})

	t.Run("canonical only has no discovery", func(t *testing.T) {
		require.Empty(t, discoveryFrom(`<html><head>
			<link rel="canonical" href="/canonical">
		</head></html>`))
	})
}
