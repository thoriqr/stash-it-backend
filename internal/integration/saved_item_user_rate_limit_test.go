package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/enrichment"
	saveditem "github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	saveditemdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/saved_item/generated"
)

// The per-user budgets on the two saved-item routes that cost something beyond a
// database read: POST /saved-items, which schedules an enrichment task nobody
// deduplicates, and POST /saved-items/:id/enrich, which performs an outbound
// fetch inside the API process.
//
// These are driven end to end through the real app — real Auth, real middleware,
// real handlers, real service, real repository, real database. Only the limiter
// itself is substituted, so what is proved is which budget a route spends, what a
// refused request does and does not do, and that the counter is keyed on the
// account rather than on anything the caller chooses.
//
// The fake limiter proves the wiring; the real counter's atomicity is proven
// against Redis by the ratelimit package's own integration tests and is not
// re-proved here.

type userLimitResponse struct {
	status     int
	retryAfter string
	code       string
	message    string
}

// userLimitedApp is an app built around one controllable limiter, plus the two
// collaborators the assertions need to look inside: the enricher that would
// perform a fetch, and the enqueuer that would schedule a task.
type userLimitedApp struct {
	app      *fiber.App
	limiter  *testutil.CountingPinRateLimiter
	enricher *testutil.FakeEnricher
	enqueuer *testutil.FakeSavedItemEnqueuer
}

// newUserLimitedApp builds the app with one limiter standing in for every budget
// in the process, exactly as the composition root does.
//
// The collaborators are supplied here rather than per test so that "no fetch
// happened" and "no task was scheduled" are assertable against the very instances
// the app was given, rather than against fresh ones the test forgot to wire.
func newUserLimitedApp(
	t *testing.T,
	limiter *testutil.CountingPinRateLimiter,
) userLimitedApp {
	t.Helper()

	enricher := &testutil.FakeEnricher{}
	enqueuer := &testutil.FakeSavedItemEnqueuer{}

	app, _ := testutil.NewAppWithEnqueuerAndLimiter(
		testPool,
		enricher,
		enqueuer,
		limiter,
	)

	return userLimitedApp{
		app:      app,
		limiter:  limiter,
		enricher: enricher,
		enqueuer: enqueuer,
	}
}

func (a userLimitedApp) do(
	t *testing.T,
	req *http.Request,
) userLimitResponse {
	t.Helper()

	resp, err := a.app.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)

	result := userLimitResponse{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if result.status >= 400 {
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		result.code = body.Error.Code
		result.message = body.Error.Message
	}

	return result
}

// save issues one POST /saved-items as the given account.
//
// The body is a real URL, so a request that is within budget really does insert a
// row. That is the point: a test asserting a refused save left nothing behind is
// only meaningful if the same request would have created something.
func (a userLimitedApp) save(
	t *testing.T,
	userID uuid.UUID,
	rawURL string,
) userLimitResponse {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items",
		strings.NewReader(`{"url":"`+rawURL+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	return a.do(t, req)
}

func (a userLimitedApp) enrich(
	t *testing.T,
	userID uuid.UUID,
	savedItemID uuid.UUID,
) userLimitResponse {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items/"+savedItemID.String()+"/enrich",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	return a.do(t, req)
}

// createRateLimitUser creates an account with the Unsorted collection a save
// needs. saved_items.collection_id is NOT NULL and the collection has to belong to
// the same user, so a save cannot be attempted without one.
func createRateLimitUser(t *testing.T, email string) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	db := saveditemdbtest.New(testPool)

	userID, err := db.CreateSavedItemUser(
		ctx,
		saveditemdbtest.CreateSavedItemUserParams{
			Email:       email,
			DisplayName: "Rate Limit Test User",
		},
	)
	require.NoError(t, err)

	createUnsortedCollection(t, ctx, db, userID)

	return userID
}

// A save within budget is charged to the caller's own account, under the feature's
// own namespace and its own documented threshold.
//
// Asserting the policy rather than only the outcome is what pins the wiring to
// the constants: a route that consulted a limiter under the right namespace but
// with a different allowance would still pass a test that only counted calls.
func TestSavedItemCreateUserRateLimit_WithinBudgetChargesTheCallersAccount(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createRateLimitUser(t, "create-within-budget@example.com")
	limiter := testutil.NewCountingPinRateLimiter()
	app := newUserLimitedApp(t, limiter)

	result := app.save(t, userID, "https://example.com/articles/1")

	require.Equal(t, http.StatusCreated, result.status)
	require.Empty(t, result.retryAfter)

	require.Equal(
		t,
		[]string{testutil.SavedItemCreateUserNamespace},
		limiter.Namespaces,
	)
	require.Equal(
		t,
		[]string{userID.String()},
		limiter.SubjectsIn(testutil.SavedItemCreateUserNamespace),
	)
	require.Equal(
		t,
		[]ratelimit.Policy{{
			Max:    saveditem.CreateUserRateLimitMax,
			Window: saveditem.CreateUserRateLimitWindow,
		}},
		limiter.Policies,
	)

	require.Len(t, app.enqueuer.Queued(), 1, "a save that succeeded schedules enrichment")
}

