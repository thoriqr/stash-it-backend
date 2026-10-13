package integration_test

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// A rolling window is only as good as its atomicity, and atomicity cannot be
// proven with a fake: a fake is exactly the thing that would hide the
// read-modify-write race between pruning, counting and recording. These run
// against the real Redis the queue tests already start, so the Lua script is the
// server's own and the interleaving is real.

const slidingTestSecret = "sliding-window-integration-secret"

// slidingTestPolicy is small on purpose. A large maximum would make the
// concurrency test slow without making it any more convincing, and a large window
// would make nothing here more interesting.
var slidingTestPolicy = ratelimit.SlidingPolicy{
	Max:    5,
	Window: 10 * time.Minute,
}

// newSlidingLimiter returns a sliding-window limiter over the shared test Redis,
// with its own keys cleared so one test cannot see another's entries.
//
// Only the `rlw` prefix is cleared. The fixed-window limiter keeps `rl`, and the
// two families write different types, so clearing one namespace must never
// disturb the other — a test that flushed the whole database would make that
// isolation impossible to observe.
func newSlidingLimiter(
	t *testing.T,
) (*ratelimit.SlidingLimiter, redis.UniversalClient) {
	t.Helper()

	client, err := testutil.StartRedisForTests(t.Context())
	require.NoError(t, err)

	require.NoError(
		t,
		testutil.ResetPINEmailBudgetKeys(t.Context(), client),
	)

	return ratelimit.NewSlidingLimiter(
		client,
		[]byte(slidingTestSecret),
	), client
}

// A budget admits exactly Max and refuses everything past it, and the count it
// reports is the utilisation rather than the number of attempts.
func TestSlidingLimiter_AdmitsUpToTheMaximumThenRefuses(t *testing.T) {
	limiter, _ := newSlidingLimiter(t)
	ctx := t.Context()

	for want := int64(1); want <= slidingTestPolicy.Max; want++ {
		result, err := limiter.Allow(ctx, "pin-email", "alice@example.com", slidingTestPolicy)
		require.NoError(t, err)
		require.True(t, result.Allowed, "call %d should be admitted", want)
		require.Equal(t, want, result.Count)
		require.NotEmpty(t, result.Admission)
	}

	result, err := limiter.Allow(ctx, "pin-email", "alice@example.com", slidingTestPolicy)
	require.NoError(t, err)
	require.False(t, result.Allowed)
	require.Equal(t, slidingTestPolicy.Max, result.Count)
	require.Empty(
		t,
		result.Admission,
		"a refusal records no admission, so there is nothing to hand back",
	)
}

// A refusal is not recorded. This is the deliberate difference from the
// fixed-window limiter, and it is observable: without it a client hammering a
// closed budget would grow a set meant to hold at most Max entries.
func TestSlidingLimiter_RefusalsAreNotRecorded(t *testing.T) {
	limiter, client := newSlidingLimiter(t)
	ctx := t.Context()

	for range slidingTestPolicy.Max {
		_, err := limiter.Allow(ctx, "pin-email", "alice@example.com", slidingTestPolicy)
		require.NoError(t, err)
	}

	const extraAttempts = 50

	for range extraAttempts {
		result, err := limiter.Allow(
			ctx,
			"pin-email",
			"alice@example.com",
			slidingTestPolicy,
		)
		require.NoError(t, err)
		require.False(t, result.Allowed)
	}

	keys, err := client.Keys(ctx, "rlw:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	size, err := client.ZCard(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.EqualValues(
		t,
		slidingTestPolicy.Max,
		size,
		"a refused admission must not grow the set",
	)
}

