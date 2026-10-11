package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	login "github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	logintestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/login/generated"
)

// The account-link confirmation against real PostgreSQL.
//
// What these cover cannot be proven with a mock repository: every assertion is
// about what the database holds afterwards, and about which transaction wins a
// row lock. The identity comparison itself is pinned in the service tests; what
// is pinned here is that it sits in front of a transaction whose single-use,
// expiry and uniqueness guarantees are unchanged by it.
//
// The integration app injects a fake Google verifier that always resolves to one
// account — subject google-subject-123, address google@example.com — so these
// tests never vary the token. They vary the confirmation it is compared against,
// which is the side the comparison is actually allowed to trust.
//
// Synchronization is a barrier, never a sleep, for the reason the session
// concurrency tests give: participants contend inside the same window instead of
// being issued one after another.

const (
	// testGoogleSubject is the subject the app's fake verifier resolves to.
	testGoogleSubject = "google-subject-123"

	// someOtherGoogleSubject is a different Google account. A confirmation
	// naming it must not be completable with the app's token.
	someOtherGoogleSubject = "a-different-google-account"
)

// seededLink is one pending confirmation and the account it would attach to.
type seededLink struct {
	userID         uuid.UUID
	confirmationID uuid.UUID
	db             *logintestdb.Queries
}

// seedLink creates an account and a live confirmation naming the given provider
// and subject. The subject is what the token is compared against, so passing a
// different one is how a test arranges a mismatch.
func seedLink(
	t *testing.T,
	provider string,
	providerSubject string,
) seededLink {
	t.Helper()

	ctx := context.Background()
	db := logintestdb.New(testPool)

	require.NoError(t, db.TruncateLoginData(ctx))

	userID, err := db.CreateLoginUser(
		ctx,
		logintestdb.CreateLoginUserParams{
			Email:       "existing-user@example.com",
			DisplayName: "Existing User",
		},
	)
	require.NoError(t, err)

	confirmationID, err := db.CreateAccountLinkConfirmation(
		ctx,
		logintestdb.CreateAccountLinkConfirmationParams{
			UserID:          userID,
			Provider:        provider,
			ProviderSubject: providerSubject,
			DisplayNameSnapshot: pgtype.Text{
				String: "Google User",
				Valid:  true,
			},
		},
	)
	require.NoError(t, err)

	return seededLink{
		userID:         userID,
		confirmationID: confirmationID,
		db:             db,
	}
}

// addConfirmation adds another live confirmation to the same account, which is
// what a user retrying the login flow gets.
func (l seededLink) addConfirmation(
	t *testing.T,
	providerSubject string,
) uuid.UUID {
	t.Helper()

	confirmationID, err := l.db.CreateAccountLinkConfirmation(
		context.Background(),
		logintestdb.CreateAccountLinkConfirmationParams{
			UserID:          l.userID,
			Provider:        "google",
			ProviderSubject: providerSubject,
			DisplayNameSnapshot: pgtype.Text{
				String: "Google User",
				Valid:  true,
			},
		},
	)
	require.NoError(t, err)

	return confirmationID
}

// confirmAccountLink posts to the confirmation route with the app's Google
// token, and reports the status and error code.
func confirmAccountLink(
	t *testing.T,
	confirmationID uuid.UUID,
	body string,
) (int, string) {
	t.Helper()

	return request(t, http.MethodPost, confirmPath(confirmationID), body)
}

// getAccountLinkConfirmation reads a confirmation through its GET route.
func getAccountLinkConfirmation(
	t *testing.T,
	confirmationID uuid.UUID,
) (int, string) {
	t.Helper()

	return request(t, http.MethodGet, confirmationPath(confirmationID), "")
}

func confirmPath(confirmationID uuid.UUID) string {
	return "/auth/login/google/account-link/" + confirmationID.String() + "/confirm"
}

func confirmationPath(confirmationID uuid.UUID) string {
	return "/auth/login/google/account-link/" + confirmationID.String()
}

// matchingToken is the body a request carries when it is proving control of the
// account the app's fake verifier resolves to.
func matchingToken() string {
	return `{"id_token":"test-google-id-token"}`
}

func request(t *testing.T, method string, path string, body string) (int, string) {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	require.NoError(t, err)

	if resp.StatusCode < 400 {
		return resp.StatusCode, ""
	}

	var decoded registerAPIError
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))

	return resp.StatusCode, decoded.Error.Code
}

