package user

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/thoriqr/stash-it-backend/internal/security"
)

// UserProfile is the current user's profile, as this feature projects it.
//
// It is deliberately narrower than the users row. email_verified_at is absent
// because it is NOT NULL with no default — every account has a value, so
// reporting it would present a constant as information. updated_at is absent
// because nothing here orders by it and no response needs to say when a profile
// was last touched.
//
// HasPasswordCredential is the one value that changes what a client does next:
// it decides whether the password form asks for a current password. It is read
// from the database on every call rather than remembered, and the password
// endpoint decides its own behaviour from the same database rather than trusting
// whatever this field said when it was fetched.
type UserProfile struct {
	ID                    uuid.UUID
	Email                 string
	DisplayName           string
	HasPasswordCredential bool
	CreatedAt             time.Time
	Identities            []UserIdentity
}

// UserIdentity is one authentication provider linked to the account.
//
// Provider is the stored value, verbatim. The column is VARCHAR(32) with no
// check constraint and no registry, so this feature never interprets it; a client
// renders whatever arrives and treats a provider it has never seen as a provider
// rather than as an error.
//
// LinkedAt is when the link was made.
//
// EmailSnapshot and DisplayNameSnapshot are what the provider asserted about the
// account at the moment the link was made. They are historical metadata and are
// reported as stored, which means they can disagree with the account's own email
// and display name and are expected to go stale: nothing refreshes them, and
// nothing here tries. Deciding what to do about a stale snapshot is the client's
// call, not a reason for this endpoint to contact the provider.
//
// They are pgtype.Text rather than string so that NULL stays distinguishable from
// the empty string. Both are reachable — the columns are nullable and a provider
// may legitimately assert no display name — and the mapper turns a NULL into a nil
// pointer, which is what keeps them JSON null rather than "".
//
// provider_subject is the credential the identity is bound to, and the row's id is
// an internal identifier no endpoint here addresses. Neither is carried at all —
// not to be ignored by a client, but so neither can be published by a later
// change to this projection.
type UserIdentity struct {
	Provider            string
	LinkedAt            time.Time
	EmailSnapshot       pgtype.Text
	DisplayNameSnapshot pgtype.Text
}

// PasswordHasher is the password work this feature performs, as a consuming
// package names it.
//
// It is declared here for the same reason login.PasswordVerifier is: this package
// is the consumer, and a consuming package names its own dependencies.
// *security.PasswordHasher satisfies it unchanged and the production wiring is
// unaffected.
//
// The method set is exactly what SetPassword needs — Verify, Hash and Wait — and
// nothing else. DummyVerify is deliberately absent: it exists so that an
// unauthenticated login against an unknown address pays the same Argon2id cost
// as one against a known address, and there is no unknown caller here. The
// request is authenticated, the account is the caller's own, and whether it holds
// a credential is already in that caller's hands as HasPasswordCredential.
// Spending a derivation to equalize a distinction that cannot be observed would
// be cost spent on nothing.
//
// Having Verify on an interface rather than behind a concrete type is what makes
// one claim testable without a clock: that a failed current-password check costs
// exactly one derivation and never reaches Hash. login/dummy_verification_seam_test.go
// is the precedent for asserting a call rather than a duration.
type PasswordHasher interface {
	// Verify checks a submitted password against a stored hash.
	Verify(
		ctx context.Context,
		password string,
		encodedHash string,
	) (security.PasswordVerificationResult, error)

	// Hash derives a new hash for a submitted password.
	Hash(ctx context.Context, password string) (string, error)

	// Wait reports how long a caller waits for password-work capacity before being
	// refused. It is carried through to the Retry-After the caller is given, and it
	// comes from the limiter rather than from a constant here because the header
	// has to be the number the caller was actually kept waiting for.
	Wait() time.Duration
}
