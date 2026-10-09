package registration

import (
	"fmt"
	"time"

	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func validateVerificationPending(
	verification registrationdb.GetVerificationRow,
) error {
	if verification.Status != string(VerificationRequestPending) {
		return apperror.ConflictWith(
			CodeVerificationNotPending,
			"verification request is no longer pending",
			nil,
		)
	}

	if verification.RegistrationStatus != string(PendingRegistrationPending) {
		return apperror.ConflictWith(
			CodeRegistrationNotPending,
			"registration is no longer pending",
			nil,
		)
	}

	if !verification.RegistrationExpiresAt.Valid {
		return apperror.Internal(
			fmt.Errorf("registration expiration is missing"),
		)
	}

	if !time.Now().Before(verification.RegistrationExpiresAt.Time) {
		return apperror.ConflictWith(
			CodeRegistrationExpired,
			"registration has expired",
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
// would leave the other as the way around it.
//
// A verification that has never sent a code has no cooldown to observe, so the
// first issuance is always allowed and this does not change what a caller
// starting a registration can do.
func ensureVerificationCooldownElapsed(
	verification registrationdb.GetVerificationRow,
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

func validateRegistrationContinuation(
    continuation registrationdb.GetRegistrationContinuationRow,
) error {
    now := time.Now()

    if continuation.ConsumedAt.Valid {
        return apperror.ConflictWith(
            CodeRegistrationContinuationConsumed,
            "registration continuation has already been consumed",
            nil,
        )
    }

    if !continuation.ExpiresAt.Valid {
        return apperror.Internal(
            fmt.Errorf("registration continuation expiration is missing"),
        )
    }

    if !now.Before(continuation.ExpiresAt.Time) {
        return apperror.ConflictWith(
            CodeRegistrationContinuationExpired,
            "registration continuation has expired",
            nil,
        )
    }

    if continuation.RegistrationStatus != string(PendingRegistrationPending) {
        return apperror.ConflictWith(
            CodeRegistrationNotPending,
            "registration is no longer pending",
            nil,
        )
    }

    if !continuation.RegistrationExpiresAt.Valid {
        return apperror.Internal(
            fmt.Errorf("registration expiration is missing"),
        )
    }

    if !now.Before(continuation.RegistrationExpiresAt.Time) {
        return apperror.ConflictWith(
            CodeRegistrationExpired,
            "registration has expired",
            nil,
        )
    }

    return nil
}