package enrichment

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// extractFrom parses HTML and returns the selected metadata.
//
// The base URL is explicit in every test because relative metadata resolution
// depends on it, and a test that inherited one by accident would not be asserting
// what it looks like it asserts.
func extractFrom(
	t *testing.T,
	document string,
	baseURL string,
) Metadata {
	t.Helper()

	parsed, err := url.Parse(baseURL)
	require.NoError(t, err)

	doc, err := extract(strings.NewReader(document), parsed)
	require.NoError(t, err)

	return selectMetadata(doc, parsed)
}

func requireMetadata(
	t *testing.T,
	metadata Metadata,
	field string,
	expected string,
) {
	t.Helper()

	var actual *string

	switch field {
	case "title":
		actual = metadata.Title
	case "platform":
		actual = metadata.Platform
	case "description":
		actual = metadata.Description
	case "image":
		actual = metadata.ImageURL
	case "canonical":
		actual = metadata.CanonicalURL
	case "siteName":
		actual = metadata.SiteName
	case "author":
		actual = metadata.Author
	default:
		t.Fatalf("unknown metadata field %q", field)
	}

	require.NotNil(t, actual, "%s should have been extracted", field)
	require.Equal(t, expected, *actual, "%s", field)
}

func requireNoMetadata(
	t *testing.T,
	metadata Metadata,
	field string,
) {
	t.Helper()

	switch field {
	case "title":
		require.Nil(t, metadata.Title, "title should be nil")
	case "platform":
		require.Nil(t, metadata.Platform, "platform should be nil")
	case "description":
		require.Nil(t, metadata.Description, "description should be nil")
	case "image":
		require.Nil(t, metadata.ImageURL, "image should be nil")
	case "canonical":
		require.Nil(t, metadata.CanonicalURL, "canonical should be nil")
	case "siteName":
		require.Nil(t, metadata.SiteName, "site name should be nil")
	case "author":
		require.Nil(t, metadata.Author, "author should be nil")
	default:
		t.Fatalf("unknown metadata field %q", field)
	}
}

