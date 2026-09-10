package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	authdb "github.com/thoriqr/stash-it-backend/internal/auth/generated"
)

type RegistrationRepository interface {
	CreateManualRegistration(
		ctx context.Context,
		params CreateManualRegistrationParams,
	) (authdb.VerificationRequest, error)

	GetVerification(
		ctx context.Context,
		id uuid.UUID,
	) (authdb.GetVerificationRow, error)

	ResendVerification(
		ctx context.Context,
		params ResendVerificationParams,
	) (authdb.VerificationRequest, error)

	GetActiveVerificationCode(
		ctx context.Context,
		verificationRequestID uuid.UUID,
		maxAttempts int32,
	) (authdb.VerificationCode, error)

	IncrementVerificationCodeAttempts(
		ctx context.Context,
		id uuid.UUID,
		maxAttempts int32,
	) error

	CompleteVerification(
		ctx context.Context,
		params CompleteVerificationParams,
	) (authdb.RegistrationContinuation, error)

	GetPendingRegistrationByEmail(
    ctx context.Context,
    email string,
	) (authdb.GetPendingRegistrationByEmailRow, bool, error)

	GetRegistrationContinuation(
		ctx context.Context,
		tokenHash string,
	) (authdb.GetRegistrationContinuationRow, error)

	FinalizeManualRegistration(
    ctx context.Context,
    params FinalizeManualRegistrationParams,
  ) (authdb.CreateUserRow, error)
	
}

type registrationRepository struct {
	pool    *pgxpool.Pool
	queries *authdb.Queries
}

func NewRegistrationRepository(
	pool *pgxpool.Pool,
	queries *authdb.Queries,
) RegistrationRepository {
	return &registrationRepository{
		pool:    pool,
		queries: queries,
	}
}

type CreateManualRegistrationParams struct {
	Email                 string
	RegistrationExpiresAt pgtype.Timestamptz
	CodeHash              string
	CodeExpiresAt         pgtype.Timestamptz
}

func (r *registrationRepository) GetPendingRegistrationByEmail(
    ctx context.Context,
    email string,
) (authdb.GetPendingRegistrationByEmailRow, bool, error) {
    registration, err := r.queries.GetPendingRegistrationByEmail(
        ctx,
        email,
    )
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return authdb.GetPendingRegistrationByEmailRow{}, false, nil
        }

        return authdb.GetPendingRegistrationByEmailRow{}, false,
            apperror.Internal(err)
    }

    return registration, true, nil
}

