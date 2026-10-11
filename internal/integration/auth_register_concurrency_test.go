package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
	registrationtestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/registration/generated"
)

// These tests pin the two state transitions that are single-use by design.
//
// Both operations validate outside their transaction and mutate inside it, so
// two requests carrying the same credential can both pass every check the
// service makes before either reaches the database. What stops the second one
// is the database:
//
//   - CompleteVerification opens with ConsumeVerificationCode, which matches
//     only while consumed_at IS NULL, then MarkVerificationRequestVerified,
//     which matches only while status = 'pending'. Both results are checked
//     against rowsAffected == 1, and the continuation itself is additionally
//     protected by a unique index on pending_registration_id.
//   - FinalizeManualRegistration opens with the users insert, which loses the
//     unique index on email, and maps that violation to USER_ALREADY_EXISTS.
//
// Every assertion below is read from the database rather than inferred from a
// status code. The status codes say how the losers were told; the counts say what
// actually happened.
//
// A note on what these tests do and do not prove, established by removing each
// guard in turn and re-running: they hold the invariant of "exactly one
// continuation" and "exactly one account", but they do not discriminate *which*
// guard produces it. In a losing request the rejection usually happens at the
// service's first read, before the transaction is ever opened, because the
// winner has already committed. Each guard is therefore redundant to the others,
// which is the correct arrangement for a security-relevant transition, and any
// one of them removed leaves the outcome unchanged. What a removal would break
// is a request that reaches the transaction without having observed the winner —
// a narrower window than a barrier against two HTTP requests can open.

// verificationRaceOutcome is one request's result in a verification race.
type verificationRaceOutcome struct {
	status int
	code   string
	token  string
}

// The barrier is the synchronization: runConcurrently parks every goroutine
// before releasing them together, so the requests contend for the same window
// rather than being issued one after another. Waiting on readiness before
// closing the channel is what makes it a barrier rather than a stagger, and it
// is why no sleep appears anywhere in this file — a sleep would let the first
// request finish before the second was even built, which is the case where the
// bug these tests look for cannot happen.

// postVerify calls the verification route with the given PIN.
func postVerify(
	t *testing.T,
	verificationID uuid.UUID,
	pin string,
) verificationRaceOutcome {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+verificationID.String()+"/verify",
		strings.NewReader(`{"pin":"`+pin+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	outcome := verificationRaceOutcome{status: resp.StatusCode}

	if outcome.status < 400 {
		var body struct {
			Data struct {
				RegistrationContinuationToken string `json:"registration_continuation_token"`
			} `json:"data"`
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		outcome.token = body.Data.RegistrationContinuationToken

		return outcome
	}

	var errBody registerAPIError

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errBody))
	outcome.code = errBody.Error.Code

	return outcome
}

// countContinuations counts the continuations created for one verification's
// pending registration.
//
// This is the assertion the HTTP status code cannot make. Both requests were
// answered with a well-formed response, and only this count distinguishes one
// continuation from two.
func countContinuations(t *testing.T, email string) int64 {
	t.Helper()

	var count int64

	err := testPool.QueryRow(
		context.Background(),
		`SELECT COUNT(*)
		 FROM registration_continuations rc
		 JOIN pending_registrations pr
		   ON pr.id = rc.pending_registration_id
		 WHERE pr.email = $1`,
		email,
	).Scan(&count)
	require.NoError(t, err)

	return count
}

// countConsumedVerificationCodes counts consumed codes for one verification.
//
// A second continuation would be reachable only if the code could be consumed
// twice, so this is the lower-level statement of the same invariant.
func countConsumedVerificationCodes(
	t *testing.T,
	verificationID uuid.UUID,
) int64 {
	t.Helper()

	var count int64

	err := testPool.QueryRow(
		context.Background(),
		`SELECT COUNT(*)
		 FROM verification_codes
		 WHERE verification_request_id = $1
		   AND consumed_at IS NOT NULL`,
		verificationID,
	).Scan(&count)
	require.NoError(t, err)

	return count
}