func TestExtract_Title(t *testing.T) {
	t.Run("from the title element", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><title>An interesting article</title></head></html>`,
			"https://example.com/articles/1",
		)

		requireMetadata(t, metadata, "title", "An interesting article")
	})

	t.Run("surrounding whitespace and newlines are collapsed", func(t *testing.T) {
		metadata := extractFrom(
			t,
			"<html><head><title>\n\t  Spaced    out\n\ttitle \n</title></head></html>",
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Spaced out title")
	})

	t.Run("non breaking spaces are folded", func(t *testing.T) {
		metadata := extractFrom(
			t,
			"<html><head><title>Wide\u00a0space</title></head></html>",
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Wide space")
	})

	t.Run("punctuation and case are left alone", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><title>Why Go's "Fuzzing" Works — A Study</title></head></html>`,
			"https://example.com/",
		)

		// Over-normalizing a user visible title is a bug, not tidiness.
		requireMetadata(
			t,
			metadata,
			"title",
			"Why Go's \"Fuzzing\" Works — A Study",
		)
	})

	t.Run("an empty title element is ignored", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><title>   </title>
			<meta property="og:title" content="From Open Graph"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "From Open Graph")
	})

	t.Run("a meta name title is not the document title", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta name="title" content="Never rendered"></head></html>`,
			"https://example.com/",
		)

		// The document title is the <title> element. A meta name="title" is a
		// different and much rarer thing, and treating it as the title would let
		// a page claim a title it never displayed.
		requireNoMetadata(t, metadata, "title")
	})
}

func TestExtract_TitlePrecedence(t *testing.T) {
	// One document declaring every title source, so precedence is asserted as an
	// order rather than inferred from separate examples.
	const full = `<html><head>
		<title>Document Title</title>
		<meta property="og:title" content="Open Graph Title">
		<meta name="twitter:title" content="Twitter Title">
		<script type="application/ld+json">
		{"@type":"Article","headline":"JSON-LD Headline"}
		</script>
	</head></html>`

	t.Run("og:title beats everything", func(t *testing.T) {
		requireMetadata(
			t,
			extractFrom(t, full, "https://example.com/"),
			"title",
			"Open Graph Title",
		)
	})

	t.Run("twitter:title beats the document title", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<title>Document Title</title>
				<meta name="twitter:title" content="Twitter Title">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Twitter Title")
	})

	t.Run("the document title beats JSON-LD", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<title>Document Title</title>
				<script type="application/ld+json">
				{"@type":"Article","headline":"JSON-LD Headline"}
				</script>
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Document Title")
	})

	t.Run("JSON-LD headline is used when nothing else exists", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","headline":"JSON-LD Headline"}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "JSON-LD Headline")
	})

	t.Run("JSON-LD name is the last resort", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","name":"JSON-LD Name"}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "JSON-LD Name")
	})

	t.Run("a meta name title does not participate", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<title>Document Title</title>
				<meta name="title" content="Meta Title">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Document Title")
	})
}

func TestExtract_DescriptionPrecedence(t *testing.T) {
	t.Run("og:description beats the meta description", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta name="description" content="Standard description">
				<meta property="og:description" content="Open Graph description">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(
			t,
			metadata,
			"description",
			"Open Graph description",
		)
	})

	t.Run("the meta description is used on its own", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta name="description" content="Standard description">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "description", "Standard description")
	})

	t.Run("twitter:description beats the meta description", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta name="description" content="Standard description">
				<meta name="twitter:description" content="Twitter description">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "description", "Twitter description")
	})

	t.Run("an empty description falls through", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta property="og:description" content="   ">
				<meta name="description" content="Standard description">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "description", "Standard description")
	})

	t.Run("whitespace is collapsed", func(t *testing.T) {
		metadata := extractFrom(
			t,
			"<html><head><meta name=\"description\" content=\"  A   long\n  description.  \"></head></html>",
			"https://example.com/",
		)

		requireMetadata(t, metadata, "description", "A long description.")
	})
}

func TestExtract_Image(t *testing.T) {
	t.Run("og:image", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:image" content="https://example.com/og.png"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "image", "https://example.com/og.png")
	})

	t.Run("og:image beats twitter:image", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta property="og:image" content="https://example.com/og.png">
				<meta name="twitter:image" content="https://example.com/twitter.png">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "image", "https://example.com/og.png")
	})

	t.Run("twitter:image is the fallback", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta name="twitter:image" content="https://example.com/twitter.png"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "image", "https://example.com/twitter.png")
	})

	t.Run("twitter:image:src is the last image fallback", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta name="twitter:image:src" content="https://example.com/twitter-src.png"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(
			t,
			metadata,
			"image",
			"https://example.com/twitter-src.png",
		)
	})

	t.Run("a relative image is resolved against the page", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:image" content="/static/og.png"></head></html>`,
			"https://example.com/articles/1",
		)

		requireMetadata(t, metadata, "image", "https://example.com/static/og.png")
	})

	t.Run("a document relative image is resolved against the page", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:image" content="images/og.png"></head></html>`,
			"https://example.com/articles/1",
		)

		requireMetadata(
			t,
			metadata,
			"image",
			"https://example.com/articles/images/og.png",
		)
	})

	t.Run("a relative image resolves against the redirected page", func(t *testing.T) {
		// The base is the URL the document was served from, not the one requested.
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:image" content="og.png"></head></html>`,
			"https://example.com/final/page",
		)

		requireMetadata(t, metadata, "image", "https://example.com/final/og.png")
	})

	t.Run("a non http scheme is dropped", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:image" content="javascript:alert(1)"></head></html>`,
			"https://example.com/",
		)

		requireNoMetadata(t, metadata, "image")
	})
}

