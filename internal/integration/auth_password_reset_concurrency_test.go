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

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
)

// These tests pin the two password-reset transitions that are single-use by
// design, and that nothing in the application makes single-use on its own.
//
// Both validate outside their transaction and mutate inside it, so two requests
// carrying the same credential can both pass every check the service makes before
// either reaches the database. What separates them is PostgreSQL:
//
//   - VerifyPasswordReset opens with ConsumeVerificationCode, which matches only
//     while consumed_at IS NULL, then MarkVerificationRequestVerified, which
//     matches only while status = 'pending'. Both results are checked against
//     rowsAffected == 1.
//   - FinalizePasswordReset opens with an upsert on password_credentials, whose
//     ON CONFLICT DO UPDATE takes a row lock on the conflicting tuple, and then
//     refuses to continue unless ConsumePasswordResetContinuation matched exactly
//     one row.
//
// Nothing here can be proven with a mock repository. Every assertion below is
// about which transaction wins a row lock, what PostgreSQL does to a predicate
// when the row it matched has since changed, and what is left in the tables
// afterwards — none of which a stub can express. The assertions are read from the
// database rather than inferred from a status code, because two well-formed
// responses cannot be told apart by their shape: only the stored rows say whether
// one credential was written or two.
//
// A note on what these tests do and do not prove. They hold the invariant of
// "exactly one continuation" and "exactly one credential", but they do not
// discriminate *which* guard produced it. In a losing request the rejection
// usually happens at the service's first read, before the transaction is ever
// opened, because the winner has already committed — so which error code the
// loser sees depends only on scheduling. Both legitimate codes are therefore
// accepted and the invariant is asserted from the database instead. This mirrors
// what auth_register_concurrency_test.go established for the equivalent
// registration transitions; it does not copy its cases, because password reset
// has no registration type, creates no account, and revokes sessions rather than
// creating one.
//
// Synchronization is the barrier in runConcurrently, never a sleep. Each
// participant parks until every participant is ready, so the requests contend
// inside the same window rather than being issued one after another. In sequence
// the second request would simply be refused by already-consumed state and these
// tests would prove nothing about concurrency.

// passwordResetAPIError is the error envelope every failure in this flow returns.
type passwordResetAPIError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// passwordResetPINPrefix is how the password reset verification mail opens.
//
// The PIN is read out of the message rather than chosen by the test, because it is
// six random digits generated at issuance and no value may be assumed. The message
// is the only place the plaintext exists.
const passwordResetPINPrefix = "Your password reset verification code is: "

// startPasswordResetWithPIN creates an account, opens a reset through the request
// route and issues a code through the issuance route, then returns the user id,
// the verification id and the PIN that was actually mailed.
//
// The whole flow is driven through the real endpoints rather than seeded, so the
// state these tests race over is state production code created. The user id comes
// back because a caller may need to arrange state the reset depends on — a prior
// credential, an active session — and the account has to exist before a reset can
// be opened for it.
func startPasswordResetWithPIN(
	t *testing.T,
	emailAddress string,
) (uuid.UUID, uuid.UUID, string) {
	t.Helper()

	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	userID, err := db.CreatePasswordResetUser(ctx, emailAddress)
	require.NoError(t, err)

	// Cleared before the flow rather than after it, so the message read below is
	// unambiguously this flow's. The sender is shared across the suite.
	testEmailSender.Messages = nil

	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"`+emailAddress+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	requestResponse, err := testApp.Test(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, requestResponse.StatusCode)

	var requestBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(requestResponse.Body).Decode(&requestBody),
	)

	verificationID := requestBody.Data.VerificationID
	require.NotEqual(t, uuid.Nil, verificationID)

	// A verification that has never sent a code has no cooldown to observe, so the
	// first issuance is always allowed and needs no ageing first.
	issue := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+verificationID.String()+"/pin",
		nil,
	)

	issueResponse, err := testApp.Test(issue)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, issueResponse.StatusCode)

	require.Len(t, testEmailSender.Messages, 1)

	issuedPIN := strings.TrimPrefix(
		testEmailSender.Messages[0].Text,
		passwordResetPINPrefix,
	)

	require.Len(t, issuedPIN, 6, "a verification code is six digits")

	return userID, verificationID, issuedPIN
}

