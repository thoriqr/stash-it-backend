package testutil

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// NewRedisPINEmailBudget builds the real Redis-backed budget over an existing
// client.
//
// It exists so a test that wants to prove the budget's *Redis* behaviour — that
// concurrent admissions do not exceed the limit, that a refund really removes an
// entry, that a key carries no readable address — can use the production component
// rather than a second implementation of the same rules.
//
// The logger is a no-op rather than a real one: a budget that logs refusals is
// useful in production and is noise in a test, and a test asserting on refusals
// asserts on the returned result rather than on what was written down.
func NewRedisPINEmailBudget(
	client redis.UniversalClient,
	secret []byte,
	perEmailMax int64,
	window time.Duration,
	globalMax int64,
) *email.PINEmailBudget {
	return email.NewPINEmailBudget(
		zap.NewNop(),
		ratelimit.NewSlidingLimiter(client, secret),
		perEmailMax,
		window,
		globalMax,
		window,
	)
}

// ResetPINEmailBudgetKeys removes every sliding-window entry the budget wrote.
//
// The integration suite shares one Redis, so a test that spends a budget would
// otherwise see another's counts. Only the sliding-window prefix is cleared, so
// the fixed-window counters and the queue's keys are left alone.
func ResetPINEmailBudgetKeys(
	ctx context.Context,
	client redis.UniversalClient,
) error {
	keys, err := client.Keys(ctx, "rlw:*").Result()
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		return nil
	}

	return client.Del(ctx, keys...).Err()
}

// PermissivePINEmailBudget admits everything and refunds everything.
//
// It is the default for the integration app so that a suite about registration or
// password reset does not have to stand up Redis to assert something else. The
// budget's own Redis binding is proven against a real Redis in the integration
// tests; this exists so unrelated tests are not coupled to that container.
type PermissivePINEmailBudget struct{}

func NewPermissivePINEmailBudget() *PermissivePINEmailBudget {
	return &PermissivePINEmailBudget{}
}

func (PermissivePINEmailBudget) ReservePerEmail(
	_ context.Context,
	normalizedEmail string,
) (ratelimit.SlidingResult, error) {
	return ratelimit.SlidingResult{
		Count:     1,
		Allowed:   true,
		Admission: "permissive-" + normalizedEmail,
	}, nil
}

func (PermissivePINEmailBudget) ReserveGlobal(
	_ context.Context,
) (ratelimit.SlidingResult, error) {
	return ratelimit.SlidingResult{
		Count:     1,
		Allowed:   true,
		Admission: "permissive-global",
	}, nil
}

// RefundGlobal counts nothing and always succeeds. It exists so the wiring does
// not have to branch on which budget it was handed.
func (PermissivePINEmailBudget) RefundGlobal(
	_ context.Context,
	_ string,
) error {
	return nil
}

// Deliver calls the sender and applies the real refund rule rather than a
// simplified one.
//
// A permissive budget that skipped Deliver's decision would make every test that
// injects it blind to the one behaviour it is not meant to be permissive about —
// whether a failed send refunds the deployment-wide slot. Tests that care about
// that inject a CountingPINEmailBudget instead.
func (b PermissivePINEmailBudget) Deliver(
	ctx context.Context,
	global ratelimit.SlidingResult,
	sender email.Sender,
	message email.Message,
) error {
	err := sender.Send(ctx, message)

	if err != nil && !email.Attempted(err) {
		_ = b.RefundGlobal(ctx, global.Admission)
	}

	return err
}

// PINEmailBudgetOutcome is one budget's answer from a CountingPINEmailBudget.
type PINEmailBudgetOutcome struct {
	// Allowed is whether the reservation is admitted.
	Allowed bool

	// Err, when set, is returned instead of Allowed, standing in for a Redis the
	// budget cannot reach.
	Err error

	// RetryAfter is the wait reported on a refusal, so a test can assert the
	// value that reaches the response rather than only that one was sent.
	RetryAfter time.Duration
}

// CountingPINEmailBudget records what it was asked and answers with
// caller-controlled results.
//
// It is for proving which budget a service spends, in what order, and what a
// refusal costs — the things that are about the *flow*. What it is not for is the
// Redis behaviour underneath, which belongs in the limiter's own integration
// tests against a real server.
type CountingPINEmailBudget struct {
	mutex sync.Mutex

	// PerEmail and Global answer the two reservations. They are separate fields
	// rather than one shared outcome because the whole point of the ordering rules
	// is that one can refuse while the other admits, and a fake that could not
	// express that could not test them.
	PerEmail PINEmailBudgetOutcome
	Global   PINEmailBudgetOutcome

	// SendErr, when set, is what Deliver hands back instead of calling the
	// sender. It stands in for a sender that was reached and failed.
	//
	// A nil value means Deliver really calls the sender, which is what a test
	// about delivery wants. Wrap it with email.NotAttempted to model a sender that
	// never reached a provider, which is the one failure that is refunded.
	SendErr error

	// PerEmailSubjects records every address a per-address reservation was asked
	// about, in order.
	PerEmailSubjects []string

	// PerEmailCalls counts per-address reservations.
	PerEmailCalls int

	// GlobalCalls counts deployment-wide reservations.
	GlobalCalls int

	// Refunds records every admission handed back, in order.
	Refunds []string

	// Delivered counts the messages Deliver passed to the sender.
	Delivered int

	admissionSeq int
}

// NewCountingPINEmailBudget returns a budget that admits everything and records.
func NewCountingPINEmailBudget() *CountingPINEmailBudget {
	return &CountingPINEmailBudget{
		PerEmail: PINEmailBudgetOutcome{Allowed: true},
		Global:   PINEmailBudgetOutcome{Allowed: true},
	}
}

