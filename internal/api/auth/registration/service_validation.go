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

// verificationCooldownRemaining is the wait a caller must observe before its
// next issuance attempt can succeed.
//
// It is the whole cooldown rather than a subtraction, and that is accurate rather
// than lazy. The transaction that persisted the code is what stamps last_sent_at,
// and it committed before the sender was called, so by the time a delivery failure
// is being reported the cooldown was restarted moments ago and essentially all of
// it is still ahead of the caller.
//
// Reporting a remainder computed against an earlier read would be wrong in the
// unhelpful direction: the read happened *before* the stamp, so subtracting from
// it would understate the wait and tell a client to come back while the cooldown
// is still closed. Erring long is the safe side — the client waits a moment longer
// than strictly necessary and is never turned away for asking too early.
func verificationCooldownRemaining() time.Duration {
	return verificationResendCooldown
}

// reservePINEmailBudget records one issuance attempt against the shared
// per-address budget.
//
// It sits after the verification has been resolved, because the budget is the
// address's and the address only becomes known there. Reading it from the same
// row the rest of the flow already needs means the check costs no extra query.
//
// Both issuance paths call it, so one address spends one budget no matter which
// endpoint it uses or how many verifications it has. Password reset spends the
// same budget through the same component, which is what makes it one budget
// rather than two that happen to share a name.
//
// The address is normalized again here. It is already stored normalized, so this
// changes nothing today; it is here because the key must be the canonical form of
// the address, and because this feature owns the rule that says what canonical
// means — the other feature reaches the same answer by calling NormalizeEmail
// rather than by repeating it.
//
// A budget that cannot be evaluated is treated as a refusal. The alternative is
// to let the request through whenever Redis is unreachable, which hands an
// attacker the ability to switch protection off by causing a failure. Refusing
// means an outage stops PIN issuance, which is a visible and recoverable
// condition, rather than silently removing the ceiling on what an address — or
// the whole deployment — can be sent.
func (s *service) reservePINEmailBudget(
	ctx context.Context,
	email string,
) (ratelimit.SlidingResult, error) {
	result, err := s.pinEmailBudget.ReservePerEmail(
		ctx,
		NormalizeEmail(email),
	)
	if err != nil {
		return result, apperror.ServiceUnavailableWith(
			CodePinRateLimitUnavailable,
			"too many verification codes requested, please try again shortly",
			err,
		)
	}

	if !result.Allowed {
		// The wait comes from the oldest entry still inside the rolling window
		// rather than from the configured window, so a caller arriving late is
		// told to come back when a slot actually frees and not a whole day later.
		return result, apperror.TooManyRequestsWithRetryAfter(
			CodePinRateLimitExceeded,
			"too many verification codes requested, please try again later",
			result.RetryAfter,
		)
	}

	return result, nil
}

// reserveGlobalPINEmailBudget records one issuance against the deployment-wide
// budget.
//
// It is a separate operation from the per-address one rather than a flag on it,
// because the two refuse differently and the caller has to be able to tell which
// happened: one is this address's problem and the other is a condition an
// operator has to see.
//
// The same fail-closed rule applies, and for the same reason.
func (s *service) reserveGlobalPINEmailBudget(
	ctx context.Context,
) (ratelimit.SlidingResult, error) {
	result, err := s.pinEmailBudget.ReserveGlobal(ctx)
	if err != nil {
		return result, apperror.ServiceUnavailableWith(
			CodePINEmailBudgetUnavailable,
			"verification codes are temporarily unavailable, please try again shortly",
			err,
		)
	}

	if !result.Allowed {
		return result, apperror.TooManyRequestsWithRetryAfter(
			CodePINEmailBudgetExceeded,
			"verification codes are temporarily unavailable, please try again later",
			result.RetryAfter,
		)
	}

	return result, nil
}

// refundGlobalPINEmailBudget gives a global reservation back after a failure
// that provably happened before the provider could have been contacted.
//
// The decision of whether a given failure qualifies is not made here and is not
// made by this feature at all — it belongs to the budget, which knows what the
// sender reported. This exists only to name the call, so every place that can
// reach it reads as the same decision rather than as several.
//
// The refund's own error is discarded on purpose. The work it was protecting has
// already failed, and turning that into a different error would tell a caller
// their account is broken when what happened is that a counter is one too high
// until its window passes.
func (s *service) refundGlobalPINEmailBudget(
	ctx context.Context,
	global ratelimit.SlidingResult,
) {
	_ = s.pinEmailBudget.RefundGlobal(ctx, global.Admission)
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
