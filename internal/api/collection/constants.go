package collection

const (
	// CollectionNameMaxLength bounds a collection's display name, measured in
	// runes after trimming. It matches the display name limit already applied to
	// users, and is enforced in the service because the database has no such
	// constraint.
	CollectionNameMaxLength = 100

	// CollectionListDefaultLimit and CollectionListMaxLimit bound a page of
	// collections. Both mirror the saved item and session list limits so the three
	// list endpoints answer to the same numbers, and they are applied in the service
	// the same way rather than only in the query.
	CollectionListDefaultLimit = 20
	CollectionListMaxLimit     = 50
)
