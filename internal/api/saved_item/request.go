package saved_item

type CreateSavedItemRequest struct {
	URL string `json:"url" validate:"required,url,max=2048" example:"https://example.com/articles/1"`

	// Note is optional plain text the user wants to remember about this URL. It is
	// nullable and may span multiple lines, and leaving it out (or sending null)
	// preserves the existing save behavior exactly. A value containing only
	// whitespace is stored as NULL.
	//
	// max=5000 is measured in runes by the validator, so a multilingual note is not
	// rejected for being long in bytes. The service applies the same bound again for
	// callers that reach it without this type.
	Note *string `json:"note" validate:"omitempty,max=5000" example:"Read the section about database indexing later."`
}

// UpdateSavedItemNoteRequest is the body of PUT /saved-items/:id/note.
//
// The note is required here: this route adds or edits one, and clearing is the
// separate DELETE. The field carries no `validate` constraint because the service
// owns these rules and answers with INVALID_NOTE, so a message can say which rule
// was broken rather than collapsing every case into a generic validation error. A
// whitespace-only value is rejected rather than treated as a clear.
type UpdateSavedItemNoteRequest struct {
	Note string `json:"note" maxLength:"5000" example:"My updated context for saving this URL."`
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