// A rolling window has no boundary: an entry leaves it a full window after its
// own timestamp, whatever order the calls arrived in. This is the property a
// fixed window cannot express, and the reason the budget is not one.
func TestSlidingLimiter_AnEntryLeavesTheWindowOnItsOwnClock(t *testing.T) {
	limiter, client := newSlidingLimiter(t)
	ctx := t.Context()

	// A short window, so waiting for the oldest entry to age out costs a test a
	// moment rather than a minute.
	policy := ratelimit.SlidingPolicy{Max: 2, Window: 2 * time.Second}

	for range 2 {
		_, err := limiter.Allow(ctx, "rolling", "alice@example.com", policy)
		require.NoError(t, err)
	}

	result, err := limiter.Allow(ctx, "rolling", "alice@example.com", policy)
	require.NoError(t, err)
	require.False(t, result.Allowed)

	// The oldest entry is the one written first, and it is the one that frees the
	// slot — not whichever entry happens to be last.
	require.Positive(t, result.RetryAfter)
	require.LessOrEqual(t, result.RetryAfter, policy.Window)

	time.Sleep(result.RetryAfter + 200*time.Millisecond)

	result, err = limiter.Allow(ctx, "rolling", "alice@example.com", policy)
	require.NoError(t, err)
	require.True(
		t,
		result.Allowed,
		"the window must free as entries age out rather than at a fixed boundary",
	)

	// The set never held more than the maximum at once: one entry aged out and
	// one was written, so the count went back down rather than up.
	keys, err := client.Keys(ctx, "rlw:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	size, err := client.ZCard(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.LessOrEqual(t, size, int64(policy.Max))
	require.Equal(t, int64(1), size, "the entry that aged out must be gone")
}

// The wait is computed from the oldest entry still inside the window, not from
// the key's TTL. The TTL is garbage collection, re-armed to a whole window on
// every call, so a caller refused on a budget whose key was written a moment ago
// would otherwise be told to wait nearly a whole day for a slot that frees in
// seconds.
func TestSlidingLimiter_RetryAfterComesFromTheOldestEntryNotTheKeyTTL(t *testing.T) {
	limiter, client := newSlidingLimiter(t)
	ctx := t.Context()

	policy := ratelimit.SlidingPolicy{Max: 1, Window: 60 * time.Second}

	_, err := limiter.Allow(ctx, "retry-after", "alice@example.com", policy)
	require.NoError(t, err)

	// Well after the key was created, and well before its window ends.
	time.Sleep(2 * time.Second)

	result, err := limiter.Allow(ctx, "retry-after", "alice@example.com", policy)
	require.NoError(t, err)
	require.False(t, result.Allowed)

	keys, err := client.Keys(ctx, "rlw:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	ttl, err := client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)

	require.Greater(
		t,
		result.RetryAfter,
		ttl-5*time.Second,
		"the key is garbage collected to a whole window, which is not the wait",
	)
	require.LessOrEqual(
		t,
		result.RetryAfter,
		policy.Window,
		"the wait can never exceed the window it is measured against",
	)
	require.Greater(t, result.RetryAfter, 55*time.Second)
}

// A key is visible to anything holding the connection string and to a key scan,
// and neither should be able to read an email address back out of it.
func TestSlidingLimiter_KeysNeverCarryTheSubject(t *testing.T) {
	limiter, client := newSlidingLimiter(t)
	ctx := t.Context()

	const address = "alice@example.com"

	_, err := limiter.Allow(ctx, "pin-email", address, slidingTestPolicy)
	require.NoError(t, err)

	// Scoped to this limiter's own namespace rather than to every key in the
	// shared test Redis. The claim is about what this limiter writes, and a
	// neighbouring test's key has nothing to say about it.
	keys, err := client.Keys(ctx, "rlw:pin-email:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	require.NotContains(
		t,
		strings.ToLower(keys[0]),
		strings.ToLower(address),
		"a Redis key must not carry the subject it was derived from",
	)
	require.NotContains(t, keys[0], "example.com")
	require.NotContains(t, keys[0], "@")

	// The namespace survives, because an operator has to be able to find and
	// reason about these counters; the address does not.
	require.True(t, strings.HasPrefix(keys[0], "rlw:"))
	require.Contains(t, keys[0], "pin-email")

	// The member inside the set is opaque too. A derived member would put back
	// inside the value the very thing the key is written to hide, and a timestamp
	// would collide for two admissions in the same millisecond.
	members, err := client.ZRange(ctx, keys[0], 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.NotContains(t, strings.ToLower(members[0]), "alice")
	require.NotContains(t, members[0], "@")

	_, err = uuid.Parse(members[0])
	require.NoError(t, err, "the member must be an opaque random identifier")
}

// The two limiter families write different types under overlapping namespace
// names. A rolling window written over a counter an earlier build left behind
// would fail with WRONGTYPE on every call, which under a fail-closed budget reads
// as an outage rather than as a rollout. Distinct prefixes make them incapable of
// colliding, and a deployment can move without clearing anything.
func TestSlidingLimiter_DoesNotCollideWithTheFixedWindowLimiter(t *testing.T) {
	ctx := t.Context()

	fixed, client := newRateLimiter(t)

	require.NoError(
		t,
		testutil.ResetPINEmailBudgetKeys(ctx, client),
	)

	sliding := ratelimit.NewSlidingLimiter(client, []byte(slidingTestSecret))

	// The fixed-window limiter charges the same namespace a rolling window uses.
	for range 3 {
		_, err := fixed.Allow(ctx, "pin-email", "alice@example.com", ratelimitTestPolicy)
		require.NoError(t, err)
	}

	result, err := sliding.Allow(
		ctx,
		"pin-email",
		"alice@example.com",
		slidingTestPolicy,
	)
	require.NoError(
		t,
		err,
		"a rolling window must not trip over a counter left by a fixed one",
	)
	require.True(t, result.Allowed)
	require.Equal(t, int64(1), result.Count)

	// And the fixed one is untouched by it, which is the other half of the same
	// property.
	fixedResult, err := fixed.Allow(
		ctx,
		"pin-email",
		"alice@example.com",
		ratelimitTestPolicy,
	)
	require.NoError(t, err)
	require.Equal(t, int64(4), fixedResult.Count)
}

// Concurrent admissions must never exceed the maximum. This is the whole reason
// the decision is one script: pruning, counting and recording as three commands
// would let a burst of goroutines all read a count that another was about to
// change.
func TestSlidingLimiter_ConcurrentAdmissionsNeverExceedTheMaximum(t *testing.T) {
	limiter, _ := newSlidingLimiter(t)
	ctx := t.Context()

	const (
		subjects = 8
		attempts = 25
		maximum  = 7
	)

	policy := ratelimit.SlidingPolicy{Max: maximum, Window: 10 * time.Minute}

	var admitted atomic.Int64

	var wait sync.WaitGroup

	for range subjects {
		for range attempts {
			wait.Add(1)

			go func() {
				defer wait.Done()

				// One subject for the whole burst on purpose. Spreading across
				// subjects would give every goroutine its own budget and the
				// maximum would never be in contention.
				result, err := limiter.Allow(
					ctx,
					"concurrent",
					"one-subject",
					policy,
				)

				if err == nil && result.Allowed {
					admitted.Add(1)
				}
			}()
		}
	}

	wait.Wait()

	require.Equal(
		t,
		int64(maximum),
		admitted.Load(),
		"concurrent admissions must not exceed the configured maximum",
	)
}

// Two limiter instances over one Redis share one budget. Without that, a
// deployment running more than one instance would give every instance its own
// allowance, and the limit would be a per-instance fiction.
func TestSlidingLimiter_TwoInstancesShareOneBudget(t *testing.T) {
	ctx := t.Context()

	first, client := newSlidingLimiter(t)
	second := ratelimit.NewSlidingLimiter(client, []byte(slidingTestSecret))

	policy := ratelimit.SlidingPolicy{Max: 3, Window: 10 * time.Minute}

	result, err := first.Allow(ctx, "shared", "alice@example.com", policy)
	require.NoError(t, err)
	require.True(t, result.Allowed)

	result, err = second.Allow(ctx, "shared", "alice@example.com", policy)
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Count, "the second instance must see the first")

	for range 2 {
		result, err = first.Allow(ctx, "shared", "alice@example.com", policy)
		require.NoError(t, err)
	}

	result, err = second.Allow(ctx, "shared", "alice@example.com", policy)
	require.NoError(t, err)
	require.False(t, result.Allowed, "the budget is one budget across instances")
}

// A refund gives exactly one admission back, identified by the member Allow
// returned. A second refund of the same admission removes nothing, which is what
// makes it safe to call twice.
func TestSlidingLimiter_ReleaseRemovesExactlyOneAdmission(t *testing.T) {
	limiter, _ := newSlidingLimiter(t)
	ctx := t.Context()

	policy := ratelimit.SlidingPolicy{Max: 3, Window: 10 * time.Minute}

	first, err := limiter.Allow(ctx, "refund", "alice@example.com", policy)
	require.NoError(t, err)
	require.True(t, first.Allowed)

	_, err = limiter.Allow(ctx, "refund", "alice@example.com", policy)
	require.NoError(t, err)

	_, err = limiter.Allow(ctx, "refund", "alice@example.com", policy)
	require.NoError(t, err)

	result, err := limiter.Allow(ctx, "refund", "alice@example.com", policy)
	require.NoError(t, err)
	require.False(t, result.Allowed)

	require.NoError(
		t,
		limiter.Release(ctx, "refund", "alice@example.com", first.Admission),
	)

	result, err = limiter.Allow(ctx, "refund", "alice@example.com", policy)
	require.NoError(t, err)
	require.True(t, result.Allowed, "a refunded slot must be usable again")

	// Releasing twice must not hand out a second slot.
	require.NoError(
		t,
		limiter.Release(ctx, "refund", "alice@example.com", first.Admission),
	)

	result, err = limiter.Allow(ctx, "refund", "alice@example.com", policy)
	require.NoError(t, err)
	require.False(
		t,
		result.Allowed,
		"a second release must not free another slot",
	)
}

// Releasing an admission that was never recorded is a no-op rather than an error,
// because the member names exactly one entry. A caller that unwound twice must
// not be told its request failed when nothing went wrong.
func TestSlidingLimiter_ReleaseOfAnUnknownAdmissionIsANoOp(t *testing.T) {
	limiter, _ := newSlidingLimiter(t)
	ctx := t.Context()

	policy := ratelimit.SlidingPolicy{Max: 2, Window: 10 * time.Minute}

	_, err := limiter.Allow(ctx, "refund-empty", "alice@example.com", policy)
	require.NoError(t, err)

	require.NoError(
		t,
		limiter.Release(ctx, "refund-empty", "alice@example.com", ""),
		"an empty admission is not a refund",
	)

	require.NoError(
		t,
		limiter.Release(
			ctx,
			"refund-empty",
			"alice@example.com",
			"not-a-member-that-was-ever-written",
		),
	)
}

// A refund must never bring a budget into existence. Creating the key to remove
// something from it would mean a deployment that had never sent a message held
// one.
func TestSlidingLimiter_ReleaseDoesNotCreateTheKey(t *testing.T) {
	limiter, client := newSlidingLimiter(t)
	ctx := t.Context()

	require.NoError(
		t,
		limiter.Release(ctx, "never-used", "alice@example.com", "anything"),
	)

	keys, err := client.Keys(ctx, "rlw:*").Result()
	require.NoError(t, err)
	require.Empty(t, keys)
}

// A policy that cannot be enforced is reported rather than half-applied. A
// maximum of zero would refuse every PIN with no error anywhere, which is
// indistinguishable from the feature being broken.
func TestSlidingLimiter_UnusablePoliciesAreRejected(t *testing.T) {
	limiter, _ := newSlidingLimiter(t)
	ctx := t.Context()

	for name, policy := range map[string]ratelimit.SlidingPolicy{
		"zero maximum":      {Max: 0, Window: time.Minute},
		"negative maximum":  {Max: -1, Window: time.Minute},
		"zero window":       {Max: 1, Window: 0},
		"sub-second window": {Max: 1, Window: time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := limiter.Allow(ctx, "invalid", "alice@example.com", policy)
			require.Error(t, err)
		})
	}
}

// An unreachable Redis is not permission. The limiter has not said the subject is
// within budget, it has said nothing, and a limiter that cannot answer must not
// be treated as one that allows.
func TestSlidingLimiter_UnreachableRedisIsAnError(t *testing.T) {
	unreachable := ratelimit.NewSlidingLimiter(
		redis.NewClient(&redis.Options{
			Addr:         "127.0.0.1:1",
			DialTimeout:  250 * time.Millisecond,
			MaxRetries:   -1,
			ReadTimeout:  250 * time.Millisecond,
			WriteTimeout: 250 * time.Millisecond,
		}),
		[]byte(slidingTestSecret),
	)

	result, err := unreachable.Allow(
		t.Context(),
		"pin-email",
		"alice@example.com",
		slidingTestPolicy,
	)
	require.Error(t, err)
	require.False(
		t,
		result.Allowed,
		"a budget that could not be evaluated must not report permission",
	)
	require.Empty(t, result.Admission)
}
