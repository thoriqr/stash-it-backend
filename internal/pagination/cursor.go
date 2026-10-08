// Package pagination provides the opaque cursor tokens that keyset-paginated list
// endpoints hand to clients.
//
// It exists as its own package rather than inside a feature because the codec is
// mechanism with no domain knowledge: it encodes a caller-supplied payload and
// decodes it back. What goes in the payload belongs to the feature that issued it,
// so payload structs stay next to their feature and only the envelope lives here.
// That split is deliberate: a single shared payload struct with optional fields
// would be one shape that two endpoints could disagree about, which is the failure
// this codebase's other decisions keep rejecting.
//
// Encoding is JSON over base64.RawURLEncoding. The base64 variant matches
// internal/security, which uses RawURLEncoding for the tokens it issues, and the
// JSON payload matches internal/worker/queue, whose task payloads are explicit
// structs with documented shapes. A cursor is URL-safe without escaping, which is
// what lets it travel as a plain query parameter.
package pagination

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Errors returned by Decode. Callers translate these into an application error;
// they are deliberately sentinel errors so a feature does not have to match on
// error text to decide between a malformed cursor and a valid one.
var (
	// ErrMalformed means the token could not be decoded, or its payload was not
	// the expected JSON shape.
	ErrMalformed = errors.New("cursor is malformed")

	// ErrUnsupportedVersion means the payload was well formed but carries a format
	// version this build does not understand.
	ErrUnsupportedVersion = errors.New("cursor version is not supported")
)

// CurrentVersion is the payload format this build issues and accepts.
//
// A version exists because cursors outlive the process that made them: a client
// holds one across requests and may hold one across app launches, so tokens
// written by an earlier deployment can still arrive here. Without a version, a
// payload that gained or changed a field would be misread as a valid position,
// and the failure would be silently skipping rows rather than an error. Version 1
// is the initial format.
//
// Bump this only for a change that makes previously issued cursors unreadable or
// ambiguous. A purely additive change does not need it.
const CurrentVersion = 1

// Encode turns a payload into an opaque cursor token.
//
// The payload is JSON-marshalled and then base64-encoded. Marshalling failure is
// returned rather than swallowed: it means the caller's payload contains something
// unmarshalable, which is a programming error and must not be turned into a token
// that decodes to nothing.
func Encode(payload any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal cursor payload: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

// Decode reads a token back into the payload type T.
//
// A token that is not base64, not JSON, or not a T is ErrMalformed. A payload
// carrying a different version is ErrUnsupportedVersion. Everything else, including
// whether the decoded values are meaningful, is the caller's to check: this
// function knows how to read the envelope, not what any particular field means.
//
// Decoding into a struct with pointer fields is what makes "field absent"
// distinguishable from "field present and zero". That distinction is load-bearing for
// cursors, where an absent position and a zero position mean different things.
func Decode[T any](token string) (T, error) {
	var zero T

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return zero, ErrMalformed
	}

	var payload T

	if err := json.Unmarshal(decoded, &payload); err != nil {
		return zero, ErrMalformed
	}

	return payload, nil
}

// DecodeVersioned is Decode plus the version check, which every cursor in this
// project needs because every cursor payload carries a version.
//
// It keeps the version rule in one place rather than repeating it per feature: a
// feature that forgot the check would silently accept tokens from another format,
// which is the exact failure the version exists to prevent.
func DecodeVersioned[T any](token string) (T, error) {
	payload, err := Decode[T](token)
	if err != nil {
		return payload, err
	}

	return payload, checkVersion(payload)
}

// versioned is implemented by cursor payloads that carry a format version.
type versioned interface {
	// CursorVersion reports the payload's format version.
	CursorVersion() int
}

func checkVersion[T any](payload T) error {
	// T is a struct in every use here, so a pointer receiver method is reachable
	// through it. A payload that does not participate in versioning is accepted as
	// version 0, which only DecodeVersioned on a versioned payload will ever query.
	v, ok := any(payload).(versioned)
	if !ok {
		return nil
	}

	if v.CursorVersion() != CurrentVersion {
		return ErrUnsupportedVersion
	}

	return nil
}
