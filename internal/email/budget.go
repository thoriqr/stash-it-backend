package email

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// The PIN email budgets.
//
// Every message this application sends today is a verification PIN, and every one
// of them is produced by exactly two features — registration and password reset.
// They share two budgets, and the shape of that sharing is the whole design.
//
// The per-address budget is shared between the two features on purpose. The
// alternative, a budget each, was rejected after reading the schema rather than
// on first principles: `pending_registrations_email_idx` is UNIQUE over addresses
// whose registration is pending or completed, `RegisterManual` answers 409 for an
// address that already has a completed registration, and a pending registration
// belongs to somebody with no account for a password reset to find. So for any
// given address exactly one of the two flows can issue anything at all, and two
// budgets would be two budgets where one is never reachable.
//
// **That is a dependency, not an accident.** If the product ever allows a second
// account per address, re-registration after expiry, or a change of address, the
// flows stop being mutually exclusive and this has to be revisited: the shared
// budget would then let registration traffic spend a registered user's recovery
// allowance, which is precisely the cost sharing was chosen to avoid.
const (
	// PINEmailBudgetNamespace is the per-address budget, shared by both features
	// and by both issuance routes in each of them.
	//
	// It names the action and not the route or the feature, for the same reason
	// every other namespace in this application does: CreatePIN and
	// ResendVerification are the same act, and naming either separately would let
	// a caller double its allowance by switching between them.
	PINEmailBudgetNamespace = "pin-email"

	// PINEmailGlobalBudgetNamespace is the deployment-wide budget that every PIN
	// email draws on, whatever produced it.
	//
	// It is a separate namespace from the per-address one because it bounds a
	// different thing. The per-address budget bounds what one mailbox can cost;
	// this bounds what the whole deployment can spend, and it is the only budget
	// that survives an attacker spreading requests across addresses, across client
	// addresses, or across both.
	PINEmailGlobalBudgetNamespace = "pin-email-global"
)

// pinEmailGlobalSubject is the single subject the deployment-wide budget is
// keyed on.
//
// A global budget has exactly one subject by definition, so this is not a
// stand-in for a real one. It is a fixed string rather than, say, a hostname or
// an instance id, so that two instances share one budget instead of each having
// its own, and so that it cannot vary with the deployment's topology.
const pinEmailGlobalSubject = "application"

// PINEmailBudget is the reservation side of the two budgets above.
//
// It owns the namespaces and the policies so the two features cannot drift apart
// on them, and so a test can assert that both features spent the same budget by
// observing the same component rather than by comparing two declarations.
//
// It depends on ratelimit rather than on any provider, a feature, or HTTP: what it
// knows is that a bounded number of messages may be admitted over a rolling
// period. Selecting and integrating a delivery provider remains a separate task,
// and nothing here would need to change when one is added.
type PINEmailBudget struct {
	log       *zap.Logger
	limiter   *ratelimit.SlidingLimiter
	perEmail  ratelimit.SlidingPolicy
	global    ratelimit.SlidingPolicy
	globalSub string
}

// NewPINEmailBudget builds the shared budget over a sliding-window limiter.
//
// The policies are arguments rather than constants because their right values are
// properties of a deployment rather than of this code: how much mail the chosen
// provider's plan allows is a fact about an account someone chose. A value written
// into a package would silently become wrong the day the plan changed, and would
// be wrong in whichever direction nobody noticed. This is the same reasoning that
// puts PASSWORD_WORK_CONCURRENCY in configuration.
func NewPINEmailBudget(
	log *zap.Logger,
	limiter *ratelimit.SlidingLimiter,
	perEmailMax int64,
	perEmailWindow time.Duration,
	globalMax int64,
	globalWindow time.Duration,
) *PINEmailBudget {
	return &PINEmailBudget{
		log:     log,
		limiter: limiter,
		perEmail: ratelimit.SlidingPolicy{
			Max:    perEmailMax,
			Window: perEmailWindow,
		},
		global: ratelimit.SlidingPolicy{
			Max:    globalMax,
			Window: globalWindow,
		},
		globalSub: pinEmailGlobalSubject,
	}
}

// ReservePerEmail records one PIN issuance attempt against an address's budget.
//
// The address is expected to already be normalized by the feature that owns the
// rule for what an address is, exactly as the fixed-window limiter expects. The
// limiter hashes whatever it is given and never interprets it, so producing the
// canonical form stays with the caller — and the two features sharing one budget
// is only correct because they share one normalization.
func (b *PINEmailBudget) ReservePerEmail(
	ctx context.Context,
	normalizedEmail string,
) (ratelimit.SlidingResult, error) {
	result, err := b.limiter.Allow(
		ctx,
		PINEmailBudgetNamespace,
		normalizedEmail,
		b.perEmail,
	)
	if err != nil {
		return result, err
	}

	if !result.Allowed {
		// The count is recorded and the address deliberately is not. A budget
		// refusal is a fact an operator needs — how many addresses are being
		// refused and how long until each frees — and none of that requires
		// saying which mailbox it was.
		b.log.Warn(
			"pin email budget exhausted",
			zap.String("namespace", PINEmailBudgetNamespace),
			zap.Int64("count", result.Count),
			zap.Int64("max", b.perEmail.Max),
			zap.Duration("retry_after", result.RetryAfter),
		)
	}

	return result, nil
}

