package security

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These tests never sleep to arrange a situation. Where something has to be
// observed not happening, the observation is a bounded negative assertion rather
// than a pause, and where a result has to be awaited the wait is a timeout on
// the channel rather than an assumption about scheduling. The only place real
// time passes is where the timeout behaviour is itself the thing under test.

// awaitResult waits for one value with a ceiling that is generous enough that a
// loaded machine cannot fail the test, and fails the test outright if it never
// arrives. A hang is a defect being reported, not a flake to be retried.
func awaitResult(t *testing.T, ch <-chan error) error {
	t.Helper()

	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the limiter never returned; it is deadlocked or leaked")

		return nil
	}
}

// awaitClosed waits for a channel to be closed, on the same ceiling.
func awaitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the limiter never returned; it is deadlocked or leaked")
	}
}

// newLimiter builds a limiter or fails the test on a configuration the
// constructor refuses.
func newLimiter(
	t *testing.T,
	maxConcurrent int,
	wait time.Duration,
) *PasswordWorkLimiter {
	t.Helper()

	limiter, err := NewPasswordWorkLimiter(maxConcurrent, wait)
	require.NoError(t, err)

	return limiter
}

// acquire is the ordinary success path: take a slot and hand back its release,
// failing the test if there was none.
func acquire(t *testing.T, limiter *PasswordWorkLimiter) func() {
	t.Helper()

	release, err := limiter.Acquire(t.Context())
	require.NoError(t, err)
	require.NotNil(t, release, "a successful acquisition must hand back a release")

	return release
}

