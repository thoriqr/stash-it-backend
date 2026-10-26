package registration

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type Repository interface {
	CreateManualRegistration(
		ctx context.Context,
		params CreateManualRegistrationParams,
	) (CreateManualRegistrationResult, error)

	CreateSocialRegistration(
		ctx context.Context,
		params CreateSocialRegistrationParams,
	) (CreateSocialRegistrationResult, error)

	GetVerification(
		ctx context.Context,
		id uuid.UUID,
	) (registrationdb.GetVerificationRow, error)

	IssueVerificationCode(
		ctx context.Context,
		params IssueVerificationCodeParams,
	) (registrationdb.VerificationRequest, error)

	GetActiveVerificationCode(
		ctx context.Context,
		verificationRequestID uuid.UUID,
		maxAttempts int32,
	) (registrationdb.VerificationCode, error)

	IncrementVerificationCodeAttempts(
		ctx context.Context,
		id uuid.UUID,
		maxAttempts int32,
	) (int32, error)

	CompleteVerification(
		ctx context.Context,
		params CompleteVerificationParams,
	) (registrationdb.RegistrationContinuation, error)

	GetCompletedRegistrationByEmail(
		ctx context.Context,
		email string,
	) (uuid.UUID, error)

	GetRegistrationContinuation(
		ctx context.Context,
		tokenHash string,
	) (registrationdb.GetRegistrationContinuationRow, error)

	FinalizeManualRegistration(
		ctx context.Context,
		params FinalizeManualRegistrationParams,
	) (registrationdb.CreateUserRow, error)

	FinalizeSocialRegistration(
		ctx context.Context,
		params FinalizeSocialRegistrationParams,
	) (registrationdb.CreateUserRow, error)
}

type repository struct {
	pool    *pgxpool.Pool
	queries *registrationdb.Queries
}

func NewRepository(
	pool *pgxpool.Pool,
	queries *registrationdb.Queries,
) Repository {
	return &repository{
		pool:    pool,
		queries: queries,
	}
}

func (r *repository) GetCompletedRegistrationByEmail(
	ctx context.Context,
	email string,
) (uuid.UUID, error) {
	id, err := r.queries.GetCompletedRegistrationByEmail(
		ctx,
		email,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, nil
		}

		return uuid.Nil, apperror.Internal(err)
	}

	return id, nil
}

type CreateManualRegistrationResult struct {
	VerificationRequest registrationdb.VerificationRequest
	AlreadyPending      bool
}

type CreateManualRegistrationParams struct {
	Email                 string
	RegistrationExpiresAt pgtype.Timestamptz
}

// pendingRegistrationEmailIndex is the partial unique index that guarantees at
// most one pending or completed registration per address. It is the authority on
// that invariant; everything this repository does before the insert is an
// optimization that avoids hitting it.
const pendingRegistrationEmailIndex = "pending_registrations_email_idx"

// CreateManualRegistration opens a registration for an address, or reports the one
// that is already open.
//
// Two requests for an address with no registration yet cannot both be decided by
// the lookup below, and they must not be: GetPendingRegistrationByEmailForUpdate
// locks a row, and there is no row yet. Both proceed to the insert and one of them
// loses on pending_registrations_email_idx. That loser is not an error the caller
// should have to distinguish from the sequential case, so it simply runs again: by
// then the winner has committed, the lookup finds its row, and the ordinary
// already-pending path produces the same answer the second sequential request
// would have got.
//
// The retry is a single re-run rather than a loop, and it only happens on that one
// constraint. A second loss, or any other failure, is reported as it is.
func (r *repository) CreateManualRegistration(
	ctx context.Context,
	params CreateManualRegistrationParams,
) (CreateManualRegistrationResult, error) {
	result, err := r.createManualRegistrationOnce(ctx, params)
	if !isPendingRegistrationEmailConflict(err) {
		return result, err
	}

	return r.createManualRegistrationOnce(ctx, params)
}

