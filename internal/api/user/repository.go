package user

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	userdb "github.com/thoriqr/stash-it-backend/internal/api/user/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type Repository interface {
	// GetUserProfile reads one user's profile and whether a password credential
	// exists for them.
	//
	// No row is CodeUserNotFound. For every caller in this feature the id is the
	// authenticated subject, so it can only be reached by a token that outlived
	// its account; it is still reported rather than folded into an empty profile,
	// because a caller holding a valid token for a deleted user has no profile to
	// be given and inventing one would be worse than saying so.
	GetUserProfile(ctx context.Context, userID uuid.UUID) (userdb.GetUserProfileRow, error)

	// ListAuthIdentitiesForUser lists the providers linked to one user, oldest
	// first.
	//
	// An empty slice is an ordinary result and is never an error: most accounts
	// hold one identity and some hold none.
	ListAuthIdentitiesForUser(ctx context.Context, userID uuid.UUID) ([]userdb.ListAuthIdentitiesForUserRow, error)

	// UpdateUserDisplayName renames one user and returns the row as written.
	UpdateUserDisplayName(ctx context.Context, userID uuid.UUID, displayName string) (userdb.UpdateUserDisplayNameRow, error)

	// GetPasswordCredentialForUser reads the stored hash for one user's
	// credential.
	//
	// No row is CodePasswordCredentialNotFound, not a not-found: the account
	// exists, the thing the request assumed was there is not, and the response
	// has to say which.
	GetPasswordCredentialForUser(ctx context.Context, userID uuid.UUID) (string, error)

	// CreatePasswordCredential creates one user's credential.
	//
	// It refuses rather than overwriting. A caller that loses to a concurrent
	// creator receives CodePasswordCredentialAlreadyExists, and the credential
	// that won is left exactly as its creator wrote it.
	CreatePasswordCredential(ctx context.Context, userID uuid.UUID, passwordHash string) error

	// SetPasswordAndRevokeOtherSessions replaces a password and revokes every
	// other active session of that user, as one transaction.
	//
	// The new hash is only written while the stored one still equals
	// previousPasswordHash — the value the caller verified a submitted password
	// against. If it does not, nothing at all is written and not one session is
	// revoked; the whole operation is CodePasswordAlreadyChanged.
	//
	// Both the write and the revocation are in one transaction on purpose. A
	// password that changed without the revocation would leave the other devices
	// of an account signed in to an account whose password had just been replaced,
	// and a revocation that committed without the write would sign them out over a
	// change that never happened. Neither is a state worth being able to observe,
	// so neither is a state the code can produce.
	SetPasswordAndRevokeOtherSessions(
		ctx context.Context,
		userID uuid.UUID,
		currentSessionID uuid.UUID,
		previousPasswordHash string,
		passwordHash string,
	) error
}

type repository struct {
	queries *userdb.Queries
	pool    *pgxpool.Pool
}

// NewRepository wires the queries and the pool.
//
// The pool is held because this feature owns a transaction. AGENTS.md puts
// transaction ownership in the repository and nowhere else: there is no shared
// unit of work in this project, so a service that needs two statements to commit
// together has to ask one repository for both, and SetPasswordAndRevokeOtherSessions
// is the reason the pool is here rather than only the queries.
func NewRepository(
	queries *userdb.Queries,
	pool *pgxpool.Pool,
) Repository {
	return &repository{
		queries: queries,
		pool:    pool,
	}
}

func (r *repository) GetUserProfile(
	ctx context.Context,
	userID uuid.UUID,
) (userdb.GetUserProfileRow, error) {
	profile, err := r.queries.GetUserProfile(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return userdb.GetUserProfileRow{}, apperror.NotFoundWith(
				CodeUserNotFound,
				"user not found",
				err,
			)
		}

		return userdb.GetUserProfileRow{}, apperror.Internal(err)
	}

	return profile, nil
}

