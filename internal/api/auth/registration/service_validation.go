package registration

import (
	"context"
	"fmt"
	"time"

	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
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

// ensurePinRateAvailable reports whether one address may have another PIN issued.
//
// It sits after the verification has been resolved, because the budget is the
// address's and the address only becomes known there. Reading it from the same
// row the rest of the flow already needs means the check costs no extra query.
//
// Both issuance paths call it with the same namespace, so one address spends one
// budget no matter which endpoint it uses or how many verifications it has.
//
// The address is normalized again here. It is already stored normalized, so this
// changes nothing today; it is here because the key must be the canonical form of
// the address and that rule belongs to this feature, not to the limiter.
//
// A limiter that cannot answer is treated as a refusal. The alternative is to
// let the request through whenever Redis is unreachable, which hands an attacker
// the ability to switch protection off by causing a failure. Refusing means an
// outage stops PIN issuance, which is a visible and recoverable condition, rather
// than silently removing the ceiling on what an address can be sent.
func (s *service) ensurePinRateAvailable(
	ctx context.Context,
	email string,
) error {
	result, err := s.pinRateLimiter.Allow(
		ctx,
		pinRateLimitNamespace,
		NormalizeEmail(email),
		ratelimit.Policy{
			Max:    PinRateLimitMax,
			Window: PinRateLimitWindow,
		},
	)
	if err != nil {
		return apperror.ServiceUnavailableWith(
			CodePinRateLimitUnavailable,
			"too many verification codes requested, please try again shortly",
			err,
		)
	}

	if !result.Allowed {
		// The wait comes from the counter's own remaining window rather than from
		// the configured policy, so a caller arriving late into a window is told
		// to come back when this one actually ends and not a full window later.
		return apperror.TooManyRequestsWithRetryAfter(
			CodePinRateLimitExceeded,
			"too many verification codes requested, please try again later",
			result.RetryAfter,
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
