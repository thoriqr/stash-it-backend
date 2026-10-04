package collection

const (
	// CollectionNameMaxLength bounds a collection's display name, measured in
	// runes after trimming. It matches the display name limit already applied to
	// users, and is enforced in the service because the database has no such
	// constraint.
	CollectionNameMaxLength = 100
)