func (r *registrationRepository) CreateManualRegistration(
	ctx context.Context,
	params CreateManualRegistrationParams,
) (authdb.VerificationRequest, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return authdb.VerificationRequest{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	pendingRegistration, err := qtx.CreatePendingRegistration(
		ctx,
		authdb.CreatePendingRegistrationParams{
			Email:            params.Email,
			RegistrationType: string(RegistrationTypeManual),
			Status:            string(PendingRegistrationPending),
			ExpiresAt:         params.RegistrationExpiresAt,
		},
	)
	if err != nil {
		return authdb.VerificationRequest{}, mapRegistrationDBError(err)
	}

	verificationRequest, err := qtx.CreateVerificationRequest(
		ctx,
		authdb.CreateVerificationRequestParams{
			SubjectType: string(VerificationSubjectPendingRegistration),
			SubjectID:   pendingRegistration.ID,
			Purpose:     string(VerificationPurposeRegistration),
			Status:      string(VerificationRequestPending),
		},
	)
	if err != nil {
		return authdb.VerificationRequest{}, apperror.Internal(err)
	}

	_, err = qtx.CreateVerificationCode(
		ctx,
		authdb.CreateVerificationCodeParams{
			VerificationRequestID: verificationRequest.ID,
			CodeHash:              params.CodeHash,
			ExpiresAt:             params.CodeExpiresAt,
		},
	)
	if err != nil {
		return authdb.VerificationRequest{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return authdb.VerificationRequest{}, apperror.Internal(err)
	}

	return verificationRequest, nil
}

func (r *registrationRepository) GetVerification(
	ctx context.Context,
	id uuid.UUID,
) (authdb.GetVerificationRow, error) {
	verification, err := r.queries.GetVerification(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return authdb.GetVerificationRow{}, apperror.NotFoundWith(
				"VERIFICATION_NOT_FOUND",
				"verification request not found",
				err,
			)
		}

		return authdb.GetVerificationRow{}, apperror.Internal(err)
	}

	return verification, nil
}

type ResendVerificationParams struct {
    VerificationID uuid.UUID
    CodeHash       string
    CodeExpiresAt  pgtype.Timestamptz
}


func (r *registrationRepository) ResendVerification(
	ctx context.Context,
	params ResendVerificationParams,
) (authdb.VerificationRequest, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return authdb.VerificationRequest{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	// Invalidate the currently active verification code.
	if err := qtx.InvalidateVerificationCode(
		ctx,
		params.VerificationID,
	); err != nil {
		return authdb.VerificationRequest{}, apperror.Internal(err)
	}

	// Create the new verification code.
	_, err = qtx.CreateVerificationCode(
		ctx,
		authdb.CreateVerificationCodeParams{
			VerificationRequestID: params.VerificationID,
			CodeHash:              params.CodeHash,
			ExpiresAt:             params.CodeExpiresAt,
		},
	)
	if err != nil {
		return authdb.VerificationRequest{}, mapRegistrationDBError(err)
	}

	// Update resend metadata.
	verificationRequest, err := qtx.UpdateVerificationRequestResend(
		ctx,
		params.VerificationID,
	)
	if err != nil {
		return authdb.VerificationRequest{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return authdb.VerificationRequest{}, apperror.Internal(err)
	}

	return verificationRequest, nil
}

func (r *registrationRepository) GetActiveVerificationCode(
    ctx context.Context,
    verificationRequestID uuid.UUID,
    maxAttempts int32,
) (authdb.VerificationCode, error) {
    code, err := r.queries.GetActiveVerificationCode(
        ctx,
        authdb.GetActiveVerificationCodeParams{
            VerificationRequestID: verificationRequestID,
            MaxAttempts:           maxAttempts,
        },
    )
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return authdb.VerificationCode{}, apperror.ConflictWith(
                "VERIFICATION_CODE_UNAVAILABLE",
                "verification code is no longer available",
                err,
            )
        }

        return authdb.VerificationCode{}, apperror.Internal(err)
    }

    return code, nil
}

func (r *registrationRepository) IncrementVerificationCodeAttempts(
    ctx context.Context,
    id uuid.UUID,
    maxAttempts int32,
) error {
    err := r.queries.IncrementVerificationCodeAttempts(
        ctx,
        authdb.IncrementVerificationCodeAttemptsParams{
            ID:         id,
            MaxAttempts: maxAttempts,
        },
    )
    if err != nil {
        return apperror.Internal(err)
    }

    return nil
}

type CompleteVerificationParams struct {
	VerificationCodeID    uuid.UUID
	VerificationRequestID uuid.UUID
	PendingRegistrationID uuid.UUID
	TokenHash             string
	ExpiresAt             pgtype.Timestamptz
}

func (r *registrationRepository) CompleteVerification(
	ctx context.Context,
	params CompleteVerificationParams,
) (authdb.RegistrationContinuation, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return authdb.RegistrationContinuation{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	rowsAffected, err := qtx.ConsumeVerificationCode(
		ctx,
		params.VerificationCodeID,
	)
	if err != nil {
		return authdb.RegistrationContinuation{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return authdb.RegistrationContinuation{}, apperror.ConflictWith(
			"VERIFICATION_CODE_UNAVAILABLE",
			"verification code is no longer available",
			nil,
		)
	}

	rowsAffected, err = qtx.MarkVerificationRequestVerified(
		ctx,
		params.VerificationRequestID,
	)
	if err != nil {
		return authdb.RegistrationContinuation{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return authdb.RegistrationContinuation{}, apperror.ConflictWith(
			"VERIFICATION_NOT_PENDING",
			"verification request is no longer pending",
			nil,
		)
	}

	continuation, err := qtx.CreateRegistrationContinuation(
		ctx,
		authdb.CreateRegistrationContinuationParams{
			PendingRegistrationID: params.PendingRegistrationID,
			TokenHash:             params.TokenHash,
			ExpiresAt:             params.ExpiresAt,
		},
	)
	if err != nil {
		return authdb.RegistrationContinuation{}, mapRegistrationDBError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return authdb.RegistrationContinuation{}, apperror.Internal(err)
	}

	return continuation, nil
}

func (r *registrationRepository) GetRegistrationContinuation(
    ctx context.Context,
    tokenHash string,
) (authdb.GetRegistrationContinuationRow, error) {
    continuation, err := r.queries.GetRegistrationContinuation(
        ctx,
        tokenHash,
    )
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return authdb.GetRegistrationContinuationRow{}, apperror.ConflictWith(
                "REGISTRATION_CONTINUATION_UNAVAILABLE",
                "registration continuation is no longer available",
                err,
            )
        }

        return authdb.GetRegistrationContinuationRow{}, apperror.Internal(err)
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

func (r *registrationRepository) FinalizeManualRegistration(
	ctx context.Context,
	params FinalizeManualRegistrationParams,
) (authdb.CreateUserRow, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return authdb.CreateUserRow{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	user, err := qtx.CreateUser(
		ctx,
		authdb.CreateUserParams{
			Email:           params.Email,
			DisplayName:     params.DisplayName,
			EmailVerifiedAt: params.EmailVerifiedAt,
		},
	)
	if err != nil {
		return authdb.CreateUserRow{}, mapRegistrationDBError(err)
	}

	_, err = qtx.CreatePasswordCredential(
		ctx,
		authdb.CreatePasswordCredentialParams{
			UserID:       user.ID,
			PasswordHash: params.PasswordHash,
		},
	)
	if err != nil {
		return authdb.CreateUserRow{}, apperror.Internal(err)
	}

	rowsAffected, err := qtx.ConsumeRegistrationContinuation(
		ctx,
		params.ContinuationID,
	)
	if err != nil {
		return authdb.CreateUserRow{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return authdb.CreateUserRow{}, apperror.ConflictWith(
			"REGISTRATION_CONTINUATION_UNAVAILABLE",
			"registration continuation is no longer available",
			nil,
		)
	}

	rowsAffected, err = qtx.CompletePendingRegistration(
		ctx,
		params.PendingRegistrationID,
	)
	if err != nil {
		return authdb.CreateUserRow{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return authdb.CreateUserRow{}, apperror.ConflictWith(
			"REGISTRATION_NOT_PENDING",
			"registration is no longer pending",
			nil,
		)
	}

	if err := tx.Commit(ctx); err != nil {
		return authdb.CreateUserRow{}, apperror.Internal(err)
	}

	return user, nil
}

func mapRegistrationDBError(err error) error {
	var pgErr *pgconn.PgError

	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "pending_registrations_active_email_idx":
			return apperror.ConflictWith(
				"REGISTRATION_ALREADY_PENDING",
				"registration is already pending",
				err,
			)

		case "verification_codes_one_active_per_request_idx":
			return apperror.ConflictWith(
				"ACTIVE_VERIFICATION_CODE_EXISTS",
				"an active verification code already exists",
				err,
			)
		}
	}

	return apperror.Internal(err)
}