// A refused save is a 429 carrying the wait, and it is refused before the row
// exists.
//
// Both halves matter. A limiter mounted after the handler would report a 429 and
// still leave a saved item and a queued task behind, which is the failure mode
// this whole budget exists to prevent.
func TestSavedItemCreateUserRateLimit_ExceededCreatesNothingAndQueuesNothing(t *testing.T) {
	truncateEnrichmentData(t)

	ctx := context.Background()
	db := saveditemdbtest.New(testPool)

	userID := createRateLimitUser(t, "create-exceeded@example.com")

	limiter := &testutil.CountingPinRateLimiter{
		Allowed:    false,
		RetryAfter: 137 * time.Second,
	}
	app := newUserLimitedApp(t, limiter)

	result := app.save(t, userID, "https://example.com/articles/1")

	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, middleware.CodeUserRateLimitExceeded, result.code)
	require.Equal(t, "137", result.retryAfter)
	require.NotContains(t, result.message, userID.String())

	require.Zero(
		t,
		countSavedItemsForUser(t, ctx, db, userID),
		"a refused save must not leave a row behind",
	)
	require.Empty(
		t,
		app.enqueuer.Queued(),
		"a refused save must not schedule work for a row that does not exist",
	)
}

// The enrichment route spends the same dimension and a different namespace.
func TestSavedItemEnrichUserRateLimit_WithinBudgetFetches(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "enrich-within-budget@example.com")
	collectionID := createUnsortedForEnrichment(t, userID)
	savedItemID := createPendingSavedItem(
		t,
		userID,
		collectionID,
		"https://example.com/articles/1",
	)

	limiter := testutil.NewCountingPinRateLimiter()
	app := newUserLimitedApp(t, limiter)

	result := app.enrich(t, userID, savedItemID)

	require.Equal(t, http.StatusOK, result.status)
	require.Empty(t, result.retryAfter)

	require.Equal(
		t,
		[]string{testutil.SavedItemEnrichUserNamespace},
		limiter.Namespaces,
	)
	require.Equal(
		t,
		[]string{userID.String()},
		limiter.SubjectsIn(testutil.SavedItemEnrichUserNamespace),
	)
	require.Equal(
		t,
		[]ratelimit.Policy{{
			Max:    enrichment.EnrichUserRateLimitMax,
			Window: enrichment.EnrichUserRateLimitWindow,
		}},
		limiter.Policies,
	)

	require.Equal(
		t,
		[]string{"https://example.com/articles/1"},
		app.enricher.RequestedURLs(),
	)
}

// A refused enrichment must not fetch the page and must not write enrichment
// state.
//
// The fetch is the expensive half — it holds a handler and a database connection
// for as long as the outbound timeout allows — so a limiter that ran after the
// handler would bound nothing at all.
func TestSavedItemEnrichUserRateLimit_ExceededDoesNotFetchAndWritesNothing(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "enrich-exceeded@example.com")
	collectionID := createUnsortedForEnrichment(t, userID)
	savedItemID := createPendingSavedItem(
		t,
		userID,
		collectionID,
		"https://example.com/articles/1",
	)

	limiter := &testutil.CountingPinRateLimiter{
		Allowed:    false,
		RetryAfter: 29 * time.Second,
	}
	app := newUserLimitedApp(t, limiter)

	result := app.enrich(t, userID, savedItemID)

	require.Equal(t, http.StatusTooManyRequests, result.status)
	require.Equal(t, middleware.CodeUserRateLimitExceeded, result.code)
	require.Equal(t, "29", result.retryAfter)
	require.NotContains(t, result.message, userID.String())

	require.Empty(
		t,
		app.enricher.RequestedURLs(),
		"a refused enrichment must not perform the outbound fetch it exists to bound",
	)

	state := getEnrichmentState(t, savedItemID)
	require.Equal(t, "pending", state.EnrichmentStatus)
	require.False(t, state.LastEnrichedAt.Valid)
	require.False(t, state.Title.Valid)
}