// Two requests carrying the same valid PIN must produce exactly one
// continuation. The credential is single-use, and the loser is refused rather
// than answered with a second token that would both be valid and would leave the
// code consumable again.
func TestVerifyRegistration_ConcurrentSamePINIssuesExactlyOneContinuation(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))
	testEmailSender.Messages = nil

	const email = "verify-race@example.com"

	verificationID := registerAndReturnVerificationID(t, email)

	// Issue the code the way a client would, then read the PIN out of the mail
	// that was actually sent. The code is six random digits, so it is not a value
	// this test may assume; the message is the only place the plaintext exists.
	require.Equal(
		t,
		http.StatusCreated,
		requestCreatePIN(t, verificationID).StatusCode,
	)
	require.Len(t, testEmailSender.Messages, 1)

	issuedPIN := strings.TrimPrefix(
		testEmailSender.Messages[0].Text,
		"Your registration verification code is: ",
	)

	require.Len(t, issuedPIN, 6, "a verification code is six digits")

	// Both requests are built and parked before either runs, so they contend
	// rather than arriving in sequence. In sequence the second would be refused
	// by the already-consumed code and this test would prove nothing about
	// concurrency.
	outcomes := make([]verificationRaceOutcome, 2)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = postVerify(t, verificationID, issuedPIN)
	})

	var succeeded, conflicted int

	var winner verificationRaceOutcome

	for _, outcome := range outcomes {
		switch outcome.status {
		case http.StatusOK:
			succeeded++
			winner = outcome
		case http.StatusConflict:
			conflicted++
		}
	}

	require.Equal(t, 1, succeeded, "exactly one verification may succeed: %+v", outcomes)
	require.Equal(t, 1, conflicted, "exactly one must be refused: %+v", outcomes)

	// The loser is refused, and which conflict it sees depends only on how far it
	// had got when the winner committed — not on anything a caller controls.
	//
	// VerifyRegistration re-reads the verification before it compares the PIN, so
	// a request whose read happens after the winner committed sees
	// VERIFICATION_NOT_PENDING. One that read before, then lost the race to
	// consume the same code, sees CONFLICT. Both are correct refusals of a
	// single-use credential, and pinning either one exactly would assert on
	// scheduling rather than on behaviour.
	require.Contains(t, []string{
		apperror.CodeConflict,
		registration.CodeVerificationNotPending,
		registration.CodeInvalidVerificationCode,
		registration.CodeVerificationCodeAttemptsExceeded,
	}, loserCode(outcomes), "the loser must be refused, never given a second token")

	require.Equal(
		t,
		int64(1),
		countContinuations(t, email),
		"two requests with one valid PIN must leave exactly one continuation",
	)

	require.Equal(
		t,
		int64(1),
		countConsumedVerificationCodes(t, verificationID),
		"the code must be consumed exactly once",
	)

	require.NotEmpty(t, winner.token, "the winner must receive the continuation token")

	// The verification request reached its terminal state, so the flow cannot be
	// replayed a third time even if the token were obtained again.
	state, err := db.GetRegistrationState(ctx, email)
	require.NoError(t, err)

	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		state.VerificationStatus,
	)

	// The registration is still pending: it becomes completed only at
	// finalization, and finalizing it early would be a different bug.
	require.Equal(
		t,
		string(registration.PendingRegistrationPending),
		state.Status,
	)
}

// loserCode returns the error code reported by whichever request did not succeed.
func loserCode(outcomes []verificationRaceOutcome) string {
	for _, outcome := range outcomes {
		if outcome.status >= 400 {
			return outcome.code
		}
	}

	return ""
}

// countFinalizedArtifacts counts the rows one finalized manual registration
// must produce for an address: the account, its credential, and its Unsorted
// collection.
//
// Each is asserted separately because they are created by three statements in
// one transaction, and a partial failure would leave all three absent while a
// count of one of them alone could not tell which.
func countFinalizedArtifacts(
	t *testing.T,
	email string,
) (users, credentials, collections int64) {
	t.Helper()

	ctx := context.Background()

	require.NoError(t, testPool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM users WHERE email = $1",
		email,
	).Scan(&users))

	require.NoError(t, testPool.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM password_credentials pc
		 JOIN users u ON u.id = pc.user_id
		 WHERE u.email = $1`,
		email,
	).Scan(&credentials))

	require.NoError(t, testPool.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM collections c
		 JOIN users u ON u.id = c.user_id
		 WHERE u.email = $1`,
		email,
	).Scan(&collections))

	return users, credentials, collections
}

