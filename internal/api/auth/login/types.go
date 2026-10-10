package login

import (
	"context"

	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

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