func TestExtract_CanonicalURL(t *testing.T) {
	t.Run("from a canonical link", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><link rel="canonical" href="https://example.com/canonical"></head></html>`,
			"https://example.com/original",
		)

		requireMetadata(
			t,
			metadata,
			"canonical",
			"https://example.com/canonical",
		)
	})

	t.Run("rel with several tokens still matches", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><link rel="alternate canonical" href="https://example.com/canonical"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(
			t,
			metadata,
			"canonical",
			"https://example.com/canonical",
		)
	})

	t.Run("a canonical link beats og:url", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta property="og:url" content="https://example.com/og">
				<link rel="canonical" href="https://example.com/canonical">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(
			t,
			metadata,
			"canonical",
			"https://example.com/canonical",
		)
	})

	t.Run("og:url is used when there is no canonical link", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:url" content="https://example.com/og"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "canonical", "https://example.com/og")
	})

	t.Run("a relative canonical is resolved", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><link rel="canonical" href="/canonical"></head></html>`,
			"https://example.com/articles/1",
		)

		requireMetadata(
			t,
			metadata,
			"canonical",
			"https://example.com/canonical",
		)
	})

	t.Run("a canonical does not replace the requested URL", func(t *testing.T) {
		// The extractor reports what the page declared. Deciding whether to
		// replace a saved item's url belongs to the caller, so nothing here may
		// imply it happened.
		const requested = "https://example.com/tracking?utm_source=newsletter"

		metadata := extractFrom(
			t,
			`<html><head><link rel="canonical" href="https://example.com/clean"></head></html>`,
			requested,
		)

		requireMetadata(t, metadata, "canonical", "https://example.com/clean")
	})
}

func TestExtract_SiteNameAndPlatform(t *testing.T) {
	t.Run("og:site_name becomes the site name", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:site_name" content="Example Journal"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "siteName", "Example Journal")
	})

	t.Run("application-name becomes the platform", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta name="application-name" content="Example App"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "platform", "Example App")
	})

	t.Run("application-name beats og:site_name for the platform", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta property="og:site_name" content="Example Journal">
				<meta name="application-name" content="Example App">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "platform", "Example App")
		requireMetadata(t, metadata, "siteName", "Example Journal")
	})

	t.Run("og:site_name is the platform fallback", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:site_name" content="Example Journal"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "platform", "Example Journal")
	})

	t.Run("platform is not derived from the hostname", func(t *testing.T) {
		// Phase A deliberately does not infer platform from a URL, and decision 1
		// and 3 in PROGRESS.md fix the meaning of the column. A recognised
		// hostname with no metadata must yield nothing.
		metadata := extractFrom(
			t,
			`<html><body><p>Just some content.</p></body></html>`,
			"https://www.youtube.com/watch?v=abc123",
		)

		requireNoMetadata(t, metadata, "platform")
	})

	t.Run("platform is not derived from a recognisable domain", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><title>A video</title></head></html>`,
			"https://www.tiktok.com/@creator/video/1",
		)

		requireNoMetadata(t, metadata, "platform")
		requireMetadata(t, metadata, "title", "A video")
	})

	t.Run("a page with no signals has no platform", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><title>Something</title></head></html>`,
			"https://unknown.example/",
		)

		requireNoMetadata(t, metadata, "platform")
	})
}

func TestExtract_Author(t *testing.T) {
	t.Run("from a meta author", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta name="author" content="Jane Doe"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "author", "Jane Doe")
	})

	t.Run("from og:author", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:author" content="Jane Doe"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "author", "Jane Doe")
	})

	t.Run("from a JSON-LD author object", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","author":{"@type":"Person","name":"Jane Doe"}}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "author", "Jane Doe")
	})
}

