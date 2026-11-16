package enrichment

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	require.NoError(t, err)

	return parsed
}

const amazonProductPage = `<!doctype html>
<html><head>
	<title>Amazon.com: A Thing</title>
	<meta name="description" content="Amazon.com: A Thing">
	<link rel="canonical" href="https://www.amazon.com/dp/B000000001">
</head><body>
	<img id="landingImage"
		src="https://m.media-amazon.com/images/I/small.jpg"
		data-old-hires="https://m.media-amazon.com/images/I/large.jpg"
		data-a-dynamic-image="{&quot;https://m.media-amazon.com/images/I/small.jpg&quot;:[300,300],&quot;https://m.media-amazon.com/images/I/large.jpg&quot;:[1500,1500]}">
</body></html>`

func TestFirstDynamicImageURL(t *testing.T) {
	t.Run("picks the largest image", func(t *testing.T) {
		require.Equal(
			t,
			"https://m.media-amazon.com/images/I/large.jpg",
			firstDynamicImageURL(
				`{"https://m.media-amazon.com/images/I/small.jpg":[300,300],`+
					`"https://m.media-amazon.com/images/I/large.jpg":[1500,1500]}`,
			),
		)
	})

	t.Run("an empty attribute has no image", func(t *testing.T) {
		require.Empty(t, firstDynamicImageURL(""))
	})

	t.Run("malformed JSON has no image", func(t *testing.T) {
		require.Empty(t, firstDynamicImageURL(`{"broken":`))
	})
}

func TestAmazonProductImage(t *testing.T) {
	pageURL := mustParseURL(t, "https://www.amazon.com/dp/B000000001")

	doc, err := extract(strings.NewReader(amazonProductPage), pageURL)
	require.NoError(t, err)

	require.Equal(
		t,
		"https://m.media-amazon.com/images/I/large.jpg",
		amazonProductImage(doc),
	)

	metadata := applyAmazonProductImage(doc, pageURL, Metadata{})

	require.NotNil(t, metadata.ImageURL)
	require.Equal(
		t,
		"https://m.media-amazon.com/images/I/large.jpg",
		*metadata.ImageURL,
	)
}

func TestAmazonProductImage_FallsBackToTheDynamicAttribute(t *testing.T) {
	pageURL := mustParseURL(t, "https://www.amazon.com/dp/B000000001")

	const page = `<html><body>
		<img id="landingImage"
			data-a-dynamic-image="{&quot;https://example.com/images/I/small.jpg&quot;:[300,300],&quot;https://example.com/images/I/large.jpg&quot;:[1500,1500]}">
	</body></html>`

	doc, err := extract(strings.NewReader(page), pageURL)
	require.NoError(t, err)

	metadata := applyAmazonProductImage(doc, pageURL, Metadata{})

	require.NotNil(t, metadata.ImageURL)
	require.Equal(t, "https://example.com/images/I/large.jpg", *metadata.ImageURL)
}

func TestAmazonProductImage_OtherHostsAreUntouched(t *testing.T) {
	otherURL := mustParseURL(t, "https://example.com/dp/B000000001")

	doc, err := extract(strings.NewReader(amazonProductPage), otherURL)
	require.NoError(t, err)

	metadata := applyAmazonProductImage(doc, otherURL, Metadata{})

	require.Nil(t, metadata.ImageURL)
}

func TestAmazonProductImage_DoesNotOverrideAPublishedImage(t *testing.T) {
	pageURL := mustParseURL(t, "https://www.amazon.com/dp/B000000001")

	doc, err := extract(strings.NewReader(amazonProductPage), pageURL)
	require.NoError(t, err)

	published := "https://example.com/og.jpg"
	metadata := applyAmazonProductImage(
		doc,
		pageURL,
		Metadata{ImageURL: &published},
	)

	require.Equal(t, published, *metadata.ImageURL)
}

func TestAmazonProductImage_IgnoresShareIcons(t *testing.T) {
	pageURL := mustParseURL(t, "https://www.amazon.com/dp/B000000001")

	const page = `<html><body>
		<img id="landingImage"
			data-old-hires="https://m.media-amazon.com/images/G/01/share-icons/amazon.png">
	</body></html>`

	doc, err := extract(strings.NewReader(page), pageURL)
	require.NoError(t, err)

	require.Empty(t, amazonProductImage(doc))
}

func TestFirstDynamicImageURL_IgnoresShareIcons(t *testing.T) {
	require.Equal(
		t,
		"https://m.media-amazon.com/images/I/product.jpg",
		firstDynamicImageURL(
			`{"https://m.media-amazon.com/images/G/01/share-icons/amazon.png":[1500,1500],`+
				`"https://m.media-amazon.com/images/I/product.jpg":[300,300]}`,
		),
	)
}

// A page that answered with a shell title should not store it.
func TestDropGenericTitle_Amazon(t *testing.T) {
	pageURL := mustParseURL(t, "https://www.amazon.com/dp/B000000001")

	metadata := dropGenericTitle(
		pageURL,
		Metadata{Title: optionalText("Amazon")},
	)

	require.Nil(t, metadata.Title)
}

// Amazon publishes no Open Graph and no JSON-LD, so the image and the deduplicated
// description are the only additions over the generic path.
func TestEnrich_AmazonProductPage(t *testing.T) {
	enricher := newResolverEnricher(&fakeProviderResolver{ok: false})

	enricher.genericClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/html; charset=utf-8"},
				},
				Body:    io.NopCloser(strings.NewReader(amazonProductPage)),
				Request: r,
			}, nil
		}),
	}

	metadata, err := enricher.Enrich(
		context.Background(),
		"https://www.amazon.com/dp/B000000001",
	)

	require.NoError(t, err)
	require.Equal(t, "Amazon.com: A Thing", *metadata.Title)
	require.Nil(t, metadata.Description, "the description repeats the title")
	require.Nil(t, metadata.Platform, "Amazon publishes no platform signal")

	require.NotNil(t, metadata.ImageURL)
	require.Equal(
		t,
		"https://m.media-amazon.com/images/I/large.jpg",
		*metadata.ImageURL,
	)
}
