package login

import (
	"context"
	"time"

	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// PasswordVerifier is the password work this feature performs, as a consuming
// package names it.
//
// It is declared here for the same reason LoginRateLimiter is: this package is
// the consumer, and a consuming package names its own dependencies. It covers
// the whole of what LoginManual needs — Verify, DummyVerify and Wait — and
// nothing else, so the concrete hasher satisfies it unchanged and production
// wiring is unaffected.
//
// The reason it exists is testability of one specific claim. Whether an unknown
// address pays for password work was previously observable only as elapsed time,
// because the hasher was a concrete type behind which nothing could be seen. A
// test that can watch DummyVerify being called asserts that directly; a test
// that can only compare durations is asserting about a shared machine's
// scheduler, which is not the same claim and is not a reliable one.
//
// DummyVerify is on this interface rather than being reached for directly
// because it is part of the work manual login does, not an implementation
// detail of the hasher: it is what keeps an unknown address from returning
// measurably faster than a wrong password on a known one.
type PasswordVerifier interface {
	// Verify checks a password against a stored hash.
	Verify(
		ctx context.Context,
		password string,
		encodedHash string,
	) (security.PasswordVerificationResult, error)

	// DummyVerify performs the same derivation against a fixed hash, for an
	// address that has no account to check against.
	DummyVerify(ctx context.Context, password string) error

	// Wait reports how long a caller waits for capacity before being refused.
	// It is carried through to the Retry-After the caller is given.
	Wait() time.Duration
}

// LoginRateLimiter is the budget guarding manual login.
//
// It is declared here rather than imported from a feature because this package is
// the consumer, and a consuming package names its own dependencies. It is the
// same shape every limiter in the project has, so the Redis-backed implementation
// satisfies it unchanged.
//
// Both operations are needed, and needing both is what makes the accounting
// correct. Allow is called before the password work so that the work is bounded;
// Release is called when that work turns out to have succeeded, so a successful
// login does not spend the failure budget it was charged optimistically.
type LoginRateLimiter interface {
	Allow(
		ctx context.Context,
		namespace string,
		subject string,
		policy ratelimit.Policy,
	) (ratelimit.Result, error)

	Release(
		ctx context.Context,
		namespace string,
		subject string,
	) (ratelimit.Result, error)
}

type LoginOutcome string

const (
	LoginOutcomeAuthenticated        LoginOutcome = "authenticated"
	LoginOutcomeAccountLinkRequired  LoginOutcome = "account_link_required"
	LoginOutcomeRegistrationRequired LoginOutcome = "registration_required"
)
