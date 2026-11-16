package enrichment

import "strings"

// Platform canonicalization.
//
// Some platforms publish their own product name in <meta name="application-name">,
// but localize it per section and per locale: Facebook answers "Facebook Groups",
// "Grup Facebook" and "Facebook 小组" for the same kind of page depending on where
// the request comes from and which Accept-Language it sent. Storing that raw value
// would make the platform depend on the datacenter the worker runs in, and would
// create a separate collection per locale.
//
// The rule here reduces a declared name to the brand the page itself names. It is
// still metadata-derived: the value arrives from the page, and only a name that
// actually names a known brand is reduced. Nothing is inferred from the URL, which
// would reintroduce the hostname-based platform the product decisions forbid.
//
// The reduction is deliberately narrow. A name that does not name a brand is left
// alone, so an ordinary site keeps the exact platform its page published.

// platformBrand is one canonical name and the word that identifies it.
type platformBrand struct {
	// canonical is the platform value recorded when the brand is recognised.
	canonical string

	// token is the brand as it appears inside a localized name, lowercased. It is
	// compared as a whole word so a longer word containing it does not match.
	token string
}

// platformBrands are the platforms whose product name is localized.
//
// A single-letter brand such as X is absent on purpose: it appears as a whole word
// in far too many ordinary names to be recognised this way.
var platformBrands = []platformBrand{
	{canonical: "Facebook", token: "facebook"},
	{canonical: "Instagram", token: "instagram"},
	{canonical: "TikTok", token: "tiktok"},
}

// canonicalPlatform reduces a localized product name to the brand it names.
//
// A whole-word match handles "Grup Facebook", "Facebook Groups" and
// "Facebook 小组", where the qualifier may precede or follow the brand. A prefix
// match covers a name written without a separating space, which is how a CJK
// translation is sometimes rendered.
//
// The value is returned unchanged when it names no known brand, including when it
// is empty.
func canonicalPlatform(value string) string {
	normalized := normalizeText(value)
	if normalized == "" {
		return ""
	}

	lowered := strings.ToLower(normalized)

	for _, brand := range platformBrands {
		if lowered == brand.token {
			return brand.canonical
		}

		if strings.HasPrefix(lowered, brand.token) {
			return brand.canonical
		}

		for field := range strings.FieldsSeq(lowered) {
			if field == brand.token {
				return brand.canonical
			}
		}
	}

	return normalized
}

// optionalPlatformName returns a canonicalized platform, or nil when there is no
// value worth storing.
//
// Every path uses this rather than optionalText for the platform field, so a
// localized name is reduced no matter which source produced it.
func optionalPlatformName(value string) *string {
	canonical := canonicalPlatform(value)
	if canonical == "" {
		return nil
	}

	return &canonical
}
