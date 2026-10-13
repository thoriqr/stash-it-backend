package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
)

// Two simultaneous first-time requests for the same address must both be answered,
// and must agree.
//
// Neither request can be decided by the lookup inside CreatePasswordReset, because
// there is no row yet for it to lock: both find nothing, both proceed to the
// insert, and one loses on pending_password_resets_email_pending_idx. Before the
// bounded retry that loser was answered 409 PASSWORD_RESET_ALREADY_PENDING and
// was handed no verification id at all, so a user who double-tapped "request
// reset" could not start the flow.
//
// The assertions are read from the database, not from the two bodies: both
// responses agreeing is necessary but not sufficient, because a pair of identical
// ids could in principle name nothing. The counts are what prove one reset and
// one verification request exist.
//
// The barrier matters. In sequence the second request would find the winner's
// committed row and take the ordinary reuse path, which is correct but proves
// nothing about the race — this test would pass with the retry removed.

// openPasswordResetStatus is the observable result of one reset request.
type openPasswordResetStatus struct {
	status         int
	verificationID string
	code           string
}

// postPasswordResetFrom requests a reset and reports the whole answer.
//
// No client address is involved: this route's own budget is charged, but the
// requests here are two, which is far below it.
func postPasswordResetFrom(
	t *testing.T,
	appAddress string,
	emailAddress string,
) openPasswordResetStatus {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"`+emailAddress+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	req.Header.Set("X-Forwarded-For", appAddress)

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	outcome := openPasswordResetStatus{status: resp.StatusCode}

	if outcome.status < 400 {
		var body struct {
			Data struct {
				VerificationID string `json:"verification_id"`
			} `json:"data"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		outcome.verificationID = body.Data.VerificationID

		return outcome
	}

	var decoded registerAPIError

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))

	outcome.code = decoded.Error.Code

	return outcome
}

// The number of participants is deliberately far larger than two, and that is a
// measured choice rather than a stylistic one.
//
// The window between `GetPendingPasswordResetByEmailForUpdate` returning "no
// rows" and the INSERT is microseconds, and each request spends far longer before
// it — two indexed lookups, handler dispatch and encoding. With two contenders one
// usually commits before the other has issued its SELECT, so the loser is absorbed
// by the ordinary already-pending path and the insert race is never entered. That
// was verified: a two-participant version of this test passed with the retry
// removed, so it would have proved nothing.
//
// Sixty-four contenders against one unique index cannot all be absorbed that way.
// Measured on this setup with the retry removed, 16 contenders produce four or
// five conflicts every run, 32 produce six or seven, and 64 produce seven on every
// run across repeated trials. Sixty-four is used so the race is entered
// reliably rather than usually.
//
// These requests go through the suite's own app, whose rate limiter is permissive,
// so arranging this contention never spends a budget under test — the same reason
// openPendingReset uses that app.
const concurrentFirstRequestParticipants = 64

func TestRequestPasswordReset_ConcurrentFirstRequestsShareOneReset(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "concurrent-first-reset@example.com"

	_, err := db.CreatePasswordResetUser(ctx, emailAddress)
	require.NoError(t, err)

	// Asserted before the race so a row left by the setup cannot be confused with
	// one the race created.
	before, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Zero(t, before.PendingResetCount)
	require.Zero(t, before.VerificationRequestCount)

	outcomes := make([]openPasswordResetStatus, concurrentFirstRequestParticipants)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = postPasswordResetFrom(
			t,
			"198.51.100.60",
			emailAddress,
		)
	})

	for index, outcome := range outcomes {
		require.Equalf(
			t,
			http.StatusCreated,
			outcome.status,
			"request %d must be answered, not refused: %s",
			index,
			outcome.code,
		)
		require.NotEmptyf(
			t,
			outcome.verificationID,
			"request %d was handed no verification id",
			index,
		)

		// Every request must name the same reset, or some of them cannot use it.
		require.Equalf(
			t,
			outcomes[0].verificationID,
			outcome.verificationID,
			"request %d was handed a different verification id",
			index,
		)
	}

	// One reset and one verification request, whatever the interleaving.
	after, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(
		t,
		int64(1),
		after.PendingResetCount,
		"two concurrent first requests must not open two resets",
	)
	require.Equal(
		t,
		int64(1),
		after.VerificationRequestCount,
		"two concurrent first requests must not open two verification requests",
	)

	// The id both responses name is the row that actually exists.
	state, err := db.GetPasswordResetState(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(t, outcomes[0].verificationID, state.VerificationID.String())
	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetPending),
		state.PasswordResetStatus,
	)

	// And it is usable: the flow that hangs off it still works.
	require.Equal(
		t,
		http.StatusCreated,
		resetCall(
			t,
			testApp,
			http.MethodPost,
			"/auth/password-reset/verification/"+outcomes[0].verificationID+"/pin",
			"",
			"",
		).status,
	)
}

// The sequential already-pending path is unchanged: a second request reuses the
// open reset rather than opening another, and answers 201 both times.
//
// This is the path the retry's second pass runs on, so it is asserted directly
// rather than only as a by-product of the race above.
func TestRequestPasswordReset_SecondRequestReusesTheOpenReset(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "sequential-reuse@example.com"

	_, err := db.CreatePasswordResetUser(ctx, emailAddress)
	require.NoError(t, err)

	first := postPasswordResetFrom(t, "198.51.100.61", emailAddress)
	require.Equal(t, http.StatusCreated, first.status, first.code)

	second := postPasswordResetFrom(t, "198.51.100.61", emailAddress)
	require.Equal(t, http.StatusCreated, second.status, second.code)

	require.Equal(t, first.verificationID, second.verificationID)

	after, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(t, int64(1), after.PendingResetCount)
	require.Equal(t, int64(1), after.VerificationRequestCount)
}

// A request for an address no account holds still writes nothing, and still
// answers 201.
//
// The retry is gated on one unique constraint, so it must not turn the
// non-enumerating path into one that creates rows for unknown addresses: there is
// no insert to lose a race on, and the second pass must never be reached.
func TestRequestPasswordReset_UnknownAddressStillWritesNothing(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "nobody-here-concurrent@example.com"

	// The sender is shared across the suite, so it is cleared here rather than
	// reasoned about: what matters is that this flow adds nothing to it.
	testEmailSender.Messages = nil

	outcomes := make([]openPasswordResetStatus, 4)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = postPasswordResetFrom(
			t,
			"198.51.100.62",
			emailAddress,
		)
	})

	for index, outcome := range outcomes {
		require.Equalf(
			t,
			http.StatusCreated,
			outcome.status,
			"request %d must be answered, not refused: %s",
			index,
			outcome.code,
		)
		require.NotEmptyf(
			t,
			outcome.verificationID,
			"request %d was handed no placeholder",
			index,
		)
	}

	// Every placeholder is distinct and unpersisted, which is what keeps the
	// unknown-address response indistinguishable from the known one.
	seen := make(map[string]struct{}, len(outcomes))

	for _, outcome := range outcomes {
		require.NotEmpty(t, outcome.verificationID)

		_, repeated := seen[outcome.verificationID]
		require.Falsef(
			t,
			repeated,
			"a placeholder was served twice: %s",
			outcome.verificationID,
		)

		seen[outcome.verificationID] = struct{}{}
	}

	counts, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)

	require.Zero(t, counts.PendingResetCount)
	require.Zero(t, counts.VerificationRequestCount)
	require.Zero(t, counts.VerificationCodeCount)
	require.Zero(t, counts.CredentialCount)

	require.Empty(
		t,
		testEmailSender.Messages,
		"an address with no account must never be mailed",
	)
}