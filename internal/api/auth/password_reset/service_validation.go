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

// ensureVerificationCooldownElapsed rejects a verification code that was sent
// too recently to send another.
//
// Both issuance paths go through here, because they send the same kind of message
// to the same address and have the same cost: CreatePIN issues the first code,
// ResendVerification issues a replacement. Only one of them enforcing the rule
// would leave the other as the way around it, and the two routes are
// unauthenticated, so whoever holds a verification id may call either one.
//
// It also does the work of a per-code attempt limit that would otherwise be
// trivially re-armed: a fresh code is a fresh row whose attempt counter starts at
// zero, so issuing is as much a way to keep guessing as it is a way to receive.
//
// A verification that has never sent a code has no cooldown to observe, so the
// first issuance is always allowed and this does not change what a caller
// starting a password reset can do.
func ensureVerificationCooldownElapsed(
	verification passwordresetdb.GetVerificationRow,
) error {
	if !verification.LastSentAt.Valid {
		return nil
	}

	if time.Now().Before(
		verification.LastSentAt.Time.Add(verificationResendCooldown),
	) {
		return apperror.ConflictWith(
			CodeVerificationResendCooldown,
			"verification code was sent too recently",
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