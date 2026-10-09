package registration

import (
	"context"

	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// PinRateLimiter is the budget one email address has for having a PIN issued.
//
// It is keyed by the address rather than by the verification being acted on,
// which is the whole reason it exists. The verification cooldown is per
// verification, so an address with many of them would otherwise be able to
// request a code from each in turn and the cooldown would not slow it down at all.
// Keying by address makes one budget cover every verification for that address.
//
// The same limiter instance also backs the per-IP budget mounted on the routes
// themselves, under a different namespace. Two budgets on one limiter is the
// intended shape: they guard different dimensions, neither stands in for the
// other, and keeping them on one component means one Redis and one script own
// both.
//
// The subject passed in is expected to be an already-normalized address. The
// limiter hashes whatever it is given and never interprets it, so producing the
// canonical form stays with the feature that owns the rule for it.
type PinRateLimiter interface {
	Allow(
		ctx context.Context,
		namespace string,
		subject string,
		policy ratelimit.Policy,
	) (ratelimit.Result, error)
}

type VerificationSubjectType string

const (
	VerificationSubjectPendingRegistration VerificationSubjectType = "pending_registration"
)

type VerificationPurpose string

const (
	VerificationPurposeRegistration VerificationPurpose = "registration"
)

type VerificationRequestStatus string

const (
	VerificationRequestPending  VerificationRequestStatus = "pending"
	VerificationRequestVerified VerificationRequestStatus = "verified"
)

type RegistrationType string

const (
	RegistrationTypeManual RegistrationType = "manual"
	RegistrationTypeSocial RegistrationType = "social"
)

type PendingRegistrationStatus string

const (
	PendingRegistrationPending   PendingRegistrationStatus = "pending"
	PendingRegistrationCompleted PendingRegistrationStatus = "completed"
	PendingRegistrationExpired   PendingRegistrationStatus = "expired"
)
