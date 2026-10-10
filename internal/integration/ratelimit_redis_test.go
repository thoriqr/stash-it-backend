package integration_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// The limiter is only as good as its atomicity, and atomicity cannot be proven
// with a fake: a fake is exactly the thing that would hide a read-modify-write
// race. These tests run against the real Redis the queue tests already start, so
// INCR and EXPIRE are the server's own and the interleaving is real.

const ratelimitTestSecret = "ratelimit-integration-secret"

// ratelimitTestPolicy is small on purpose. A large window would make the tests
// slow without making them any more convincing.
var ratelimitTestPolicy = ratelimit.Policy{
	Max:    5,
	Window: 10 * time.Minute,
}

// newRateLimiter returns a limiter over the shared test Redis, with its keys
// cleared so one test cannot see another's counts.
func newRateLimiter(t *testing.T) (*ratelimit.Limiter, redis.UniversalClient) {
	t.Helper()

	client, err := testutil.StartRedisForTests(t.Context())
	require.NoError(t, err)

	keys, err := client.Keys(t.Context(), "rl:*").Result()
	require.NoError(t, err)

	if len(keys) > 0 {
		require.NoError(t, client.Del(t.Context(), keys...).Err())
	}

	return ratelimit.New(client, []byte(ratelimitTestSecret)), client
}

