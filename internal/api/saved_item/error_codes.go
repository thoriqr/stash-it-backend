package saved_item

const (
	CodeInvalidURL = "INVALID_URL"

	// CodeInvalidCursor is returned when a cursor cannot be used. That covers every
	// way a token can fail: malformed base64 or JSON, an unsupported payload version,
	// a missing field, and a position that is not a timestamp. They share one code
	// because they all mean the same thing to a caller, start over without a cursor,
	// and telling them apart in the message would only make the token's internal
	// shape easier to probe.
	//
	// The same string is defined independently by internal/api/collection, which
	// issues its own cursor. The two features are separate and do not import one
	// another, so each states the code it depends on, in the same way each states
	// CollectionSystemKeyUnsorted. They must agree, because a client handles the
	// code and not the package it came from.
	CodeInvalidCursor = "INVALID_CURSOR"
)
