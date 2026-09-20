package password_reset

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type Repository interface {
	GetUserByEmail(
		ctx context.Context,
		email string,
	) (passwordresetdb.GetUserByEmailRow, error)

	GetActivePasswordResetByEmail(
		ctx context.Context,
		email string,
	) (passwordresetdb.GetActivePasswordResetByEmailRow, bool, error)

	CreatePasswordReset(
		ctx context.Context,
		params CreatePasswordResetParams,
	) (passwordresetdb.VerificationRequest, error)

	GetVerification(
		ctx context.Context,
		id uuid.UUID,
	) (passwordresetdb.GetVerificationRow, error)

	ResendVerification(
		ctx context.Context,
		params ResendVerificationParams,
	) (passwordresetdb.VerificationRequest, error)

	GetActiveVerificationCode(
		ctx context.Context,
		verificationRequestID uuid.UUID,
		maxAttempts int32,
	) (passwordresetdb.VerificationCode, error)

	IncrementVerificationCodeAttempts(
		ctx context.Context,
		id uuid.UUID,
		maxAttempts int32,
	) (int32, error)

	CompleteVerification(
		ctx context.Context,
		params CompleteVerificationParams,
	) (passwordresetdb.PasswordResetContinuation, error)

	GetPasswordResetContinuation(
		ctx context.Context,
		tokenHash string,
	) (passwordresetdb.GetPasswordResetContinuationRow, error)

	UpsertPasswordCredentialAndCompleteReset(
		ctx context.Context,
		params UpsertPasswordCredentialAndCompleteResetParams,
	) error
}

type repository struct {
	db      *pgxpool.Pool
	queries *passwordresetdb.Queries
}

func NewRepository(
	db *pgxpool.Pool,
	queries *passwordresetdb.Queries,
) Repository {
	return &repository{
		db:      db,
		queries: queries,
	}
}

type CreatePasswordResetParams struct {
	Email          string
	ResetExpiresAt pgtype.Timestamptz
	CodeHash       string
	CodeExpiresAt  pgtype.Timestamptz
}

func (r *repository) GetUserByEmail(
	ctx context.Context,
	email string,
) (passwordresetdb.GetUserByEmailRow, error) {
	user, err := r.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return passwordresetdb.GetUserByEmailRow{}, apperror.NotFoundWith("", "User not found", err)
		}

		return passwordresetdb.GetUserByEmailRow{}, apperror.Internal(err)
	}

	return user, nil
}

func (r *repository) GetActivePasswordResetByEmail(
	ctx context.Context,
	email string,
) (passwordresetdb.GetActivePasswordResetByEmailRow, bool, error) {
	reset, err := r.queries.GetActivePasswordResetByEmail(
		ctx,
		email,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return passwordresetdb.GetActivePasswordResetByEmailRow{}, false, nil
		}

		return passwordresetdb.GetActivePasswordResetByEmailRow{}, false,
			apperror.Internal(err)
	}

	return reset, true, nil
}

