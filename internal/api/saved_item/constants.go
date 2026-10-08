package saved_item

const (
	SavedItemListDefaultLimit = 20
	SavedItemListMaxLimit     = 50

	// CollectionSystemKeyUnsorted is the stable identity of the Unsorted
	// collection, the one collection a user may never delete.
	//
	// It is compared as an exact value and never derived from the display name.
	// Unsorted is identified by this key because the name is free text a client
	// renders, while the key is what the schema requires every system collection
	// to carry and what collections_system_key_unique makes unique per user.
	//
	// The same identity is defined by internal/api/collection and by
	// internal/worker/organization. They are separate features on purpose and do
	// not import one another, so each states the value it depends on, and the
	// three must agree: the key is the one definition of which collection is
	// Unsorted.
	//
	// A collection's type is deliberately not part of this. 'system' describes who
	// created a collection, not who owns it and not what it is, so a system
	// collection is one the user may delete exactly like one of their own.
	CollectionSystemKeyUnsorted = "unsorted"
)
