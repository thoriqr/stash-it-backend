package testutil

import (
	"context"
	"sync"
	"time"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
)

// PermissivePinRateLimiter allows every request.
//
// It is the default for the integration app so that a suite about registration
// does not have to stand up Redis to assert something else. The limiter's own
// behaviour is proven against a real Redis in the ratelimit integration tests;
// this exists so unrelated tests are not coupled to that container.
type PermissivePinRateLimiter struct{}

// NewPermissivePinRateLimiter returns a limiter that never refuses.
func NewPermissivePinRateLimiter() *PermissivePinRateLimiter {
	return &PermissivePinRateLimiter{}
}

// Allow always reports the request as within budget.
//
// The reported wait is the full window rather than a real remaining one, because
// a request this limiter allows is never told to wait, and a refusal from it is
// only ever produced by a limiter that does know.
func (PermissivePinRateLimiter) Allow(
	_ context.Context,
	_ string,
	_ string,
	policy ratelimit.Policy,
) (ratelimit.Result, error) {
	return ratelimit.Result{
		Count:      1,
		Allowed:    true,
		RetryAfter: policy.Window,
	}, nil
}

// CountingPinRateLimiter records what it was asked and answers with a
// caller-controlled result.
//
// It is for proving that the endpoints consult the limiter, which budget they
// spend, and that a refusal stops the PIN being issued, without a Redis container
// in the way. Because one instance backs both the per-IP middleware and the
// per-email service, Namespaces is what tells the two apart.
type CountingPinRateLimiter struct {
	mutex sync.Mutex

	// Allowed is what Allow reports. False models an exhausted budget.
	Allowed bool

	// Err, when set, is returned instead of Allowed.
	Err error

	// RetryAfter is the remaining window reported on a refusal. It exists so a
	// test can assert the wait that reaches the response rather than only that a
	// wait was sent at all.
	RetryAfter time.Duration

	// Subjects records every subject asked about, in order.
	Subjects []string

	// Namespaces records every namespace asked about, in order.
	Namespaces []string

	// Policies records every policy given, in order.
	Policies []ratelimit.Policy

	// Overrides answers per namespace, taking precedence over Allowed and Err.
	//
	// One request now crosses two budgets before anything is issued — one for
	// the client address in front of the route, one for the email address inside
	// the service. A test about either has to be able to let the other through,
	// or it would end up asserting on whichever happened to be consulted first.
	Overrides map[string]NamespaceOutcome

	// counts records how many occurrences each namespace has seen, so a fake
	// reports a plausible count rather than one shared across every namespace.
	counts map[string]int64
}

// NamespaceOutcome is one namespace's answer from a CountingPinRateLimiter.
type NamespaceOutcome struct {
	Allowed bool
	Err     error

	// RetryAfter is the remaining window reported on a refusal.
	RetryAfter time.Duration
}

// NewCountingPinRateLimiter returns a limiter that allows every request and
// remembers what it was asked.
func NewCountingPinRateLimiter() *CountingPinRateLimiter {
	return &CountingPinRateLimiter{Allowed: true}
}

// Allow records the call and returns whatever Allowed and Err say.
func (l *CountingPinRateLimiter) Allow(
	_ context.Context,
	namespace string,
	subject string,
	policy ratelimit.Policy,
) (ratelimit.Result, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Namespaces = append(l.Namespaces, namespace)
	l.Subjects = append(l.Subjects, subject)
	l.Policies = append(l.Policies, policy)

	if l.counts == nil {
		l.counts = map[string]int64{}
	}

	l.counts[namespace]++

	if outcome, overridden := l.Overrides[namespace]; overridden {
		if outcome.Err != nil {
			return ratelimit.Result{}, outcome.Err
		}

		return ratelimit.Result{
			Count:      l.counts[namespace],
			Allowed:    outcome.Allowed,
			RetryAfter: outcome.RetryAfter,
		}, nil
	}

	if l.Err != nil {
		return ratelimit.Result{}, l.Err
	}

	return ratelimit.Result{
		Count:      l.counts[namespace],
		Allowed:    l.Allowed,
		RetryAfter: l.RetryAfter,
	}, nil
}

// AllowNamespace returns a copy of the limiter that answers the named namespace
// with a fixed outcome and leaves every other namespace as it is.
//
// It is how a test about one budget says what the other one does. Without it, a
// test about the email budget would have to arrange for the IP budget to pass
// some other way, which makes it depend on the order the two happen to be
// consulted in.
func (l *CountingPinRateLimiter) AllowNamespace(
	namespace string,
	outcome NamespaceOutcome,
) *CountingPinRateLimiter {
	overrides := make(map[string]NamespaceOutcome, len(l.Overrides)+1)
	for name, existing := range l.Overrides {
		overrides[name] = existing
	}

	overrides[namespace] = outcome

	l.Overrides = overrides

	return l
}

// SubjectsIn returns every subject the given namespace was asked about.
func (l *CountingPinRateLimiter) SubjectsIn(namespace string) []string {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	var subjects []string

	for index, seen := range l.Namespaces {
		if seen == namespace {
			subjects = append(subjects, l.Subjects[index])
		}
	}

	return subjects
}

// CallsIn returns how many times the given namespace was consulted.
func (l *CountingPinRateLimiter) CallsIn(namespace string) int {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return int(l.counts[namespace])
}

// ExceededPinRateLimiter returns a limiter that refuses every request, standing
// in for a budget that is spent.
func ExceededPinRateLimiter() *CountingPinRateLimiter {
	return &CountingPinRateLimiter{Allowed: false, RetryAfter: 42 * time.Second}
}

// The two namespaces the PIN endpoints spend. They are named here because a test
// that is about one of them has to say what the other does, and spelling them out
// in every test would obscure which one is under test.
const (
	// PinEmailNamespace is the per-address budget, spent inside the service.
	PinEmailNamespace = "pin-email"

	// PinIPNamespace is the per-client-address budget, spent before the handler.
	PinIPNamespace = "pin-ip"
)

// UnavailablePinRateLimiter returns a limiter that cannot answer, standing in for
// Redis being unreachable.
func UnavailablePinRateLimiter(err error) *CountingPinRateLimiter {
	return &CountingPinRateLimiter{Err: err}
}

// compile-time proof that the fakes satisfy both interfaces that consume a
// limiter: the feature's service and the route middleware.
var (
	_ registration.PinRateLimiter = (*PermissivePinRateLimiter)(nil)
	_ registration.PinRateLimiter = (*CountingPinRateLimiter)(nil)
	_ middleware.RateLimiter      = (*PermissivePinRateLimiter)(nil)
	_ middleware.RateLimiter      = (*CountingPinRateLimiter)(nil)
)
