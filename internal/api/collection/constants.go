package collection

const (
	// CollectionNameMaxLength bounds a collection's display name, measured in
	// runes after trimming. It matches the display name limit already applied to
	// users, and is enforced in the service because the database has no such
	// constraint.
	CollectionNameMaxLength = 100

	// CollectionListDefaultLimit and CollectionListMaxLimit bound a page of
	// collections. Both mirror the saved item and session list limits so the list
	// endpoints answer to the same numbers, and they are applied in the service the
	// same way rather than only in the query.
	CollectionListDefaultLimit = 20
	CollectionListMaxLimit     = 50

	// SavedItemListDefaultLimit and SavedItemListMaxLimit bound a page of a
	// collection's saved items. They match CollectionListDefaultLimit and
	// CollectionListMaxLimit, which is why they are stated as their own constants
	// rather than borrowed: the two listings bound different resources and could
	// reasonably move apart later without silently changing the other.
	SavedItemListDefaultLimit = 20
	SavedItemListMaxLimit     = 50
)
