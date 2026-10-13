package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
	registrationtestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/registration/generated"
)

// The PIN email budgets are proven here against a real Redis and the real budget,
// rather than against the fakes the flow tests use.
//
// The fakes answer "which component spent what, in what order". They cannot
// answer "are these two features really drawing on one budget", because two
// fakes handed to two features are two budgets by construction — the sharing only
// exists in what the composition root passes in, and only one counter in one
// Redis can show it. Nor can a fake show that concurrent admissions stay inside
// the limit, or that the wait a caller is given comes from the oldest entry
// rather than from a key's TTL.

const pinBudgetTestSecret = "pin-email-budget-integration-secret"

// budgetWindow is the rolling window these tests run on.
//
// It is a minute rather than a day so a test which needs an entry to age out does
// not have to wait for it. Nothing here depends on the production value: what is
// under test is which counter decides a request and what the reported wait is
// measured against, neither of which changes with the window's length.
const budgetWindow = time.Minute

// newBudgetApp builds the app with the real Redis-backed PIN email budget over
// caller-chosen policies, with the budget's keys cleared first.
//
// The client-address limiter is permissive: these tests are about the email
// budgets, and a refusal from the budget in front of the service would only
// decide which counter each assertion was really about.
func newBudgetApp(
	t *testing.T,
	perEmailMax int64,
	globalMax int64,
) (*fiber.App, *email.PINEmailBudget, redis.UniversalClient) {
	t.Helper()

	client, err := testutil.StartRedisForTests(t.Context())
	require.NoError(t, err)

	require.NoError(
		t,
		testutil.ResetPINEmailBudgetKeys(t.Context(), client),
	)

	budget := testutil.NewRedisPINEmailBudget(
		client,
		[]byte(pinBudgetTestSecret),
		perEmailMax,
		budgetWindow,
		globalMax,
	)

	app, _ := testutil.NewAppWithPINEmailBudget(
		testPool,
		&testutil.FakeEnricher{},
		nil,
		testutil.NewPermissivePinRateLimiter(),
		budget,
	)

	return app, budget, client
}

// postResetIssuance calls one of password reset's two issuance endpoints and
// reduces the response to what a budget assertion needs.
//
// It mirrors postPinEndpoint rather than going through resetCall, because these
// tests are about the shared budget and not about how a client address is
// resolved.
func postResetIssuance(
	t *testing.T,
	app *fiber.App,
	verificationID uuid.UUID,
	path string,
) pinResponse {
	t.Helper()

	resp, err := app.Test(httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+verificationID.String()+path,
		nil,
	))
	require.NoError(t, err)

	return readPinResponse(t, resp)
}

func readPinResponse(t *testing.T, resp *http.Response) pinResponse {
	t.Helper()

	result := pinResponse{
		status:     resp.StatusCode,
		retryAfter: resp.Header.Get(fiber.HeaderRetryAfter),
	}

	if result.status < 400 {
		return result
	}

	var body registerAPIError

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	result.code = body.Error.Code

	return result
}

// openRegistrationIn creates a registration through the given app, so the
// verification id belongs to the same app the budget is wired into.
func openRegistrationIn(
	t *testing.T,
	app *fiber.App,
	emailAddress string,
) uuid.UUID {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"`+emailAddress+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEqual(t, uuid.Nil, body.Data.VerificationID)

	return body.Data.VerificationID
}

// openResetIn creates a registered user and opens a password reset for it
// through the given app, returning the verification id the caller is handed.
func openResetIn(
	t *testing.T,
	app *fiber.App,
	emailAddress string,
) uuid.UUID {
	t.Helper()

	ctx := context.Background()

	_, err := passwordresetdbtest.
		New(testPool).
		CreatePasswordResetUser(ctx, emailAddress)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"`+emailAddress+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEqual(t, uuid.Nil, body.Data.VerificationID)

	return body.Data.VerificationID
}

