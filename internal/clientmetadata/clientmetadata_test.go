package clientmetadata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func text(value string) *string {
	return &value
}

func TestNormalize_TrimsAndDropsBlankFields(t *testing.T) {
	normalized, err := Normalize(&Metadata{
		Title:       text("  A title  "),
		Description: text("   "),
		ImageURL:    text(" https://cdn.example/a.jpg "),
		Platform:    text(" Instagram "),
	})

	require.NoError(t, err)
	require.Equal(t, "A title", *normalized.Title)
	require.Nil(t, normalized.Description)
	require.Equal(t, "https://cdn.example/a.jpg", *normalized.ImageURL)
	require.Equal(t, "Instagram", *normalized.Platform)
	require.True(t, normalized.HasValue())
}

func TestNormalize_NothingIsNotAnError(t *testing.T) {
	normalized, err := Normalize(nil)

	require.NoError(t, err)
	require.False(t, normalized.HasValue())
}

func TestNormalize_RejectsOverlongTitle(t *testing.T) {
	value := make([]rune, TitleMaxRunes+1)
	for index := range value {
		value[index] = 'a'
	}

	_, err := Normalize(&Metadata{Title: text(string(value))})

	require.Error(t, err)
}

func TestNormalize_AcceptsOnlyHTTPImages(t *testing.T) {
	for _, value := range []string{
		"javascript:alert(1)",
		"data:image/png;base64,AAAA",
		"/relative.png",
		"ftp://example.com/a.png",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := Normalize(&Metadata{ImageURL: text(value)})

			require.Error(t, err)
		})
	}

	for _, value := range []string{
		"http://example.com/a.png",
		"https://example.com/a.png",
	} {
		t.Run(value, func(t *testing.T) {
			normalized, err := Normalize(&Metadata{ImageURL: text(value)})

			require.NoError(t, err)
			require.Equal(t, value, *normalized.ImageURL)
		})
	}
}
