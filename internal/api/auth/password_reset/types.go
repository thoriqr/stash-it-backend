package password_reset

import (
	"context"

	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// PINEmailBudget is what bounds how much PIN mail this feature may cause.
//
// It is the same budget registration spends, not a parallel one: the two features
// issue the same act to the same kind of recipient, so they draw on one
// allowance. Before it existed this feature had no per-address budget at all, and
// its only ceiling was the per-IP one — which an attacker defeats by spreading
// requests across addresses, leaving one mailbox to be mailed as fast as the
// cooldown allows, indefinitely.
//
// **It declares its own interface rather than importing registration's**, and the
// duplication is the point: a consumer names what it consumes, so a change to
// either declaration cannot silently change what the other depends on. The method
// sets must stay the same or the shared component stops satisfying both.
//
// The subject passed to ReservePerEmail is expected to be an already-normalized
// address, and this feature reaches that form through NormalizeEmail rather than
// through a copy of the rule. The shared budget is only shared if both features
// key it on the same spelling; two identical implementations would drift the day
// one of them was edited.
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
	VerificationSubjectPendingPasswordReset VerificationSubjectType = "pending_password_reset"
)

type VerificationPurpose string

const (
	VerificationPurposePasswordReset VerificationPurpose = "password_reset"
)

type VerificationRequestStatus string

const (
	VerificationRequestPending  VerificationRequestStatus = "pending"
	VerificationRequestVerified VerificationRequestStatus = "verified"
)

type PendingPasswordResetStatus string

const (
	PendingPasswordResetPending   PendingPasswordResetStatus = "pending"
	PendingPasswordResetCompleted PendingPasswordResetStatus = "completed"
	PendingPasswordResetExpired   PendingPasswordResetStatus = "expired"
)