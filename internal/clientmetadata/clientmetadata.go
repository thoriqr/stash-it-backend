// Package clientmetadata models the metadata an app extracts from a page the
// server cannot read.
//
// It exists as its own package because two features accept the same value: a save
// carries it, and a re-enrich carries it again. Features do not import one another,
// so the type, its bounds and its normalization live here and each feature
// consumes them.
//
// The values are untrusted. They did not come from the server's extractor, so
// every field is bounded before it can reach a column that has no length limit of
// its own, and an image URL must be an absolute http or https URL that is never
// fetched.
package clientmetadata

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// Bounds on the client-supplied metadata.
//
// They are generous enough for a long caption or description and small enough that
// a client cannot use the route as storage.
const (
	TitleMaxRunes       = 1000
	DescriptionMaxRunes = 10000
	ImageURLMaxBytes    = 2048
)

// Metadata is what a client extracted from a page.
//
// Every field is a pointer because absence is meaningful: a field the client did
// not find is stored as NULL, never as an empty string, and blank-after-trim
// counts as absent.
//
// There is deliberately no platform here. It becomes a collection's name, and a
// value the client chose would let a typo or a localized name create a second
// collection for the same host. The server derives it from the URL's host instead,
// so one host always maps to one collection.
type Metadata struct {
	Title       *string `json:"title" example:"An interesting reel"`
	Description *string `json:"description" example:"A short summary of the post"`
	ImageURL    *string `json:"image_url" example:"https://example.com/preview.jpg"`
}

// Normalized is the client's metadata after trimming and validation.
//
// A nil field is absent: it is what the database stores as NULL.
type Normalized struct {
	Title       *string
	Description *string
	ImageURL    *string
}

// HasValue reports whether at least one field carries a value.
//
// This is the only rule that decides between a completed and a failed
// client-assisted enrichment: a client that extracted something completed it, and
// one that extracted nothing recorded the failure.
func (n Normalized) HasValue() bool {
	return n.Title != nil || n.Description != nil || n.ImageURL != nil
}

// Normalize trims the client's values and enforces the bounds.
//
// A blank field becomes nil, so "the client sent nothing useful" cannot be
// mistaken for a real value. An invalid value is a validation error and nothing is
// written: a client that sends a non-http image URL or an over-long title is a
// client bug, and storing a truncated or scheme-less value would hide it.
//
// A nil Metadata is not an error. It normalizes to an empty value, which is what a
// caller that sent no metadata at all means.
func Normalize(
	metadata *Metadata,
) (Normalized, error) {
	if metadata == nil {
		return Normalized{}, nil
	}

	title, err := normalizeText(metadata.Title, TitleMaxRunes, "title")
	if err != nil {
		return Normalized{}, err
	}

	description, err := normalizeText(
		metadata.Description,
		DescriptionMaxRunes,
		"description",
	)
	if err != nil {
		return Normalized{}, err
	}

	imageURL, err := normalizeImageURL(metadata.ImageURL)
	if err != nil {
		return Normalized{}, err
	}

	return Normalized{
		Title:       title,
		Description: description,
		ImageURL:    imageURL,
	}, nil
}

// normalizeText trims one value, rejects an over-long one and treats a blank one
// as absent.
func normalizeText(
	value *string,
	maxRunes int,
	field string,
) (*string, error) {
	if value == nil {
		return nil, nil
	}

	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}

	if utf8.RuneCountInString(trimmed) > maxRunes {
		return nil, apperror.Validation(
			[]apperror.ErrorField{{
				Field:   field,
				Message: "is too large",
			}},
			nil,
		)
	}

	return &trimmed, nil
}

// normalizeImageURL accepts only an absolute http or https URL.
//
// The URL is never fetched. It is stored so a client can render a preview, and a
// non-http scheme would be a value the product cannot use and a potential source
// of a JavaScript or data URL in a client.
func normalizeImageURL(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}

	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}

	if len(trimmed) > ImageURLMaxBytes {
		return nil, invalidImageURL("is too large")
	}

	parsed, parseErr := url.Parse(trimmed)
	if parseErr != nil || !parsed.IsAbs() || parsed.Host == "" {
		return nil, invalidImageURL("must be an absolute http or https URL")
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, invalidImageURL("must be an absolute http or https URL")
	}

	return &trimmed, nil
}

func invalidImageURL(message string) error {
	return apperror.Validation(
		[]apperror.ErrorField{{
			Field:   "image_url",
			Message: message,
		}},
		nil,
	)
}