// identityExists reports whether an identity is linked for a subject. A rejected
// attempt leaves no row, so the lookup failing is the expected answer rather
// than an error.
func identityExists(
	t *testing.T,
	link seededLink,
	providerSubject string,
) bool {
	t.Helper()

	_, err := link.db.GetGoogleAuthIdentityState(
		context.Background(),
		logintestdb.GetGoogleAuthIdentityStateParams{
			UserID:          link.userID,
			ProviderSubject: providerSubject,
		},
	)

	return err == nil
}

func sessionCount(t *testing.T, link seededLink) int64 {
	t.Helper()

	count, err := link.db.CountUserSessions(
		context.Background(),
		link.userID,
	)
	require.NoError(t, err)

	return count
}

// The matching identity completes the link and is signed in. Everything else in
// this file is a variation on this being refused.
func TestConfirmAccountLink_AMatchingIdentityIsAccepted(t *testing.T) {
	link := seedLink(t, "google", testGoogleSubject)

	status, _ := confirmAccountLink(t, link.confirmationID, matchingToken())

	require.Equal(t, http.StatusOK, status)
	require.True(t, identityExists(t, link, testGoogleSubject))
	require.Equal(t, int64(1), sessionCount(t, link))
}

// A token belonging to a different Google account is refused, and nothing is
// written.
//
// The stored address is the same one the token resolves to, which is the point:
// the account was matched on that address, so an implementation comparing email
// would wrongly accept this.
func TestConfirmAccountLink_ADifferentGoogleIdentityIsRejected(t *testing.T) {
	link := seedLink(t, "google", someOtherGoogleSubject)

	status, code := confirmAccountLink(t, link.confirmationID, matchingToken())

	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, login.CodeAccountLinkIdentityMismatch, code)

	require.False(t, identityExists(t, link, testGoogleSubject))
	require.False(t, identityExists(t, link, someOtherGoogleSubject))
	require.Zero(t, sessionCount(t, link))
}

// The provider is compared too. It is a column this feature writes, but it is
// data rather than code — it is read back out and copied into auth_identities —
// so a row naming another provider must not be satisfied by a Google token.
func TestConfirmAccountLink_AProviderMismatchIsRejected(t *testing.T) {
	ctx := context.Background()

	link := seedLink(t, "apple", testGoogleSubject)

	status, code := confirmAccountLink(t, link.confirmationID, matchingToken())

	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, login.CodeAccountLinkIdentityMismatch, code)
	require.Zero(t, sessionCount(t, link))

	// The confirmation is not consumed by the refusal.
	state, err := link.db.GetAccountLinkConfirmationState(
		ctx,
		link.confirmationID,
	)
	require.NoError(t, err)
	require.False(t, state.ConfirmedAt.Valid)
}

// A rejected attempt must leave the flow usable, or the check would be a
// lockout rather than a protection. This is what a user who tapped confirm with
// the wrong credential sees: refused, then free to complete normally.
func TestConfirmAccountLink_ARejectionLeavesTheFlowUsable(t *testing.T) {
	link := seedLink(t, "google", someOtherGoogleSubject)

	status, code := confirmAccountLink(t, link.confirmationID, matchingToken())
	require.Equal(t, login.CodeAccountLinkIdentityMismatch, code)
	require.NotEqual(t, http.StatusOK, status)

	// The confirmation the user gets by starting the flow again.
	retry := link.addConfirmation(t, testGoogleSubject)

	retryStatus, _ := confirmAccountLink(t, retry, matchingToken())

	require.Equal(t, http.StatusOK, retryStatus)
	require.True(t, identityExists(t, link, testGoogleSubject))
	require.Equal(t, int64(1), sessionCount(t, link))
}

// A body the handler cannot bind is refused before anything is written, and the
// confirmation is left alone.
func TestConfirmAccountLink_AMissingTokenIsRefusedWithoutWriting(t *testing.T) {
	ctx := context.Background()

	link := seedLink(t, "google", testGoogleSubject)

	for _, body := range []string{
		`{}`,
		`{"id_token":""}`,
		`{"provider":"google"}`,
		`not json at all`,
		``,
	} {
		status, _ := confirmAccountLink(t, link.confirmationID, body)

		require.Equal(
			t,
			http.StatusBadRequest,
			status,
			"body %q must be refused", body,
		)
	}

	require.False(t, identityExists(t, link, testGoogleSubject))
	require.Zero(t, sessionCount(t, link))

	state, err := link.db.GetAccountLinkConfirmationState(
		ctx,
		link.confirmationID,
	)
	require.NoError(t, err)
	require.False(t, state.ConfirmedAt.Valid)

	// And it still works once a real token is presented.
	status, _ := confirmAccountLink(t, link.confirmationID, matchingToken())
	require.Equal(t, http.StatusOK, status)
}

