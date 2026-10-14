package user

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	userdb "github.com/thoriqr/stash-it-backend/internal/api/user/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type Service interface {
	// GetUser reads the authenticated user's profile and linked identities.
	GetUser(ctx context.Context, userID uuid.UUID) (UserProfile, error)

	// UpdateUser renames the authenticated user and returns the profile as
	// written.
	UpdateUser(
		ctx context.Context,
		userID uuid.UUID,
		displayName string,
	) (UserProfile, error)

	// SetPassword creates or changes the authenticated user's password credential,
	// depending on what the database holds.
	SetPassword(ctx context.Context, params SetPasswordParams) (SetPasswordResult, error)
}

type service struct {
	repository     Repository
	passwordHasher PasswordHasher
}

func NewService(
	repository Repository,
	passwordHasher PasswordHasher,
) Service {
	return &service{
		repository:     repository,
		passwordHasher: passwordHasher,
	}
}

// SetPasswordParams is everything the service needs to set a password.
//
// CurrentSessionID is the caller's own session, read from the verified access
// token's sid claim. It is carried alongside UserID rather than looked up from it
// for the reason session.Logout documents: both come from the token the verifier
// already accepted, so neither is chosen by the caller, and the repository's
// predicate is satisfied by construction rather than by an argument the caller
// supplied.
//
// It is used to spare the caller's own session from the revocation, and for
// nothing else.
type SetPasswordParams struct {
	UserID           uuid.UUID
	CurrentSessionID uuid.UUID
	CurrentPassword  string
	NewPassword      string
}

// SetPasswordResult names which of the two things the endpoint did.
//
// It is not on the wire — the response carries no data — but the two branches are
// genuinely different operations, and "password created" and "password changed"
// mean different things to a person and to a client's own audit trail. The
// revocation difference in particular is not visible anywhere in the response, so
// this is where it is at least named.
type SetPasswordResult string

const (
	// SetPasswordCreated means the account held no credential and now holds one.
	// No other session was revoked.
	SetPasswordCreated SetPasswordResult = "created"

	// SetPasswordChanged means a credential existed, was replaced, and every other
	// active session of the account was revoked.
	SetPasswordChanged SetPasswordResult = "changed"
)

func (s *service) GetUser(
	ctx context.Context,
	userID uuid.UUID,
) (UserProfile, error) {
	row, err := s.repository.GetUserProfile(ctx, userID)
	if err != nil {
		return UserProfile{}, err
	}

	profile, err := s.withIdentities(ctx, userID, profileRowFromRead(row))
	if err != nil {
		return UserProfile{}, err
	}

	return profile, nil
}

func (s *service) UpdateUser(
	ctx context.Context,
	userID uuid.UUID,
	displayName string,
) (UserProfile, error) {
	// The row this returns is the row its own UPDATE produced, read inside the
	// statement that wrote it. There is deliberately no commit-then-re-read here:
	// reading afterwards would let a second request landing in between report a
	// display name this one did not set, and the response would then disagree with
	// what the request just wrote.
	//
	// updated_at needs no mention. The users_set_updated_at trigger fires before
	// the RETURNING clause is evaluated, so the returned row already reflects it,
	// and updated_at is not part of the profile response in any case.
	updated, err := s.repository.UpdateUserDisplayName(ctx, userID, displayName)
	if err != nil {
		return UserProfile{}, err
	}

	// Identities are read separately rather than returned by the UPDATE. A rename
	// cannot change them, so there is nothing to hold consistent between the two
	// reads and no reason to keep a transaction open across a statement that needs
	// none.
	profile, err := s.withIdentities(ctx, userID, profileRowFromUpdate(updated))
	if err != nil {
		return UserProfile{}, err
	}

	return profile, nil
}

func (s *service) SetPassword(
	ctx context.Context,
	params SetPasswordParams,
) (SetPasswordResult, error) {
	// The database is asked which case this is. has_password_credential on the
	// profile response is what a client uses to render the form; this read is what
	// decides what is actually required. A client holding a flag fetched an hour
	// ago may be sending the wrong shape, and the answer it gets is the one the
	// rows support rather than the one it was told.
	previousPasswordHash, err := s.repository.GetPasswordCredentialForUser(
		ctx,
		params.UserID,
	)

	if err != nil {
		// The repository reports a missing credential as a conflict carrying
		// CodePasswordCredentialNotFound. That is the create branch rather than a
		// failure: an account with no password has exactly one correct request
		// shape, and this is it. Anything else is returned unchanged, because
		// treating a database fault as "no credential" would turn an outage into a
		// credential being created.
		if !isCredentialNotFound(err) {
			return "", err
		}

		return s.createPasswordCredential(ctx, params)
	}

	return s.changePassword(ctx, params, previousPasswordHash)
}

