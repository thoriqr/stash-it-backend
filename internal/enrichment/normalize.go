package enrichment

import (
	"net/url"
	"strings"
	"unicode"
)

// Normalization for extracted metadata.
//
// Everything here is conservative. A title is something a person reads, so
// over-normalizing it is worse than leaving it slightly untidy: the text is not
// lowercased, punctuation is not touched, and nothing is truncated. What is done
// is trimming the whitespace that markup routinely introduces, and dropping
// values that carry no information.

// normalizeText trims surrounding whitespace and collapses internal whitespace
// runs to a single space.
//
// HTML collapses whitespace when it renders, so a title written across several
// indented lines in a template is a single line to a reader. Collapsing it here
// means the stored value matches what the page showed, rather than carrying the
// template's indentation into the database.
//
// Splitting on whitespace also folds the non-breaking spaces that appear in
// scraped and machine-generated titles back into ordinary ones.
func normalizeText(value string) string {
	fields := strings.FieldsFunc(value, unicode.IsSpace)
	if len(fields) == 0 {
		return ""
	}

	return strings.Join(fields, " ")
}

// normalizeURL resolves a metadata URL against the page it was found on.
//
// Metadata URLs are frequently relative, most often as a root relative path such
// as /og.png, so an unresolved value would be stored as something that does not
// identify a resource.
//
// Resolution is purely local. The result is not fetched and not validated against
// the outbound fetch policy, because that policy governs requests this server
// makes, and a stored image URL is data the client will act on rather than
// something the server retrieves. A client that displays it must apply its own
// rules.
//
// A value that cannot be resolved is dropped. That is deliberate: a metadata URL
// the extractor cannot make sense of is not worth persisting, and it must not
// fail an enrichment that otherwise succeeded.
func normalizeURL(
	value string,
	baseURL *url.URL,
) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	reference, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}

	// A base is needed for a relative reference. Without one there is nothing to
	// resolve against, and an unresolved relative URL is not a usable value.
	if baseURL == nil {
		if !reference.IsAbs() {
			return ""
		}

		return reference.String()
	}

	resolved := baseURL.ResolveReference(reference)
	if resolved == nil {
		return ""
	}

	// Only http and https are carried through. A page declaring a javascript: or
	// data: URL as its own canonical or image is not producing something the
	// saved item should be pointing at.
	switch strings.ToLower(resolved.Scheme) {
	case "http", "https":
		return resolved.String()

	default:
		return ""
	}
}

// optionalText returns a pointer to the normalized text, or nil when there is
// nothing worth storing.
//
// Returning nil for a blank value rather than a pointer to "" is what makes
// "no description" distinguishable from "an empty description" at the call site,
// and it keeps the caller from having to re-apply this rule.
func optionalText(value string) *string {
	normalized := normalizeText(value)
	if normalized == "" {
		return nil
	}

	return &normalized
}

// optionalURL resolves a metadata URL and returns it, or nil when there is no
// usable value.
func optionalURL(
	value string,
	baseURL *url.URL,
) *string {
	resolved := normalizeURL(value, baseURL)
	if resolved == "" {
		return nil
	}

	return &resolved
}