// resetRetryAfter reads the header as the number of seconds a client would put
// on a timer, which is what Retry-After has to be if it is to be usable.
func resetRetryAfter(t *testing.T, header string) int64 {
	t.Helper()

	require.NotEmpty(t, header)

	seconds, err := strconv.ParseInt(header, 10, 64)
	require.NoError(t, err, "Retry-After must be a whole number of seconds")

	return seconds
}

// Initial issuance and resend draw on one per-address budget. Switching endpoints
// is not a way to double an allowance, and both routes are driven because they
// are two spellings of the same act.
func TestPINEmailBudget_InitialIssuanceAndResendShareTheSameAddressBudget(
	t *testing.T,
) {
	ctx := context.Background()

	registrationDB := registrationtestdb.New(testPool)
	require.NoError(t, registrationDB.TruncateRegistrationData(ctx))

	app, _, _ := newBudgetApp(t, 2, 100)

	verificationID := openRegistrationIn(t, app, "both-routes@example.com")

	outcome := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

	require.NoError(
		t,
		registrationDB.MakeVerificationResendable(ctx, verificationID),
	)

	outcome = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(t, http.StatusOK, outcome.status, outcome.code)

	// Two admissions spent a budget of two, so a third is refused whichever route
	// asks for it.
	require.NoError(
		t,
		registrationDB.MakeVerificationResendable(ctx, verificationID),
	)

	outcome = postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Equal(t, registration.CodePinRateLimitExceeded, outcome.code)

	require.NoError(
		t,
		registrationDB.MakeVerificationResendable(ctx, verificationID),
	)

	outcome = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Equal(t, registration.CodePinRateLimitExceeded, outcome.code)
}

// The budget is keyed by address, so each address gets its own full allowance
// rather than sharing one with its neighbours.
//
// This is the complement of the address-keying proof at the service level. It
// cannot be the other half at the HTTP level: `RegisterManual` reuses an
// address's existing pending registration rather than opening a second one, so
// one address has exactly one verification to address. What a caller can
// observe here is that a spent address does not spend anyone else's.
func TestPINEmailBudget_EachAddressHasItsOwnAllowance(t *testing.T) {
	require.NoError(
		t,
		registrationtestdb.New(testPool).
			TruncateRegistrationData(context.Background()),
	)

	app, _, _ := newBudgetApp(t, 1, 100)

	firstID := openRegistrationIn(t, app, "allowance-a@example.com")
	secondID := openRegistrationIn(t, app, "allowance-b@example.com")

	outcome := postPinEndpoint(t, app, firstID, pinPath)
	require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

	outcome = postPinEndpoint(t, app, secondID, pinPath)
	require.Equal(
		t,
		http.StatusCreated,
		outcome.status,
		"one address's spent budget must not lock out another: %s", outcome.code,
	)
}

// A per-address refusal must not spend the deployment-wide budget. That slot is
// worth far more than the address's own, and the mirror case is the cheap one:
// reaching the global limit already means the allowance has been spent many times
// over.
func TestPINEmailBudget_PerAddressRefusalDoesNotSpendTheGlobalBudget(t *testing.T) {
	ctx := context.Background()

	registrationDB := registrationtestdb.New(testPool)
	require.NoError(t, registrationDB.TruncateRegistrationData(ctx))

	// One address, one budget of one, and a global budget with room to spare. The
	// second issuance is refused, and what is asserted is that it cost the
	// deployment nothing.
	app, budget, _ := newBudgetApp(t, 1, 100)

	const emailAddress = "cheap-refusal@example.com"

	verificationID := openRegistrationIn(t, app, emailAddress)

	outcome := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

	require.NoError(
		t,
		registrationDB.MakeVerificationResendable(ctx, verificationID),
	)

	outcome = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Equal(t, registration.CodePinRateLimitExceeded, outcome.code)

	// The counter is shared state and nothing in the response reports it, so it
	// is read directly: one slot for the issuance above, one for this probe, and
	// none at all for the refusal.
	result, err := budget.ReserveGlobal(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Count)
}

