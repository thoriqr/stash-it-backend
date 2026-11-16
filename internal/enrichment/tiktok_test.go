package enrichment

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const tiktokUniversalJSON = `{
	"__DEFAULT_SCOPE__": {
		"webapp.video-detail": {
			"itemInfo": {
				"itemStruct": {
					"desc": "S42 catch #spacex #starship #space",
					"video": {
						"cover": "https://cdn.example/cover.jpg"
					}
				}
			}
		}
	}
}`

func TestTikTokMetadataFromUniversalData(t *testing.T) {
	t.Run("reads the caption and cover", func(t *testing.T) {
		metadata := tiktokMetadataFromUniversalData(tiktokUniversalJSON)

		require.Equal(
			t,
			"S42 catch #spacex #starship #space",
			*metadata.Title,
		)
		require.Equal(
			t,
			"https://cdn.example/cover.jpg",
			*metadata.ImageURL,
		)
	})

	t.Run("an empty blob is empty", func(t *testing.T) {
		require.True(t, tiktokMetadataFromUniversalData("").IsEmpty())
	})

	t.Run("malformed JSON is ignored", func(t *testing.T) {
		require.True(
			t,
			tiktokMetadataFromUniversalData(`{"__DEFAULT_SCOPE__":`).IsEmpty(),
		)
	})

	t.Run("a missing entity is ignored", func(t *testing.T) {
		require.True(
			t,
			tiktokMetadataFromUniversalData(`{"__DEFAULT_SCOPE__":{}}`).IsEmpty(),
		)
	})
}

func TestApplyTikTokUniversalData(t *testing.T) {
	pageURL, err := url.Parse("https://www.tiktok.com/@creator/video/123")
	require.NoError(t, err)

	doc := &document{
		scriptPayloads: map[string]string{
			tiktokUniversalDataScriptID: tiktokUniversalJSON,
		},
	}

	t.Run("replaces the generic document title", func(t *testing.T) {
		metadata := applyTikTokUniversalData(
			doc,
			pageURL,
			Metadata{Title: optionalText("TikTok - Make Your Day")},
		)

		require.Equal(
			t,
			"S42 catch #spacex #starship #space",
			*metadata.Title,
		)
		require.Equal(
			t,
			"https://cdn.example/cover.jpg",
			*metadata.ImageURL,
		)
	})

	t.Run("another host is untouched", func(t *testing.T) {
		other, err := url.Parse("https://example.com/video/123")
		require.NoError(t, err)

		metadata := applyTikTokUniversalData(
			doc,
			other,
			Metadata{Title: optionalText("A title")},
		)

		require.Equal(t, "A title", *metadata.Title)
		require.Nil(t, metadata.ImageURL)
	})

	t.Run("a missing document is untouched", func(t *testing.T) {
		metadata := applyTikTokUniversalData(
			nil,
			pageURL,
			Metadata{Title: optionalText("A title")},
		)

		require.Equal(t, "A title", *metadata.Title)
	})
}

func TestIsTikTokNotFoundURL(t *testing.T) {
	cases := map[string]bool{
		"https://www.tiktok.com/404?fromUrl=/ZSpbx7bDc/": true,
		"https://www.tiktok.com/404/":                    true,
		"https://vm.tiktok.com/404":                      true,
		"https://www.tiktok.com/@creator/video/1":        false,
		"https://example.com/404":                        false,
	}

	for rawURL, expected := range cases {
		parsed, err := url.Parse(rawURL)
		require.NoError(t, err)

		require.Equal(t, expected, isTikTokNotFoundURL(parsed), rawURL)
	}

	require.False(t, isTikTokNotFoundURL(nil))
}

func TestDropGenericTitle(t *testing.T) {
	cases := []struct {
		name   string
		rawURL string
		title  string
		want   *string
	}{
		{
			name:   "the TikTok shell title is dropped",
			rawURL: "https://www.tiktok.com/@creator/video/1",
			title:  "TikTok - Make Your Day",
		},
		{
			name:   "the Instagram shell title is dropped",
			rawURL: "https://www.instagram.com/reel/1",
			title:  "Instagram",
		},
		{
			name:   "the YouTube shell title is dropped",
			rawURL: "https://www.youtube.com/watch?v=1",
			title:  "YouTube",
		},
		{
			name:   "a global not found title is dropped",
			rawURL: "https://example.com/article",
			title:  "Page not found",
		},
		{
			name:   "a real caption is kept",
			rawURL: "https://www.tiktok.com/@creator/video/1",
			title:  "S42 catch #spacex #starship #space",
			want:   optionalText("S42 catch #spacex #starship #space"),
		},
		{
			name:   "a real Instagram caption is kept",
			rawURL: "https://www.instagram.com/reel/1",
			title:  "Cavin Dennis Tito Siregar on Instagram: a caption",
			want:   optionalText("Cavin Dennis Tito Siregar on Instagram: a caption"),
		},
		{
			name:   "the same title on another host is kept",
			rawURL: "https://example.com/article",
			title:  "Instagram",
			want:   optionalText("Instagram"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := url.Parse(tc.rawURL)
			require.NoError(t, err)

			metadata := dropGenericTitle(
				parsed,
				Metadata{Title: optionalText(tc.title)},
			)

			if tc.want == nil {
				require.Nil(t, metadata.Title)
			} else {
				require.Equal(t, *tc.want, *metadata.Title)
			}
		})
	}
}

// The extractor captures the state blob only by its allowlisted id, and the
// decoder reads the fields this package can store.
func TestExtract_TikTokUniversalData(t *testing.T) {
	pageURL, err := url.Parse("https://www.tiktok.com/@creator/video/123")
	require.NoError(t, err)

	doc, err := extract(strings.NewReader(tiktokUniversalPage), pageURL)
	require.NoError(t, err)

	require.NotEmpty(t, doc.scriptPayloads[tiktokUniversalDataScriptID])

	metadata := applyTikTokUniversalData(
		doc,
		pageURL,
		selectMetadata(doc, pageURL),
	)

	require.Equal(
		t,
		"S42 catch #spacex #starship #space",
		*metadata.Title,
	)
	require.Equal(t, "https://cdn.example/cover.jpg", *metadata.ImageURL)
}
