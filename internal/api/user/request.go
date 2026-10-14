package user

// UpdateUserRequest is the body of PATCH /users/me.
//
// display_name is the only editable field on a profile, and it is the only one
// this endpoint accepts. Email is deliberately absent rather than read-only: a
// field that can be submitted and ignored teaches a client it was changed, and
// changing a users.email is a flow of its own rather than a profile edit.
//
// The validate tag is `required,min=2,max=100`, which is the tag
// registration/request.go already applies to the same column. It is repeated
// rather than shared because the two live in different packages and a shared
// constant would be one more thing to keep in step; matching it exactly is the
// part that matters. A user who registered with a two-character name and a
// hundred-character name must not be told by their own profile that either is
// invalid, and identical bounds are what guarantees that.
//
// Neither the value nor the column is trimmed or case-folded, and that is a
// deliberate match rather than an omission: registration passes its
// display_name straight through to the INSERT, so a name with surrounding
// whitespace is already stored verbatim. Normalising here and not there would
// give two paths into the same column that disagree about what it holds.
type UpdateUserRequest struct {
	DisplayName string `json:"display_name" validate:"required,min=2,max=100" example:"Jane Doe"`
}

// SetPasswordRequest is the body of PUT /users/me/password.
//
// The two fields exist because the endpoint serves two cases, and the case is
// decided by the database rather than by what arrives here: an account holding
// no credential must send new_password alone, and one that does must also send
// current_password. The service reads the credential and applies the
// requirement it finds, so has_password_credential on the profile is a hint for
// rendering the form and never an input to the decision.
//
// CurrentPassword is a plain string rather than a pointer because an empty
// string is not a password anybody can hold: new_password is required and at
// least eight characters, so an omitted current_password and one sent as "" are
// the same request. A pointer would distinguish them and make the service answer
// a question with no consequence either way.
//
// new_password is bounded at 128 to match login.LoginManualRequest, which is the
// one place a password is already bounded at both ends. Registration and password
// reset still declare `min=8` with no maximum; aligning those is a separate piece
// of work and not this one's, so the new field matches the stricter of the two
// existing declarations rather than loosening either.
type SetPasswordRequest struct {
	// CurrentPassword is required only when the account already holds a
	// credential. Supplying it for an account that holds none is refused with
	// UNEXPECTED_CURRENT_PASSWORD rather than ignored, because a caller told its
	// password was set while the password it sent was never checked would have no
	// way to learn that.
	CurrentPassword string `json:"current_password" example:"current-password-123"`

	// NewPassword is the password to store. It is never persisted as submitted.
	NewPassword string `json:"new_password" validate:"required,min=8,max=128" example:"new-password-456"`
}
