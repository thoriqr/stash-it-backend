package enrichment

// Field selection order.
//
// Precedence follows how web metadata is actually meant to be read: a specific,
// platform-specific claim about the page beats a generic one, and content
// authored for the page beats a machine readable description of it.
//
// The orders are declared as slices rather than being hardcoded at each use site
// so that the precedence for one field is readable in one place, and so a test can
// assert an order itself instead of inferring it from a handful of examples.
var (
	// titlePrecedence is the order title candidates are taken from.
	//
	// og:title and twitter:title are authored specifically for this page and so
	// beat the document title, which is frequently padded with the site name, as
	// in "An interesting article | Example". The JSON-LD fields come last
	// because they are the least likely to be present and the most likely to
	// describe something other than this page.
	titlePrecedence = []string{
		metaOpenGraphTitle,
		metaTwitterTitle,
		titleElement,
		jsonLDHeadline,
		jsonLDName,
	}

	// descriptionPrecedence is the order description candidates are taken from.
	//
	// The Open Graph description is written for sharing and is usually the most
	// deliberate text on the page. The standard meta description is the
	// search-engine facing equivalent and is the next best thing. JSON-LD is last
	// because a description there often describes a related entity rather than
	// the page itself.
	descriptionPrecedence = []string{
		metaOpenGraphDescription,
		metaTwitterDescription,
		metaDescription,
		jsonLDDescription,
	}

	// imagePrecedence is the order image candidates are taken from.
	imagePrecedence = []string{
		metaOpenGraphImage,
		metaTwitterImage,
		metaTwitterImageSrc,
		jsonLDImage,
	}

	// canonicalPrecedence is the order canonical URL candidates are taken from.
	//
	// A declared rel="canonical" is the site's own statement about which URL
	// represents this page, so it beats og:url, which is often the URL a share
	// refers to rather than the canonical one.
	canonicalPrecedence = []string{
		metaLinkCanonical,
		metaOpenGraphURL,
		jsonLDURL,
	}

	// platformPrecedence is the order platform candidates are taken from.
	//
	// Platform is deliberately the most conservative field in this package,
	// because it is a product-level classification rather than something the page
	// states outright. Only signals that name a site, an application, or an
	// organisation are considered, and the hostname is never consulted: deriving
	// a platform from the URL is exactly what Phase A deliberately did not do.
	//
	// application-name comes first because it is the page making a claim about
	// itself. The JSON-LD publisher and organisation names follow for the same
	// reason. og:site_name is last because it is frequently a marketing name
	// rather than the name of the platform the content belongs to.
	platformPrecedence = []string{
		metaApplicationName,
		jsonLDPublisherName,
		jsonLDOrganizationName,
		jsonLDWebSiteName,
		metaOpenGraphSiteName,
	}

	// siteNamePrecedence is the order site name candidates are taken from.
	siteNamePrecedence = []string{
		metaOpenGraphSiteName,
		metaApplicationName,
		jsonLDWebSiteName,
		jsonLDPublisherName,
	}

	// authorPrecedence is the order author candidates are taken from.
	//
	// Author is read because pages publish it, but it is not a saved_items
	// column, so the result exposes it without implying it should be stored.
	authorPrecedence = []string{
		metaAuthor,
		metaOpenGraphAuthor,
		jsonLDAuthorName,
	}
)

// Candidate keys.
//
// Every source, whether a meta tag, the title element, a canonical link or a
// JSON-LD block, contributes to one flat map keyed by these. Keeping them in one
// namespace is what lets precedence be expressed as data instead of as a chain of
// conditionals.
//
// The JSON-LD keys are namespaced with a "jsonld:" prefix because the property
// names JSON-LD uses are the same words the HTML vocabularies use. Without the
// prefix a meta description and a JSON-LD description would be the same key, and
// which of the two won would depend on the order they appeared in the document
// rather than on the precedence declared above.
//
// The other two keys are written the way they are to look like markup, because
// they are not meta tags at all and must not be reachable as one.
const (
	titleElement      = "<title>"
	metaLinkCanonical = "<link rel=canonical>"

	metaDescription          = "description"
	metaAuthor               = "author"
	metaApplicationName      = "application-name"
	metaOpenGraphTitle       = "og:title"
	metaOpenGraphURL         = "og:url"
	metaOpenGraphImage       = "og:image"
	metaOpenGraphSiteName    = "og:site_name"
	metaOpenGraphAuthor      = "og:author"
	metaOpenGraphDescription = "og:description"
	metaTwitterTitle         = "twitter:title"
	metaTwitterDescription   = "twitter:description"
	metaTwitterImage         = "twitter:image"
	metaTwitterImageSrc      = "twitter:image:src"

	jsonLDName             = "jsonld:name"
	jsonLDHeadline         = "jsonld:headline"
	jsonLDDescription      = "jsonld:description"
	jsonLDImage            = "jsonld:image"
	jsonLDURL              = "jsonld:url"
	jsonLDAuthorName       = "jsonld:author.name"
	jsonLDPublisherName    = "jsonld:publisher.name"
	jsonLDOrganizationName = "jsonld:organization.name"
	jsonLDWebSiteName      = "jsonld:website.name"
)

// linkRelCanonical is the link relation that declares a canonical URL. It is
// matched as one token of a space separated list, because rel may carry several.
const linkRelCanonical = "canonical"

// scriptTypeJSONLD is the script type carrying a JSON-LD block.
const scriptTypeJSONLD = "application/ld+json"

// JSON-LD property names.
//
// These are the keys as they appear inside the JSON document, and are a different
// set from the candidate keys above. JSON-LD is not a fixed vocabulary, and the
// same property appears both as a plain key and as an "@" keyword in the wild, so
// the reader accepts either form.
//
// Only the properties that map onto a saved_items column or onto a decision the
// caller makes are read. This is not a Schema.org implementation and does not
// attempt to be one.
const (
	jsonPropName        = "name"
	jsonPropHeadline    = "headline"
	jsonPropDescription = "description"
	jsonPropImage       = "image"
	jsonPropURL         = "url"
	jsonPropAuthor      = "author"
	jsonPropPublisher   = "publisher"
	jsonPropGraph       = "@graph"
	jsonPropValue       = "@value"
	jsonPropType        = "@type"
	jsonPropContentURL  = "contentUrl"

	// jsonTypeOrganization and jsonTypeWebSite are the @type values that make an
	// entity usable as a platform signal.
	//
	// Pages write a type as "Organization", "schema:Organization" and
	// "https://schema.org/Organization" interchangeably, so only the final
	// segment is compared and all three forms resolve to the same type.
	jsonTypeOrganization = "organization"
	jsonTypeWebSite      = "website"
)
