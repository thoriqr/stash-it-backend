package enrichment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Facebook localizes <meta name="application-name"> per section and locale. The
// same kind of page answered "Grup Facebook", "Facebook Groups" and
// "Facebook 小组" from different addresses, so the platform must not be stored
// verbatim.
func TestCanonicalPlatform(t *testing.T) {
	cases := map[string]string{
		"Grup Facebook":          "Facebook",
		"Facebook Groups":        "Facebook",
		"Facebook 小组":            "Facebook",
		"Facebook":               "Facebook",
		"facebook":               "Facebook",
		"  Grup   Facebook  ":    "Facebook",
		"Instagram":              "Instagram",
		"TikTok":                 "TikTok",
		"TikTok - Make Your Day": "TikTok",

		// A name that mentions a brand without being that platform's own name is
		// not reduced: the reduction is for the platform's localized product
		// name, not for arbitrary text.
		"Tokopedia": "Tokopedia",
		"X":         "X",
		"":          "",
	}

	for raw, expected := range cases {
		require.Equal(t, expected, canonicalPlatform(raw), raw)
	}
}

// A name that only contains a brand inside a longer word is left alone, so an
// ordinary site is never renamed.
func TestCanonicalPlatform_DoesNotMatchInsideWords(t *testing.T) {
	for _, value := range []string{
		"Bookface",
		"Instantgram",
		"NotTikToker",
	} {
		require.Equal(t, value, canonicalPlatform(value), value)
	}
}

func TestOptionalPlatformName(t *testing.T) {
	require.Nil(t, optionalPlatformName(""))

	name := optionalPlatformName("Grup Facebook")
	require.NotNil(t, name)
	require.Equal(t, "Facebook", *name)
}

// The HTML path records the canonical platform, not the localized name.
func TestExtract_PlatformIsCanonicalized(t *testing.T) {
	metadata := extractFrom(
		t,
		`<html><head>
			<meta name="application-name" content="Grup Facebook">
		</head></html>`,
		"https://www.facebook.com/groups/1/posts/2/",
	)

	requireMetadata(t, metadata, "platform", "Facebook")
}

// A platform with no recognized brand is stored exactly as the page published it.
func TestExtract_UnknownPlatformIsUnchanged(t *testing.T) {
	metadata := extractFrom(
		t,
		`<html><head>
			<meta name="application-name" content="Example App">
		</head></html>`,
		"https://example.com/",
	)

	requireMetadata(t, metadata, "platform", "Example App")
}
