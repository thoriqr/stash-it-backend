package password_reset

import (
	"context"
	"fmt"
	"time"

	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
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

// verificationCooldownRemaining is the wait a caller must observe before its
// next issuance attempt can succeed.
//
// It is the whole cooldown rather than a subtraction, and that is accurate rather
// than lazy. The transaction that persisted the code is what stamps last_sent_at,
// and it committed before the sender was called, so by the time a delivery failure
// is being reported the cooldown was restarted moments ago and essentially all of
// it is still ahead of the caller.
//
// Erring long is the safe side: a client that waits a moment longer than strictly
// necessary is never turned away for asking too early.
func verificationCooldownRemaining() time.Duration {
	return verificationResendCooldown
}

// reservePINEmailBudget records one issuance attempt against the shared
// per-address budget.
//
// This feature had no per-address budget at all before this. Its only ceiling on
// mailing one mailbox was the per-IP one, which an attacker defeats by spreading
// requests across source addresses — leaving the victim's cooldown as the only
// thing between them and an unlimited mail cannon.
//
// A budget that cannot be evaluated is a refusal. Allowing on failure would let
// anyone remove the ceiling by causing the failure that produces it, and refusing
// means an outage stops PIN issuance, which is visible and recoverable, rather
// than silently removing the ceiling.
func (s *Service) reservePINEmailBudget(
	ctx context.Context,
	emailAddress string,
) (ratelimit.SlidingResult, error) {
	result, err := s.pinEmailBudget.ReservePerEmail(
		ctx,
		normalizeEmail(emailAddress),
	)
	if err != nil {
		return result, apperror.ServiceUnavailableWith(
			CodePinRateLimitUnavailable,
			"too many verification codes requested, please try again shortly",
			err,
		)
	}

	if !result.Allowed {
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
// It is a separate operation rather than a flag on the per-address one because
// the two refuse differently and the caller must be able to tell which happened:
// one is this address's problem, the other is a condition an operator has to see.
//
// The same fail-closed rule applies, and for the same reason.
func (s *Service) reserveGlobalPINEmailBudget(
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
// Whether a given failure qualifies is not decided here and not decided by this
// feature at all — it belongs to the budget, which knows what the sender
// reported. This exists only to name the call, so every place that can reach it
// reads as one decision rather than as several.
//
// The refund's own error is discarded on purpose. The work it was protecting has
// already failed, and turning that into a different error would tell a caller
// their account is broken when what happened is that a counter is one too high
// until its window passes.
func (s *Service) refundGlobalPINEmailBudget(
	ctx context.Context,
	global ratelimit.SlidingResult,
) {
	_ = s.pinEmailBudget.RefundGlobal(ctx, global.Admission)
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