// The two budgets are separate namespaces, so exhausting one leaves the other
// whole.
//
// This is why they are not one allowance: the routes cost different things, and a
// user who has been throttled for saving must still be able to refresh an item
// whose page looks wrong, and the reverse.
func TestSavedItemUserRateLimit_TheTwoBudgetsAreIndependent(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "budgets-independent@example.com")
	collectionID := createUnsortedForEnrichment(t, userID)

	// Two items, so the enrichment budget is spent without leaving the user
	// unable to use the other route afterwards.
	first := createPendingSavedItem(
		t, userID, collectionID, "https://example.com/articles/1",
	)
	second := createPendingSavedItem(
		t, userID, collectionID, "https://example.com/articles/2",
	)

	t.Run("an exhausted save budget does not stop an enrichment", func(t *testing.T) {
		limiter := testutil.NewCountingPinRateLimiter().
			AllowNamespace(testutil.SavedItemCreateUserNamespace, testutil.NamespaceOutcome{
				Allowed:    false,
				RetryAfter: 5 * time.Minute,
			})

		app := newUserLimitedApp(t, limiter)

		require.Equal(
			t,
			http.StatusTooManyRequests,
			app.save(t, userID, "https://example.com/articles/3").status,
		)

		require.Equal(
			t,
			http.StatusOK,
			app.enrich(t, userID, first).status,
			"a user throttled on saving must still be able to refresh an item",
		)
	})

	t.Run("an exhausted enrichment budget does not stop a save", func(t *testing.T) {
		limiter := testutil.NewCountingPinRateLimiter().
			AllowNamespace(testutil.SavedItemEnrichUserNamespace, testutil.NamespaceOutcome{
				Allowed:    false,
				RetryAfter: 30 * time.Second,
			})

		app := newUserLimitedApp(t, limiter)

		require.Equal(
			t,
			http.StatusTooManyRequests,
			app.enrich(t, userID, second).status,
		)

		require.Equal(
			t,
			http.StatusCreated,
			app.save(t, userID, "https://example.com/articles/4").status,
			"a user throttled on enrichment must still be able to save",
		)
	})
}

// The budget belongs to the account, so one account's flood does not stop
// another's.
//
// This is the property an address-keyed limit cannot offer and the reason these
// two routes are keyed on the user: a caller changing address would escape an
// address budget entirely, and a household behind one NAT would spend each other's
// allowance.
func TestSavedItemCreateUserRateLimit_OneAccountDoesNotSpendAnother(t *testing.T) {
	truncateEnrichmentData(t)

	ctx := context.Background()
	db := saveditemdbtest.New(testPool)

	flooder := createRateLimitUser(t, "create-flooder@example.com")
	neighbour := createRateLimitUser(t, "create-neighbour@example.com")

	app := newUserLimitedApp(t, testutil.NewEnforcingPinRateLimiter())

	for attempt := range saveditem.CreateUserRateLimitMax {
		result := app.save(t, flooder, "https://example.com/articles/"+uuid.NewString())

		require.NotEqual(
			t,
			http.StatusTooManyRequests,
			result.status,
			"attempt %d should be within budget, got %s", attempt, result.code,
		)
	}

	require.Equal(
		t,
		http.StatusTooManyRequests,
		app.save(t, flooder, "https://example.com/articles/last").status,
		"the budget should be spent after %d requests",
		saveditem.CreateUserRateLimitMax,
	)

	// Same route, same moment, same process — a different account.
	require.Equal(
		t,
		http.StatusCreated,
		app.save(t, neighbour, "https://example.com/articles/other").status,
	)

	require.Equal(
		t,
		int64(1),
		countSavedItemsForUser(t, ctx, db, neighbour),
	)
}

// An unreachable counter is not permission.
//
// Both routes fail closed, so a caller cannot remove the ceiling by causing the
// failure that produces it. For the save that is a deliberate choice rather than
// the only defensible one — the row would be cheap to write — and it is recorded
// here so the behaviour is pinned rather than discovered: the save schedules a
// task on a queue nobody can currently measure, and accepting writes while blind
// to the queue is how that queue grows unbounded exactly when the process is
// already in trouble. The enrichment route has no such choice to make: it spends
// an outbound fetch inline, and that is the work the budget exists to bound.
func TestSavedItemUserRateLimit_UnreachableCounterFailsClosed(t *testing.T) {
	t.Run("a save is refused and writes nothing", func(t *testing.T) {
		truncateEnrichmentData(t)

		ctx := context.Background()
		db := saveditemdbtest.New(testPool)

		userID := createRateLimitUser(t, "create-unavailable@example.com")

		app := newUserLimitedApp(
			t,
			testutil.UnavailablePinRateLimiter(
				errors.New("dial tcp 127.0.0.1:6379: connect: connection refused"),
			),
		)

		result := app.save(t, userID, "https://example.com/articles/1")

		require.Equal(t, http.StatusServiceUnavailable, result.status)
		require.Equal(t, middleware.CodeUserRateLimitUnavailable, result.code)
		require.Empty(
			t,
			result.retryAfter,
			"there is no known recovery time, so no wait should be named",
		)
		require.Zero(t, countSavedItemsForUser(t, ctx, db, userID))
		require.Empty(t, app.enqueuer.Queued())
	})

	t.Run("an enrichment is refused and does not fetch", func(t *testing.T) {
		truncateEnrichmentData(t)

		userID := createEnrichmentUser(t, "enrich-unavailable@example.com")
		collectionID := createUnsortedForEnrichment(t, userID)
		savedItemID := createPendingSavedItem(
			t,
			userID,
			collectionID,
			"https://example.com/articles/1",
		)

		app := newUserLimitedApp(
			t,
			testutil.UnavailablePinRateLimiter(
				errors.New("dial tcp 127.0.0.1:6379: connect: connection refused"),
			),
		)

		result := app.enrich(t, userID, savedItemID)

		require.Equal(t, http.StatusServiceUnavailable, result.status)
		require.Equal(t, middleware.CodeUserRateLimitUnavailable, result.code)
		require.Empty(t, result.retryAfter)
		require.Empty(
			t,
			app.enricher.RequestedURLs(),
			"a refused enrichment must not perform the fetch",
		)

		state := getEnrichmentState(t, savedItemID)
		require.Equal(t, "pending", state.EnrichmentStatus)
		require.False(t, state.LastEnrichedAt.Valid)
	})
}