func TestExtract_JSONLD(t *testing.T) {
	t.Run("a single object", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{
				"@context": "https://schema.org",
				"@type": "Article",
				"headline": "The Headline",
				"description": "The Description",
				"image": "https://example.com/ld.png",
				"url": "https://example.com/ld",
				"publisher": {"@type": "Organization", "name": "The Publisher"}
			}
			</script></head></html>`,
			"https://example.com/original",
		)

		requireMetadata(t, metadata, "title", "The Headline")
		requireMetadata(t, metadata, "description", "The Description")
		requireMetadata(t, metadata, "image", "https://example.com/ld.png")
		requireMetadata(t, metadata, "canonical", "https://example.com/ld")
		requireMetadata(t, metadata, "platform", "The Publisher")
	})

	t.Run("a publisher name never becomes the title", func(t *testing.T) {
		// publisher.name is on a large share of pages. Collected as a generic
		// name it would outrank the article headline.
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","publisher":{"@type":"Organization","name":"The Publisher"}}
			</script></head></html>`,
			"https://example.com/",
		)

		requireNoMetadata(t, metadata, "title")
		requireMetadata(t, metadata, "platform", "The Publisher")
	})

	t.Run("an array of objects, first value wins", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			[
				{"@type":"Article","headline":"First Headline"},
				{"@type":"Article","headline":"Second Headline"}
			]
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "First Headline")
	})

	t.Run("an @graph container", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{
				"@context": "https://schema.org",
				"@graph": [
					{"@type": "WebSite", "name": "The Site"},
					{
						"@type": "Article",
						"headline": "The Headline",
						"publisher": {"@type": "Organization", "name": "The Publisher"}
					}
				]
			}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "The Headline")
		requireMetadata(t, metadata, "platform", "The Publisher")
	})

	t.Run("an Organization node supplies the platform", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@context":"https://schema.org","@graph":[
				{"@type":"Organization","name":"The Organisation"},
				{"@type":"Article","headline":"The Headline"}
			]}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "platform", "The Organisation")
		requireMetadata(t, metadata, "title", "The Headline")
	})

	t.Run("a WebSite node supplies the site name", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"WebSite","name":"The Web Site"}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "siteName", "The Web Site")
	})

	t.Run("a fully qualified @type is recognised", func(t *testing.T) {
		// Pages write schema.org types in at least three ways.
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"https://schema.org/Organization","name":"The Organisation"}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "platform", "The Organisation")
	})

	t.Run("an ImageObject url is used", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","image":{"@type":"ImageObject","url":"https://example.com/obj.png"}}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "image", "https://example.com/obj.png")
	})

	t.Run("an ImageObject contentUrl is the fallback", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","image":{"@type":"ImageObject","contentUrl":"https://example.com/content.png"}}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "image", "https://example.com/content.png")
	})

	t.Run("an image list takes the first usable entry", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","image":[
				"https://example.com/first.png",
				"https://example.com/second.png"
			]}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "image", "https://example.com/first.png")
	})

	t.Run("a publisher given as a bare string", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","publisher":"The Publisher"}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "platform", "The Publisher")
	})

	t.Run("a JSON-LD value is read as a string", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","headline":{"@value":"The Headline"}}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "The Headline")
	})

	t.Run("a list of types is matched on any of them", func(t *testing.T) {
		// @type is routinely a list, for instance ["Article","NewsArticle"].
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":["Article","NewsArticle"],"headline":"The Headline"}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "The Headline")
	})

	t.Run("a list of types can name an organisation", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@context":"https://schema.org","@graph":[
				{"@type":["Organization","NGO"],"name":"The Organisation"},
				{"@type":"Article","headline":"The Headline"}
			]}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "platform", "The Organisation")
		requireMetadata(t, metadata, "title", "The Headline")
	})

	t.Run("a list of authors takes the first with a name", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","author":[
				{"@type":"Person"},
				{"@type":"Person","name":"Jane Doe"},
				{"@type":"Person","name":"John Roe"}
			]}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "author", "Jane Doe")
	})

	t.Run("an author given as a bare string", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"Article","author":"Jane Doe"}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "author", "Jane Doe")
	})

	t.Run("a nested related entity does not become the title", func(t *testing.T) {
		// Descending into a nested entity with reference set is what stops a
		// related article or an author from being mistaken for this page.
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{
				"@type":"Article",
				"headline":"The Headline",
				"mainEntity": {"@type":"Person","name":"Not The Title"}
			}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "The Headline")
	})

	t.Run("a nested entity is still read for its own platform signal", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json">
			{"@type":"WebPage","mainEntity":{"@type":"Organization","name":"The Organisation"}}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "platform", "The Organisation")
	})

	t.Run("a script with no type attribute is ignored", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<title>Document Title</title>
				<script>{"@type":"Article","headline":"Not JSON-LD"}</script>
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Document Title")
	})

	t.Run("a meta tag with no content attribute is ignored", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta property="og:title">
				<title>Document Title</title>
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Document Title")
	})

	t.Run("a meta tag with no name or property is ignored", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta charset="utf-8">
				<title>Document Title</title>
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Document Title")
	})

	t.Run("a canonical link with no href is ignored", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<link rel="canonical">
				<meta property="og:url" content="https://example.com/og">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "canonical", "https://example.com/og")
	})

	t.Run("a type attribute with a charset parameter is recognised", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><script type="application/ld+json; charset=utf-8">
			{"@type":"Article","headline":"The Headline"}
			</script></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "The Headline")
	})

	t.Run("a script with another type is ignored", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<script type="application/json">
				{"@type":"Article","headline":"Not JSON-LD"}
				</script>
			</head></html>`,
			"https://example.com/",
		)

		requireNoMetadata(t, metadata, "title")
	})

	t.Run("malformed JSON-LD does not break normal extraction", func(t *testing.T) {
		// JSON-LD is hand written and templated, so it is the most common source
		// of a broken block on an otherwise fine page.
		metadata := extractFrom(
			t,
			`<html><head>
				<meta property="og:title" content="Open Graph Title">
				<meta property="og:description" content="Open Graph Description">
				<meta property="og:image" content="https://example.com/og.png">
				<script type="application/ld+json">
				{ this is not json at all }
				</script>
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Open Graph Title")
		requireMetadata(
			t,
			metadata,
			"description",
			"Open Graph Description",
		)
		requireMetadata(t, metadata, "image", "https://example.com/og.png")
	})

	t.Run("malformed JSON-LD falls back to the document title", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<title>Document Title</title>
				<script type="application/ld+json">
				{"@type":"Article","headline":
				</script>
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Document Title")
	})

	t.Run("an empty JSON-LD block is ignored", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<title>Document Title</title>
				<script type="application/ld+json"></script>
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Document Title")
	})

	t.Run("JSON-LD does not override richer HTML metadata", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta property="og:title" content="Open Graph Title">
				<script type="application/ld+json">
				{"@type":"Article","headline":"JSON-LD Headline"}
				</script>
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Open Graph Title")
	})
}

