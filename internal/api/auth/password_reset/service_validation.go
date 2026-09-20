package password_reset

import (
	"fmt"
	"time"

	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func validateVerificationPending(
	verification passwordresetdb.GetVerificationRow,
) error {
	if verification.Status != string(VerificationRequestPending) {
		return apperror.ConflictWith(
			CodeVerificationNotPending,
			"verification request is no longer pending",
			nil,
		)
	}

	if verification.PasswordResetStatus != string(PendingPasswordResetPending) {
		return apperror.ConflictWith(
			CodePasswordResetNotPending,
			"password reset is no longer pending",
			nil,
		)
	}

	if !verification.PasswordResetExpiresAt.Valid {
		return apperror.Internal(
			fmt.Errorf("password reset expiration is missing"),
		)
	}

	if !time.Now().Before(verification.PasswordResetExpiresAt.Time) {
		return apperror.ConflictWith(
			CodePasswordResetExpired,
			"password reset has expired",
			nil,
		)
	}

	return nil
}

func validatePasswordResetContinuation(
	continuation passwordresetdb.GetPasswordResetContinuationRow,
) error {
	now := time.Now()

	if continuation.ConsumedAt.Valid {
		return apperror.ConflictWith(
			CodePasswordResetContinuationConsumed,
			"password reset continuation has already been consumed",
			nil,
		)
	}

	if !continuation.ExpiresAt.Valid {
		return apperror.Internal(
			fmt.Errorf("password reset continuation expiration is missing"),
		)
	}

	if !now.Before(continuation.ExpiresAt.Time) {
		return apperror.ConflictWith(
			CodePasswordResetContinuationExpired,
			"password reset continuation has expired",
			nil,
		)
	}

	if continuation.PasswordResetStatus != string(PendingPasswordResetPending) {
		return apperror.ConflictWith(
			CodePasswordResetNotPending,
			"password reset is no longer pending",
			nil,
		)
	}

	if !continuation.PasswordResetExpiresAt.Valid {
		return apperror.Internal(
			fmt.Errorf("password reset expiration is missing"),
			)
	}

	if !now.Before(continuation.PasswordResetExpiresAt.Time) {
		return apperror.ConflictWith(
			CodePasswordResetExpired,
			"password reset has expired",
			nil,
		)
	}

	return nil
}