// ReserveGlobal records one PIN email against the deployment-wide budget.
//
// It takes no address: the budget is not per recipient, and asking the caller for
// one would invite keying it on something that varies.
func (b *PINEmailBudget) ReserveGlobal(
	ctx context.Context,
) (ratelimit.SlidingResult, error) {
	result, err := b.limiter.Allow(
		ctx,
		PINEmailGlobalBudgetNamespace,
		b.globalSub,
		b.global,
	)
	if err != nil {
		return result, err
	}

	if !result.Allowed {
		b.log.Warn(
			"global pin email budget exhausted",
			zap.String("namespace", PINEmailGlobalBudgetNamespace),
			zap.Int64("count", result.Count),
			zap.Int64("max", b.global.Max),
			zap.Duration("retry_after", result.RetryAfter),
		)
	}

	return result, nil
}

// Deliver attempts one message against a reserved global slot and applies the
// budget's rule for the outcome.
//
// It exists so the rule about what may be refunded lives in exactly one place
// rather than once per feature that sends mail. Two copies of a rule about
// provider failures is two rules to keep in step, and the direction they could
// drift in — refunding more than is safe — is the one that costs money.
//
// The rule is one line: **the slot is given back only when the sender reports
// that it never tried.** Everything else keeps it, including a timeout, a
// connection reset and a generic provider error, none of which prove the
// provider declined to process the message.
//
// It returns the sender's own error unchanged so the caller keeps the underlying
// fault for logging, and does the logging itself so neither feature has to
// remember to.
func (b *PINEmailBudget) Deliver(
	ctx context.Context,
	global ratelimit.SlidingResult,
	sender Sender,
	message Message,
) error {
	err := sender.Send(ctx, message)

	fields := []zap.Field{
		zap.String("namespace", PINEmailGlobalBudgetNamespace),
		zap.Int64("count", global.Count),
		zap.Int64("max", b.global.Max),
	}

	switch {
	case err == nil:
		b.log.Info("pin email send attempted", fields...)
	case Attempted(err):
		b.log.Warn(
			"pin email send failed after an attempt",
			append(fields, zap.Error(err))...,
		)
	default:
		b.log.Warn(
			"pin email send failed before any attempt",
			append(fields, zap.Error(err))...,
		)

		b.refund(ctx, global)
	}

	return err
}

// refund gives back the slot a reservation took, logging rather than reporting a
// failure.
//
// A refund that cannot be completed must not become the caller's problem: the
// work it was protecting has already failed, and turning that into a different
// error would tell a caller their account is broken when what actually happened
// is that a counter is one too high until its window passes. The cost is bounded
// and self-correcting, and an operator sees it here.
func (b *PINEmailBudget) refund(ctx context.Context, global ratelimit.SlidingResult) {
	if global.Admission == "" {
		return
	}

	if err := b.RefundGlobal(ctx, global.Admission); err != nil {
		b.log.Warn(
			"global pin email budget refund failed",
			zap.String("namespace", PINEmailGlobalBudgetNamespace),
			zap.Error(err),
		)
	}
}

// RefundGlobal gives a global reservation back, identified by the Admission that
// ReserveGlobal returned for it.
//
// It is for exactly one situation: the request failed before the provider could
// have been contacted. Once the sender has been invoked, the slot is spent
// whatever comes back, because a timeout or a connection failure does not prove
// the provider declined to process the message.
//
// A refund that cannot be completed is not reported to the caller. The work it
// was protecting has already failed, and refusing that request because the undo
// did not land would tell the caller their account is broken when what actually
// happened is that a counter is one too high until its window passes.
func (b *PINEmailBudget) RefundGlobal(ctx context.Context, admission string) error {
	if admission == "" {
		return nil
	}

	return b.limiter.Release(
		ctx,
		PINEmailGlobalBudgetNamespace,
		b.globalSub,
		admission,
	)
}

// PerEmailPolicy reports the per-address budget this instance enforces, so a
// startup line can record the configuration a deployment actually resolved
// rather than leaving an operator to infer it from the environment file.
func (b *PINEmailBudget) PerEmailPolicy() ratelimit.SlidingPolicy {
	return b.perEmail
}

// GlobalPolicy reports the deployment-wide budget this instance enforces.
func (b *PINEmailBudget) GlobalPolicy() ratelimit.SlidingPolicy {
	return b.global
}

// ErrNotAttempted reports that a sender made no attempt to deliver a message.
//
// It exists because the only question the email budget needs answered about a
// failure is *whether the provider was reached at all*, and "the call returned an
// error" does not answer it. A sender that refuses because it was never
// configured cannot have spent anything, so its slot is given back. A sender that
// reached a provider and then timed out may have had the message accepted, and
// its slot is not.
//
// Reporting this from the sender rather than inferring it keeps provider-specific
// knowledge — which failures prove non-delivery — inside whichever sender is
// eventually written, where it can be decided against real responses instead of
// guessed at here.
var ErrNotAttempted = errors.New("no email delivery was attempted")

// NotAttempted marks err as having occurred before the provider was contacted,
// keeping err so a log still carries the reason.
//
// It returns an error that satisfies errors.Is against ErrNotAttempted while
// preserving whatever the sender actually failed on, which is the part a reader
// needs and the part a boolean would throw away.
func NotAttempted(err error) error {
	return errors.Join(ErrNotAttempted, err)
}

// Attempted reports whether err means the provider may have been reached.
//
// A nil error is not a failure and is not asked about. Everything that is not
// explicitly marked is treated as attempted, which is the safe direction: it
// keeps a budget slot that the alternative would have handed back.
func Attempted(err error) bool {
	return err != nil && !errors.Is(err, ErrNotAttempted)
}
