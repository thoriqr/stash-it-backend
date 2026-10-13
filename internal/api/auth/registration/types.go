package registration

import (
	"context"

	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// PINEmailBudget is what bounds how much PIN mail this feature may cause.
//
// It is two budgets behind one component, and both are spent by the same two
// routes. The per-address budget is shared with the password reset feature — the
// two features issue the same act to the same kind of recipient, so they draw on
// one allowance — and the deployment-wide budget is shared with everything that
// sends mail at all.
//
// **It declares its own interface rather than importing one, and the password
// reset feature declares an identical one.** That duplication is the point: a
// consumer names what it consumes, so a change to either declaration cannot
// silently change what the other depends on. The method sets must stay the same
// or the wiring stops satisfying both.
//
// Deliver is here rather than being called as sender.Send followed by a refund so
// that the rule about which failures may be refunded lives in one place instead of
// once per feature. Two copies of a rule about provider failures is two rules to
// keep in step, and the direction they could drift in is the one that costs
// money.
//
// The subject passed to ReservePerEmail is expected to be an already-normalized
// address. The budget hashes whatever it is given and never interprets it, so
// producing the canonical form stays with this feature, which owns the rule for
// what an address is. That rule is what makes the shared budget a shared budget:
// two features keying the same allowance on two spellings would each have their
// own.
type PINEmailBudget interface {
	ReservePerEmail(
		ctx context.Context,
		normalizedEmail string,
	) (ratelimit.SlidingResult, error)

	ReserveGlobal(ctx context.Context) (ratelimit.SlidingResult, error)

	RefundGlobal(ctx context.Context, admission string) error

	Deliver(
		ctx context.Context,
		global ratelimit.SlidingResult,
		sender email.Sender,
		message email.Message,
	) error
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
