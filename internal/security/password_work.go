package security

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Password-work concurrency is a different control from rate limiting, and this
// file exists to keep the two apart.
//
// A rate limit bounds how often one subject may submit requests. It does not
// bound how much expensive work is running right now, because a limit per
// subject composes: a caller holding a thousand addresses holds a thousand
// budgets, and every one of those budgets can be spent at the same moment. What
// bounds the work itself is a limit on how many operations are in flight across
// every subject at once, which is what this does and what no rate limit can.
//
// It matters here because of the cost. One Argon2id derivation at the active
// parameters in password.go allocates its memory up front and keeps it for the
// whole derivation, so simultaneous operations multiply the instance's memory
// directly. On a constrained instance a handful of concurrent logins from
// unrelated callers is the difference between serving requests and being killed
// by the OOM killer, and the callers causing it are frequently not the ones
// being rate limited.

// ErrPasswordWorkCapacityTimeout reports that no slot became free before the
// configured wait ran out.
//
// It is distinct from a cancelled context on purpose. The two ask the caller to
// do different things: this one means the server is busy and will be again
// shortly, while a cancellation means the caller went away and the work should
// simply stop. Collapsing them would leave a caller unable to tell a transient
// refusal from its own request ending, which is the difference between a client
// that retries later and one that retries forever.
var ErrPasswordWorkCapacityTimeout = errors.New(
	"password work capacity was not available within the wait",
)

// PasswordWorkLimiter bounds how many expensive password operations may be in
// flight at the same time, across every subject and every feature that performs
// one.
//
// It is safe for concurrent use and keeps no queue, no per-caller state and no
// goroutine of its own, so a caller that walks away mid-wait costs nothing but
// the goroutine it already had. What it does keep is one closure per successful
// acquisition, which is what makes a release belong to the acquisition that
// earned it.
//
// It is NOT a rate limiter and shares nothing with ratelimit.Limiter. The two
// are configured, observed and reasoned about separately, and a caller may be
// comfortably inside every budget it has and still be refused here.
type PasswordWorkLimiter struct {
	// changed carries a wakeup, never a permit. Its capacity is one whatever the
	// limit is: it only has to make a waiter re-read the count, and a re-read is
	// free and idempotent. Treating it as a permit would put the limit's soundness
	// in the hands of a channel nobody drains.
	changed chan struct{}

	mu   sync.Mutex
	held int

	maxConcurrent int
	wait          time.Duration
}

// NewPasswordWorkLimiter returns a limiter admitting at most maxConcurrent
// simultaneous operations, each caller waiting at most wait for a slot.
//
// The numbers are the caller's to choose, and deliberately not defaulted here.
// What they mean depends on the instance: one derivation costs a fixed amount of
// memory, so the concurrency that is safe is a fraction of the memory the
// deployment actually has, and a value chosen once in this package would be a
// deployment decision wearing the costume of a constant. The caller reads both
// from configuration and can change them without this changing.
//
// A wait of zero is allowed and refuses rather than queues. That is a real
// choice rather than a degenerate one — a deployment that would rather shed
// password work than hold a request open gets exactly that — so it is permitted
// rather than rejected.
func NewPasswordWorkLimiter(
	maxConcurrent int,
	wait time.Duration,
) (*PasswordWorkLimiter, error) {
	if maxConcurrent < 1 {
		// Not defaulted or corrected. Zero simultaneous operations is not a slower
		// configuration of this feature, it is every password login and every
		// registration refused, which nobody means to ask for and which is much
		// easier to notice here than as a lockout nobody can explain.
		return nil, fmt.Errorf(
			"password work concurrency must be positive, got %d",
			maxConcurrent,
		)
	}

	if wait < 0 {
		return nil, fmt.Errorf(
			"password work wait must not be negative, got %s",
			wait,
		)
	}

	return &PasswordWorkLimiter{
		changed:       make(chan struct{}, 1),
		maxConcurrent: maxConcurrent,
		wait:          wait,
	}, nil
}

