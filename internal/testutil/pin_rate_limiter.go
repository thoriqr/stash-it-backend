package testutil

import (
	"context"
	"sync"
	"time"

	"github.com/thoriqr/stash-it-backend/internal/api/auth"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
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

// Release is a no-op on a limiter that never charged anything.
//
// It exists to satisfy the same interface the real limiter does, so the test app
// can be built with either without the wiring branching on which one it got.
func (PermissivePinRateLimiter) Release(
	_ context.Context,
	_ string,
	_ string,
) (ratelimit.Result, error) {
	return ratelimit.Result{Count: 0, Allowed: true}, nil
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

	// Allowed is what Allow reports. False models an exhausted budget. It is
	// ignored when EnforcePolicy is set.
	Allowed bool

	// EnforcePolicy answers from the policy the call was given rather than from
	// Allowed, so a test can spend a real budget without a Redis behind it: the
	// Max'th request is allowed and the next one is refused, exactly as the
	// counter itself behaves.
	//
	// It is off by default, because a limiter that is only recording something
	// has no business deciding whether a request succeeds. A test that turns it
	// on is testing a budget being exhausted, and is exercising the counter's
	// arithmetic rather than its Redis binding — the latter still belongs to the
	// limiter's own integration tests.
	EnforcePolicy bool

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
	// One request crosses several budgets before anything is issued — an address
	// budget in front of the route, an identity budget inside the service, and
	// they are deliberately separate namespaces. A test about either has to be
	// able to let the other through, or it would end up asserting on whichever
	// happened to be consulted first.
	Overrides map[string]NamespaceOutcome

	// counts records how much of each subject's budget is currently held, keyed
	// by namespace and subject together.
	//
	// Keying by subject is what makes EnforcePolicy meaningful: a budget belongs
	// to one address, so a second address must not be refused because the first
	// spent one. It is also what the real limiter does, and a fake that counted a
	// namespace as a whole would let one address's flood lock every other address
	// out — the opposite of the property under test.
	counts map[string]int64

	// calls records every consultation per namespace, charged or released, kept
	// apart from counts because a release lowers counts while still being a call.
	calls map[string]int

	// releases records how many times each namespace was released against.
	releases map[string]int

	// subjectsByNamespace records subjects per namespace.
	//
	// It replaces indexing Namespaces and Subjects in parallel, which breaks the
	// moment a call records a namespace without recording a subject in the same
	// position — which is exactly what a release is.
	subjectsByNamespace map[string][]string
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

// NewEnforcingPinRateLimiter returns a limiter that spends a real budget from the
// policy it is given, and remembers what it was asked.
//
// It is the one to reach for when the thing under test is a budget running out
// rather than the wiring that spends it.
func NewEnforcingPinRateLimiter() *CountingPinRateLimiter {
	return &CountingPinRateLimiter{EnforcePolicy: true}
}

// countKey identifies one subject's budget within a namespace.
func countKey(namespace string, subject string) string {
	return namespace + "\x00" + subject
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

	if l.subjectsByNamespace == nil {
		l.subjectsByNamespace = map[string][]string{}
	}

	l.subjectsByNamespace[namespace] = append(
		l.subjectsByNamespace[namespace],
		subject,
	)

	if l.counts == nil {
		l.counts = map[string]int64{}
	}

	if l.calls == nil {
		l.calls = map[string]int{}
	}

	key := countKey(namespace, subject)

	l.calls[namespace]++

	l.counts[key]++

	if outcome, overridden := l.Overrides[namespace]; overridden {
		if outcome.Err != nil {
			return ratelimit.Result{}, outcome.Err
		}

		return ratelimit.Result{
			Count:      l.counts[key],
			Allowed:    outcome.Allowed,
			RetryAfter: outcome.RetryAfter,
		}, nil
	}

	if l.Err != nil {
		return ratelimit.Result{}, l.Err
	}

	allowed := l.Allowed

	if l.EnforcePolicy {
		// The occurrence is already counted, so the Max'th call is the one
		// still within budget. This is the arithmetic the real limiter performs
		// on the count it read back from the same atomic step.
		allowed = l.counts[key] <= policy.Max
	}

	return ratelimit.Result{
		Count:      l.counts[key],
		Allowed:    allowed,
		RetryAfter: l.RetryAfter,
	}, nil
}

// AllowNamespace answers the named namespace with a fixed outcome and leaves
// every other namespace as it is.
//
// It is how a test about one budget says what the other one does. Without it, a
// test about the email budget would have to arrange for the address budget to
// pass some other way, which makes it depend on the order the two happen to be
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

// Release gives one counted occurrence back.
//
// The fake models the counter it actually kept, so a test can assert that a
// successful login left the budget where it started rather than one unit lower.
func (l *CountingPinRateLimiter) Release(
	_ context.Context,
	namespace string,
	subject string,
) (ratelimit.Result, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Namespaces = append(l.Namespaces, namespace)
	l.Subjects = append(l.Subjects, subject)

	if l.subjectsByNamespace == nil {
		l.subjectsByNamespace = map[string][]string{}
	}

	l.subjectsByNamespace[namespace] = append(
		l.subjectsByNamespace[namespace],
		subject,
	)

	if l.Err != nil {
		return ratelimit.Result{}, l.Err
	}

	if l.releases == nil {
		l.releases = map[string]int{}
	}

	if l.calls == nil {
		l.calls = map[string]int{}
	}

	key := countKey(namespace, subject)

	l.calls[namespace]++

	l.releases[namespace]++

	if remaining, counted := l.counts[key]; counted && remaining > 0 {
		l.counts[key] = remaining - 1
	}

	return ratelimit.Result{
		Count:   l.counts[key],
		Allowed: true,
	}, nil
}

// ReleasesIn returns how many times the given namespace was released against.
func (l *CountingPinRateLimiter) ReleasesIn(namespace string) int {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return l.releases[namespace]
}

// ChargeFor returns how much of the given namespace's budget is currently held,
// summed over every subject that namespace has been asked about.
//
// A namespace holds one budget per subject, so a total is the only figure that
// reads sensibly when a test spent the budget of more than one address.
func (l *CountingPinRateLimiter) ChargeFor(namespace string) int64 {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	var total int64

	seen := map[string]struct{}{}

	for _, subject := range l.subjectsByNamespace[namespace] {
		if _, counted := seen[subject]; counted {
			continue
		}

		seen[subject] = struct{}{}

		total += l.counts[countKey(namespace, subject)]
	}

	return total
}

// SubjectsIn returns every subject the given namespace was charged against.
func (l *CountingPinRateLimiter) SubjectsIn(namespace string) []string {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return append([]string(nil), l.subjectsByNamespace[namespace]...)
}

// CallsIn returns how many times the given namespace was consulted at all,
// whether it was charged or released.
func (l *CountingPinRateLimiter) CallsIn(namespace string) int {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return l.calls[namespace]
}

// ExceededPinRateLimiter returns a limiter that refuses every request, standing
// in for a budget that is spent.
func ExceededPinRateLimiter() *CountingPinRateLimiter {
	return &CountingPinRateLimiter{Allowed: false, RetryAfter: 42 * time.Second}
}

// The namespaces the protected endpoints spend. They are named here because a
// test that is about one of them has to say what the others do, and spelling them
// out in every test would obscure which one is under test.
const (
	// PinEmailNamespace is the per-address PIN budget, spent inside the service.
	PinEmailNamespace = "pin-email"

	// PinIPNamespace is the per-client-address PIN budget, spent before the handler.
	PinIPNamespace = "pin-ip"

	// LoginEmailNamespace is the per-address login failure budget, spent inside
	// the login service and released when a login succeeds.
	LoginEmailNamespace = "login-email"

	// LoginIPNamespace is the per-client-address login budget, spent before the
	// handler.
	LoginIPNamespace = "login-ip"

	// GoogleAuthIPNamespace is the per-client-address budget covering all three
	// Google authentication routes, spent before the handler and shared between
	// them. It is a separate counter from LoginIPNamespace, and the two must
	// stay that way: a caller signing in with Google has not spent anything on
	// their manual-login budget, and a caller signing in with a password has not
	// earned a Google one.
	GoogleAuthIPNamespace = "google-auth-ip"
)

// UnavailablePinRateLimiter returns a limiter that cannot answer, standing in for
// Redis being unreachable.
func UnavailablePinRateLimiter(err error) *CountingPinRateLimiter {
	return &CountingPinRateLimiter{Err: err}
}

// ReleaseFailingPinRateLimiter answers every charge and fails every release.
//
// The two failures are not the same event and a caller must not treat them the
// same way. A charge that cannot be taken means the budget could not be
// established, so the request is refused; a release that cannot be taken happens
// after the guarded work already succeeded, and failing there would report an
// authentication as broken when it was not. UnavailablePinRateLimiter fails both,
// which is right for an outage that starts before the request and wrong for one
// that starts after it.
type ReleaseFailingPinRateLimiter struct {
	// Err is what every release reports.
	Err error

	// Releases counts the release attempts, so a test can tell a release that was
	// never attempted from one that was attempted and refused.
	Releases int
}

// NewReleaseFailingPinRateLimiter returns a limiter that charges normally and
// fails to release.
func NewReleaseFailingPinRateLimiter(err error) *ReleaseFailingPinRateLimiter {
	return &ReleaseFailingPinRateLimiter{Err: err}
}

// Allow always reports the request as within budget.
func (l *ReleaseFailingPinRateLimiter) Allow(
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

// Release always fails.
func (l *ReleaseFailingPinRateLimiter) Release(
	_ context.Context,
	_ string,
	_ string,
) (ratelimit.Result, error) {
	l.Releases++

	return ratelimit.Result{}, l.Err
}

// compile-time proof that the fakes satisfy every interface that consumes a
// limiter: the composition root, the feature services, and the route middleware.
var (
	_ auth.RateLimiter            = (*PermissivePinRateLimiter)(nil)
	_ auth.RateLimiter            = (*CountingPinRateLimiter)(nil)
	_ registration.PinRateLimiter = (*PermissivePinRateLimiter)(nil)
	_ registration.PinRateLimiter = (*CountingPinRateLimiter)(nil)
	_ login.LoginRateLimiter      = (*PermissivePinRateLimiter)(nil)
	_ login.LoginRateLimiter      = (*CountingPinRateLimiter)(nil)
	_ middleware.RateLimiter      = (*PermissivePinRateLimiter)(nil)
	_ middleware.RateLimiter      = (*CountingPinRateLimiter)(nil)
)