func TestExtract_MissingMetadata(t *testing.T) {
	t.Run("a page with no metadata yields nothing and is not a failure", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head></head><body><p>Just text.</p></body></html>`,
			"https://example.com/",
		)

		require.True(t, metadata.IsEmpty())
	})

	t.Run("a page with only a body yields nothing", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><body><h1>Heading</h1></body></html>`,
			"https://example.com/",
		)

		require.True(t, metadata.IsEmpty())
	})
}

func TestExtract_MalformedHTML(t *testing.T) {
	t.Run("unclosed tags still yield what came before", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="og:title" content="Found Early">
			<body><div><p>never closed`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Found Early")
	})

	t.Run("unquoted attributes are handled", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property=og:title content=Unquoted></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Unquoted")
	})

	t.Run("an unclosed title element yields no title", func(t *testing.T) {
		// <title> is an RCDATA element, so an unclosed one makes the tokenizer
		// read to end of file and emit the rest of the document, markup included,
		// as title text. Storing that as a title would be worse than storing
		// nothing, so the accumulated text is discarded instead.
		metadata := extractFrom(
			t,
			`<html><head><title>Never closed</head><body><p>Body text</p></body></html>`,
			"https://example.com/",
		)

		requireNoMetadata(t, metadata, "title")
	})

	t.Run("a properly closed title at end of input is kept", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><title>Closed properly</title>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Closed properly")
	})

	t.Run("empty input is handled", func(t *testing.T) {
		metadata := extractFrom(t, ``, "https://example.com/")

		require.True(t, metadata.IsEmpty())
	})
}

func TestExtract_MetaKeyNormalization(t *testing.T) {
	t.Run("an upper case Open Graph key is found", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta property="OG:Title" content="Found"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "Found")
	})

	t.Run("an upper case attribute name is found", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head><meta NAME="description" CONTENT="Found"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "description", "Found")
	})

	t.Run("the property attribute works for standard metadata", func(t *testing.T) {
		// Some pages use property where name is expected.
		metadata := extractFrom(
			t,
			`<html><head><meta property="description" content="Found"></head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "description", "Found")
	})

	t.Run("the first declaration of a duplicated tag wins", func(t *testing.T) {
		metadata := extractFrom(
			t,
			`<html><head>
				<meta property="og:title" content="First">
				<meta property="og:title" content="Second">
			</head></html>`,
			"https://example.com/",
		)

		requireMetadata(t, metadata, "title", "First")
	})
}