func (r *repository) ListAuthIdentitiesForUser(
	ctx context.Context,
	userID uuid.UUID,
) ([]userdb.ListAuthIdentitiesForUserRow, error) {
	identities, err := r.queries.ListAuthIdentitiesForUser(ctx, userID)
	if err != nil {
		return nil, apperror.Internal(err)
	}

	return identities, nil
}

func (r *repository) UpdateUserDisplayName(
	ctx context.Context,
	userID uuid.UUID,
	displayName string,
) (userdb.UpdateUserDisplayNameRow, error) {
	profile, err := r.queries.UpdateUserDisplayName(
		ctx,
		userdb.UpdateUserDisplayNameParams{
			DisplayName: displayName,
			UserID:      userID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return userdb.UpdateUserDisplayNameRow{}, apperror.NotFoundWith(
				CodeUserNotFound,
				"user not found",
				err,
			)
		}

		return userdb.UpdateUserDisplayNameRow{}, apperror.Internal(err)
	}

	return profile, nil
}

func (r *repository) GetPasswordCredentialForUser(
	ctx context.Context,
	userID uuid.UUID,
) (string, error) {
	passwordHash, err := r.queries.GetPasswordCredentialForUser(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperror.ConflictWith(
				CodePasswordCredentialNotFound,
				"this account has no password yet",
				err,
			)
		}

		return "", apperror.Internal(err)
	}

	return passwordHash, nil
}

func (r *repository) CreatePasswordCredential(
	ctx context.Context,
	userID uuid.UUID,
	passwordHash string,
) error {
	_, err := r.queries.CreatePasswordCredential(
		ctx,
		userdb.CreatePasswordCredentialParams{
			UserID:       userID,
			PasswordHash: passwordHash,
		},
	)
	if err != nil {
		var pgErr *pgconn.PgError

		if errors.As(err, &pgErr) &&
			pgErr.ConstraintName == passwordCredentialPrimaryKey {
			// The one conflict this statement can have. Someone else created this
			// account's credential between the read that decided there was none and
			// this insert, and their hash is what stays: this request wrote nothing,
			// because an INSERT cannot overwrite.
			return apperror.ConflictWith(
				CodePasswordCredentialAlreadyExists,
				"a password has already been set for this account",
				err,
			)
		}

		return apperror.Internal(err)
	}

	return nil
}

func (r *repository) SetPasswordAndRevokeOtherSessions(
	ctx context.Context,
	userID uuid.UUID,
	currentSessionID uuid.UUID,
	previousPasswordHash string,
	passwordHash string,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return apperror.Internal(err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	// Lock order is credential row first, then sessions. Password reset takes the
	// same relative order, and the refresh path locks refresh_tokens and sessions
	// without ever touching password_credentials, so no cycle exists between any
	// two of them.
	rowsAffected, err := qtx.UpdatePasswordCredentialIfUnchanged(
		ctx,
		userdb.UpdatePasswordCredentialIfUnchangedParams{
			PasswordHash:         passwordHash,
			UserID:               userID,
			PreviousPasswordHash: previousPasswordHash,
		},
	)
	if err != nil {
		return apperror.Internal(err)
	}

	// The guard is the whole of the concurrency rule for a password change, and it
	// sits before the revocation on purpose. A request whose credential was
	// replaced by a concurrent one must not sign that request's other devices out
	// of an account whose password it did not actually change.
	if rowsAffected != 1 {
		return apperror.ConflictWith(
			CodePasswordAlreadyChanged,
			"the password was changed by another request; try again",
			nil,
		)
	}

	// Zero rows here is a success. It means the caller was the only signed-in
	// session, and a request that revokes nothing has still asked for exactly the
	// right thing — the same reasoning session.RevokeSession is built on.
	if _, err := qtx.RevokeOtherSessionsForUser(
		ctx,
		userdb.RevokeOtherSessionsForUserParams{
			UserID:            userID,
			ExcludedSessionID: currentSessionID,
		},
	); err != nil {
		return apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return apperror.Internal(err)
	}

	return nil
}
