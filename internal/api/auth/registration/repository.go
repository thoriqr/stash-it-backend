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
    ) (registrationdb.VerificationRequest, error)

    GetVerification(
        ctx context.Context,
        id uuid.UUID,
    ) (registrationdb.GetVerificationRow, error)

    ResendVerification(
        ctx context.Context,
        params ResendVerificationParams,
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

    GetActiveRegistrationByEmail(
        ctx context.Context,
        email string,
    ) (registrationdb.GetActiveRegistrationByEmailRow, bool, error)

    GetRegistrationContinuation(
        ctx context.Context,
        tokenHash string,
    ) (registrationdb.GetRegistrationContinuationRow, error)

    FinalizeManualRegistration(
        ctx context.Context,
        params FinalizeManualRegistrationParams,
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

type CreateManualRegistrationParams struct {
	Email                 string
	RegistrationExpiresAt pgtype.Timestamptz
	CodeHash              string
	CodeExpiresAt         pgtype.Timestamptz
}

func (r *repository) GetActiveRegistrationByEmail(
    ctx context.Context,
    email string,
) (registrationdb.GetActiveRegistrationByEmailRow, bool, error) {
    registration, err := r.queries.GetActiveRegistrationByEmail(
        ctx,
        email,
    )
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return registrationdb.GetActiveRegistrationByEmailRow{}, false, nil
        }

        return registrationdb.GetActiveRegistrationByEmailRow{}, false,
            apperror.Internal(err)
    }

    return registration, true, nil
}

func (r *repository) CreateManualRegistration(
	ctx context.Context,
	params CreateManualRegistrationParams,
) (registrationdb.VerificationRequest, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return registrationdb.VerificationRequest{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	pendingRegistration, err := qtx.CreatePendingRegistration(
		ctx,
		registrationdb.CreatePendingRegistrationParams{
			Email:            params.Email,
			RegistrationType: string(RegistrationTypeManual),
			Status:            string(PendingRegistrationPending),
			ExpiresAt:         params.RegistrationExpiresAt,
		},
	)
	if err != nil {
		return registrationdb.VerificationRequest{}, mapRegistrationDBError(err)
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
		return registrationdb.VerificationRequest{}, apperror.Internal(err)
	}

	_, err = qtx.CreateVerificationCode(
		ctx,
		registrationdb.CreateVerificationCodeParams{
			VerificationRequestID: verificationRequest.ID,
			CodeHash:              params.CodeHash,
			ExpiresAt:             params.CodeExpiresAt,
		},
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

type ResendVerificationParams struct {
    VerificationID uuid.UUID
    CodeHash       string
    CodeExpiresAt  pgtype.Timestamptz
}


func (r *repository) ResendVerification(
	ctx context.Context,
	params ResendVerificationParams,
) (registrationdb.VerificationRequest, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return registrationdb.VerificationRequest{}, apperror.Internal(err)
	}

	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	// Invalidate the currently active verification code.
	if err := qtx.InvalidateVerificationCode(
		ctx,
		params.VerificationID,
	); err != nil {
		return registrationdb.VerificationRequest{}, apperror.Internal(err)
	}

	// Create the new verification code.
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

	// Update resend metadata.
	verificationRequest, err := qtx.UpdateVerificationRequestResend(
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

func mapRegistrationDBError(err error) error {
	var pgErr *pgconn.PgError

	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "pending_registrations_email_idx":
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
		}
	}

	return apperror.Internal(err)
}