// resetRaceOutcome is one participant's result.
//
// It is recorded rather than asserted inside the participant: runConcurrently
// runs each body on its own goroutine, and a testify failure there would call
// t.FailNow off the test's own goroutine. Every assertion is made on the test's
// goroutine instead, which is the arrangement auth_session_concurrency_test.go
// already uses for the same reason.
type resetRaceOutcome struct {
	status int
	code   string
	token  string
	err    error
}

// verifyPasswordResetPIN calls the verification route with the given PIN.
//
// It takes no *testing.T and asserts nothing: it is called from participant
// goroutines and returns everything a caller needs to decide for itself.
func verifyPasswordResetPIN(
	verificationID uuid.UUID,
	pin string,
) resetRaceOutcome {
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+verificationID.String()+"/verify",
		strings.NewReader(`{"pin":"`+pin+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	response, err := testApp.Test(request)
	if err != nil {
		return resetRaceOutcome{err: err}
	}

	outcome := resetRaceOutcome{status: response.StatusCode}

	if outcome.status < 400 {
		var body struct {
			Data struct {
				Token string `json:"password_reset_continuation_token"`
			} `json:"data"`
		}

		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			outcome.err = err

			return outcome
		}

		outcome.token = body.Data.Token

		return outcome
	}

	var decoded passwordResetAPIError

	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		outcome.err = err

		return outcome
	}

	outcome.code = decoded.Error.Code

	return outcome
}

// finalizePasswordReset calls the finalization route with the given continuation
// token and password.
//
// Like verifyPasswordResetPIN it asserts nothing and is safe to call from a
// participant goroutine. Finalization returns no data, so only the status and any
// error code are reported.
func finalizePasswordReset(
	token string,
	password string,
) resetRaceOutcome {
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/finalize",
		strings.NewReader(`{"password":"`+password+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Password-Reset-Continuation", token)

	response, err := testApp.Test(request)
	if err != nil {
		return resetRaceOutcome{err: err}
	}

	outcome := resetRaceOutcome{status: response.StatusCode}

	if outcome.status < 400 {
		return outcome
	}

	var decoded passwordResetAPIError

	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		outcome.err = err

		return outcome
	}

	outcome.code = decoded.Error.Code

	return outcome
}

// countResetContinuations counts the continuations created for one address's
// password resets.
//
// This is the assertion an HTTP status code cannot make. Both requests are
// answered with a well-formed response, and only this count distinguishes one
// continuation from two.
//
// The query is written inline rather than added to the sqlc test target, which is
// what auth_register_concurrency_test.go does for the equivalent registration
// counts: these are single-count assertions used by one file, and the generated
// test package is not the place for a query that has exactly one caller.
func countResetContinuations(t *testing.T, email string) int64 {
	t.Helper()

	var count int64

	err := testPool.QueryRow(
		context.Background(),
		`SELECT COUNT(*)
		 FROM password_reset_continuations prc
		 JOIN pending_password_resets ppr
		   ON ppr.id = prc.pending_password_reset_id
		 WHERE ppr.email = $1`,
		email,
	).Scan(&count)
	require.NoError(t, err)

	return count
}