// Two requests carrying the same continuation token must produce exactly one
// account.
//
// The continuation is a single-use capability, and the loser is refused by the
// unique index on users.email rather than by a lock anyone took deliberately: the
// second insert blocks until the first transaction commits and then loses.
func TestFinalizeManualRegistration_ConcurrentSameTokenCreatesExactlyOneUser(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const email = "finalize-race@example.com"
	const token = "finalize-race-continuation-token"

	_, err := db.CreateRegistrationContinuation(ctx,
		registrationtestdb.CreateRegistrationContinuationParams{
			Email:     email,
			TokenHash: security.HashToken(token),
		})
	require.NoError(t, err)

	type finalizeOutcome struct {
		status int
		code   string
	}

	post := func() finalizeOutcome {
		t.Helper()

		req := httptest.NewRequest(
			http.MethodPost,
			"/auth/register/finalize/manual",
			strings.NewReader(
				`{"display_name":"Race Tester","password":"password123"}`,
			),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Registration-Continuation", token)

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		outcome := finalizeOutcome{status: resp.StatusCode}

		if outcome.status < 400 {
			return outcome
		}

		var errBody registerAPIError

		require.NoError(
			t,
			json.NewDecoder(resp.Body).Decode(&errBody),
		)
		outcome.code = errBody.Error.Code

		return outcome
	}

	outcomes := make([]finalizeOutcome, 2)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = post()
	})

	var succeeded, conflicted int

	loser := ""

	for _, outcome := range outcomes {
		switch outcome.status {
		case http.StatusCreated:
			succeeded++
		case http.StatusConflict:
			conflicted++
			loser = outcome.code
		}
	}

	require.Equal(t, 1, succeeded, "exactly one finalization may succeed: %+v", outcomes)
	require.Equal(t, 1, conflicted, "exactly one must be refused: %+v", outcomes)

	// The account already exists is what the second request is told, because that
	// is what actually happened: the first one created it. Anything else would
	// be a fault being reported as a user error.
	require.Equal(
		t,
		registration.CodeUserAlreadyExists,
		loser,
		"the loser must be told the user already exists",
	)

	users, credentials, collections := countFinalizedArtifacts(t, email)

	require.Equal(t, int64(1), users, "a concurrent finalize must not create two users")
	require.Equal(
		t,
		int64(1),
		credentials,
		"exactly one credential must exist for the account",
	)
	require.Equal(
		t,
		int64(1),
		collections,
		"every permanent user owns exactly one Unsorted collection",
	)

	// The Unsorted collection that was created is the real one, not a spare: a
	// duplicate would satisfy a count of one while leaving the user filing into
	// the wrong row.
	require.Equal(
		t,
		int64(1),
		countUnsortedCollections(t, email),
		"the account must own exactly one Unsorted collection",
	)

	state, err := db.GetFinalizedRegistrationState(ctx, email)
	require.NoError(t, err)

	require.Equal(
		t,
		string(registration.PendingRegistrationCompleted),
		state.RegistrationStatus,
	)

	require.True(
		t,
		state.ConsumedAt.Valid,
		"the continuation must be consumed, or it could be replayed",
	)

	require.Equal(t, state.UserID, state.PasswordUserID)
}

// countUnsortedCollections counts the system Unsorted collection one account
// owns.
func countUnsortedCollections(t *testing.T, email string) int64 {
	t.Helper()

	var count int64

	err := testPool.QueryRow(
		context.Background(),
		`SELECT COUNT(*)
		 FROM collections c
		 JOIN users u ON u.id = c.user_id
		 WHERE u.email = $1
		   AND c.system_key = 'unsorted'`,
		email,
	).Scan(&count)
	require.NoError(t, err)

	return count
}
