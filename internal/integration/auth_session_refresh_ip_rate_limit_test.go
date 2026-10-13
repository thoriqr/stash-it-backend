package integration_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	sessiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/session/generated"
)

// The per-client-address budget in front of POST /auth/refresh, against real
// Redis.
//
// A fake would prove the wiring and nothing else. The property that makes this
// budget worth mounting in front of the handler is that a refused request never
// reaches the rotation, and that a burst from one address cannot jointly exceed
// the allowance — a read-modify-write counter would admit a whole burst together,
// and a fake is exactly the thing that would hide that.
//
// Most requests below carry a body that cannot be parsed. The budget is charged
// before body binding, so a request that would never have reached a rotation is
// still counted. That is a property under test in its own right, and it is also
// why a flood costs this file no database work at all.

// Addresses come from the documentation range and are used by no other test here,
// so a counter left behind by one of these cannot be mistaken for another's.
const (
	refreshLimitAddress      = "198.51.100.110"
	refreshIsolationAddress  = "198.51.100.111"
	refreshIsolationAddress2 = "198.51.100.112"
	refreshNamespaceAddress  = "198.51.100.113"
	refreshConcurrencyAddr   = "198.51.100.114"

	refreshUnparseableBody = "this is not json"

	refreshRateLimitSecret = "refresh-ip-rate-limit-test-secret"
)

// refreshOutcome is what one refresh request answered with.
type refreshOutcome struct {
	status     int
	body       string
	retryAfter string
}

// newRefreshIPApp builds the app behind a trusted proxy over the shared test
// Redis, with its limiter keys cleared so one test cannot see another's counts.
//
// The proxy configuration is the only way a per-address budget can be exercised
// here: `app.Test` serves from an in-memory connection with no address of its
// own, so a forwarded header is the only thing that can vary the client.
func newRefreshIPApp(t *testing.T) (*fiber.App, redis.UniversalClient) {
	t.Helper()

	client, err := testutil.StartRedisForTests(t.Context())
	require.NoError(t, err)

	keys, err := client.Keys(t.Context(), "rl:*").Result()
	require.NoError(t, err)

	if len(keys) > 0 {
		require.NoError(t, client.Del(t.Context(), keys...).Err())
	}

	app, _ := testutil.NewAppWithTrustedProxy(
		testPool,
		ratelimit.New(client, []byte(refreshRateLimitSecret)),
	)

	return app, client
}

// postRefreshFrom issues one refresh from the given client address.
//
// The forwarded header is read only because this app is configured to read it
// under a trusted proxy, as every other per-address budget test here does.
func postRefreshFrom(
	t *testing.T,
	app *fiber.App,
	address string,
	body string,
) refreshOutcome {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/refresh",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", address)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return refreshOutcome{
		status:     resp.StatusCode,
		body:       string(raw),
		retryAfter: resp.Header.Get("Retry-After"),
	}
}

// postMalformedRefresh spends one request of the budget on a body that cannot be
// parsed, so a test can fill the budget without touching the database.
func postMalformedRefresh(
	t *testing.T,
	app *fiber.App,
	address string,
) refreshOutcome {
	t.Helper()

	return postRefreshFrom(t, app, address, refreshUnparseableBody)
}

// seedRefreshableSession creates a user, an active session and one live refresh
// token, and returns the raw token a client would hold.
//
// The raw value is what the service is given and the row stores its hash, which
// is the arrangement CreateSession produces. Seeding the hash instead would make
// the lookup miss and the test would assert nothing.
func seedRefreshableSession(
	t *testing.T,
	email string,
	rawToken string,
) (sessiondbtest.Session, uuid.UUID) {
	t.Helper()

	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	userID, err := db.CreateSessionUser(ctx, sessiondbtest.CreateSessionUserParams{
		Email:       email,
		DisplayName: "Refresh Rate Limit User",
	})
	require.NoError(t, err)

	sessionRecord, err := db.CreateTestSession(ctx, sessiondbtest.CreateTestSessionParams{
		UserID:   userID,
		Platform: "web",
		AbsoluteExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(24 * time.Hour),
			Valid: true,
		},
		RevokedAt: pgtype.Timestamptz{},
	})
	require.NoError(t, err)

	_, err = db.CreateTestRefreshToken(ctx, sessiondbtest.CreateTestRefreshTokenParams{
		SessionID:  sessionRecord.ID,
		TokenHash:  security.HashToken(rawToken),
		ReplacedBy: pgtype.UUID{},
	})
	require.NoError(t, err)

	return sessionRecord, sessionRecord.ID
}