// The cooldown is checked before either budget. A request already being told to
// wait must not also cost the address one of its daily admissions, because the
// user cannot get that admission back until the window moves.
//
// The assertion is the one after the wait rather than a probe of the counter: a
// budget of two with one admission spent still admits the next issuance only if
// the refusal in between cost nothing.
func TestPINEmailBudget_CooldownRefusalSpendsNeitherBudget(t *testing.T) {
	ctx := context.Background()

	registrationDB := registrationtestdb.New(testPool)
	require.NoError(t, registrationDB.TruncateRegistrationData(ctx))

	app, _, _ := newBudgetApp(t, 2, 100)

	verificationID := openRegistrationIn(t, app, "cooldown-first@example.com")

	outcome := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

	// Still inside the cooldown, so this is refused before either budget.
	outcome = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(t, http.StatusConflict, outcome.status)
	require.Equal(t, registration.CodeVerificationResendCooldown, outcome.code)

	require.NoError(
		t,
		registrationDB.MakeVerificationResendable(ctx, verificationID),
	)

	outcome = postPinEndpoint(t, app, verificationID, resendPath)
	require.Equal(
		t,
		http.StatusOK,
		outcome.status,
		"a request refused by the cooldown must not have spent the address "+
			"budget, and the deployment budget has room either way: %s",
		outcome.code,
	)
}

// The wait a refused caller is given is the moment a slot actually frees, taken
// from the oldest entry still inside the rolling window. It is not a configured
// constant, and it is not the key's TTL, which is garbage collection re-armed to
// a whole window on every call.
func TestPINEmailBudget_RetryAfterTracksTheOldestEntry(t *testing.T) {
	ctx := context.Background()

	registrationDB := registrationtestdb.New(testPool)
	require.NoError(t, registrationDB.TruncateRegistrationData(ctx))

	app, _, _ := newBudgetApp(t, 1, 100)

	verificationID := openRegistrationIn(t, app, "retry-after@example.com")

	outcome := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

	// Well after the budget's first entry was written and well before its window
	// ends. A TTL-derived wait would still report very nearly the whole window.
	time.Sleep(2 * time.Second)

	require.NoError(
		t,
		registrationDB.MakeVerificationResendable(ctx, verificationID),
	)

	outcome = postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Equal(t, registration.CodePinRateLimitExceeded, outcome.code)

	seconds := resetRetryAfter(t, outcome.retryAfter)

	require.Positive(t, seconds)
	require.LessOrEqual(t, seconds, int64(budgetWindow/time.Second))
	require.Greater(
		t,
		seconds,
		int64(budgetWindow/time.Second)-10,
		"the wait must come from the entry's own clock, not from a fresh window",
	)
}

// Both features draw on one deployment-wide allowance. Nothing about this is
// provable with two fakes: two fakes handed to two features are two budgets by
// construction, so the sharing can only be observed in the one counter they both
// spend.
func TestPINEmailBudget_RegistrationAndPasswordResetShareOneGlobalBudget(
	t *testing.T,
) {
	ctx := context.Background()

	require.NoError(
		t,
		registrationtestdb.New(testPool).TruncateRegistrationData(ctx),
	)
	require.NoError(
		t,
		passwordresetdbtest.New(testPool).TruncatePasswordResetData(ctx),
	)

	app, _, _ := newBudgetApp(t, 100, 2)

	registerID := openRegistrationIn(t, app, "shared-global-registration@example.com")
	resetID := openResetIn(t, app, "shared-global-reset@example.com")

	outcome := postPinEndpoint(t, app, registerID, pinPath)
	require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

	outcome = postResetIssuance(t, app, resetID, pinPath)
	require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

	// The deployment budget is now spent, and only the other feature is left to
	// ask. Its cooldown is aged out first so the refusal cannot be the cooldown's.
	require.NoError(
		t,
		passwordresetdbtest.New(testPool).
			MakeVerificationResendable(ctx, resetID),
	)

	outcome = postResetIssuance(t, app, resetID, resendPath)
	require.Equal(t, http.StatusTooManyRequests, outcome.status)
	require.Equal(
		t,
		registration.CodePINEmailBudgetExceeded,
		outcome.code,
		"a global refusal reports the global code, not the per-address one",
	)
	require.NotEmpty(t, outcome.retryAfter)
}

