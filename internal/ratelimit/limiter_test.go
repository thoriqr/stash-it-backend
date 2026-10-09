package ratelimit

import (
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const testSecret = "rate-limit-test-secret"

func TestPolicy_Validate(t *testing.T) {
	cases := map[string]struct {
		policy   Policy
		wantErr  bool
		errParts string
	}{
		"a usable policy": {
			policy: Policy{Max: 5, Window: time.Hour},
		},
		"the smallest usable window": {
			policy: Policy{Max: 1, Window: time.Second},
		},
		"a zero max": {
			policy:   Policy{Max: 0, Window: time.Hour},
			wantErr:  true,
			errParts: "max must be positive",
		},
		"a negative max": {
			policy:   Policy{Max: -1, Window: time.Hour},
			wantErr:  true,
			errParts: "max must be positive",
		},
		"a zero window": {
			policy:   Policy{Max: 5, Window: 0},
			wantErr:  true,
			errParts: "at least one second",
		},
		// Redis takes whole seconds, so a sub-second window would be truncated to
		// nothing. Rejecting it is better than quietly enforcing a window the
		// caller did not ask for.
		"a sub-second window": {
			policy:   Policy{Max: 5, Window: 500 * time.Millisecond},
			wantErr:  true,
			errParts: "at least one second",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.policy.Validate()

			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errParts)
		})
	}
}

func TestLimiter_KeyNeverCarriesTheSubject(t *testing.T) {
	limiter := New(nil, []byte(testSecret))

	subjects := []string{
		"alice@example.com",
		"alice+promo@example.co.uk",
		"a-very-long-local-part-that-goes-on@example.com",
		"weiß@example.com",
	}

	for _, subject := range subjects {
		t.Run(subject, func(t *testing.T) {
			key := limiter.key("pin-email", subject)

			// The subject is what must never be recoverable from a key. Anyone
			// holding the Redis connection, a slow log, or a key listing would
			// otherwise be reading email addresses.
			require.NotContains(t, key, subject)
			require.NotContains(t, key, "@")
			require.NotContains(t, key, "example")

			// The namespace is what makes a key diagnosable, so it stays.
			require.Contains(t, key, "pin-email")
		})
	}
}

func TestLimiter_KeyIsNamespacedAndStable(t *testing.T) {
	limiter := New(nil, []byte(testSecret))

	key := limiter.key("pin-email", "alice@example.com")

	require.True(t, strings.HasPrefix(key, "rl:pin-email:"))

	// The same subject must always land on the same key, or a budget would reset
	// itself between requests.
	require.Equal(t, key, limiter.key("pin-email", "alice@example.com"))
}

func TestLimiter_KeySeparatesSubjectsAndNamespaces(t *testing.T) {
	limiter := New(nil, []byte(testSecret))

	base := limiter.key("pin-email", "alice@example.com")

	// Two addresses are two budgets, or one address could exhaust another's.
	require.NotEqual(
		t,
		base,
		limiter.key("pin-email", "bob@example.com"),
	)

	// Two namespaces are two budgets, or adding a second protected action would
	// silently widen the first.
	require.NotEqual(
		t,
		base,
		limiter.key("login-email", "alice@example.com"),
	)
}

func TestLimiter_KeyDependsOnTheSecret(t *testing.T) {
	subject := "alice@example.com"

	// Two deployments sharing a Redis must not share budgets, and a rotated secret
	// must not leave the old keys readable by the new one.
	first := New(nil, []byte("secret-one")).key("pin-email", subject)
	second := New(nil, []byte("secret-two")).key("pin-email", subject)

	require.NotEqual(t, first, second)
}

// Normalized email variants are a property of the caller, not of the limiter: the
// limiter hashes whatever it is handed. What must hold is that two callers who
// agree on the canonical form agree on a budget, which is asserted here by feeding
// the same canonical value twice.
func TestLimiter_SameNormalizedSubjectSharesOneBudget(t *testing.T) {
	limiter := New(nil, []byte(testSecret))

	// What registration.NormalizeEmail produces for mixed-case and padded input.
	normalized := "alice@example.com"

	require.Equal(
		t,
		limiter.key("pin-email", normalized),
		limiter.key("pin-email", normalized),
	)
}

func TestLimiter_AllowRejectsAnUnusablePolicy(t *testing.T) {
	limiter := New(nil, []byte(testSecret))

	// The client is nil on purpose: a policy is refused before Redis is ever
	// touched, so this must not panic on the nil client either.
	result, err := limiter.Allow(
		t.Context(),
		"pin-email",
		"alice@example.com",
		Policy{Max: 0, Window: time.Hour},
	)

	require.Error(t, err)
	require.False(t, result.Allowed)
}

