package search

const (
	// CodeInvalidSearchQuery is returned when the submitted query is blank after
	// trimming, shorter than QueryMinLength, or longer than QueryMaxLength.
	CodeInvalidSearchQuery = "INVALID_SEARCH_QUERY"
)