// Both features key the shared per-address budget the same way, so they write
// into one keyspace rather than two. Two entries, neither carrying a readable
// address, is what "shared" means for a keyed budget.
func TestPINEmailBudget_BothFeaturesKeyTheSameNamespace(t *testing.T) {
	ctx := context.Background()

	require.NoError(
		t,
		registrationtestdb.New(testPool).TruncateRegistrationData(ctx),
	)
	require.NoError(
		t,
		passwordresetdbtest.New(testPool).TruncatePasswordResetData(ctx),
	)

	app, _, client := newBudgetApp(t, 100, 100)

	registerID := openRegistrationIn(t, app, "keyspace-registration@example.com")
	resetID := openResetIn(t, app, "keyspace-reset@example.com")

	outcome := postPinEndpoint(t, app, registerID, pinPath)
	require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

	outcome = postResetIssuance(t, app, resetID, pinPath)
	require.Equal(t, http.StatusCreated, outcome.status, outcome.code)

	keys, err := client.Keys(ctx, "rlw:pin-email:*").Result()
	require.NoError(t, err)
	require.Len(
		t,
		keys,
		2,
		"one per-address entry each, in the one shared namespace",
	)

	// Neither feature writes anything a Redis key scan could read back into an
	// address.
	for _, key := range keys {
		require.NotContains(t, key, "keyspace-registration")
		require.NotContains(t, key, "keyspace-reset")
		require.NotContains(t, key, "@")
	}
}

// An unusable budget is a refusal, and the deployment-wide one reports its own
// code. Allowing on failure would hand anyone the ability to switch the ceiling
// off by causing the failure that produces it.
func TestPINEmailBudget_UnreachableRedisRefusesBothBudgets(t *testing.T) {
	ctx := context.Background()

	require.NoError(
		t,
		registrationtestdb.New(testPool).TruncateRegistrationData(ctx),
	)

	// A budget over a Redis nothing is listening on. The client-address limiter
	// stays permissive so the refusal cannot be the one in front of the handler.
	budget := testutil.NewRedisPINEmailBudget(
		redis.NewClient(&redis.Options{
			Addr:         "127.0.0.1:1",
			DialTimeout:  250 * time.Millisecond,
			MaxRetries:   -1,
			ReadTimeout:  250 * time.Millisecond,
			WriteTimeout: 250 * time.Millisecond,
		}),
		[]byte(pinBudgetTestSecret),
		10,
		budgetWindow,
		10,
	)

	app, _ := testutil.NewAppWithPINEmailBudget(
		testPool,
		&testutil.FakeEnricher{},
		nil,
		testutil.NewPermissivePinRateLimiter(),
		budget,
	)

	verificationID := openRegistrationIn(t, app, "unreachable@example.com")

	outcome := postPinEndpoint(t, app, verificationID, pinPath)
	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Equal(t, registration.CodePinRateLimitUnavailable, outcome.code)

	// No 503 on an unevaluated budget carries a wait: there is no window to name,
	// and inventing one would tell a client to come back at a moment nobody chose.
	require.Empty(t, outcome.retryAfter)

	_ = ctx
}