// Acquire takes one slot and blocks at most the configured wait for one.
//
// On success it returns the release that gives the slot back. That release
// belongs to this acquisition and to no other, and calling it more than once is
// harmless: the second and later calls do nothing. The pairing is what keeps the
// limit honest, so a caller should take the release at the point of success and
// arrange for it to run — a defer immediately after the error check is the
// shape that leaves nothing to forget:
//
//	release, err := limiter.Acquire(ctx)
//	if err != nil {
//		return err
//	}
//	defer release()
//
// On failure it returns a nil release, so a caller that ignores the error cannot
// accidentally give back a slot it was never given.
//
// The three outcomes are distinguishable, and a caller is meant to treat them
// differently:
//
//   - nil error: the operation may proceed, and must release when it finishes.
//   - ErrPasswordWorkCapacityTimeout: capacity did not free up in time. The
//     server is busy, the work was never started, and trying again shortly is
//     the right response.
//   - anything else: ctx.Err(). The caller went away, or its deadline passed,
//     and the work was never started. Retrying is not the response; stopping is.
//
// ctx is checked before the fast path and again throughout the wait, so a
// request that was already cancelled does not start an expensive derivation it
// will never finish.
func (l *PasswordWorkLimiter) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// The uncontended case allocates no timer and does no waiting, which is what
	// a deployment running at concurrency one looks like almost all of the time.
	if l.tryAcquire() {
		return l.releaseOnce(), nil
	}

	// One timer for the whole call, not one per retry. The bound is on how long a
	// caller may be kept waiting, and a fresh timer per loop iteration would let
	// a stream of releases keep a caller parked indefinitely.
	timer := time.NewTimer(l.wait)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-l.changed:
			// A release may have happened, or this wakeup may be stale. Only the
			// count under the mutex says which, and it is cheap to ask.
			if l.tryAcquire() {
				return l.releaseOnce(), nil
			}
		case <-timer.C:
			// A cancellation that landed in the same instant as the timer must
			// still read as a cancellation. select chooses at random among ready
			// cases, so without this a cancelled caller could be told it was merely
			// busy and told to come back.
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			return nil, ErrPasswordWorkCapacityTimeout
		}
	}
}

// releaseOnce returns the release belonging to one successful acquisition.
//
// Each call produces its own closure over its own sync.Once, and that is the
// whole ownership guarantee. A caller may call its release twice, or call it
// again after some other caller has taken a slot, and the extra calls are
// dropped instead of decrementing a count that no longer belongs to it. Without
// this, a release that ran again for an acquisition that had already completed
// would hand back a slot an unrelated operation was relying on, and the limiter
// would admit one more operation than its limit — which is the exact failure
// this exists to make impossible.
//
// The closure is deliberately not a named exported type. A caller can only get
// one from a successful Acquire, so there is nothing to construct by hand and
// nothing to store somewhere it could outlive the acquisition it came from.
func (l *PasswordWorkLimiter) releaseOnce() func() {
	var once sync.Once

	return func() {
		once.Do(l.release)
	}
}

// release returns one slot and wakes a waiter. It runs at most once per
// acquisition, because the closure that calls it is once-guarded.
func (l *PasswordWorkLimiter) release() {
	l.mu.Lock()
	held := l.held

	if held > 0 {
		l.held--
	}

	l.mu.Unlock()

	// Nothing to announce: there was nothing held, so nothing changed and a
	// waiter that woke for it would learn the same thing by asking again.
	//
	// The clamp above is a backstop, not the ownership mechanism. Every release
	// reaching here belongs to a real acquisition, so held is at least one; were
	// it ever not, the count is still never driven below zero and the limiter
	// still cannot hand out capacity that was never taken.
	if held == 0 {
		return
	}

	select {
	case l.changed <- struct{}{}:
	default:
		// A wakeup is already pending, which is enough: whoever takes it re-reads
		// the count and finds the slot this release returned. Dropping it here
		// cannot lose a wakeup, because the one in flight is the same signal.
	}
}

// tryAcquire takes a slot if one is free, and reports whether it got one.
func (l *PasswordWorkLimiter) tryAcquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.held >= l.maxConcurrent {
		return false
	}

	l.held++

	return true
}
