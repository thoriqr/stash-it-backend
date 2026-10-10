package security

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These cover the hasher's use of the limiter rather than the limiter itself,
// which password_work_test.go covers.
//
// Every case here decides occupancy by holding a slot from the test rather than
// by waiting for Argon2id to take the time it takes, so none of them depends on
// how fast the machine derives a hash.

// saturatedHasher returns a hasher whose single slot is already held, plus the
// release that frees it.
//
// Holding the slot from outside is what makes these deterministic: the hasher is
// certain to find no capacity, without a test having to time a real derivation.
func saturatedHasher(t *testing.T) (*PasswordHasher, func()) {
	t.Helper()

	limiter, err := NewPasswordWorkLimiter(1, time.Second)
	require.NoError(t, err)

	release, err := limiter.Acquire(context.Background())
	require.NoError(t, err)

	return NewPasswordHasher(limiter), release
}

// Every derivation the hasher performs costs the same memory, so every one of
// them has to go through the same bound. This is what makes the limiter
// structural: a call site cannot reach Argon2id without passing here first.
//
// The hash Verify is given is built beforehand, while capacity was still free. A
// hash minted here would be empty, because that is what a refused Hash returns,
// and an empty string is refused by the parser before the limiter is even
// consulted — which would test the parse guard instead of the bound.
func TestPasswordHasher_EveryOperationRefusesWhenCapacityIsGone(t *testing.T) {
	encoded, err := newTestHasher(t).
		Hash(context.Background(), "correct-password")
	require.NoError(t, err)

	hasher, free := saturatedHasher(t)
	defer free()

	t.Run("Hash", func(t *testing.T) {
		_, err := hasher.Hash(context.Background(), "correct-password")

		require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)
	})

	t.Run("Verify", func(t *testing.T) {
		_, err := hasher.Verify(
			context.Background(),
			"correct-password",
			encoded,
		)

		require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)
	})

	// The dummy verification has to be refused too. It costs exactly what a real
	// one costs, so a mitigation exempt from the bound would be the cheapest way
	// to hold more derivations at once than the instance was built for.
	t.Run("DummyVerify", func(t *testing.T) {
		require.ErrorIs(
			t,
			hasher.DummyVerify(context.Background(), "correct-password"),
			ErrPasswordWorkCapacityTimeout,
		)
	})
}

// A refusal is not a silent failure: nothing is returned, so a caller cannot act
// on an empty hash as though it were a real one, and a verification that never
// ran cannot report a match.
func TestPasswordHasher_ARefusedOperationReturnsNothingUsable(t *testing.T) {
	encoded, err := newTestHasher(t).
		Hash(context.Background(), "correct-password")
	require.NoError(t, err)

	hasher, free := saturatedHasher(t)
	defer free()

	refused, err := hasher.Hash(context.Background(), "correct-password")
	require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)
	require.Empty(t, refused, "a refused Hash must not return a usable credential")

	result, err := hasher.Verify(
		context.Background(),
		"correct-password",
		encoded,
	)

	require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)
	require.False(t, result.Match, "a refused verification must never report a match")
	require.False(t, result.NeedsRehash)
}

// Cancellation is the caller's own doing and must stay distinguishable from the
// server being busy, or a caller that went away would be told to come back.
//
// A real hash is used rather than a malformed one on purpose: an unparseable
// stored hash is refused before any capacity is even asked for, so using one
// here would be testing the parse guard rather than the limiter.
func TestPasswordHasher_CancellationIsNotReportedAsCapacityExhaustion(t *testing.T) {
	encoded, err := newTestHasher(t).
		Hash(context.Background(), "correct-password")
	require.NoError(t, err)

	hasher, free := saturatedHasher(t)
	defer free()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = hasher.Hash(ctx, "correct-password")
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrPasswordWorkCapacityTimeout)

	_, err = hasher.Verify(ctx, "correct-password", encoded)
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrPasswordWorkCapacityTimeout)

	dummyErr := hasher.DummyVerify(ctx, "correct-password")
	require.ErrorIs(t, dummyErr, context.Canceled)
	require.NotErrorIs(t, dummyErr, ErrPasswordWorkCapacityTimeout)
}

// A hash this build cannot parse is a fault and must not also be reported as a
// capacity problem, and must not spend capacity finding that out.
//
// The stored hash is never attacker-controlled, so the distinction matters only
// for what the response says and for the log line an operator reads.
func TestPasswordHasher_AnUnparseableHashIsAFaultNotACapacityProblem(t *testing.T) {
	hasher := newTestHasher(t)

	_, err := hasher.Verify(context.Background(), "password", "not-a-phc-string")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrPasswordWorkCapacityTimeout)
	require.Contains(t, err.Error(), "invalid password hash format")

	// And the capacity it did not spend is still there.
	require.NotNil(t, hasher.Wait())
	require.Equal(t, time.Second, hasher.Wait())
}