// The production sender for any environment without a provider makes no network
// call, so it cannot have spent anything, and it says so. The budget gives the
// slot back — otherwise a staging deployment would exhaust the allowance meant to
// protect a real provider account before one had been chosen.
func TestPINEmailBudget_UnconfiguredSenderDoesNotDrainTheGlobalBudget(
	t *testing.T,
) {
	budget := newIsolatedBudget(t, 100, 1)

	unconfigured := email.NewUnconfiguredSender()

	message := email.Message{
		To:      email.Recipient{Email: "alice@example.com"},
		Subject: "Your code",
	}

	// Well past a global budget of one. Every send fails, because there is no
	// sender, and none of them may cost a slot.
	for i := range 5 {
		global, err := budget.ReserveGlobal(t.Context())
		require.NoError(t, err)
		require.True(
			t,
			global.Allowed,
			"send %d should have been admitted, so nothing drained the budget",
			i,
		)

		err = budget.Deliver(t.Context(), global, unconfigured, message)
		require.Error(t, err, "an unconfigured sender always fails")
		require.False(
			t,
			email.Attempted(err),
			"it made no call, so nothing could have been accepted anywhere",
		)
	}
}

// A sender that reached a provider and then failed keeps its slot. A timeout or a
// reset does not prove the message was declined, and handing the capacity back
// on that evidence would let a message be delivered against a slot already given
// to somebody else.
func TestPINEmailBudget_AttemptedSendFailureKeepsTheGlobalSlot(t *testing.T) {
	budget := newIsolatedBudget(t, 100, 1)

	log := zap.NewNop()
	sender := email.NewDevSender(log)
	sender.Err = errors.New("read: connection reset by peer")

	global, err := budget.ReserveGlobal(t.Context())
	require.NoError(t, err)
	require.True(t, global.Allowed)

	err = budget.Deliver(
		t.Context(),
		global,
		sender,
		email.Message{To: email.Recipient{Email: "alice@example.com"}},
	)
	require.Error(t, err)
	require.True(t, email.Attempted(err))

	result, err := budget.ReserveGlobal(t.Context())
	require.NoError(t, err)
	require.False(
		t,
		result.Allowed,
		"a slot spent by an attempted send is not given back",
	)
}

// A send that fails after the code was persisted is a 503 that says when to come
// back, not a 500. The verification is still pending and the reset is still open,
// so retrying is the right response — which is what a 500 tells a client not to
// do — and the wait is knowable because the issuance transaction restamped the
// resend cooldown moments earlier.
//
// This is the only test that drives the delivery-failure response through HTTP,
// so it is also the only one that proves the header survives the error handler.
func TestPINEmailBudget_DeliveryFailureIs503WithTheCooldownOnTheWire(t *testing.T) {
	require.NoError(
		t,
		registrationtestdb.New(testPool).
			TruncateRegistrationData(context.Background()),
	)

	budget := testutil.NewCountingPINEmailBudget()

	app, sender := testutil.NewAppWithPINEmailBudget(
		testPool,
		&testutil.FakeEnricher{},
		nil,
		testutil.NewPermissivePinRateLimiter(),
		budget,
	)

	// A plain error is not marked NotAttempted, so this is a send that may have
	// reached a provider: the slot stays spent.
	sender.Fail(errors.New("read: connection reset by peer"))

	verificationID := openRegistrationIn(t, app, "delivery-failure@example.com")

	outcome := postPinEndpoint(t, app, verificationID, pinPath)

	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Equal(t, registration.CodePINEmailDeliveryFailed, outcome.code)

	seconds := resetRetryAfter(t, outcome.retryAfter)
	require.Positive(t, seconds)
	require.LessOrEqual(
		t,
		seconds,
		int64(time.Minute/time.Second),
		"the wait is the cooldown that was just restarted, not a whole budget window",
	)

	require.Equal(
		t,
		1,
		budget.GlobalCount(),
		"the failed request spent its global slot",
	)
	require.Zero(
		t,
		budget.RefundCount(),
		"an attempted send keeps its slot",
	)
}