// createPasswordCredential stores a first password.
//
// No Argon2 verification happens here, and that is what the branch is for: there
// is no stored hash to verify against, so a verification would be against
// nothing. security.PasswordHasher.DummyVerify exists so an unauthenticated login
// cannot answer measurably faster for an address with no account, and this caller
// is already authenticated and already knows it has no credential. Deriving
// anyway would cost a full password's worth of memory to equalize a distinction
// no caller can observe.
//
// No session is revoked either, and the asymmetry with changePassword is a
// decision rather than an oversight. Nothing was compromised: there was no
// credential to compromise. Evicting every other signed-in device of an account
// that has been using Google sign-in on a phone and a laptop would punish its
// owner for adding a password. The branch that evicts is the one where a secret
// was replaced, which is the one an owner might have replaced because they believe
// it was seen.
func (s *service) createPasswordCredential(
	ctx context.Context,
	params SetPasswordParams,
) (SetPasswordResult, error) {
	if params.CurrentPassword != "" {
		return "", apperror.BadRequestWith(
			CodeUnexpectedCurrentPassword,
			"this account has no password yet; send new_password only",
			nil,
		)
	}

	passwordHash, err := s.passwordHasher.Hash(ctx, params.NewPassword)
	if err != nil {
		return "", passwordWorkError(err, s.passwordHasher.Wait())
	}

	// Plain INSERT. The credential's primary key is user_id, so a request that
	// lost a race is refused by the database rather than overwriting the winner —
	// the repository maps that refusal to CodePasswordCredentialAlreadyExists.
	//
	// Nothing earlier in this function is what authorizes the write. The read that
	// chose this branch was not a lock and not a check-then-act, and it happened
	// before the hash, which takes long enough for any number of requests to have
	// created a credential in the meantime. The insert is the authority, and it is
	// the only statement that can be.
	if err := s.repository.CreatePasswordCredential(
		ctx,
		params.UserID,
		passwordHash,
	); err != nil {
		return "", err
	}

	return SetPasswordCreated, nil
}

// changePassword replaces an existing password and evicts the account's other
// sessions.
//
// The order is the whole design. Verify first, so a wrong current password costs
// one derivation and never reaches Hash. Hash second, so a password nobody is
// allowed to change is not derived at all. Write third, in one transaction with
// the revocation and conditioned on the hash that was verified.
func (s *service) changePassword(
	ctx context.Context,
	params SetPasswordParams,
	previousPasswordHash string,
) (SetPasswordResult, error) {
	if params.CurrentPassword == "" {
		return "", apperror.BadRequestWith(
			CodeCurrentPasswordRequired,
			"current_password is required to change an existing password",
			nil,
		)
	}

	verification, err := s.passwordHasher.Verify(
		ctx,
		params.CurrentPassword,
		previousPasswordHash,
	)
	if err != nil {
		// Reached before anything was written and before the new password was
		// derived, so every refusal here leaves the credential and every session
		// exactly as they were.
		return "", passwordWorkError(err, s.passwordHasher.Wait())
	}

	if !verification.Match {
		// 401 rather than 400: the credential is what failed, and it is worth
		// telling a caller apart from a malformed request, because one is worth
		// retrying with a different password and the other is not.
		//
		// The underlying error is deliberately nil. The verification result carries
		// nothing worth reporting and nothing about it may reach the response.
		return "", apperror.UnauthorizedWith(
			CodeCurrentPasswordInvalid,
			"current password is incorrect",
			nil,
		)
	}

	passwordHash, err := s.passwordHasher.Hash(ctx, params.NewPassword)
	if err != nil {
		return "", passwordWorkError(err, s.passwordHasher.Wait())
	}

	// previousPasswordHash goes into the write's predicate. Two requests changing
	// the same password both verify against the same row, and without it the second
	// would overwrite the first invisibly; with it the second matches no row, is
	// refused, and — because the guard sits before the revocation inside the same
	// transaction — never signs the account's other devices out of a change it did
	// not make.
	if err := s.repository.SetPasswordAndRevokeOtherSessions(
		ctx,
		params.UserID,
		params.CurrentSessionID,
		previousPasswordHash,
		passwordHash,
	); err != nil {
		return "", err
	}

	return SetPasswordChanged, nil
}