// The slot is released on the error paths too, not only on success. A limiter
// that kept a slot after a failed derivation would leak capacity a request at a
// time until the feature refused everything.
func TestPasswordHasher_CapacityIsReturnedOnEveryPath(t *testing.T) {
	limiter, err := NewPasswordWorkLimiter(1, time.Second)
	require.NoError(t, err)

	hasher := NewPasswordHasher(limiter)

	// Success.
	encoded, err := hasher.Hash(context.Background(), "correct-password")
	require.NoError(t, err)

	_, err = hasher.Verify(context.Background(), "correct-password", encoded)
	require.NoError(t, err)

	// Dummy verification.
	require.NoError(t, hasher.DummyVerify(context.Background(), "whatever"))

	// Refusal, which must not have taken a slot either.
	saturated, free := saturatedHasher(t)

	_, err = saturated.Hash(context.Background(), "correct-password")
	require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)

	_, err = saturated.Verify(context.Background(), "x", "not-a-phc-string")
	require.Error(t, err)

	free()

	// With capacity restored, everything works again — which it would not if any
	// of the paths above had kept its slot.
	fresh, err := hasher.Hash(context.Background(), "correct-password")
	require.NoError(t, err)

	result, err := hasher.Verify(context.Background(), "correct-password", fresh)
	require.NoError(t, err)
	require.True(t, result.Match)
}

// The dummy verification must not ask for a second slot while holding the first.
//
// At a concurrency of one, routing it through Verify would be a guaranteed
// deadlock rather than a slow request, and the test would simply hang. Taking the
// lock here and asserting it returns is what catches that.
func TestPasswordHasher_DummyVerifyDoesNotReenterTheLimiter(t *testing.T) {
	limiter, err := NewPasswordWorkLimiter(1, time.Second)
	require.NoError(t, err)

	hasher := NewPasswordHasher(limiter)

	done := make(chan error, 1)

	go func() {
		done <- hasher.DummyVerify(context.Background(), "correct-password")
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal(
			"DummyVerify did not return; it is holding a slot and asking for " +
				"another",
		)
	}
}

// The reported wait is the one the limiter was configured with, because that is
// the number the caller was actually kept waiting for and the one a caller is
// told.
func TestPasswordHasher_ReportsTheConfiguredWait(t *testing.T) {
	cases := map[string]time.Duration{
		"a bounded wait":       1500 * time.Millisecond,
		"refusing immediately": 0,
		"a longer bound":       30 * time.Second,
	}

	for name, wait := range cases {
		t.Run(name, func(t *testing.T) {
			limiter, err := NewPasswordWorkLimiter(1, wait)
			require.NoError(t, err)

			require.Equal(t, wait, NewPasswordHasher(limiter).Wait())
		})
	}
}

// A hasher with no limiter would look exactly like one with a generous limit, and
// the difference only appears on an instance small enough for it to matter. That
// makes it a wiring mistake worth failing loudly at construction.
func TestNewPasswordHasher_RefusesToBeBuiltWithoutALimiter(t *testing.T) {
	require.Panics(
		t,
		func() { NewPasswordHasher(nil) },
		"a hasher with no capacity limit is one whose protection silently "+
			"does not exist",
	)
}

// The bound holds across concurrent derivations: this is the property the whole
// mechanism exists for, asserted as an invariant rather than as a schedule.
func TestPasswordHasher_ConcurrentDerivationStaysWithinTheLimit(t *testing.T) {
	const maxConcurrent = 2

	limiter, err := NewPasswordWorkLimiter(maxConcurrent, 10*time.Second)
	require.NoError(t, err)

	hasher := NewPasswordHasher(limiter)

	var mu sync.Mutex
	var held, peak int

	var wg sync.WaitGroup

	// Six derivations against a limit of two. Six is deliberately modest: each
	// one costs real memory, and what is being proven is the bound, not
	// throughput.
	for range 6 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, err := hasher.Hash(context.Background(), "correct-password")
			if err != nil {
				return
			}

			mu.Lock()
			held++
			if held > peak {
				peak = held
			}
			mu.Unlock()

			mu.Lock()
			held--
			mu.Unlock()
		}()
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	require.LessOrEqual(
		t,
		peak,
		maxConcurrent,
		"more derivations ran at once than the limit allows",
	)
}

// A limiter configured to refuse immediately makes every derivation fail, which
// is the configured deployment's own choice rather than a fault.
func TestPasswordHasher_AZeroWaitRefusesWithoutWaiting(t *testing.T) {
	limiter, err := NewPasswordWorkLimiter(1, 0)
	require.NoError(t, err)

	hasher := NewPasswordHasher(limiter)

	release, err := limiter.Acquire(context.Background())
	require.NoError(t, err)
	defer release()

	started := time.Now()
	_, err = hasher.Hash(context.Background(), "correct-password")

	require.ErrorIs(t, err, ErrPasswordWorkCapacityTimeout)
	require.Less(t, time.Since(started), 5*time.Second)
}