// Single use. The second attempt is refused and leaves exactly one identity and
// exactly one session, which is the combination a replay would break.
func TestConfirmAccountLink_AConfirmationIsSingleUse(t *testing.T) {
	link := seedLink(t, "google", testGoogleSubject)

	status, _ := confirmAccountLink(t, link.confirmationID, matchingToken())
	require.Equal(t, http.StatusOK, status)

	secondStatus, secondCode := confirmAccountLink(
		t,
		link.confirmationID,
		matchingToken(),
	)

	require.Equal(t, http.StatusConflict, secondStatus)
	require.Equal(t, login.CodeAccountLinkConfirmationInvalid, secondCode)

	require.True(t, identityExists(t, link, testGoogleSubject))
	require.Equal(t, int64(1), sessionCount(t, link))
}

// Expiry. The row is moved past its window rather than waited on, so this
// asserts the predicate and not the clock.
func TestConfirmAccountLink_AnExpiredConfirmationIsRefused(t *testing.T) {
	ctx := context.Background()

	link := seedLink(t, "google", testGoogleSubject)

	require.NoError(
		t,
		link.db.ExpireAccountLinkConfirmation(ctx, link.confirmationID),
	)

	getStatus, _ := getAccountLinkConfirmation(t, link.confirmationID)
	require.Equal(t, http.StatusConflict, getStatus)

	confirmStatus, confirmCode := confirmAccountLink(
		t,
		link.confirmationID,
		matchingToken(),
	)

	require.Equal(t, http.StatusConflict, confirmStatus)
	require.Equal(t, login.CodeAccountLinkConfirmationInvalid, confirmCode)

	require.False(t, identityExists(t, link, testGoogleSubject))
	require.Zero(t, sessionCount(t, link))
}

// Concurrency. Every request below is valid, so a correct implementation links
// the identity exactly once and refuses every other attempt with the same answer
// a second sequential attempt gets.
//
// This is the property asserting single use twice cannot reach: two sequential
// attempts are ordered by definition, these are not.
//
// **A single run is weak evidence here, and that was measured rather than
// assumed.** With FOR UPDATE removed from GetAccountLinkConfirmationForUpdate,
// one run of this test still passed — the unique index on
// (provider, provider_subject) is independently enough to keep the losers
// failing. Repeated runs did catch it. So this test verifies the outcome under
// contention, not which of the three guards is responsible for producing it, and
// it should be run with -count rather than once when that distinction matters.
func TestConfirmAccountLink_ConcurrentAttemptsLinkExactlyOnce(t *testing.T) {
	link := seedLink(t, "google", testGoogleSubject)

	const attempts = 8

	start := make(chan struct{})

	var ready sync.WaitGroup
	var done sync.WaitGroup

	var mutex sync.Mutex

	accepted := 0
	codes := map[string]int{}

	ready.Add(attempts)
	done.Add(attempts)

	for range attempts {
		go func() {
			defer done.Done()

			ready.Done()
			<-start

			status, code := confirmAccountLink(t, link.confirmationID, matchingToken())

			mutex.Lock()
			defer mutex.Unlock()

			if status == http.StatusOK {
				accepted++
			}

			codes[code]++
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	require.Equal(t, 1, accepted, "exactly one attempt may link the identity")

	// Every other attempt is refused as a spent confirmation, never as an
	// identity mismatch: they all carried a matching token, so anything else
	// would mean the losing transactions compared against something other than
	// the row as the winner left it.
	require.Equal(
		t,
		map[string]int{
			// The one that succeeded carries no error code at all.
			"":                                       1,
			login.CodeAccountLinkConfirmationInvalid: attempts - 1,
		},
		codes,
	)

	require.True(t, identityExists(t, link, testGoogleSubject))
	require.Equal(t, int64(1), sessionCount(t, link))
}

// Two confirmations for the same identity, which is what a user retrying the
// login flow produces, end in one link. The loser is told the identity exists
// rather than that its confirmation is unusable, because it is not the
// confirmation that is the problem.
func TestConfirmAccountLink_ASecondConfirmationForTheSameIdentityLosesCleanly(t *testing.T) {
	link := seedLink(t, "google", testGoogleSubject)

	second := link.addConfirmation(t, testGoogleSubject)

	status, _ := confirmAccountLink(t, link.confirmationID, matchingToken())
	require.Equal(t, http.StatusOK, status)

	loserStatus, loserCode := confirmAccountLink(t, second, matchingToken())

	require.Equal(t, http.StatusConflict, loserStatus)
	require.Equal(t, login.CodeAuthIdentityAlreadyExists, loserCode)

	require.True(t, identityExists(t, link, testGoogleSubject))
	require.Equal(t, int64(1), sessionCount(t, link))
}