// profileRow is the part of a users row that both profile queries return.
//
// GetUserProfileRow and UpdateUserDisplayNameRow are separate generated types
// carrying identical fields, because sqlc names a Row per statement. This is the
// feature's own shape over them, so assembling a profile from either query is one
// function rather than two. It follows the same rule as SearchSavedItem: a
// consuming package owns its projection and does not borrow another feature's.
type profileRow struct {
	ID                    uuid.UUID
	Email                 string
	DisplayName           string
	CreatedAt             time.Time
	HasPasswordCredential bool
}

func profileRowFromRead(row userdb.GetUserProfileRow) profileRow {
	return profileRow{
		ID:                    row.ID,
		Email:                 row.Email,
		DisplayName:           row.DisplayName,
		CreatedAt:             row.CreatedAt.Time,
		HasPasswordCredential: row.HasPasswordCredential,
	}
}

func profileRowFromUpdate(row userdb.UpdateUserDisplayNameRow) profileRow {
	return profileRow{
		ID:                    row.ID,
		Email:                 row.Email,
		DisplayName:           row.DisplayName,
		CreatedAt:             row.CreatedAt.Time,
		HasPasswordCredential: row.HasPasswordCredential,
	}
}

// withIdentities attaches the account's linked providers to a profile.
//
// The order is not re-established here. ListAuthIdentitiesForUser orders by
// created_at then id in SQL, because that is where the rows are, and re-sorting
// in Go would be a second definition of the same rule that could disagree with
// the first.
func (s *service) withIdentities(
	ctx context.Context,
	userID uuid.UUID,
	row profileRow,
) (UserProfile, error) {
	identityRows, err := s.repository.ListAuthIdentitiesForUser(ctx, userID)
	if err != nil {
		return UserProfile{}, err
	}

	// Non-nil even at length zero. A nil slice would serialize as null, which is a
	// different answer to "this account has no linked providers" than an empty
	// array is, and the mapper would then have to defend against a client reading
	// a length off a missing key.
	identities := make([]UserIdentity, 0, len(identityRows))

	for _, identity := range identityRows {
		// The snapshots are carried through as pgtype.Text rather than being
		// unwrapped here. Unwrapping is where a NULL and an empty string stop being
		// distinguishable, and the two are both reachable: the columns are nullable
		// and a provider may assert no display name. Decoding to string at this
		// layer would turn every NULL into "", which reads as "the provider said it
		// was blank" rather than "the provider said nothing".
		identities = append(identities, UserIdentity{
			Provider:            identity.Provider,
			LinkedAt:            identity.LinkedAt.Time,
			EmailSnapshot:       identity.EmailSnapshot,
			DisplayNameSnapshot: identity.DisplayNameSnapshot,
		})
	}

	return UserProfile{
		ID:                    row.ID,
		Email:                 row.Email,
		DisplayName:           row.DisplayName,
		HasPasswordCredential: row.HasPasswordCredential,
		CreatedAt:             row.CreatedAt,
		Identities:            identities,
	}, nil
}

// isCredentialNotFound reports whether the repository's answer was "this account
// holds no credential".
//
// It matches on the code rather than on the error type, because the repository
// deliberately reports this as a conflict and not a not-found: the account exists
// and the caller is looking at it, so nothing is missing from the resource the
// request named. A 404 would describe it as though it were.
func isCredentialNotFound(err error) bool {
	return apperror.FromError(err).Code == CodePasswordCredentialNotFound
}

// passwordWorkError maps a refusal to do password work at all onto the response
// the caller receives.
//
// It is the same mapping login, registration and password reset perform, with the
// same code and the same message, deliberately: it is one condition with one
// recovery for every caller that performs password work, so a client learns the
// rule once. This package declares its own constant rather than importing another
// feature's, because a change to one must not be able to change another silently.
//
// Only ErrPasswordWorkCapacityTimeout becomes this. A cancelled context asks the
// caller to stop rather than to come back, and a stored hash this build cannot
// parse is a fault with nothing the caller can act on. Folding them together would
// report a request nobody is waiting for as a merely busy server.
//
// The wait comes from the hasher rather than from a constant, because it is the
// deployment's configured value and the Retry-After has to be the number the
// caller was actually kept waiting for.
func passwordWorkError(err error, wait time.Duration) error {
	if !errors.Is(err, security.ErrPasswordWorkCapacityTimeout) {
		return apperror.Internal(err)
	}

	return apperror.ServiceUnavailableWithRetryAfter(
		CodePasswordWorkUnavailable,
		"the server is busy, please try again shortly",
		wait,
		err,
	)
}