// A counter must never outlive its window, because a counter with no TTL would
// accumulate forever and rate-limit the subject permanently. This asserts the TTL
// is applied by the first increment rather than by a second command that a crash
// could have skipped.
func TestRateLimiter_CounterGetsATTLOnFirstIncrement(t *testing.T) {
	limiter, client := newRateLimiter(t)
	ctx := t.Context()

	_, err := limiter.Allow(ctx, "pin-email", "alice@example.com", ratelimitTestPolicy)
	require.NoError(t, err)

	keys, err := client.Keys(ctx, "rl:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	ttl, err := client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)

	require.Positive(
		t,
		ttl,
		"a counter created by the first increment must already carry a TTL",
	)
	require.LessOrEqual(t, ttl, ratelimitTestPolicy.Window)
}

// The TTL is set once, when the counter is created. Re-arming it on every call
// would slide the window forward with each request, so a subject could never fall
// out of its budget however slowly it spent it.
func TestRateLimiter_TTLIsNotExtendedByLaterCalls(t *testing.T) {
	limiter, client := newRateLimiter(t)
	ctx := t.Context()

	_, err := limiter.Allow(ctx, "pin-email", "alice@example.com", ratelimitTestPolicy)
	require.NoError(t, err)

	keys, err := client.Keys(ctx, "rl:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	firstTTL, err := client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)

	require.Eventually(
		t,
		func() bool {
			_, allowErr := limiter.Allow(
				ctx,
				"pin-email",
				"alice@example.com",
				ratelimitTestPolicy,
			)

			return allowErr == nil
		},
		2*time.Second,
		20*time.Millisecond,
	)

	// Redis reports TTL in whole seconds, so a second call some milliseconds
	// later may well report the same value. What must not happen is it going up.
	secondTTL, err := client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)

	require.LessOrEqual(
		t,
		secondTTL,
		firstTTL,
		"the window must not slide forward with each request",
	)
}

// A release with nothing left to give back is the one case the clamp covers, and
// it is the one case that used to write to the counter. Nothing the API can
// reach produces it — every Release is preceded by an Allow that incremented the
// counter — so the state is staged directly rather than waited for.
//
// The counter is set to zero *with* a TTL because SET replaces a key outright:
// staged without one, the key would already be permanent and the test would be
// measuring the staging rather than the release.
//
// Redis reports -1 for a key with no expiry and -2 for a key that has gone, so a
// positive TTL afterwards is the assertion that distinguishes "kept its window"
// from "the release left a key nothing will ever reap".
func TestRateLimiter_ReleaseWithNothingLeftToGiveBackKeepsItsTTL(t *testing.T) {
	limiter, client := newRateLimiter(t)
	ctx := t.Context()

	_, err := limiter.Allow(ctx, "login-email", "alice@example.com", ratelimitTestPolicy)
	require.NoError(t, err)

	keys, err := client.Keys(ctx, "rl:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	staged, err := client.Set(
		ctx,
		keys[0],
		0,
		ratelimitTestPolicy.Window,
	).Result()
	require.NoError(t, err)
	require.Equal(t, "OK", staged)

	before, err := client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.Positive(t, before, "the counter must have a window to lose")

	result, err := limiter.Release(ctx, "login-email", "alice@example.com")
	require.NoError(t, err)
	require.Zero(t, result.Count)

	value, err := client.Get(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.Equal(t, "0", value, "a release must never hand out budget that was not taken")

	after, err := client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.Positive(
		t,
		after,
		"a clamped release must not leave a counter with no expiry, which "+
			"would accumulate forever and rate-limit the subject permanently",
	)
	require.LessOrEqual(
		t,
		after,
		before,
		"a released slot belongs to the window it was taken from",
	)
}

// The ordinary case the clamp sits beside: a counter that still holds a slot
// gives exactly one of it back, and its window is left alone.
//
// Without this, the guard above is only ever seen refusing, and a Release that
// stopped decrementing altogether would still pass it.
func TestRateLimiter_ReleaseGivesBackExactlyOneSlotAndKeepsTheWindow(t *testing.T) {
	limiter, client := newRateLimiter(t)
	ctx := t.Context()

	for range 2 {
		_, err := limiter.Allow(ctx, "login-email", "alice@example.com", ratelimitTestPolicy)
		require.NoError(t, err)
	}

	keys, err := client.Keys(ctx, "rl:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	before, err := client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.Positive(t, before)

	result, err := limiter.Release(ctx, "login-email", "alice@example.com")
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Count)

	value, err := client.Get(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.Equal(t, "1", value)

	after, err := client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.Positive(t, after)
	require.LessOrEqual(
		t,
		after,
		before,
		"releasing must not slide the window forward",
	)
}

// The property the whole component exists for. Every goroutine is released at the
// same moment, so they all issue INCR against the same key as nearly as
// concurrently as a scheduler allows.
//
// A limiter built from a non-atomic read-increment-write would let several of them
// observe the same count and all decide they were within budget, admitting more
// than Max. Exactly Max must be admitted.
func TestRateLimiter_ConcurrentRequestsAdmitNoMoreThanTheLimit(t *testing.T) {
	limiter, _ := newRateLimiter(t)

	const (
		attempts = 40
		limit    = 5
	)

	var admitted atomic.Int64

	start := make(chan struct{})

	var ready sync.WaitGroup
	var done sync.WaitGroup

	ready.Add(attempts)
	done.Add(attempts)

	for range attempts {
		go func() {
			defer done.Done()

			ready.Done()
			<-start

			result, err := limiter.Allow(
				t.Context(),
				"pin-email",
				"alice@example.com",
				ratelimit.Policy{
					Max:    limit,
					Window: 10 * time.Minute,
				},
			)

			require.NoError(t, err)

			if result.Allowed {
				admitted.Add(1)
			}
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	require.Equal(
		t,
		int64(limit),
		admitted.Load(),
		"exactly Max requests may be admitted, no matter how many arrived together",
	)
}

// The same race, run repeatedly, because a race that only loses sometimes is still
// a race. Each round uses a fresh subject so the rounds do not share a counter.
func TestRateLimiter_ConcurrentRequestsCannotBypassTheLimitAcrossRounds(t *testing.T) {
	limiter, _ := newRateLimiter(t)

	const (
		rounds   = 10
		attempts = 16
		limit    = 3
	)

	for round := range rounds {
		subject := "round-" + string(rune('a'+round)) + "@example.com"

		var admitted atomic.Int64

		start := make(chan struct{})

		var ready sync.WaitGroup
		var done sync.WaitGroup

		ready.Add(attempts)
		done.Add(attempts)

		for range attempts {
			go func() {
				defer done.Done()

				ready.Done()
				<-start

				result, err := limiter.Allow(
					t.Context(),
					"pin-email",
					subject,
					ratelimit.Policy{Max: limit, Window: 10 * time.Minute},
				)

				require.NoError(t, err)

				if result.Allowed {
					admitted.Add(1)
				}
			}()
		}

		ready.Wait()
		close(start)
		done.Wait()

		require.Equal(
			t,
			int64(limit),
			admitted.Load(),
			"round %d admitted more than the limit", round,
		)
	}
}

// A budget that never came back would be a bug in the other direction: the subject
// could never request again. The window here is one second so expiry can be waited
// out rather than slept through a long production-sized window.
func TestRateLimiter_BudgetIsRestoredWhenTheWindowExpires(t *testing.T) {
	limiter, client := newRateLimiter(t)
	ctx := t.Context()

	policy := ratelimit.Policy{Max: 1, Window: time.Second}

	first, err := limiter.Allow(ctx, "pin-email", "alice@example.com", policy)
	require.NoError(t, err)
	require.True(t, first.Allowed, "the first request is always within budget")

	second, err := limiter.Allow(ctx, "pin-email", "alice@example.com", policy)
	require.NoError(t, err)
	require.False(t, second.Allowed, "a second request inside the window is over budget")

	// Redis expires the counter itself. Waiting for the key to disappear is waiting
	// on the server's own clock and TTL, not on a guess.
	keys, err := client.Keys(ctx, "rl:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	require.Eventually(
		t,
		func() bool {
			remaining, keysErr := client.Keys(ctx, "rl:*").Result()

			return keysErr == nil && len(remaining) == 0
		},
		10*time.Second,
		50*time.Millisecond,
		"the counter must expire once its window passes",
	)

	third, err := limiter.Allow(ctx, "pin-email", "alice@example.com", policy)
	require.NoError(t, err)
	require.True(t, third.Allowed, "a subject must get its budget back after the window")
}

// Distinct subjects and distinct namespaces are distinct budgets. Getting this
// wrong would let one address exhaust another's budget.
func TestRateLimiter_BudgetsAreIndependentAcrossSubjectsAndNamespaces(t *testing.T) {
	limiter, _ := newRateLimiter(t)
	ctx := t.Context()

	exhaust := func(namespace string, subject string) {
		t.Helper()

		for range ratelimitTestPolicy.Max {
			result, err := limiter.Allow(
				ctx,
				namespace,
				subject,
				ratelimitTestPolicy,
			)

			require.NoError(t, err)
			require.True(t, result.Allowed)
		}

		result, err := limiter.Allow(ctx, namespace, subject, ratelimitTestPolicy)
		require.NoError(t, err)
		require.False(t, result.Allowed)
	}

	exhaust("pin-email", "alice@example.com")

	// Another address is untouched.
	result, err := limiter.Allow(ctx, "pin-email", "bob@example.com", ratelimitTestPolicy)
	require.NoError(t, err)
	require.True(t, result.Allowed)

	// And so is the same address in another namespace.
	result, err = limiter.Allow(ctx, "login-email", "alice@example.com", ratelimitTestPolicy)
	require.NoError(t, err)
	require.True(t, result.Allowed)
}

// The key never carries the address, which is asserted here against what the
// server actually holds rather than against what the key builder intended.
func TestRateLimiter_StoredKeysNeverCarryTheAddress(t *testing.T) {
	limiter, client := newRateLimiter(t)
	ctx := t.Context()

	const address = "alice@example.com"

	_, err := limiter.Allow(ctx, "pin-email", address, ratelimitTestPolicy)
	require.NoError(t, err)

	keys, err := client.Keys(ctx, "rl:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	require.NotContains(t, keys[0], address)
	require.False(t, strings.Contains(keys[0], "@"))

	value, err := client.Get(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.NotContains(t, value, address)
}

// Two deployments sharing one Redis must not share budgets.
func TestRateLimiter_SecretsProduceSeparateBudgetsOnSharedRedis(t *testing.T) {
	limiter, client := newRateLimiter(t)
	ctx := t.Context()

	other := ratelimit.New(client, []byte("a-different-secret"))

	exhaust := func(l *ratelimit.Limiter) {
		t.Helper()

		for range ratelimitTestPolicy.Max {
			result, err := l.Allow(
				ctx,
				"pin-email",
				"alice@example.com",
				ratelimitTestPolicy,
			)

			require.NoError(t, err)
			require.True(t, result.Allowed)
		}
	}

	exhaust(limiter)

	result, err := limiter.Allow(ctx, "pin-email", "alice@example.com", ratelimitTestPolicy)
	require.NoError(t, err)
	require.False(t, result.Allowed)

	// A different secret means a different key, so this is a fresh budget rather
	// than the tail of the exhausted one.
	result, err = other.Allow(ctx, "pin-email", "alice@example.com", ratelimitTestPolicy)
	require.NoError(t, err)
	require.True(t, result.Allowed)
}

// A limiter built over a client that cannot reach Redis must refuse rather than
// allow, because the whole point of guarding an email-sending path is that an
// outage must not quietly remove the ceiling.
func TestRateLimiter_UnreachableRedisFailsClosed(t *testing.T) {
	unreachable := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: time.Second,
		MaxRetries:  -1,
	})

	t.Cleanup(func() {
		_ = unreachable.Close()
	})

	limiter := ratelimit.New(unreachable, []byte(ratelimitTestSecret))

	result, err := limiter.Allow(
		context.Background(),
		"pin-email",
		"alice@example.com",
		ratelimitTestPolicy,
	)

	require.Error(t, err)
	require.False(t, result.Allowed)
}

// The IP budget is the same mechanism under a different namespace, and it is
// concurrency that matters just as much: this is the counter an attacker's
// parallel requests land on. Exactly Max may be admitted no matter how many
// arrived together.
func TestRateLimiter_ConcurrentIPRequestsAdmitNoMoreThanTheLimit(t *testing.T) {
	limiter, _ := newRateLimiter(t)

	const (
		attempts = 50
		limit    = 20
	)

	policy := ratelimit.Policy{Max: limit, Window: 10 * time.Minute}

	var admitted atomic.Int64

	start := make(chan struct{})

	var ready sync.WaitGroup
	var done sync.WaitGroup

	ready.Add(attempts)
	done.Add(attempts)

	for range attempts {
		go func() {
			defer done.Done()

			ready.Done()
			<-start

			result, err := limiter.Allow(
				t.Context(),
				"pin-ip",
				"198.51.100.23",
				policy,
			)

			require.NoError(t, err)

			if result.Allowed {
				admitted.Add(1)
			}
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	require.Equal(
		t,
		int64(limit),
		admitted.Load(),
		"an address must not be able to exceed its budget by arriving in parallel",
	)
}

// Neighbours behind one address are the whole reason this budget is looser than
// the email one, so one address exhausting it must leave other addresses alone.
func TestRateLimiter_IPBudgetsAreIndependentPerAddress(t *testing.T) {
	limiter, _ := newRateLimiter(t)
	ctx := t.Context()

	policy := ratelimit.Policy{Max: 3, Window: 10 * time.Minute}

	exhaust := func(subject string) {
		t.Helper()

		for range policy.Max {
			result, err := limiter.Allow(ctx, "pin-ip", subject, policy)
			require.NoError(t, err)
			require.True(t, result.Allowed, "subject: %s", subject)
		}

		result, err := limiter.Allow(ctx, "pin-ip", subject, policy)
		require.NoError(t, err)
		require.False(t, result.Allowed, "subject: %s", subject)
	}

	exhaust("198.51.100.23")

	for _, neighbour := range []string{
		"198.51.100.24",
		"203.0.113.7",
		"2001:db8::1",
	} {
		result, err := limiter.Allow(ctx, "pin-ip", neighbour, policy)
		require.NoError(t, err)
		require.True(t, result.Allowed, "a neighbour at %s was blocked", neighbour)
	}

	// And the email budget is a separate dimension entirely: one address being
	// spent on IP does not spend it on email, and vice versa.
	result, err := limiter.Allow(
		ctx,
		"pin-email",
		"198.51.100.23",
		ratelimit.Policy{Max: 3, Window: time.Hour},
	)
	require.NoError(t, err)
	require.True(t, result.Allowed)
}

// The wait has to come from the counter's own remaining window. Reported from the
// configured policy instead, a caller arriving late in a window would be told to
// wait a full window and would still be refused when it came back.
func TestRateLimiter_RetryAfterCountsDownAsTheWindowExpires(t *testing.T) {
	limiter, _ := newRateLimiter(t)
	ctx := t.Context()

	// A window long enough to observe movement in, short enough that the test
	// does not wait on it.
	policy := ratelimit.Policy{Max: 1, Window: 3 * time.Second}

	result, err := limiter.Allow(ctx, "pin-ip", "198.51.100.23", policy)
	require.NoError(t, err)
	require.True(t, result.Allowed)

	result, err = limiter.Allow(ctx, "pin-ip", "198.51.100.23", policy)
	require.NoError(t, err)
	require.False(t, result.Allowed)

	firstWait := result.RetryAfter
	require.Positive(t, firstWait)
	require.LessOrEqual(t, firstWait, policy.Window)

	// A refused request is counted, and counting must not extend the window. If
	// the refusal re-armed the expiry, this wait would come back as the full
	// window again and a client retrying on the reported value would never be
	// let in.
	refused, err := limiter.Allow(ctx, "pin-ip", "198.51.100.23", policy)
	require.NoError(t, err)
	require.False(t, refused.Allowed)
	require.LessOrEqual(
		t,
		refused.RetryAfter,
		firstWait,
		"a refused request must not extend the window",
	)

	// Waiting is on the key leaving Redis, which is the server's own clock and
	// TTL rather than a guess.
	client, err := testutil.StartRedisForTests(ctx)
	require.NoError(t, err)

	require.Eventually(
		t,
		func() bool {
			keys, keysErr := client.Keys(ctx, "rl:*").Result()

			return keysErr == nil && len(keys) == 0
		},
		10*time.Second,
		50*time.Millisecond,
	)

	afterExpiry, err := limiter.Allow(ctx, "pin-ip", "198.51.100.23", policy)
	require.NoError(t, err)
	require.True(
		t,
		afterExpiry.Allowed,
		"the budget must be whole again once the window has passed",
	)
}

// The wait must never be negative, whatever Redis reports. A header carrying a
// negative number is a header no client can act on.
func TestRateLimiter_RetryAfterIsNeverNegative(t *testing.T) {
	limiter, client := newRateLimiter(t)
	ctx := t.Context()

	policy := ratelimit.Policy{Max: 1, Window: time.Hour}

	_, err := limiter.Allow(ctx, "pin-ip", "198.51.100.23", policy)
	require.NoError(t, err)

	keys, err := client.Keys(ctx, "rl:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	// A counter left with no expiry is the one failure the script exists to
	// prevent, and it is also the only way to make Redis report a negative
	// remaining life. Forcing one proves the script repairs it and that the
	// caller is never handed a negative wait.
	removed, err := client.Persist(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.True(t, removed, "the counter must have had an expiry to remove")

	result, err := limiter.Allow(ctx, "pin-ip", "198.51.100.23", policy)
	require.NoError(t, err)
	require.False(t, result.Allowed)
	require.GreaterOrEqual(t, result.RetryAfter, time.Duration(0))

	// And the repair took: the counter has an expiry again.
	ttl, err := client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)
	require.Positive(t, ttl, "the script must repair a counter left without an expiry")
}
