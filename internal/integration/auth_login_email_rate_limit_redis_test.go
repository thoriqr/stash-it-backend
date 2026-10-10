package integration_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	login "github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	logintestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/login/generated"
)

// The per-address login budget, run against the real Redis limiter and through
// HTTP.
//
// The fake proves the service calls Allow and Release with the right arguments,
// and ratelimit_redis_test.go proves the counter itself. Neither shows that the
// two are connected: this is the only test where a request crosses the HTTP
// route, the service, and the Redis key the service actually writes, and where
// the number in Redis is read back after each step.
//
// It is deliberately sequential. Every request below that reaches an existing
// account costs a real Argon2id derivation, and the accounting being proven here
// does not depend on requests arriving together — ratelimit_redis_test.go already
// proves the counter is atomic under concurrency, with no hashing involved.
//
// Isolation is the suite's existing convention rather than anything new here:
// newIPLimitedApp clears every rl:* key before the app is built, so no counter
// from another test is visible here, and loginEmailCounterKey then requires that
// exactly one per-address budget exists, which is the same fact asserted from the
// other end.
//
// What this test cannot prove is that Release reads its own reply correctly. The
// script runs and the counter moves whatever Go makes of the answer, and the
// service discards both the error and the result, so a parsing regression would
// leave every count below exactly as asserted. That is proved where Release is
// called directly and its result is read — TestRateLimiter_Release* in
// ratelimit_redis_test.go. Between the two, the counter's behaviour and the
// caller's view of it are each covered once.

// loginEmailCounterKey returns the one Redis key the per-address login budget
// is spending, found by namespace rather than recomputed.
//
// The limiter HMACs the subject with a secret the test does not own, and a key
// rebuilt here from a copy of that scheme could disagree with the one the
// application writes without either side noticing. The namespace is a plain
// segment of the key, so matching on it identifies the counter without
// reproducing any of it.
func loginEmailCounterKey(t *testing.T, client redis.UniversalClient) string {
	t.Helper()

	keys, err := client.Keys(
		t.Context(),
		"rl:"+testutil.LoginEmailNamespace+":*",
	).Result()
	require.NoError(t, err)

	// Exactly one, which is also the isolation this test needs: a second key
	// would mean another test's address was still spending a budget here.
	require.Len(
		t,
		keys,
		1,
		"one address must spend exactly one per-address budget",
	)

	return keys[0]
}

// loginEmailCounter reads how much of the per-address budget is currently spent,
// straight out of Redis rather than out of a response.
func loginEmailCounter(t *testing.T, client redis.UniversalClient) int64 {
	t.Helper()

	value, err := client.Get(t.Context(), loginEmailCounterKey(t, client)).Result()
	require.NoError(t, err, "the counter must exist while its window is open")

	count, err := strconv.ParseInt(value, 10, 64)
	require.NoError(t, err)

	return count
}

// The whole accounting in one walk: a failure spends a slot, a success hands its
// own charge back, and the next failure spends a slot again.
//
// The successful login is taken one below the ceiling on purpose. Its charge is
// taken before the password is known, so at the ceiling the address would be
// refused before its password was ever checked and a release would have nothing
// to give back — the effect would be invisible. One below it, the release is
// observable in Redis: the count returns to exactly what it was.
func TestLoginAPI_EmailFailureLimit_RealRedisSpendsOnFailureAndReleasesOnSuccess(t *testing.T) {
	ctx := context.Background()
	db := logintestdb.New(testPool)
	require.NoError(t, db.TruncateLoginData(ctx))

	app, _ := newIPLimitedApp(t)

	client, err := testutil.StartRedisForTests(ctx)
	require.NoError(t, err)

	const (
		email    = "login-real-redis@example.com"
		password = "correct-password"
		wrong    = "wrong-password"
	)

	userID := createLoginAccount(t, email, password)

	// Every failure is a real derivation against a real account, so this loop is
	// the expensive part of the test. It runs to one below the ceiling and no
	// further, because that is all the accounting below needs to show.
	for i := 1; i < login.LoginEmailFailureLimit; i++ {
		result := postLogin(t, app, email, wrong)

		require.Equal(t, http.StatusUnauthorized, result.status, result.code)
		require.Equal(t, login.CodeInvalidCredentials, result.code)

		require.Equal(
			t,
			int64(i),
			loginEmailCounter(t, client),
			"each failed authentication must spend exactly one of the budget",
		)
	}

	spent := loginEmailCounter(t, client)
	require.Equal(t, int64(login.LoginEmailFailureLimit-1), spent)

	key := loginEmailCounterKey(t, client)

	windowBefore, err := client.TTL(ctx, key).Result()
	require.NoError(t, err)
	require.Positive(t, windowBefore)
	require.LessOrEqual(t, windowBefore, login.LoginEmailFailureWindow)

	// A successful authentication hands its own charge back, so signing in does
	// not spend the failure budget of the address it authenticated. Without the
	// release this count would be one higher, and every login a real person makes
	// would come out of the allowance reserved for mistakes.
	authenticated := postLogin(t, app, email, password)
	require.Equal(t, http.StatusOK, authenticated.status, authenticated.code)

	require.Equal(
		t,
		spent,
		loginEmailCounter(t, client),
		"a successful login must leave the budget where it started",
	)

	// The slot goes back to the window it was taken from rather than into a fresh
	// one. A release that re-armed the expiry would let a successful call postpone
	// the reset, and an address spending its budget slowly would never fall out of
	// it.
	windowAfter, err := client.TTL(ctx, key).Result()
	require.NoError(t, err)
	require.Positive(
		t,
		windowAfter,
		"the counter must still carry a window after a release; one with no "+
			"expiry accumulates forever and rate-limits the address permanently",
	)
	require.LessOrEqual(
		t,
		windowAfter,
		windowBefore,
		"a released slot belongs to the window it was taken from",
	)

	// The slot it gave back is a real slot: the next mistake costs one of the
	// budget rather than being refused on top of the ones already spent.
	afterSuccess := postLogin(t, app, email, wrong)
	require.Equal(t, http.StatusUnauthorized, afterSuccess.status, afterSuccess.code)

	require.Equal(
		t,
		spent+1,
		loginEmailCounter(t, client),
		"a released slot must be spendable again",
	)

	// And the ceiling is the ceiling, which is what makes the count above a real
	// measurement rather than an internal detail.
	refused := postLogin(t, app, email, wrong)
	require.Equal(t, http.StatusTooManyRequests, refused.status, refused.code)

	// The two budgets are separate namespaces with separate codes, so the code is
	// what says which one refused. Without it this assertion would be satisfied by
	// the per-address budget, which is not what is under test.
	require.Equal(t, login.CodeLoginRateLimitExceeded, refused.code)

	require.NotEmpty(
		t,
		refused.retryAfter,
		"a refusal must carry a wait or a client cannot tell when to come back",
	)

	// A refused attempt is still an attempt, and counting it is what stops a
	// caller spending forever inside one window.
	require.Equal(t, spent+2, loginEmailCounter(t, client))

	// The success was a real authentication and not merely a budget that did not
	// move: the session it issued is what makes the release above meaningful.
	sessions, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(1), sessions)
}