// A configuration nobody meant is reported rather than corrected.
//
// Zero simultaneous operations is not a slower version of this feature, it is
// every password login and registration refused — a self-inflicted lockout that
// is much easier to catch at startup than as a support report. A negative wait
// would make the bound meaningless rather than merely strict.
func TestNewPasswordWorkLimiter_RejectsAnUnusableConfiguration(t *testing.T) {
	cases := map[string]struct {
		maxConcurrent int
		wait          time.Duration
		errContains   string
	}{
		"zero concurrency": {
			maxConcurrent: 0,
			wait:          time.Second,
			errContains:   "must be positive",
		},
		"negative concurrency": {
			maxConcurrent: -1,
			wait:          time.Second,
			errContains:   "must be positive",
		},
		"a negative wait": {
			maxConcurrent: 1,
			wait:          -time.Second,
			errContains:   "must not be negative",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			limiter, err := NewPasswordWorkLimiter(tc.maxConcurrent, tc.wait)

			require.Error(t, err)
			require.Nil(t, limiter)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// The deployment baseline has to be expressible, and a limiter that refuses to
// wait is a real choice rather than a degenerate one.
func TestNewPasswordWorkLimiter_AcceptsTheBaselineAndAZeroWait(t *testing.T) {
	require.NotNil(t, newLimiter(t, 1, time.Second))

	require.NotNil(
		t,
		newLimiter(t, 1, 0),
		"a deployment that would rather shed password work than hold a request "+
			"open must be able to say so",
	)
}

// A failed acquisition hands back nothing to release.
//
// A nil release is what makes the error path safe: there is no value a caller
// could call by mistake, and nothing that could give back a slot it never held.
func TestPasswordWorkLimiter_AFailedAcquisitionHandsBackNoRelease(t *testing.T) {
	limiter := newLimiter(t, 1, 20*time.Millisecond)

	release := acquire(t, limiter)
	defer release()

	refused, err := limiter.Acquire(t.Context())

	require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)
	require.Nil(t, refused, "a refusal must not hand back a release")
}

// A release is idempotent: calling it again for an acquisition that has already
// completed changes nothing.
func TestPasswordWorkLimiter_AReleaseIsIdempotent(t *testing.T) {
	const maxConcurrent = 2

	limiter := newLimiter(t, maxConcurrent, 20*time.Millisecond)

	release := acquire(t, limiter)
	release()

	for range 5 {
		released := make(chan struct{})

		go func() {
			release()
			close(released)
		}()

		awaitClosed(t, released)
	}

	// Still exactly two, and no more.
	for range maxConcurrent {
		acquire(t, limiter)
	}

	_, err := limiter.Acquire(t.Context())
	require.ErrorIs(
		t,
		err,
		ErrPasswordWorkCapacityTimeout,
		"calling a release twice widened the limit",
	)
}

// The regression this API exists for.
//
// A release that runs again for an acquisition that has already finished must not
// hand back a slot that a different operation is relying on. Before each
// acquisition carried its own release, this sequence let two operations run at
// once against a limit of one: A finishes, B takes the slot, A's release runs
// again and frees B's, and C is admitted while B is still working.
func TestPasswordWorkLimiter_AStaleReleaseCannotFreeAnotherCallersSlot(t *testing.T) {
	const maxConcurrent = 1

	limiter := newLimiter(t, maxConcurrent, 20*time.Millisecond)

	// A acquires and finishes, keeping its release.
	stale := acquire(t, limiter)
	stale()

	// B now holds the only slot and is still working.
	bRelease := acquire(t, limiter)

	// A's release runs again, long after A finished and while B is mid-operation.
	stale()
	stale()

	// C must be refused. B is genuinely holding the slot, so admitting C is the
	// exact failure being guarded against.
	_, err := limiter.Acquire(t.Context())
	require.ErrorIs(
		t,
		err,
		ErrPasswordWorkCapacityTimeout,
		"a stale release freed another caller's slot",
	)

	// And when B finishes for real, the slot is genuinely available again, which
	// is what proves B's slot was still held throughout rather than merely
	// unavailable.
	bRelease()

	cRelease := acquire(t, limiter)
	cRelease()
}

// The same guarantee with more than one slot: a stale release must not free a
// slot that some other caller took afterwards.
func TestPasswordWorkLimiter_AStaleReleaseCannotWidenAMultiSlotLimit(t *testing.T) {
	const maxConcurrent = 3

	limiter := newLimiter(t, maxConcurrent, 20*time.Millisecond)

	stale := acquire(t, limiter)
	stale()

	held := make([]func(), 0, maxConcurrent)

	for range maxConcurrent {
		held = append(held, acquire(t, limiter))
	}

	// Three extra calls, all for an acquisition that completed long ago.
	stale()
	stale()
	stale()

	_, err := limiter.Acquire(t.Context())
	require.ErrorIs(
		t,
		err,
		ErrPasswordWorkCapacityTimeout,
		"stale releases widened a multi-slot limit",
	)

	for _, release := range held {
		release()
	}
}

// Two acquisitions each holding a release are independent of one another: one's
// release must not affect the other's slot, however many times it is called.
//
// The limit of two is what makes this observable. After the first acquisition's
// repeated releases, exactly one slot must be free — the one it held. Had the
// extra releases counted, two would be free and the fourth acquisition below
// would succeed where it must fail.
func TestPasswordWorkLimiter_EachAcquisitionOwnsItsOwnSlot(t *testing.T) {
	const maxConcurrent = 2

	limiter := newLimiter(t, maxConcurrent, 20*time.Millisecond)

	first := acquire(t, limiter)
	second := acquire(t, limiter)

	// Releasing the first repeatedly must free its slot once and only once.
	first()
	first()
	first()

	// That leaves exactly one free slot, which the third takes.
	third := acquire(t, limiter)

	_, err := limiter.Acquire(t.Context())
	require.ErrorIs(
		t,
		err,
		ErrPasswordWorkCapacityTimeout,
		"the second and third slots are both in use, so the limit is reached",
	)

	second()
	third()
}

// The limit is the whole point, so it is asserted as an invariant over many
// callers rather than as a schedule.
//
// Each caller takes a slot, records how many are held at that moment and gives
// it straight back. What is asserted is that the highest number ever observed is
// within the limit — which no interleaving can violate, because a slot has to be
// taken before the work starts — and not how many callers happened to win.
func TestPasswordWorkLimiter_CapacityIsNeverExceeded(t *testing.T) {
	const (
		maxConcurrent = 4
		callers       = 200
	)

	limiter := newLimiter(t, maxConcurrent, time.Second)

	var held atomic.Int64
	var peak atomic.Int64

	var wg sync.WaitGroup

	acquired := make(chan error, callers)

	wg.Add(callers)

	for range callers {
		go func() {
			defer wg.Done()

			release, err := limiter.Acquire(t.Context())
			if err != nil {
				acquired <- err

				return
			}

			current := held.Add(1)

			for {
				observed := peak.Load()
				if current <= observed || peak.CompareAndSwap(observed, current) {
					break
				}
			}

			held.Add(-1)
			release()

			acquired <- nil
		}()
	}

	wg.Wait()
	close(acquired)

	for err := range acquired {
		require.NoError(t, err, "a one second wait should absorb this many callers")
	}

	require.LessOrEqual(
		t,
		peak.Load(),
		int64(maxConcurrent),
		"more operations ran at once than the limit allows",
	)
}

// Holding every slot by hand makes the limit observable without any scheduling
// assumption at all: the test knows the count because it did it.
func TestPasswordWorkLimiter_RefusesOnceEverySlotIsHeld(t *testing.T) {
	const maxConcurrent = 2

	limiter := newLimiter(t, maxConcurrent, 20*time.Millisecond)

	releases := make([]func(), 0, maxConcurrent)

	for range maxConcurrent {
		releases = append(releases, acquire(t, limiter))
	}

	_, err := limiter.Acquire(t.Context())
	require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)

	for _, release := range releases {
		release()
	}
}

// A caller that arrives while the limit is full proceeds as soon as a slot
// comes back, rather than being refused or waiting out its whole budget.
func TestPasswordWorkLimiter_AWaitingCallerProceedsOnceASlotIsReleased(t *testing.T) {
	limiter := newLimiter(t, 1, 10*time.Second)

	held := acquire(t, limiter)

	waiting := make(chan error, 1)

	go func() {
		_, err := limiter.Acquire(t.Context())
		waiting <- err
	}()

	// The waiter must genuinely be waiting rather than about to return anyway.
	// This is a bounded negative assertion: it can fail if the limiter is broken
	// in one direction, and it cannot flake on a slow machine because a slow
	// machine only ever makes the wrong behaviour take longer to appear.
	require.Never(
		t,
		func() bool {
			select {
			case <-waiting:
				return true
			default:
				return false
			}
		},
		100*time.Millisecond,
		10*time.Millisecond,
		"the caller acquired a slot while every slot was held",
	)

	held()

	require.NoError(
		t,
		awaitResult(t, waiting),
		"a slot was released, so the waiting caller must proceed",
	)
}

// The refusal arrives when the budget runs out, not before it, and not long
// after it. The lower bound is what distinguishes a bounded wait from a refusal
// dressed up as one; the upper bound is generous enough that a loaded machine
// cannot trip it.
func TestPasswordWorkLimiter_RefusesOnceTheBoundedWaitExpires(t *testing.T) {
	const wait = 50 * time.Millisecond

	limiter := newLimiter(t, 1, wait)
	release := acquire(t, limiter)

	started := time.Now()
	_, err := limiter.Acquire(t.Context())
	elapsed := time.Since(started)

	require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)

	require.GreaterOrEqual(
		t,
		elapsed,
		wait,
		"the caller gave up before its wait had run out",
	)
	require.Less(
		t,
		elapsed,
		10*time.Second,
		"the wait was not bounded",
	)

	release()
}

