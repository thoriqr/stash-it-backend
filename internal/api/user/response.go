package user

import (
	"time"

	"github.com/google/uuid"
)

// UserIdentityResponse is one linked authentication provider.
//
// Provider and LinkedAt identify the link. EmailSnapshot and DisplayNameSnapshot
// are what the provider asserted about the account at the moment the link was
// made, reported exactly as stored.
//
// The snapshots are pointers because the columns are nullable, and the
// distinction is not cosmetic: pgtype.Text with Valid false is a provider that
// said nothing, and it is reported as JSON null. A non-nil pointer to an empty
// string would mean a provider that asserted an empty value. Substituting one
// for the other would tell a client the provider said something when it did not,
// and would do it silently.
//
// The two are not copies of the account's own email and display_name, and they
// are not kept in step with it. Migration 000026 removed the duplicate address
// from account_link_confirmations for exactly this reason — the account's own
// email is the authoritative one — and these columns are kept as history. Nothing
// refreshes them, this endpoint does not contact the provider to try, and a
// snapshot that has drifted from the account is a fact about when the link was
// made rather than an inconsistency to be corrected here. A client that wants to
// show which is current has both, and can compare them itself.
//
// provider_subject is the credential the identity is bound to and the row id is
// an internal identifier, so neither is carried.
type UserIdentityResponse struct {
	Provider            string    `json:"provider" example:"google"`
	LinkedAt            time.Time `json:"linked_at" example:"2026-10-01T10:30:00Z"`
	EmailSnapshot       *string   `json:"email_snapshot" example:"user@example.com"`
	DisplayNameSnapshot *string   `json:"display_name_snapshot" example:"John Doe"`
}

// UserResponse is one user's profile.
//
// It is the same shape whether it was read or written: PATCH returns the row its
// own UPDATE produced, so the client never has to merge two representations of
// the profile it is showing or follow an edit with a re-read to see what was
// stored.
//
// HasPasswordCredential decides which password form the client offers, and is
// read from the database on every call. It is not a hint the service caches or
// trusts: PUT /users/me/password decides what it is asked to do from the same
// database, so a stale value here changes which form is shown and nothing else.
//
// email_verified_at is absent because the column is NOT NULL with no default, so
// it holds a value for every account that exists and carries no information.
// updated_at is absent because it moves only when display_name does, and no
// screen needs to say when a profile was last touched.
//
// The session id is absent too, and this is the deliberate decision worth
// stating: the caller's current session is already in the verified access token's
// sid claim, which middleware.Auth has established is present and non-zero before
// any handler here runs. Publishing it would give a client a second copy of a
// security identifier that it could echo back, where today no endpoint accepts
// one from the client at all. GET /auth/sessions answers "which device am I on"
// with the device name, the platform and an is_current flag, so nothing is lost.
type UserResponse struct {
	ID                    uuid.UUID              `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Email                 string                 `json:"email" example:"user@example.com"`
	DisplayName           string                 `json:"display_name" example:"Jane Doe"`
	HasPasswordCredential bool                   `json:"has_password_credential" example:"true"`
	CreatedAt             time.Time              `json:"created_at" example:"2026-10-01T10:30:00Z"`
	Identities            []UserIdentityResponse `json:"identities"`
}

// UserData carries the profile under the envelope's data key.
//
// A GET and a PATCH both return {"data": {"user": {...}}}. Identities is an
// empty array rather than null when nothing is linked, and always is: a client
// reading identities.length has to be able to do it without first checking
// whether the key arrived, and the same convention the collection and search
// listings already follow.
type UserData struct {
	User UserResponse `json:"user"`
}

// GetUserAPIResponse documents GET /users/me.
//
// It declares two keys and the body carries two. httpx.OK builds
// {"data": ..., "message": ...} with no meta, so a meta field here would
// describe a key that is never sent — the drift AGENTS.md records and that
// saved_item_response_contract_test.go exists to catch.
type GetUserAPIResponse struct {
	Data    *UserData `json:"data"`
	Message string    `json:"message" example:"user retrieved successfully"`
}

// UpdateUserAPIResponse documents PATCH /users/me, and carries the same shape as
// GetUserAPIResponse deliberately: the two responses differ only in their
// message, and one profile representation that both endpoints return is one
// client can rely on.
type UpdateUserAPIResponse struct {
	Data    *UserData `json:"data"`
	Message string    `json:"message" example:"user updated successfully"`
}

// SetPasswordAPIResponse documents PUT /users/me/password.
//
// The body is {"data": null, "message": "..."}, which is what httpx.OKMessage
// writes and what a two-key Data pointer describes.
//
// The request already states the whole intent, so the response adds nothing by
// reporting what was done — the reasoning DELETE /collections/:id gives for the
// same shape. After either branch has_password_credential is unconditionally
// true, so echoing it would carry no information either; a client flips the flag
// locally or re-reads GET /users/me.
type SetPasswordAPIResponse struct {
	Data    *struct{} `json:"data"`
	Message string    `json:"message" example:"password created successfully"`
}
