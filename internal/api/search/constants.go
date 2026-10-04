package search

const (
	// QueryMinLength is the shortest query that can produce a meaningful match,
	// measured in runes after trimming. A single rune matches far too much to be
	// useful and forces the fuzzy threshold to admit noise.
	QueryMinLength = 2

	// QueryMaxLength bounds a search query, measured in runes after trimming. No
	// realistic query approaches it, and an unbounded query is an unbounded
	// amount of trigram work in the database.
	QueryMaxLength = 128

	// SavedItemDefaultLimit is how many saved items a search returns when the
	// caller does not ask for a different amount.
	SavedItemDefaultLimit = 20

	// SavedItemMaxLimit is the most saved items a single search can return. The
	// limit exists because search is not paged, so it is the only thing bounding
	// the work one request can cause.
	SavedItemMaxLimit = 50

	// CollectionLimit is how many collections a search returns. It is fixed rather
	// than caller controlled because collections are a navigation aid shown
	// alongside the results, not a list to page through.
	CollectionLimit = 5
)