func TestExtract_EverythingAtOnce(t *testing.T) {
	metadata := extractFrom(
		t,
		`<!doctype html>
		<html lang="en">
		<head>
			<title>Document Title | Example Journal</title>
			<link rel="canonical" href="/canonical">
			<meta name="description" content="Standard description.">
			<meta name="author" content="Jane Doe">
			<meta name="application-name" content="Example App">
			<meta property="og:title" content="Open Graph Title">
			<meta property="og:description" content="Open Graph description.">
			<meta property="og:image" content="/static/og.png">
			<meta property="og:site_name" content="Example Journal">
			<script type="application/ld+json">
			{"@context":"https://schema.org","@type":"Article",
			 "publisher":{"@type":"Organization","name":"The Publisher"}}
			</script>
		</head>
		<body><p>Body</p></body>
		</html>`,
		"https://example.com/articles/1",
	)

	requireMetadata(t, metadata, "title", "Open Graph Title")
	requireMetadata(t, metadata, "description", "Open Graph description.")
	requireMetadata(t, metadata, "image", "https://example.com/static/og.png")
	requireMetadata(t, metadata, "canonical", "https://example.com/canonical")
	requireMetadata(t, metadata, "platform", "Example App")
	requireMetadata(t, metadata, "siteName", "Example Journal")
	requireMetadata(t, metadata, "author", "Jane Doe")
	require.False(t, metadata.IsEmpty())
}

func TestPrecedenceOrdersAreComplete(t *testing.T) {
	// Every field must have a precedence order, and the orders must not be empty.
	// A slice rather than a map keyed by field name, because one field name
	// ("description") is also a candidate key and a map would confuse the two.
	orders := []struct {
		field string
		order []string
	}{
		{"title", titlePrecedence},
		{"description", descriptionPrecedence},
		{"image", imagePrecedence},
		{"canonical", canonicalPrecedence},
		{"platform", platformPrecedence},
		{"siteName", siteNamePrecedence},
		{"author", authorPrecedence},
	}

	for _, entry := range orders {
		order := entry.order

		require.NotEmpty(t, order, "%s precedence", entry.field)

		seen := make(map[string]struct{}, len(order))
		for _, key := range order {
			require.NotEmpty(
				t,
				key,
				"%s precedence has an empty key",
				entry.field,
			)
			require.NotContains(
				t,
				seen,
				key,
				"%s precedence repeats %q",
				entry.field,
				key,
			)

			seen[key] = struct{}{}
		}
	}
}

func TestPrecedenceOrdersDoNotUseThePublisherForTitle(t *testing.T) {
	// The separation that keeps publisher.name out of the title. Asserted
	// directly so that merging the two key sets later fails loudly.
	for _, key := range titlePrecedence {
		require.NotEqual(t, jsonLDPublisherName, key)
		require.NotEqual(t, jsonLDOrganizationName, key)
		require.NotEqual(t, jsonLDWebSiteName, key)
	}

	for _, key := range descriptionPrecedence {
		require.NotEqual(t, jsonLDPublisherName, key)
	}
}