// A zero wait refuses immediately. This is the configuration a deployment that
// prefers shedding work to holding requests chooses, so it must not turn into an
// accidental queue.
func TestPasswordWorkLimiter_AZeroWaitRefusesWithoutQueueing(t *testing.T) {
	limiter := newLimiter(t, 1, 0)
	release := acquire(t, limiter)

	started := time.Now()
	_, err := limiter.Acquire(t.Context())

	require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)
	require.Less(t, time.Since(started), time.Second)

	release()
}

// Cancellation is not a capacity problem and must never be reported as one. A
// caller told it was merely busy would be told to come back, when in fact it had
// gone away.
func TestPasswordWorkLimiter_ACanceledContextStopsWaitingPromptly(t *testing.T) {
	limiter := newLimiter(t, 1, time.Hour)
	held := acquire(t, limiter)

	ctx, cancel := context.WithCancel(t.Context())

	waiting := make(chan error, 1)

	go func() {
		_, err := limiter.Acquire(ctx)
		waiting <- err
	}()

	cancel()

	err := awaitResult(t, waiting)

	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(
		t,
		err,
		ErrPasswordWorkCapacityTimeout,
		"a caller that went away must not be told to come back later",
	)

	held()
}

// A context already cancelled when the call arrives is refused without taking a
// slot, so a request that is never going to finish cannot start an expensive
// derivation on the way out.
func TestPasswordWorkLimiter_AnAlreadyCancelledContextTakesNoSlot(t *testing.T) {
	limiter := newLimiter(t, 1, time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := limiter.Acquire(ctx)
	require.ErrorIs(t, err, context.Canceled)

	// The slot must still be free, which is only true if the refusal cost
	// nothing.
	release := acquire(t, limiter)
	release()
}

// A deadline on the caller's own context is the caller's deadline, and stays
// distinguishable from the limiter giving up on its own.
func TestPasswordWorkLimiter_ADeadlineIsReportedAsTheCallersDeadline(t *testing.T) {
	limiter := newLimiter(t, 1, time.Hour)
	held := acquire(t, limiter)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	_, err := limiter.Acquire(ctx)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrPasswordWorkCapacityTimeout)

	held()
}

