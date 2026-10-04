package search

// SearchRequest is the query for GET /search.
//
// The query carries no validate tag on purpose. The search service already owns
// the query rules, including the minimum and maximum length, measured in runes
// after trimming, and it reports them as INVALID_SEARCH_QUERY. A validate tag
// here would be a second implementation of the same rules in a different place,
// with its own byte-versus-rune behaviour and its own error code, and the two
// could disagree. A missing or empty q therefore reaches the service unchanged
// and is rejected there.
type SearchRequest struct {
	Query string `query:"q" example:"camera"`
}
