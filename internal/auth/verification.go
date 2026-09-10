package auth

import (
	"fmt"
	"time"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	authdb "github.com/thoriqr/stash-it-backend/internal/auth/generated"
)

func validateVerificationPending(
	verification authdb.GetVerificationRow,
) error {
	if verification.Status != string(VerificationRequestPending) {
		return apperror.ConflictWith(
			"VERIFICATION_NOT_PENDING",
			"verification request is no longer pending",
			nil,
		)
	}

	if verification.RegistrationStatus != string(PendingRegistrationPending) {
		return apperror.ConflictWith(
			"REGISTRATION_NOT_PENDING",
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
			"REGISTRATION_EXPIRED",
			"registration has expired",
			nil,
		)
	}

	return nil
}

func validateRegistrationContinuation(
    continuation authdb.GetRegistrationContinuationRow,
) error {
    now := time.Now()

    if continuation.ConsumedAt.Valid {
        return apperror.ConflictWith(
            "REGISTRATION_CONTINUATION_CONSUMED",
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
            "REGISTRATION_CONTINUATION_EXPIRED",
            "registration continuation has expired",
            nil,
        )
    }

    if continuation.RegistrationStatus != string(PendingRegistrationPending) {
        return apperror.ConflictWith(
            "REGISTRATION_NOT_PENDING",
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
            "REGISTRATION_EXPIRED",
            "registration has expired",
            nil,
        )
    }

    return nil
}