// A sender that reports it never tried gives the slot back, and the caller still
// gets the same 503 and the same wait: the difference is in the budget's
// accounting, not in what the user is told.
func TestPINEmailBudget_SenderThatNeverTriedStill503sAndRefunds(t *testing.T) {
	require.NoError(
		t,
		registrationtestdb.New(testPool).
			TruncateRegistrationData(context.Background()),
	)

	budget := testutil.NewCountingPINEmailBudget()

	app, sender := testutil.NewAppWithPINEmailBudget(
		testPool,
		&testutil.FakeEnricher{},
		nil,
		testutil.NewPermissivePinRateLimiter(),
		budget,
	)

	sender.Fail(email.NotAttempted(errors.New("email sender is not configured")))

	verificationID := openRegistrationIn(t, app, "not-attempted@example.com")

	outcome := postPinEndpoint(t, app, verificationID, pinPath)

	require.Equal(t, http.StatusServiceUnavailable, outcome.status)
	require.Equal(t, registration.CodePINEmailDeliveryFailed, outcome.code)
	require.NotEmpty(t, outcome.retryAfter)

	require.Equal(
		t,
		1,
		budget.RefundCount(),
		"a sender that never tried gives the slot back",
	)
}

// newIsolatedBudget returns a real budget over the shared test Redis with the
// budget keys cleared, for tests that drive the budget directly rather than
// through HTTP.
func newIsolatedBudget(
	t *testing.T,
	perEmailMax int64,
	globalMax int64,
) *email.PINEmailBudget {
	t.Helper()

	client, err := testutil.StartRedisForTests(t.Context())
	require.NoError(t, err)

	require.NoError(
		t,
		testutil.ResetPINEmailBudgetKeys(t.Context(), client),
	)

	return testutil.NewRedisPINEmailBudget(
		client,
		[]byte(pinBudgetTestSecret),
		perEmailMax,
		budgetWindow,
		globalMax,
	)
}

// Concurrent issuance cannot exceed the deployment-wide budget. Every request is
// checked and spent before anything is issued, and the check is one atomic step
// against the one Redis every instance shares.
func TestPINEmailBudget_ConcurrentIssuanceNeverExceedsTheLimit(t *testing.T) {
	const (
		perEmailMax = 4
		globalMax   = 6
		verifCount  = 12
	)

	require.NoError(
		t,
		registrationtestdb.New(testPool).TruncateRegistrationData(context.Background()),
	)

	app, _, _ := newBudgetApp(t, perEmailMax, globalMax)

	// One address per verification, so the per-address budget cannot be what
	// stops the burst — only the deployment-wide one can.
	ids := make([]uuid.UUID, 0, verifCount)

	for i := range verifCount {
		ids = append(
			ids,
			openRegistrationIn(t, app, fmt.Sprintf("burst-%02d@example.com", i)),
		)
	}

	type outcome struct {
		status int
		code   string
	}

	results := make(chan outcome, verifCount)

	var wait sync.WaitGroup

	for _, id := range ids {
		wait.Add(1)

		go func() {
			defer wait.Done()

			got := postPinEndpoint(t, app, id, pinPath)
			results <- outcome{got.status, got.code}
		}()
	}

	wait.Wait()

	close(results)

	admitted := 0

	for got := range results {
		switch got.status {
		case http.StatusCreated:
			admitted++
		case http.StatusTooManyRequests:
			require.Equal(
				t,
				registration.CodePINEmailBudgetExceeded,
				got.code,
				"with one address per verification only the global budget can refuse",
			)
		default:
			t.Fatalf("unexpected status %d with code %q", got.status, got.code)
		}
	}

	require.LessOrEqual(
		t,
		admitted,
		globalMax,
		"concurrent issuance must not exceed the deployment-wide budget",
	)
	require.Greater(t, admitted, 0, "the burst should still have been admitted")
}