func (r *repository) CreatePasswordReset(
	ctx context.Context,
	params CreatePasswordResetParams,
) (passwordresetdb.VerificationRequest, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	existing, err := qtx.GetPendingPasswordResetByEmailForUpdate(
		ctx,
		params.Email,
	)
	if err == nil {
		if !existing.IsExpired {
			return passwordresetdb.VerificationRequest{
				ID: existing.VerificationID,
			}, nil
		}

		rowsAffected, err := qtx.ExpirePendingPasswordReset(ctx, existing.ID)
		if err != nil {
			return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
		}
		if rowsAffected != 1 {
			return passwordresetdb.VerificationRequest{}, apperror.ConflictWith(
				"",
				"password reset is no longer pending",
				nil,
			)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
	}

	pendingReset, err := qtx.CreatePendingPasswordReset(
		ctx,
		passwordresetdb.CreatePendingPasswordResetParams{
			Email:     params.Email,
			ExpiresAt: params.ResetExpiresAt,
		},
	)
	if err != nil {
		return passwordresetdb.VerificationRequest{}, mapPasswordResetDBError(err)
	}

	verificationRequest, err := qtx.CreateVerificationRequest(
		ctx,
		pendingReset.ID,
	)
	if err != nil {
		return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
	}

	_, err = qtx.CreateVerificationCode(
		ctx,
		passwordresetdb.CreateVerificationCodeParams{
			VerificationRequestID: verificationRequest.ID,
			CodeHash:              params.CodeHash,
			ExpiresAt:             params.CodeExpiresAt,
		},
	)
	if err != nil {
		return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
	}

	return verificationRequest, nil
}

func (r *repository) GetVerification(
	ctx context.Context,
	id uuid.UUID,
) (passwordresetdb.GetVerificationRow, error) {
	verification, err := r.queries.GetVerification(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return passwordresetdb.GetVerificationRow{}, apperror.NotFound(err)
		}

		return passwordresetdb.GetVerificationRow{}, apperror.Internal(err)
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
) (passwordresetdb.VerificationRequest, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	// Invalidate the currently active verification code.
	if err := qtx.InvalidateVerificationCode(
		ctx,
		params.VerificationID,
	); err != nil {
		return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
	}

	// Create the new verification code.
	_, err = qtx.CreateVerificationCode(
		ctx,
		passwordresetdb.CreateVerificationCodeParams{
			VerificationRequestID: params.VerificationID,
			CodeHash:              params.CodeHash,
			ExpiresAt:             params.CodeExpiresAt,
		},
	)
	if err != nil {
		return passwordresetdb.VerificationRequest{}, mapPasswordResetDBError(err)
	}

	// Update resend metadata.
	verificationRequest, err := qtx.UpdateVerificationRequestResend(
		ctx,
		params.VerificationID,
	)
	if err != nil {
		return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return passwordresetdb.VerificationRequest{}, apperror.Internal(err)
	}

	return verificationRequest, nil
}

func (r *repository) GetActiveVerificationCode(
	ctx context.Context,
	verificationRequestID uuid.UUID,
	maxAttempts int32,
) (passwordresetdb.VerificationCode, error) {
	code, err := r.queries.GetActiveVerificationCode(
		ctx,
		passwordresetdb.GetActiveVerificationCodeParams{
			VerificationRequestID: verificationRequestID,
			MaxAttempts:           maxAttempts,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return passwordresetdb.VerificationCode{}, apperror.ConflictWith(
				"",
				"verification code is no longer available",
				err,
			)
		}

		return passwordresetdb.VerificationCode{}, apperror.Internal(err)
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
		passwordresetdb.IncrementVerificationCodeAttemptsParams{
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
	VerificationCodeID     uuid.UUID
	VerificationRequestID  uuid.UUID
	PendingPasswordResetID uuid.UUID
	TokenHash              string
	ExpiresAt              pgtype.Timestamptz
}

func (r *repository) CompleteVerification(
	ctx context.Context,
	params CompleteVerificationParams,
) (passwordresetdb.PasswordResetContinuation, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return passwordresetdb.PasswordResetContinuation{}, apperror.Internal(err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	rowsAffected, err := qtx.ConsumeVerificationCode(
		ctx,
		params.VerificationCodeID,
	)
	if err != nil {
		return passwordresetdb.PasswordResetContinuation{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return passwordresetdb.PasswordResetContinuation{}, apperror.ConflictWith(
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
		return passwordresetdb.PasswordResetContinuation{}, apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return passwordresetdb.PasswordResetContinuation{}, apperror.ConflictWith(
			"",
			"verification request is no longer pending",
			nil,
		)
	}

	continuation, err := qtx.CreatePasswordResetContinuation(
		ctx,
		passwordresetdb.CreatePasswordResetContinuationParams{
			PendingPasswordResetID: params.PendingPasswordResetID,
			TokenHash:              params.TokenHash,
			ExpiresAt:              params.ExpiresAt,
		},
	)
	if err != nil {
		return passwordresetdb.PasswordResetContinuation{}, apperror.Internal(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return passwordresetdb.PasswordResetContinuation{}, apperror.Internal(err)
	}

	return continuation, nil
}

func (r *repository) GetPasswordResetContinuation(
	ctx context.Context,
	tokenHash string,
) (passwordresetdb.GetPasswordResetContinuationRow, error) {
	continuation, err := r.queries.GetPasswordResetContinuation(
		ctx,
		tokenHash,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return passwordresetdb.GetPasswordResetContinuationRow{}, apperror.ConflictWith(
				"",
				"password reset continuation is no longer available",
				err,
			)
		}

		return passwordresetdb.GetPasswordResetContinuationRow{}, apperror.Internal(err)
	}

	return continuation, nil
}

type UpsertPasswordCredentialAndCompleteResetParams struct {
	UserID                      uuid.UUID
	PasswordHash                string
	PasswordResetContinuationID uuid.UUID
	PendingPasswordResetID      uuid.UUID
}

func (r *repository) UpsertPasswordCredentialAndCompleteReset(
	ctx context.Context,
	params UpsertPasswordCredentialAndCompleteResetParams,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return apperror.Internal(err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	err = qtx.UpsertPasswordCredential(
		ctx,
		passwordresetdb.UpsertPasswordCredentialParams{
			UserID:       params.UserID,
			PasswordHash: params.PasswordHash,
		},
	)
	if err != nil {
		return apperror.Internal(err)
	}

	rowsAffected, err := qtx.ConsumePasswordResetContinuation(
		ctx,
		params.PasswordResetContinuationID,
	)
	if err != nil {
		return apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return apperror.ConflictWith(
			"",
			"password reset continuation is no longer available",
			nil,
		)
	}

	rowsAffected, err = qtx.CompletePendingPasswordReset(
		ctx,
		params.PendingPasswordResetID,
	)
	if err != nil {
		return apperror.Internal(err)
	}

	if rowsAffected != 1 {
		return apperror.ConflictWith(
			"",
			"password reset is no longer pending",
			nil,
		)
	}

	if err := qtx.RevokeAllSessionsForUser(ctx, params.UserID); err != nil {
    return mapPasswordResetDBError(err)
}

	if err := tx.Commit(ctx); err != nil {
		return apperror.Internal(err)
	}

	return nil
}

func mapPasswordResetDBError(err error) error {
	var pgErr *pgconn.PgError

	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "pending_password_resets_email_pending_idx":
			return apperror.ConflictWith(
				CodePasswordResetAlreadyPending,
				"password reset already pending",
				err,
			)

		case "verification_codes_one_active_per_request_idx":
			return apperror.ConflictWith(
				CodeActiveVerificationCodeExists,
				"an active verification code already exists",
				err,
			)
		}
	}

	return apperror.Internal(err)
}
