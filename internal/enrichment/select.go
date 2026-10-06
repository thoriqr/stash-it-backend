package enrichment

import "net/url"

// selectMetadata applies the precedence orders in constants.go to a parsed
// document and returns the metadata to persist.
//
// The candidate map is walked once per field, in the order the field's
// precedence declares, and the first non-empty candidate wins. That is the whole
// selection algorithm: no per-field branching, and no field can be given a
// different rule in a different place.
//
// baseURL is the URL the document was actually served from, which after a
// redirect is not the URL that was requested. Resolving metadata against it is
// what makes a relative og:image correct on a page that moved.
func selectMetadata(
	doc *document,
	baseURL *url.URL,
) Metadata {
	return Metadata{
		Title:        optionalText(firstCandidate(doc, titlePrecedence)),
		Platform:     optionalText(firstCandidate(doc, platformPrecedence)),
		Description:  optionalText(firstCandidate(doc, descriptionPrecedence)),
		ImageURL:     optionalURL(firstCandidate(doc, imagePrecedence), baseURL),
		CanonicalURL: optionalURL(firstCandidate(doc, canonicalPrecedence), baseURL),
		SiteName:     optionalText(firstCandidate(doc, siteNamePrecedence)),
		Author:       optionalText(firstCandidate(doc, authorPrecedence)),
	}
}

// firstCandidate returns the first candidate present in the given precedence
// order.
//
// A candidate that normalizes to nothing is skipped rather than returned, so a
// whitespace-only og:title does not shadow a perfectly good <title>.
func firstCandidate(
	doc *document,
	precedence []string,
) string {
	for _, key := range precedence {
		value, ok := doc.candidates[key]
		if !ok {
			continue
		}

		if normalizeText(value) == "" {
			continue
		}

		return value
	}

	return ""
}