// countConsumedResetVerificationCodes counts consumed codes for one verification.
//
// A second continuation would be reachable only if the same code could be
// consumed twice, so this is the lower-level statement of the same invariant.
func countConsumedResetVerificationCodes(
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

// Two requests carrying the same valid PIN must produce exactly one continuation.
//
// The verification code is the credential and it is single-use, so the loser is
// refused rather than answered with a second token that would both be valid and
// would leave the code consumable again.
func TestVerifyPasswordReset_ConcurrentSamePINIssuesExactlyOneContinuation(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "verify-race@example.com"

	_, verificationID, issuedPIN := startPasswordResetWithPIN(t, emailAddress)

	// Both requests are built and parked before either runs, so they contend
	// rather than arriving in sequence. In sequence the second would be refused by
	// the already-consumed code and this test would prove nothing.
	outcomes := make([]resetRaceOutcome, 2)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = verifyPasswordResetPIN(verificationID, issuedPIN)
	})

	var succeeded, conflicted int

	var winner resetRaceOutcome

	for _, outcome := range outcomes {
		require.NoError(t, outcome.err)

		switch outcome.status {
		case http.StatusOK:
			succeeded++
			winner = outcome
		case http.StatusConflict:
			conflicted++
		}
	}

	require.Equal(
		t,
		1,
		succeeded,
		"exactly one verification may succeed: %+v",
		outcomes,
	)
	require.Equal(
		t,
		1,
		conflicted,
		"exactly one must be refused: %+v",
		outcomes,
	)

	// Which conflict the loser sees depends only on how far it had got when the
	// winner committed, not on anything a caller controls. A request that read the
	// verification after the winner committed sees VERIFICATION_NOT_PENDING; one
	// that read the code first and then lost the race to consume it sees the
	// generic CONFLICT. Both are correct refusals of a single-use credential, and
	// pinning either one exactly would assert on scheduling rather than behaviour.
	require.Contains(
		t,
		[]string{
			apperror.CodeConflict,
			passwordreset.CodeVerificationNotPending,
		},
		loserResetCode(outcomes),
		"the loser must be refused, never given a second token",
	)

	require.NotEmpty(
		t,
		winner.token,
		"the winner must receive the continuation token",
	)
	require.Empty(
		t,
		loserResetToken(outcomes),
		"the loser must not be handed a usable continuation",
	)

	require.Equal(
		t,
		int64(1),
		countResetContinuations(t, emailAddress),
		"two requests with one valid PIN must leave exactly one continuation",
	)

	require.Equal(
		t,
		int64(1),
		countConsumedResetVerificationCodes(t, verificationID),
		"the code must be consumed exactly once",
	)

	// The verification request reached its terminal state, so the flow cannot be
	// replayed a third time even if the token were obtained again.
	state, err := db.GetPasswordResetVerificationState(ctx, verificationID)
	require.NoError(t, err)

	require.Equal(
		t,
		string(passwordreset.VerificationRequestVerified),
		state.VerificationStatus,
	)

	// The reset is still pending: it becomes completed only at finalization, and
	// completing it here would be a different bug.
	resetState, err := db.GetPasswordResetState(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetPending),
		resetState.PasswordResetStatus,
		"verification must not complete the pending reset",
	)

	// Exactly one code exists for the request, so the winner consumed the one it
	// was issued rather than a second row appearing.
	require.Equal(
		t,
		int32(1),
		state.PinIssuedCount,
		"the race must not have issued another code",
	)
}

// loserResetCode returns the error code reported by whichever request did not
// succeed.
func loserResetCode(outcomes []resetRaceOutcome) string {
	for _, outcome := range outcomes {
		if outcome.status >= 400 {
			return outcome.code
		}
	}

	return ""
}

// loserResetToken returns the continuation token handed to whichever request did
// not succeed, which must be none.
func loserResetToken(outcomes []resetRaceOutcome) string {
	for _, outcome := range outcomes {
		if outcome.status >= 400 {
			return outcome.token
		}
	}

	return ""
}