// A request with no credentials never reaches the limiter.
//
// Auth runs first and refuses it, so there is no account to charge. Being told to
// wait for a budget nobody can spend would be the wrong answer to a request that
// was never authenticated.
//
// No account is created here on purpose, and the enrichment route names an id
// that does not exist. Neither could change the outcome: authentication is
// decided before a handler runs, so a 401 rather than a 404 or a rate-limit code
// is what an unauthenticated caller gets whatever the row situation is.
func TestSavedItemUserRateLimit_UnauthenticatedIsRefusedBeforeTheBudget(t *testing.T) {
	truncateEnrichmentData(t)

	limiter := testutil.NewCountingPinRateLimiter()
	app := newUserLimitedApp(t, limiter)

	t.Run("a save", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPost,
			"/saved-items",
			strings.NewReader(`{"url":"https://example.com/articles/1"}`),
		)
		req.Header.Set("Content-Type", "application/json")

		result := app.do(t, req)

		require.Equal(t, http.StatusUnauthorized, result.status)
		require.NotEqual(t, middleware.CodeUserRateLimitExceeded, result.code)
	})

	t.Run("an enrichment", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodPost,
			"/saved-items/"+uuid.NewString()+"/enrich",
			nil,
		)

		result := app.do(t, req)

		require.Equal(t, http.StatusUnauthorized, result.status)
		require.NotEqual(t, middleware.CodeUserRateLimitExceeded, result.code)
	})

	require.Empty(
		t,
		limiter.SubjectsIn(testutil.SavedItemCreateUserNamespace),
		"no identity means no budget is charged, and none is invented",
	)
	require.Empty(
		t,
		limiter.SubjectsIn(testutil.SavedItemEnrichUserNamespace),
	)
}

// Reading and deleting a saved item are deliberately outside both budgets.
//
// Neither performs an outbound fetch, schedules anything, or writes enrichment
// state; they cost what every other authenticated read in the application costs.
// Asserted because a limit mounted on the wrong route inside the same group is a
// mistake this wiring makes easy: the two limited routes and the two unlimited
// ones all live under /saved-items.
func TestSavedItemUserRateLimit_ReadsAndDeletesAreNotLimited(t *testing.T) {
	truncateEnrichmentData(t)

	userID := createEnrichmentUser(t, "reads-not-limited@example.com")
	collectionID := createUnsortedForEnrichment(t, userID)
	savedItemID := createPendingSavedItem(
		t,
		userID,
		collectionID,
		"https://example.com/articles/1",
	)

	// A limiter that refuses everything: if either route consulted a budget, it
	// would answer 429 rather than the result it actually returns.
	limiter := testutil.ExceededPinRateLimiter()
	app := newUserLimitedApp(t, limiter)

	token := newTestAccessToken(t, userID)

	t.Run("a read succeeds", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/saved-items/"+savedItemID.String(),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+token)

		require.Equal(t, http.StatusOK, app.do(t, req).status)
	})

	t.Run("a delete succeeds", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodDelete,
			"/saved-items/"+savedItemID.String(),
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+token)

		require.Equal(t, http.StatusOK, app.do(t, req).status)
	})

	require.Zero(
		t,
		limiter.CallsIn(testutil.SavedItemCreateUserNamespace),
		"reading and deleting must not spend the save budget",
	)
	require.Zero(
		t,
		limiter.CallsIn(testutil.SavedItemEnrichUserNamespace),
		"reading and deleting must not spend the enrichment budget",
	)
}
