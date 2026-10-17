package saved_item

type CreateSavedItemRequest struct {
	URL string `json:"url" validate:"required,url,max=2048" example:"https://example.com/articles/1"`
}

// ListSavedItemsRequest is the query for the recent saved items history.
//
// There is deliberately no sort parameter. A history is read newest first or not at
// all, and offering orders would answer a question this resource does not pose: a
// person returning to what they saved is looking for what is new. Sorting within a
// collection is a different resource, and that one lives in
// GET /collections/{id}/saved-items.
//
// The cursor carries no `validate` rule beyond its length. It is an opaque token
// bound here and nowhere else; decoding it and deciding whether its contents are
// usable belong to the service, which owns those rules for every cursor it issues.
type ListSavedItemsRequest struct {
	// Limit is the number of saved items to return. Absent means the default, and a
	// value above the maximum is rejected rather than clamped: max=50 on the tag fires
	// before the service's own clamp can, so a caller is told what it asked for was
	// out of range instead of quietly receiving a differently sized page. The clamp
	// still exists in the service, for callers that reach it without going through
	// this type.
	Limit int `query:"limit" validate:"omitempty,min=1,max=50" example:"20"`

	// Cursor resumes the history from where the previous page ended. Absent starts at
	// the newest item.
	//
	// max=512 is the one bound that matters here. It is what stops a client making
	// the server base64-decode an arbitrarily large string.
	Cursor string `query:"cursor" validate:"omitempty,max=512" example:"eyJ2IjoxLCJ2YWwiOiIyMDI2LTEwLTAyVDEwOjMwOjAwWiJ9"`
}