func TestLimiter_AllowFailsClosedWhenRedisIsUnreachable(t *testing.T) {
	// A client pointed at a port nothing is listening on. This is what an outage
	// looks like to the limiter, and the caller must be able to tell it apart
	// from "within budget".
	limiter := New(unreachableClient(t), []byte(testSecret))

	result, err := limiter.Allow(
		t.Context(),
		"pin-email",
		"alice@example.com",
		Policy{Max: 5, Window: time.Hour},
	)

	require.Error(t, err)
	require.False(
		t,
		result.Allowed,
		"a limiter that cannot answer must not report permission",
	)

	// The error names the namespace so an operator can tell which budget failed,
	// and carries no part of the subject.
	require.Contains(t, err.Error(), "pin-email")
	require.NotContains(t, err.Error(), "alice@example.com")
}

func TestLimiter_CheckReportsAMissingClient(t *testing.T) {
	var limiter *Limiter

	require.ErrorIs(t, limiter.Check(t.Context()), ErrNoClient)

	require.ErrorIs(
		t,
		New(nil, []byte(testSecret)).Check(t.Context()),
		ErrNoClient,
	)
}

// The wait a client is told is the length of time left in the window, so it has
// to round up. Rounding down would name a moment the window is still closed, and
// a client that trusted it would come back early and be refused for nothing.
func TestRetryAfter(t *testing.T) {
	cases := map[string]struct {
		ttlMillis int64
		want      time.Duration
	}{
		"a whole number of seconds is unchanged": {
			ttlMillis: 60_000,
			want:      60 * time.Second,
		},
		"a partial second rounds up to one": {
			ttlMillis: 1,
			want:      time.Second,
		},
		// 59.001s must not be reported as 59s: that is 1ms before the window
		// closes, and a client arriving then is refused again.
		"just over a second rounds up to two": {
			ttlMillis: 59_001,
			want:      60 * time.Second,
		},
		"exactly on a second boundary does not round": {
			ttlMillis: 59_000,
			want:      59 * time.Second,
		},
		"a millisecond short of a second rounds up": {
			ttlMillis: 999,
			want:      time.Second,
		},
		// Redis reports -1 for a key with no expiry and -2 for a key that has
		// gone. Neither is a length of time, and handing either to a client as
		// a wait would be meaningless at best.
		"no expiry reported is zero, never negative": {
			ttlMillis: -1,
			want:      0,
		},
		"a key that has gone is zero, never negative": {
			ttlMillis: -2,
			want:      0,
		},
		"zero is zero": {
			ttlMillis: 0,
			want:      0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, retryAfter(tc.ttlMillis))
		})
	}
}

func TestRetryAfter_NeverNegative(t *testing.T) {
	// A sweep rather than a table, because the property is "never", and a table
	// only proves the cases somebody thought of.
	for ttl := int64(-10_000); ttl <= 0; ttl++ {
		require.GreaterOrEqual(
			t,
			retryAfter(ttl),
			time.Duration(0),
			"a wait of %dms must not become negative", ttl,
		)
	}
}

// A reply shape the script does not produce must be an error rather than a panic,
// because this code runs on the request path of every rate-limited endpoint.
func TestToInt64(t *testing.T) {
	cases := map[string]struct {
		value   any
		want    int64
		wantErr bool
	}{
		"an integer reply":              {value: int64(42), want: 42},
		"a Go int reply":                {value: int(42), want: 42},
		"a float reply":                 {value: float64(42), want: 42},
		"a numeric string":              {value: "42", want: 42},
		"numeric bytes":                 {value: []byte("42"), want: 42},
		"a string that is not a number": {value: "not-a-number", wantErr: true},
		"nil":                           {value: nil, wantErr: true},
		"a table":                       {value: map[string]any{}, wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := toInt64(tc.value)

			if tc.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// unreachableClient returns a client pointed at a port nothing listens on, which
// is what an unreachable Redis looks like to the limiter.
//
// Port 1 is privileged and closed, so the connection is refused immediately
// rather than hanging: the failure is deterministic and the test stays fast.
func unreachableClient(t *testing.T) redis.UniversalClient {
	t.Helper()

	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: time.Second,
		MaxRetries:  -1,
	})

	t.Cleanup(func() {
		_ = client.Close()
	})

	return client
}
