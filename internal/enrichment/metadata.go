package enrichment

// Metadata is what enrichment discovered about a saved item's page.
//
// It is deliberately shaped around what saved_items can actually store. Title and
// Platform are written by enrichment, and Description and ImageURL are written by
// enrichment too: migration 000022 added those columns and migration 000024
// finalised them, and documentation/product.md lists them as enrichment output.
// No field here exists only because the extractor happens to be able to find it,
// and this package proposes no schema change.
//
// Every field is optional. A page that exposes nothing usable produces a
// zero-valued Metadata and a nil error, because "this page told us nothing" is a
// successful enrichment with an empty result, not a failure. An optional field is
// nil when nothing was found, and non-nil only when there is a value worth
// storing.
//
// Optional fields are *string rather than pgtype.Text because this package has no
// business knowing about PostgreSQL. Persistence, including the conversion to
// pgtype.Text, belongs to the caller that owns the repository.
type Metadata struct {
	// Title is the page's human readable title. It maps to saved_items.title,
	// which is NULL for every item until enrichment runs.
	//
	// Not every page has one. A document with no <title> and no metadata yields
	// nil here rather than the URL or the hostname being invented as a stand-in.
	Title *string

	// Platform is the content source the page belongs to, in the product's
	// sense, such as youtube or pinterest. It maps to saved_items.platform.
	//
	// It is derived from metadata the page publishes about itself, never from
	// the hostname of the URL. A page that says nothing about itself produces
	// nil, and that is the intended outcome.
	Platform *string

	// Description maps to saved_items.description. A page is not required to
	// expose one, and extraction of nothing useful yields nil.
	Description *string

	// ImageURL is a preview image, resolved to an absolute URL. It maps to
	// saved_items.image_url.
	ImageURL *string

	// CanonicalURL is the URL the page declares as representing itself, either
	// through <link rel="canonical"> or through og:url.
	//
	// It is exposed and deliberately not acted on. Replacing a saved item's url
	// with its canonical form is a product decision about what a saved item
	// means, and it belongs to the caller rather than to the extractor. Nothing
	// in this package fetches a canonical URL or validates it.
	CanonicalURL *string

	// SiteName is the name the page gives its site, such as a publication
	// masthead. There is no saved_items column for it; it is here because it is
	// both a useful debugging signal and a legitimate input to a future
	// decision, and because it is the raw material Platform is derived from.
	SiteName *string

	// Author is the name the page credits. There is no saved_items column for
	// it and no plan to add one, so it is exposed for the same reason as
	// SiteName and is otherwise unused.
	Author *string
}

// IsEmpty reports whether nothing usable was found.
//
// A caller persisting enrichment can use it to decide between recording a
// completed enrichment that simply found nothing and recording one that never
// ran. The two are different product states even though both leave the item's
// metadata columns NULL.
func (m Metadata) IsEmpty() bool {
	return m.Title == nil &&
		m.Platform == nil &&
		m.Description == nil &&
		m.ImageURL == nil &&
		m.CanonicalURL == nil &&
		m.SiteName == nil &&
		m.Author == nil
}
