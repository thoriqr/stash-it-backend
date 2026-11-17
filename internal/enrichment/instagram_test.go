package enrichment

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const instagramCaption = "Tiap hari kita disibukin sama hustle culture sampai lupa nanya dari mana asalnya kekayaan ekstrem orang orang."

func TestApplyInstagramTitle(t *testing.T) {
	pageURL := mustParseURL(t, "https://www.instagram.com/reel/DfRnTWVC43s/")

	full := "Cavin Dennis Tito Siregar on Instagram: \"" + instagramCaption + "\""

	metadata := applyInstagramTitle(
		pageURL,
		Metadata{
			Title:       optionalText(full),
			Description: optionalText("34K likes, 733 comments - " + full),
		},
	)

	require.NotNil(t, metadata.Title)
	require.Equal(
		t,
		"Cavin Dennis Tito Siregar on Instagram",
		*metadata.Title,
	)

	// The caption stays in the description, which is where it belongs.
	require.NotNil(t, metadata.Description)
	require.Contains(t, *metadata.Description, instagramCaption)
}

func TestApplyInstagramTitle_OtherHostsAreUntouched(t *testing.T) {
	otherURL := mustParseURL(t, "https://example.com/reel/1")

	title := "Someone on Instagram: \"a caption\""
	metadata := applyInstagramTitle(
		otherURL,
		Metadata{Title: &title},
	)

	require.Equal(t, title, *metadata.Title)
}

func TestApplyInstagramTitle_OtherFormatsAreUntouched(t *testing.T) {
	pageURL := mustParseURL(t, "https://www.instagram.com/cavindenistito/")

	title := "Cavin Dennis Tito Siregar (@cavindenistito) • Instagram photos and videos"
	metadata := applyInstagramTitle(
		pageURL,
		Metadata{Title: &title},
	)

	require.Equal(t, title, *metadata.Title)
}

func TestApplyInstagramTitle_NoTitleIsANoOp(t *testing.T) {
	pageURL := mustParseURL(t, "https://www.instagram.com/reel/1/")

	require.Nil(t, applyInstagramTitle(pageURL, Metadata{}).Title)
}

// The exported helper is what the client-assisted path uses, so it is tested
// directly rather than only through the extractor.
func TestShortenInstagramTitle(t *testing.T) {
	pageURL := mustParseURL(t, "https://www.instagram.com/reel/1/")
	full := "Shark Detail on Instagram: \"a long caption\""

	shortened := ShortenInstagramTitle(pageURL, &full)

	require.Equal(t, "Shark Detail on Instagram", *shortened)

	already := "Shark Detail on Instagram"
	require.Equal(t, already, *ShortenInstagramTitle(pageURL, &already))

	otherHost := mustParseURL(t, "https://example.com/reel/1/")
	require.Equal(t, full, *ShortenInstagramTitle(otherHost, &full))

	require.Nil(t, ShortenInstagramTitle(pageURL, nil))
}

// The Instagram path end to end: a caption-bearing Open Graph title is shortened,
// while the description keeps the caption.
func TestEnrich_InstagramTitleIsShortened(t *testing.T) {
	page := `<!doctype html>
	<html><head>
		<meta property="og:site_name" content="Instagram">
		<meta property="og:title" content="Cavin Dennis Tito Siregar on Instagram: &quot;` +
		instagramCaption + `&quot;">
		<meta property="og:description" content="34K likes, 733 comments - cavindenistito on November 3, 2026: &quot;` +
		instagramCaption + `&quot;.">
		<meta property="og:image" content="https://example.com/photo.jpg">
	</head></html>`

	enricher := newResolverEnricher(&fakeProviderResolver{ok: false})

	enricher.genericClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body:    io.NopCloser(strings.NewReader(page)),
				Request: r,
			}, nil
		}),
	}

	metadata, err := enricher.Enrich(
		context.Background(),
		"https://www.instagram.com/reel/DfRnTWVC43s/",
	)

	require.NoError(t, err)
	require.Equal(
		t,
		"Cavin Dennis Tito Siregar on Instagram",
		*metadata.Title,
	)
	require.NotNil(t, metadata.Description)
	require.Contains(t, *metadata.Description, instagramCaption)
	require.Equal(t, "Instagram", *metadata.Platform)
}