// Two requests carrying the same continuation token must produce exactly one
// password credential, holding the winner's password.
//
// The continuation is a single-use capability and both requests can read it as
// unconsumed before either commits, so the database is what separates them. The
// upsert runs first inside the transaction and takes a row lock on the
// conflicting tuple, so the loser is serialized behind the winner; it then writes
// its own password hash, fails the continuation check, and rolls the whole
// transaction back. That rollback is the property this test exists to prove, and
// it is why the two requests carry *different* passwords: identical passwords
// could not distinguish a rolled-back write from two identical ones.
func TestFinalizePasswordReset_ConcurrentSameTokenWritesOneCredential(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	const emailAddress = "finalize-race@example.com"

	// The flow is driven to a real continuation through the real endpoints. The
	// continuation is the capability the two requests contend for, so seeding one
	// straight into the database would leave the loser racing over state no client
	// could have held.
	userID, verificationID, issuedPIN := startPasswordResetWithPIN(
		t,
		emailAddress,
	)

	// A pre-existing credential and an active session, arranged after the reset is
	// open because the account has to exist before one can be opened for it. Both
	// let the assertions below tell "the winner's password replaced the old one and
	// revoked the session" apart from "a new account was created".
	oldHash, err := testutil.NewPasswordHasher().Hash(ctx, "old-password")
	require.NoError(t, err)

	require.NoError(
		t,
		db.CreatePasswordCredentialForUser(
			ctx,
			passwordresetdbtest.CreatePasswordCredentialForUserParams{
				UserID:       userID,
				PasswordHash: oldHash,
			},
		),
	)

	_, err = db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	minted := verifyPasswordResetPIN(verificationID, issuedPIN)
	require.NoError(t, minted.err)
	require.Equal(t, http.StatusOK, minted.status, minted.code)
	require.NotEmpty(t, minted.token)

	const (
		firstPassword  = "race-password-one"
		secondPassword = "race-password-two"
	)

	passwords := []string{firstPassword, secondPassword}

	outcomes := make([]resetRaceOutcome, len(passwords))

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = finalizePasswordReset(
			minted.token,
			passwords[index],
		)
	})

	var succeeded, conflicted int

	winnerIndex := -1

	for index, outcome := range outcomes {
		require.NoError(t, outcome.err)

		switch outcome.status {
		case http.StatusOK:
			succeeded++
			winnerIndex = index
		case http.StatusConflict:
			conflicted++
		}
	}

	require.NotEqual(t, -1, winnerIndex, "no request succeeded: %+v", outcomes)
	require.Equal(
		t,
		1,
		succeeded,
		"exactly one finalization may succeed: %+v",
		outcomes,
	)
	require.Equal(
		t,
		1,
		conflicted,
		"exactly one must be refused: %+v",
		outcomes,
	)

	// The loser is refused either before opening a transaction, having read the
	// continuation as already consumed, or inside it, when its consumption of the
	// same row matches nothing. Which of the two happened depends only on
	// scheduling.
	require.Contains(
		t,
		[]string{
			passwordreset.CodePasswordResetContinuationConsumed,
			apperror.CodeConflict,
		},
		loserResetCode(outcomes),
		"the loser must be refused as a spent continuation, never answered as a "+
			"second success",
	)

	finalState, err := db.GetPasswordResetFinalState(ctx, emailAddress)
	require.NoError(t, err)

	require.True(
		t,
		finalState.ConsumedAt.Valid,
		"the continuation must be consumed, or it could be replayed",
	)
	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetCompleted),
		finalState.PasswordResetStatus,
	)

	// The credential holds the winner's password and is demonstrably not the old
	// one and not the loser's.
	winnerPassword := passwords[winnerIndex]
	loserPassword := passwords[1-winnerIndex]

	require.NotEqual(
		t,
		oldHash,
		finalState.PasswordHash,
		"the reset must have replaced the previous credential",
	)

	winnerCheck, err := testutil.NewPasswordHasher().Verify(
		ctx,
		winnerPassword,
		finalState.PasswordHash,
	)
	require.NoError(t, err)
	require.True(
		t,
		winnerCheck.Match,
		"the stored hash must be the password the winning request submitted",
	)

	loserCheck, err := testutil.NewPasswordHasher().Verify(
		ctx,
		loserPassword,
		finalState.PasswordHash,
	)
	require.NoError(t, err)
	require.False(
		t,
		loserCheck.Match,
		"the losing request's password hash must not survive its rollback",
	)

	// One credential for the account, not one per request. Counted rather than
	// inferred: a duplicate would leave the account with an ambiguous login.
	counts, err := db.CountPasswordResetStateForEmail(ctx, emailAddress)
	require.NoError(t, err)

	require.Equal(
		t,
		int64(1),
		counts.CredentialCount,
		"a concurrent finalize must not create two credentials",
	)

	// The winner still revoked the sessions, so the reset is complete in every
	// respect the success response claims.
	require.Zero(
		t,
		finalState.ActiveSessionCount,
		"a successful reset must leave no active session",
	)
}