// NewExhaustedPINEmailBudget returns a budget whose per-address reservation is
// refused and whose deployment-wide one is admitted.
//
// The split is what makes it useful: a per-address refusal that reached the
// deployment-wide budget would be a bug, so a test can assert that a call
// refused this way never spent the global one.
func NewExhaustedPINEmailBudget() *CountingPINEmailBudget {
	return &CountingPINEmailBudget{
		PerEmail: PINEmailBudgetOutcome{Allowed: false, RetryAfter: 42 * time.Second},
		Global:   PINEmailBudgetOutcome{Allowed: true},
	}
}

// NewGloballyExhaustedPINEmailBudget returns a budget whose deployment-wide
// reservation is refused and whose per-address one is admitted.
func NewGloballyExhaustedPINEmailBudget() *CountingPINEmailBudget {
	return &CountingPINEmailBudget{
		PerEmail: PINEmailBudgetOutcome{Allowed: true},
		Global:   PINEmailBudgetOutcome{Allowed: false, RetryAfter: 7 * time.Minute},
	}
}

// NewUnavailablePINEmailBudget returns a budget that cannot answer either
// reservation, standing in for a Redis the budget cannot reach.
func NewUnavailablePINEmailBudget(err error) *CountingPINEmailBudget {
	return &CountingPINEmailBudget{
		PerEmail: PINEmailBudgetOutcome{Err: err},
		Global:   PINEmailBudgetOutcome{Err: err},
	}
}

func (l *CountingPINEmailBudget) ReservePerEmail(
	_ context.Context,
	normalizedEmail string,
) (ratelimit.SlidingResult, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.PerEmailCalls++
	l.PerEmailSubjects = append(l.PerEmailSubjects, normalizedEmail)

	// The error is returned rather than folded into a zero result. A caller
	// distinguishes "this address is over budget" from "the budget could not be
	// evaluated", and they answer 429 and 503 respectively, so a fake that
	// swallowed it would make a fail-closed path look like a refusal.
	if l.PerEmail.Err != nil {
		return ratelimit.SlidingResult{}, l.PerEmail.Err
	}

	if !l.PerEmail.Allowed {
		return ratelimit.SlidingResult{
			Count:      int64(l.PerEmailCalls),
			Allowed:    false,
			RetryAfter: l.PerEmail.RetryAfter,
		}, nil
	}

	l.admissionSeq++

	return ratelimit.SlidingResult{
		Count:     int64(l.admissionSeq),
		Allowed:   true,
		Admission: "per-email-" + normalizedEmail,
	}, nil
}

func (l *CountingPINEmailBudget) ReserveGlobal(
	_ context.Context,
) (ratelimit.SlidingResult, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.GlobalCalls++

	if l.Global.Err != nil {
		return ratelimit.SlidingResult{}, l.Global.Err
	}

	if !l.Global.Allowed {
		return ratelimit.SlidingResult{
			Count:      int64(l.GlobalCalls),
			Allowed:    false,
			RetryAfter: l.Global.RetryAfter,
		}, nil
	}

	l.admissionSeq++

	return ratelimit.SlidingResult{
		Count:     int64(l.admissionSeq),
		Allowed:   true,
		Admission: "global-" + strconv.Itoa(l.admissionSeq),
	}, nil
}

// RefundGlobal records the admission, or does nothing when there is none.
//
// Mirroring the real budget's rule about an empty admission matters: a refused
// reservation produces one, and recording it would let a test count a refund
// that the production component would never have performed.
func (l *CountingPINEmailBudget) RefundGlobal(
	_ context.Context,
	admission string,
) error {
	if admission == "" {
		return nil
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Refunds = append(l.Refunds, admission)

	return nil
}

func (l *CountingPINEmailBudget) Deliver(
	ctx context.Context,
	global ratelimit.SlidingResult,
	sender email.Sender,
	message email.Message,
) error {
	l.mutex.Lock()
	sendErr := l.SendErr
	l.mutex.Unlock()

	var err error

	if sendErr != nil {
		err = sendErr
	} else {
		l.mutex.Lock()
		l.Delivered++
		l.mutex.Unlock()

		err = sender.Send(ctx, message)
	}

	if err != nil && !email.Attempted(err) {
		_ = l.RefundGlobal(ctx, global.Admission)
	}

	return err
}

// RefundCount returns how many reservations were handed back.
func (l *CountingPINEmailBudget) RefundCount() int {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return len(l.Refunds)
}

// PerEmailCount returns how many per-address reservations were asked for.
func (l *CountingPINEmailBudget) PerEmailCount() int {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return l.PerEmailCalls
}

// GlobalCount returns how many deployment-wide reservations were asked for.
func (l *CountingPINEmailBudget) GlobalCount() int {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return l.GlobalCalls
}

// compile-time proof that the fakes satisfy every interface that consumes the
// budget: the composition root and both feature services.
var (
	_ auth.PINEmailBudget          = (*PermissivePINEmailBudget)(nil)
	_ auth.PINEmailBudget          = (*CountingPINEmailBudget)(nil)
	_ registration.PINEmailBudget  = (*PermissivePINEmailBudget)(nil)
	_ registration.PINEmailBudget  = (*CountingPINEmailBudget)(nil)
	_ passwordreset.PINEmailBudget = (*PermissivePINEmailBudget)(nil)
	_ passwordreset.PINEmailBudget = (*CountingPINEmailBudget)(nil)
	_ *email.PINEmailBudget        = (*email.PINEmailBudget)(nil)
)