// Capacity is reusable, not a one-shot budget that leaks.
//
// The wait is zero so that a slot which never came back fails immediately rather
// than after a timeout: the point is that each round finds the capacity whole
// again, and a limiter that quietly lost a slot would show up as a refusal
// rather than as a test that runs long and then fails.
func TestPasswordWorkLimiter_CapacityIsAvailableAgainAfterEachRelease(t *testing.T) {
	limiter := newLimiter(t, 2, 0)

	for range 10 {
		release := acquire(t, limiter)
		release()

		// The slot must be usable again straight away.
		again := acquire(t, limiter)
		again()
	}
}

// Interleaved acquisitions and releases under the race detector. The assertions
// are about finishing at all and about the bound holding, because which callers
// win a slot is not a property worth pinning down.
func TestPasswordWorkLimiter_ConcurrentAcquisitionAndRelease(t *testing.T) {
	const (
		maxConcurrent = 3
		callers       = 150
	)

	limiter := newLimiter(t, maxConcurrent, 50*time.Millisecond)

	var held atomic.Int64
	var peak atomic.Int64

	var wg sync.WaitGroup

	wg.Add(callers)

	for i := range callers {
		go func(i int) {
			defer wg.Done()

			release, err := limiter.Acquire(t.Context())
			if err != nil {
				return
			}

			current := held.Add(1)

			for {
				observed := peak.Load()
				if current <= observed || peak.CompareAndSwap(observed, current) {
					break
				}
			}

			held.Add(-1)
			release()

			// Every odd caller calls its release again, which is the misuse this
			// is here to show is survivable rather than corrupting.
			if i%2 == 1 {
				release()
			}
		}(i)
	}

	wg.Wait()

	require.LessOrEqual(t, peak.Load(), int64(maxConcurrent))
}