// Below the limit nothing is refused; at the limit the caller is told to wait.
//
// Every request here is unparseable, so this also proves the budget is charged
// ahead of body binding: sixty requests the handler could never have served from
// nevertheless spend the whole allowance, and the sixty-first is refused. Were
// the charge behind the binding, that last request would have been admitted.
func TestRefreshIPRateLimit_ExhaustionReturns429WithRetryAfter(t *testing.T) {
	app, client := newRefreshIPApp(t)

	for attempt := 1; attempt <= session.RefreshIPRateLimitMax; attempt++ {
		outcome := postMalformedRefresh(t, app, refreshLimitAddress)

		require.NotEqual(
			t,
			http.StatusTooManyRequests,
			outcome.status,
			"attempt %d of %d was refused before the budget was spent: %s",
			attempt,
			session.RefreshIPRateLimitMax,
			outcome.body,
		)
	}

	outcome := postMalformedRefresh(t, app, refreshLimitAddress)

	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Contains(t, outcome.body, middleware.CodeIPRateLimitExceeded)

	// Being refused spends budget rather than extending the window, so a caller
	// cannot punish everyone behind it by keeping trying.
	require.Equal(
		t,
		http.StatusTooManyRequests,
		postMalformedRefresh(t, app, refreshLimitAddress).status,
	)

	// Retry-After is read from the counter's own remaining life, not from the
	// key's garbage-collection TTL. The counter was created by the first request
	// above, so its remaining life is at most one whole window.
	require.NotEmpty(t, outcome.retryAfter, "a 429 must carry a Retry-After")

	seconds, err := strconv.Atoi(outcome.retryAfter)
	require.NoError(t, err)
	require.Greater(t, seconds, 0)
	require.LessOrEqual(
		t,
		time.Duration(seconds)*time.Second,
		session.RefreshIPRateLimitWindow,
	)

	keys, err := client.Keys(t.Context(), "rl:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1, "one address must have produced exactly one counter")
}

// A refusal must be total. The request never reaches the rotation, so the token
// chain the caller holds has to be exactly as it was: no successor row, no link
// written on the token that was presented, no activity stamped on the session.
//
// This is what makes the budget a ceiling on work rather than only on responses.
// The refused request below carries a genuinely valid token — if the limiter were
// mounted after body binding or inside the service, this token would rotate, the
// chain would grow, and the response codes alone would still look correct.
func TestRefreshIPRateLimit_RefusedRequestRotatesNothing(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	const rawToken = "no-mutation-refresh-token"

	sessionRecord, _ := seedRefreshableSession(
		t,
		"refresh-limit-no-mutation@test.com",
		rawToken,
	)

	app, _ := newRefreshIPApp(t)

	for range session.RefreshIPRateLimitMax {
		postMalformedRefresh(t, app, refreshLimitAddress)
	}

	outcome := postRefreshFrom(
		t,
		app,
		refreshLimitAddress,
		`{"refresh_token":"`+rawToken+`"}`,
	)

	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Contains(t, outcome.body, middleware.CodeIPRateLimitExceeded)

	// The presented token was never consumed.
	tokens, err := db.GetRefreshTokensForSession(ctx, sessionRecord.ID)
	require.NoError(t, err)
	require.Len(
		t,
		tokens,
		1,
		"a refused refresh must not create a successor token",
	)
	require.False(
		t,
		tokens[0].ReplacedBy.Valid,
		"a refused refresh must not mark the token as rotated",
	)

	// And the session is untouched: not revoked, and still carrying the activity
	// stamp it was seeded with, so nothing at all reached the service.
	afterSession, err := db.GetSessionState(ctx, sessionRecord.ID)
	require.NoError(t, err)
	require.False(t, afterSession.RevokedAt.Valid)
	require.True(
		t,
		afterSession.LastActivityAt.Time.Equal(sessionRecord.LastActivityAt.Time),
		"a refused refresh must not stamp session activity",
	)
}

// A budget that cannot be evaluated is a refusal, not permission, and the refusal
// happens before the handler. An unreachable Redis must therefore produce a 503
// and leave the token chain exactly as it was.
//
// The app is handed a limiter that cannot answer rather than a broken connection.
// `TestRateLimiter_UnreachableRedisFailsClosed` already proves the production
// limiter reports an error when Redis is gone, so what is under test here is the
// route's behaviour given that error, not the limiter's behaviour again.
func TestRefreshIPRateLimit_UnavailableRedisRefusesAndRotatesNothing(t *testing.T) {
	ctx := context.Background()
	db := sessiondbtest.New(testPool)

	require.NoError(t, db.TruncateSessionData(ctx))

	const rawToken = "redis-down-refresh-token"

	sessionRecord, _ := seedRefreshableSession(
		t,
		"refresh-limit-redis-down@test.com",
		rawToken,
	)

	app, _ := testutil.NewAppWithTrustedProxy(
		testPool,
		testutil.UnavailablePinRateLimiter(errors.New("redis is unreachable")),
	)

	outcome := postRefreshFrom(
		t,
		app,
		refreshLimitAddress,
		`{"refresh_token":"`+rawToken+`"}`,
	)

	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Contains(t, outcome.body, middleware.CodeIPRateLimitUnavailable)

	// A 503 from an unevaluated budget carries no Retry-After. There is no window
	// to name, and telling the caller to wait out one nobody spent would be a lie
	// about a window that does not exist.
	require.Empty(
		t,
		outcome.retryAfter,
		"an unusable budget must not report a wait",
	)

	// Nothing reached the database.
	tokens, err := db.GetRefreshTokensForSession(ctx, sessionRecord.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	require.False(t, tokens[0].ReplacedBy.Valid)

	afterSession, err := db.GetSessionState(ctx, sessionRecord.ID)
	require.NoError(t, err)
	require.False(t, afterSession.RevokedAt.Valid)
}

// A refused refresh is a bad way to be near an access-token expiry, so this
// budget must not be a way to lock anyone out of signing in. These are separate
// counters, asserted in both directions because a shared counter would pass the
// first half and fail on the second.
//
// The second half uses a different address: the first has already spent its
// refresh budget above, so reusing it would test that budget rather than the
// separation between the two.
func TestRefreshIPRateLimit_DoesNotSpendLoginOrGoogleBudgets(t *testing.T) {
	app, _ := newRefreshIPApp(t)

	for range session.RefreshIPRateLimitMax {
		postMalformedRefresh(t, app, refreshIsolationAddress)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		postMalformedRefresh(t, app, refreshIsolationAddress).status,
	)

	// Spending an entire refresh budget leaves manual login untouched.
	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		postMalformedLogin(t, app, refreshIsolationAddress).status,
		"an exhausted refresh budget locked out manual login",
	)

	// And the reverse, from an address whose refresh budget is untouched.
	for range login.LoginIPRateLimitMax {
		postMalformedLogin(t, app, refreshIsolationAddress2)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		postMalformedLogin(t, app, refreshIsolationAddress2).status,
	)

	require.NotEqual(
		t,
		http.StatusTooManyRequests,
		postMalformedRefresh(t, app, refreshIsolationAddress2).status,
		"an exhausted login budget locked out refresh",
	)
}

// Which namespace each charge lands on is asserted from the limiter's own
// accounting rather than from response codes: a request refused by the wrong
// budget would still be a 429, and only the namespace shows which counter was
// actually spent.
func TestRefreshIPRateLimit_ChargesItsOwnNamespaceOnly(t *testing.T) {
	limiter := testutil.NewCountingPinRateLimiter()

	app, _ := testutil.NewAppWithTrustedProxy(testPool, limiter)

	postMalformedRefresh(t, app, refreshNamespaceAddress)

	loginReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/login",
		strings.NewReader(refreshUnparseableBody),
	)
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("X-Forwarded-For", refreshNamespaceAddress)

	resp, err := app.Test(loginReq, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	require.Equal(t, 1, limiter.CallsIn(testutil.RefreshIPNamespace))
	require.Equal(t, 1, limiter.CallsIn(testutil.LoginIPNamespace))

	// The Google budget and the per-address login failure budget belong to other
	// flows entirely and were never consulted by either request.
	require.Zero(t, limiter.CallsIn(testutil.GoogleAuthIPNamespace))
	require.Zero(t, limiter.CallsIn(testutil.LoginEmailNamespace))
}

// A read-modify-write counter would admit a whole burst past the limit together.
// Only the real Redis's atomic increment can show that it does not, so this is
// the one property in this file a fake could never establish.
func TestRefreshIPRateLimit_ConcurrentBurstCannotExceedTheLimit(t *testing.T) {
	app, _ := newRefreshIPApp(t)

	// Comfortably more than the allowance, all released at once.
	const burst = session.RefreshIPRateLimitMax + 20

	var admitted atomic.Int64
	var refused atomic.Int64

	runConcurrently(t, burst, func(_ int) {
		if postMalformedRefresh(t, app, refreshConcurrencyAddr).status ==
			http.StatusTooManyRequests {
			refused.Add(1)

			return
		}

		admitted.Add(1)
	})

	require.Equal(
		t,
		int64(session.RefreshIPRateLimitMax),
		admitted.Load(),
		"more requests were admitted than the budget allows",
	)
	require.Equal(t, int64(burst-session.RefreshIPRateLimitMax), refused.Load())
}