// isPendingRegistrationEmailConflict reports whether err is the unique violation
// raised by the one-registration-per-address index.
//
// The error has already been mapped, but mapping keeps the driver error as the
// cause, so the constraint can still be read off it.
func isPendingRegistrationEmailConflict(err error) bool {
	var pgErr *pgconn.PgError

	if !errors.As(err, &pgErr) {
		return false
	}

	return pgErr.ConstraintName == pendingRegistrationEmailIndex
}

func (r *repository) createManualRegistrationOnce(
	ctx context.Context,
	params CreateManualRegistrationParams,
) (CreateManualRegistrationResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return CreateManualRegistrationResult{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	existing, err := qtx.GetPendingRegistrationByEmailForUpdate(
		ctx,
		params.Email,
	)
	if err == nil {
		if existing.RegistrationType == string(RegistrationTypeSocial) {
			rowsAffected, err := qtx.ForceExpireSocialPendingRegistration(
				ctx,
				existing.ID,
			)
			if err != nil {
				return CreateManualRegistrationResult{}, apperror.Internal(err)
			}

			if rowsAffected != 1 {
				return CreateManualRegistrationResult{}, apperror.ConflictWith(
					"",
					"registration is no longer pending",
					nil,
				)
			}
		} else if existing.IsExpired {
			rowsAffected, err := qtx.ExpirePendingRegistration(
				ctx,
				existing.ID,
			)
			if err != nil {
				return CreateManualRegistrationResult{}, apperror.Internal(err)
			}

			if rowsAffected != 1 {
				return CreateManualRegistrationResult{}, apperror.ConflictWith(
					"",
					"registration is no longer pending",
					nil,
				)
			}
		} else if existing.VerificationStatus == string(VerificationRequestPending) {
			// The status travels with the id so the caller can tell whether a
			// PIN is still required. The raw continuation token is never
			// returned by this path.
			return CreateManualRegistrationResult{
				VerificationRequest: registrationdb.VerificationRequest{
					ID:     existing.VerificationID,
					Status: existing.VerificationStatus,
				},
				AlreadyPending: true,
			}, nil
		} else {
			// The verification request is terminal, so the verification id alone
			// cannot advance this registration any more. Whether it may be
			// replaced depends on the continuation, which is the only remaining
			// capability and is stored hashed rather than handed back.
			hasUsableContinuation, err := qtx.HasUsableRegistrationContinuation(
				ctx,
				existing.ID,
			)
			if err != nil {
				return CreateManualRegistrationResult{}, apperror.Internal(err)
			}

			// A live continuation is the only way this registration can still be
			// finished. Replacing it here would let anyone who can post an
			// address cancel it — this route is unauthenticated — so the
			// registration is retained and the caller keeps the verification id
			// it was opened with. The verified status is what tells the client
			// to use its stored continuation instead of requesting a PIN.
			if hasUsableContinuation {
				return CreateManualRegistrationResult{
					VerificationRequest: registrationdb.VerificationRequest{
						ID:     existing.VerificationID,
						Status: existing.VerificationStatus,
					},
					AlreadyPending: true,
				}, nil
			}

			// The verification is terminal and its continuation can no longer be
			// used, so the registration holds no capability. Expiring it frees
			// the address: the expired row leaves the partial unique index, and
			// the replacement below can take it.
			rowsAffected, err := qtx.ForceExpireManualPendingRegistration(
				ctx,
				existing.ID,
			)
			if err != nil {
				return CreateManualRegistrationResult{}, apperror.Internal(err)
			}

			if rowsAffected != 1 {
				return CreateManualRegistrationResult{}, apperror.ConflictWith(
					"",
					"registration is no longer pending",
					nil,
				)
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return CreateManualRegistrationResult{}, apperror.Internal(err)
	}

	pendingRegistration, err := qtx.CreatePendingRegistration(
		ctx,
		registrationdb.CreatePendingRegistrationParams{
			Email:            params.Email,
			RegistrationType: string(RegistrationTypeManual),
			Status:           string(PendingRegistrationPending),
			ExpiresAt:        params.RegistrationExpiresAt,
		},
	)
	if err != nil {
		return CreateManualRegistrationResult{}, mapRegistrationDBError(err)
	}

	verificationRequest, err := qtx.CreateVerificationRequest(
		ctx,
		registrationdb.CreateVerificationRequestParams{
			SubjectType: string(VerificationSubjectPendingRegistration),
			SubjectID:   pendingRegistration.ID,
			Purpose:     string(VerificationPurposeRegistration),
			Status:      string(VerificationRequestPending),
		},
	)
	if err != nil {
		return CreateManualRegistrationResult{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return CreateManualRegistrationResult{}, apperror.Internal(err)
	}

	return CreateManualRegistrationResult{
		VerificationRequest: verificationRequest,
	}, nil
}

type CreateSocialRegistrationResult struct {
	VerificationRequest registrationdb.VerificationRequest
}

type CreateSocialRegistrationParams struct {
	Email                 string
	Provider              string
	ProviderSubject       string
	EmailSnapshot         pgtype.Text
	DisplayNameSnapshot   pgtype.Text
	RegistrationExpiresAt pgtype.Timestamptz
}

// CreateSocialRegistration opens a social registration for an address, or reports
// the one that is already open under the same identity.
//
// It carries the same single re-run as CreateManualRegistration, and for the same
// structural reason. Two requests for an address with no registration yet cannot
// both be decided by the lookup below, and they must not be:
// GetPendingRegistrationByEmailForUpdate locks a row, and there is no row yet.
// Both proceed to the insert and one of them loses on pending_registrations_email_idx.
//
// That loser is not an error the caller should have to distinguish from the
// sequential case. A client retrying after a lost response, or a sign-in button
// pressed twice, is ordinary traffic, and answering it 409
// REGISTRATION_ALREADY_EXISTS with no verification id leaves a registration that
// exists and that the caller cannot reach.
//
// The re-run is safe here for the same reason it is in the manual flow: it is not a
// retry of the insert. It re-reads through GetPendingRegistrationByEmailForUpdate,
// so it decides from the winner's committed row rather than from anything it
// assumed. Both requests carry the same verified identity, so the lookup finds the
// winner's social registration, its pending social identity matches, and the
// ordinary reuse path returns the verification id the winner created. Nothing is
// swallowed: the re-run is gated on that one constraint and happens at most once,
// and a second loss or any other failure is reported as it is.
func (r *repository) CreateSocialRegistration(
	ctx context.Context,
	params CreateSocialRegistrationParams,
) (CreateSocialRegistrationResult, error) {
	result, err := r.createSocialRegistrationOnce(ctx, params)
	if !isPendingRegistrationEmailConflict(err) {
		return result, err
	}

	return r.createSocialRegistrationOnce(ctx, params)
}

func (r *repository) createSocialRegistrationOnce(
	ctx context.Context,
	params CreateSocialRegistrationParams,
) (CreateSocialRegistrationResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return CreateSocialRegistrationResult{}, apperror.Internal(err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	existing, err := qtx.GetPendingRegistrationByEmailForUpdate(
		ctx,
		params.Email,
	)

	if err == nil {
		if existing.RegistrationType == string(RegistrationTypeManual) {
			rowsAffected, err := qtx.ForceExpireManualPendingRegistration(
				ctx,
				existing.ID,
			)
			if err != nil {
				return CreateSocialRegistrationResult{}, apperror.Internal(err)
			}

			if rowsAffected != 1 {
				return CreateSocialRegistrationResult{}, apperror.ConflictWith(
					"",
					"registration is no longer pending",
					nil,
				)
			}
		} else if existing.RegistrationType == string(RegistrationTypeSocial) {
			pendingSocialIdentity, err := qtx.GetPendingSocialIdentity(
				ctx,
				existing.ID,
			)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return CreateSocialRegistrationResult{}, apperror.Internal(
						errors.New("social pending registration has no pending social identity"),
					)
				}

				return CreateSocialRegistrationResult{}, apperror.Internal(err)
			}

			identityMatches :=
				pendingSocialIdentity.Provider == params.Provider &&
					pendingSocialIdentity.ProviderSubject == params.ProviderSubject

			// A registration that can still be finalized belongs to the identity
			// that opened it, and a different identity presenting the same address
			// does not get to take it over. The capability to finalize lives in the
			// mailbox rather than in this row, so the caller could already complete
			// the stored registration if it chose to; what expire-and-replace would
			// add is the ability to cancel it, which is a denial primitive against
			// whoever really owns the address.
			//
			// Refusing keeps both halves honest: the stored identity is never
			// rewritten, and the caller is told the address is held by another
			// identity rather than being handed a registration that is not theirs.
			//
			// The message names no stored value. Echoing the provider or subject
			// back would disclose another identity's internals to a caller who has
			// only proven it holds a matching address.
			if !identityMatches && !existing.IsExpired {
				return CreateSocialRegistrationResult{}, apperror.ConflictWith(
					CodeRegistrationIdentityMismatch,
					"a registration for this email is already in progress under a different account",
					nil,
				)
			}

			if !existing.IsExpired &&
				existing.VerificationStatus == string(VerificationRequestPending) {
				return CreateSocialRegistrationResult{
					VerificationRequest: registrationdb.VerificationRequest{
						ID: existing.VerificationID,
					},
				}, nil
			}

			// Past its deadline the registration can no longer be finalized:
			// validateVerificationPending, validateRegistrationContinuation and
			// CompletePendingRegistration all refuse it on expires_at. It holds no
			// capability, so expiring it grants the incoming identity nothing the
			// passage of time had not already granted. The original row is left
			// expired and its pending social identity is left attached to it and
			// never reused, so this is a replacement and not a transfer.
			if existing.IsExpired {
				rowsAffected, err := qtx.ExpirePendingRegistration(
					ctx,
					existing.ID,
				)
				if err != nil {
					return CreateSocialRegistrationResult{}, apperror.Internal(err)
				}

				if rowsAffected != 1 {
					return CreateSocialRegistrationResult{}, apperror.ConflictWith(
						"",
						"registration is no longer pending",
						nil,
					)
				}
			} else {
				// The registration is still inside its deadline but its
				// verification request is terminal, so the verification id alone
				// cannot advance it any more. The identity-mismatch refusal above
				// has already run, so the caller here is the same verified
				// provider identity that opened the registration; replacing it
				// is that identity choosing to restart, not another caller
				// cancelling it. Force the expiry because the row has not reached
				// its deadline.
				rowsAffected, err := qtx.ForceExpireSocialPendingRegistration(
					ctx,
					existing.ID,
				)
				if err != nil {
					return CreateSocialRegistrationResult{}, apperror.Internal(err)
				}

				if rowsAffected != 1 {
					return CreateSocialRegistrationResult{}, apperror.ConflictWith(
						"",
						"registration is no longer pending",
						nil,
					)
				}
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return CreateSocialRegistrationResult{}, apperror.Internal(err)
	}

	// create new social pending registration
	pendingRegistration, err := qtx.CreatePendingRegistration(
		ctx,
		registrationdb.CreatePendingRegistrationParams{
			Email:            params.Email,
			RegistrationType: string(RegistrationTypeSocial),
			Status:           string(PendingRegistrationPending),
			ExpiresAt:        params.RegistrationExpiresAt,
		},
	)
	if err != nil {
		return CreateSocialRegistrationResult{}, mapRegistrationDBError(err)
	}

	// create pending social identity
	_, err = qtx.CreatePendingSocialIdentity(
		ctx,
		registrationdb.CreatePendingSocialIdentityParams{
			PendingRegistrationID: pendingRegistration.ID,
			Provider:              params.Provider,
			ProviderSubject:       params.ProviderSubject,
			EmailSnapshot:         params.EmailSnapshot,
			DisplayNameSnapshot:   params.DisplayNameSnapshot,
		},
	)
	if err != nil {
		return CreateSocialRegistrationResult{}, mapRegistrationDBError(err)
	}

	// create verification request
	verificationRequest, err := qtx.CreateVerificationRequest(
		ctx,
		registrationdb.CreateVerificationRequestParams{
			SubjectType: string(VerificationSubjectPendingRegistration),
			SubjectID:   pendingRegistration.ID,
			Purpose:     string(VerificationPurposeRegistration),
			Status:      string(VerificationRequestPending),
		},
	)
	if err != nil {
		return CreateSocialRegistrationResult{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return CreateSocialRegistrationResult{}, apperror.Internal(err)
	}

	return CreateSocialRegistrationResult{
		VerificationRequest: verificationRequest,
	}, nil
}

type IssueVerificationCodeParams struct {
	VerificationID uuid.UUID
	CodeHash       string
	CodeExpiresAt  pgtype.Timestamptz
}

func (r *repository) IssueVerificationCode(
	ctx context.Context,
	params IssueVerificationCodeParams,
) (registrationdb.VerificationRequest, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return registrationdb.VerificationRequest{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	if err := qtx.InvalidateVerificationCode(
		ctx,
		params.VerificationID,
	); err != nil {
		return registrationdb.VerificationRequest{}, apperror.Internal(err)
	}

	_, err = qtx.CreateVerificationCode(
		ctx,
		registrationdb.CreateVerificationCodeParams{
			VerificationRequestID: params.VerificationID,
			CodeHash:              params.CodeHash,
			ExpiresAt:             params.CodeExpiresAt,
		},
	)
	if err != nil {
		return registrationdb.VerificationRequest{}, mapRegistrationDBError(err)
	}

	verificationRequest, err := qtx.UpdateVerificationRequestPINIssued(
		ctx,
		params.VerificationID,
	)
	if err != nil {
		return registrationdb.VerificationRequest{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return registrationdb.VerificationRequest{}, apperror.Internal(err)
	}

	return verificationRequest, nil
}

func (r *repository) GetVerification(
	ctx context.Context,
	id uuid.UUID,
) (registrationdb.GetVerificationRow, error) {
	verification, err := r.queries.GetVerification(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return registrationdb.GetVerificationRow{}, apperror.NotFound(err)
		}

		return registrationdb.GetVerificationRow{}, apperror.Internal(err)
	}

	return verification, nil
}

func (r *repository) GetActiveVerificationCode(
	ctx context.Context,
	verificationRequestID uuid.UUID,
	maxAttempts int32,
) (registrationdb.VerificationCode, error) {
	code, err := r.queries.GetActiveVerificationCode(
		ctx,
		registrationdb.GetActiveVerificationCodeParams{
			VerificationRequestID: verificationRequestID,
			MaxAttempts:           maxAttempts,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return registrationdb.VerificationCode{}, apperror.ConflictWith(
				"",
				"verification code is no longer available",
				err,
			)
		}

		return registrationdb.VerificationCode{}, apperror.Internal(err)
	}

	return code, nil
}

func (r *repository) IncrementVerificationCodeAttempts(
	ctx context.Context,
	id uuid.UUID,
	maxAttempts int32,
) (int32, error) {
	attempts, err := r.queries.IncrementVerificationCodeAttempts(
		ctx,
		registrationdb.IncrementVerificationCodeAttemptsParams{
			ID:          id,
			MaxAttempts: maxAttempts,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, apperror.ConflictWith(
				"",
				"verification code is no longer available",
				err,
			)
		}

		return 0, apperror.Internal(err)
	}

	return attempts, nil
}

type CompleteVerificationParams struct {
	VerificationCodeID    uuid.UUID
	VerificationRequestID uuid.UUID
	PendingRegistrationID uuid.UUID
	TokenHash             string
	ExpiresAt             pgtype.Timestamptz
}

func (r *repository) CompleteVerification(
	ctx context.Context,
	params CompleteVerificationParams,
) (registrationdb.RegistrationContinuation, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return registrationdb.RegistrationContinuation{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	rowsAffected, err := qtx.ConsumeVerificationCode(
		ctx,
		params.VerificationCodeID,
	)
	if err != nil {
		return registrationdb.RegistrationContinuation{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return registrationdb.RegistrationContinuation{}, apperror.ConflictWith(
			"",
			"verification code is no longer available",
			nil,
		)
	}

	rowsAffected, err = qtx.MarkVerificationRequestVerified(
		ctx,
		params.VerificationRequestID,
	)
	if err != nil {
		return registrationdb.RegistrationContinuation{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return registrationdb.RegistrationContinuation{}, apperror.ConflictWith(
			"",
			"verification request is no longer pending",
			nil,
		)
	}

	continuation, err := qtx.CreateRegistrationContinuation(
		ctx,
		registrationdb.CreateRegistrationContinuationParams{
			PendingRegistrationID: params.PendingRegistrationID,
			TokenHash:             params.TokenHash,
			ExpiresAt:             params.ExpiresAt,
		},
	)
	if err != nil {
		return registrationdb.RegistrationContinuation{}, mapRegistrationDBError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return registrationdb.RegistrationContinuation{}, apperror.Internal(err)
	}

	return continuation, nil
}

func (r *repository) GetRegistrationContinuation(
	ctx context.Context,
	tokenHash string,
) (registrationdb.GetRegistrationContinuationRow, error) {
	continuation, err := r.queries.GetRegistrationContinuation(
		ctx,
		tokenHash,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return registrationdb.GetRegistrationContinuationRow{}, apperror.ConflictWith(
				"",
				"registration continuation is no longer available",
				err,
			)
		}

		return registrationdb.GetRegistrationContinuationRow{}, apperror.Internal(err)
	}

	return continuation, nil
}

type FinalizeManualRegistrationParams struct {
	PendingRegistrationID uuid.UUID
	ContinuationID        uuid.UUID
	Email                 string
	DisplayName           string
	EmailVerifiedAt       pgtype.Timestamptz
	PasswordHash          string
}

func (r *repository) FinalizeManualRegistration(
	ctx context.Context,
	params FinalizeManualRegistrationParams,
) (registrationdb.CreateUserRow, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	user, err := qtx.CreateUser(
		ctx,
		registrationdb.CreateUserParams{
			Email:           params.Email,
			DisplayName:     params.DisplayName,
			EmailVerifiedAt: params.EmailVerifiedAt,
		},
	)
	if err != nil {
		return registrationdb.CreateUserRow{}, mapRegistrationDBError(err)
	}

	_, err = qtx.CreateUnsortedCollection(ctx, user.ID)
	if err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	_, err = qtx.CreatePasswordCredential(
		ctx,
		registrationdb.CreatePasswordCredentialParams{
			UserID:       user.ID,
			PasswordHash: params.PasswordHash,
		},
	)
	if err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	rowsAffected, err := qtx.ConsumeRegistrationContinuation(
		ctx,
		params.ContinuationID,
	)
	if err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return registrationdb.CreateUserRow{}, apperror.ConflictWith(
			"",
			"registration continuation is no longer available",
			nil,
		)
	}

	rowsAffected, err = qtx.CompletePendingRegistration(
		ctx,
		params.PendingRegistrationID,
	)
	if err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return registrationdb.CreateUserRow{}, apperror.ConflictWith(
			"",
			"registration is no longer pending",
			nil,
		)
	}

	if err := tx.Commit(ctx); err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	return user, nil
}

// FinalizeSocialRegistrationParams carries the values that decide the account.
//
// Email is the authoritative address, read from pending_registrations by the
// service. It is a parameter rather than something read here on purpose: this
// repository already holds the pending social identity, and taking the address
// from that row instead would silently promote provider metadata to the account's
// identity. The two are not the same thing, and only one of them is normalized.
type FinalizeSocialRegistrationParams struct {
	PendingRegistrationID uuid.UUID
	ContinuationID        uuid.UUID
	Email                 string
	DisplayName           string
	EmailVerifiedAt       pgtype.Timestamptz
}

func (r *repository) FinalizeSocialRegistration(
	ctx context.Context,
	params FinalizeSocialRegistrationParams,
) (registrationdb.CreateUserRow, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	pendingSocialIdentity, err := qtx.GetPendingSocialIdentity(
		ctx,
		params.PendingRegistrationID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return registrationdb.CreateUserRow{}, apperror.Internal(
				errors.New("social pending registration has no pending social identity"),
			)
		}

		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	user, err := qtx.CreateUser(
		ctx,
		registrationdb.CreateUserParams{
			// The authoritative address, not the provider's copy of it.
			// pending_registrations.email is the normalized value every read path
			// compares against, so a differently-cased snapshot would otherwise
			// create an account that GetUserForLogin and GetUserByEmail can never
			// find, since both compare with exact equality on TEXT.
			Email:           params.Email,
			DisplayName:     params.DisplayName,
			EmailVerifiedAt: params.EmailVerifiedAt,
		},
	)
	if err != nil {
		return registrationdb.CreateUserRow{}, mapRegistrationDBError(err)
	}

	_, err = qtx.CreateUnsortedCollection(ctx, user.ID)
	if err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	_, err = qtx.CreateAuthIdentity(
		ctx,
		registrationdb.CreateAuthIdentityParams{
			UserID:              user.ID,
			Provider:            pendingSocialIdentity.Provider,
			ProviderSubject:     pendingSocialIdentity.ProviderSubject,
			EmailSnapshot:       pendingSocialIdentity.EmailSnapshot,
			DisplayNameSnapshot: pendingSocialIdentity.DisplayNameSnapshot,
		},
	)
	if err != nil {
		return registrationdb.CreateUserRow{}, mapRegistrationDBError(err)
	}

	rowsAffected, err := qtx.ConsumeRegistrationContinuation(
		ctx,
		params.ContinuationID,
	)
	if err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return registrationdb.CreateUserRow{}, apperror.ConflictWith(
			"",
			"registration continuation is no longer available",
			nil,
		)
	}

	rowsAffected, err = qtx.CompletePendingRegistration(
		ctx,
		params.PendingRegistrationID,
	)
	if err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return registrationdb.CreateUserRow{}, apperror.ConflictWith(
			"",
			"registration is no longer pending",
			nil,
		)
	}

	if err := tx.Commit(ctx); err != nil {
		return registrationdb.CreateUserRow{}, apperror.Internal(err)
	}

	return user, nil
}

func mapRegistrationDBError(err error) error {
	var pgErr *pgconn.PgError

	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case pendingRegistrationEmailIndex:
			return apperror.ConflictWith(
				CodeRegistrationAlreadyExists,
				"registration already exists",
				err,
			)

		case "verification_codes_one_active_per_request_idx":
			return apperror.ConflictWith(
				CodeActiveVerificationCodeExists,
				"an active verification code already exists",
				err,
			)

		case "users_email_unique":
			return apperror.ConflictWith(
				CodeUserAlreadyExists,
				"user already exists",
				err,
			)

		case "auth_identities_provider_subject_unique":
			return apperror.ConflictWith(
				CodeAuthIdentityAlreadyExists,
				"auth identity already exists",
				err,
			)
		}
	}

	return apperror.Internal(err)
}
