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
		Platform:     optionalPlatformName(selectPlatform(doc, baseURL)),
		Description:  optionalText(firstCandidate(doc, descriptionPrecedence)),
		ImageURL:     optionalURL(firstCandidate(doc, imagePrecedence), baseURL),
		CanonicalURL: optionalURL(firstCandidate(doc, canonicalPrecedence), baseURL),
		SiteName:     optionalText(firstCandidate(doc, siteNamePrecedence)),
		Author:       optionalText(firstCandidate(doc, authorPrecedence)),
		Price:        selectPrice(doc),
	}
}

// storefrontSiteNameHosts are hosts whose JSON-LD publisher names the game's
// publisher rather than the storefront. On these hosts og:site_name is the
// storefront's own name and is the only correct platform source.
//
// The value still comes from the page. The hostname only decides which declared
// field to trust, in the same way the Amazon and Instagram rules are host-scoped.
var storefrontSiteNameHosts = map[string]struct{}{
	"gog.com":     {},
	"www.gog.com": {},
}

func selectPlatform(
	doc *document,
	baseURL *url.URL,
) string {
	platform := firstCandidate(doc, platformPrecedence)

	if baseURL != nil {
		if _, ok := storefrontSiteNameHosts[normalizeHostname(baseURL.Hostname())]; ok {
			if siteName := firstCandidate(doc, []string{metaOpenGraphSiteName}); siteName != "" {
				return siteName
			}
		}
	}

	return platform
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
