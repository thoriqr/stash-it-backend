package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationtestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/registration/generated"
)

// These tests cover the point where a registration's verification request has
// reached its terminal `verified` state while the registration itself is still
// pending. Before the lifecycle fix the reuse branch consulted only the
// registration's own status, so it handed back a verification id that every
// verification route refuses.
//
// The continuation is what decides the recovery. While it is unconsumed and
// unexpired it is the caller's only remaining way to finish the flow, and
// replacing the registration would cancel it — this route is unauthenticated.
// Once it can no longer be used the registration holds no capability and is
// replaced. Both halves are asserted here, against real PostgreSQL, because the
// decision is the interaction between a locked row, a partial unique index and a
// hashed capability, none of which a mock repository can express.

const manualRegistrationPINPrefix = "Your registration verification code is: "

// openVerifiedManualRegistration drives the real manual flow through PIN
// issuance and verification and returns the verification id plus the
// continuation token the production path minted.
func openVerifiedManualRegistration(
	t *testing.T,
	emailAddress string,
) (uuid.UUID, string) {
	t.Helper()

	testEmailSender.Messages = nil

	verificationID := registerAndReturnVerificationID(t, emailAddress)

	pinResponse := requestCreatePIN(t, verificationID)
	require.Equal(t, http.StatusCreated, pinResponse.StatusCode)

	require.Len(t, testEmailSender.Messages, 1)

	pin := strings.TrimPrefix(
		testEmailSender.Messages[0].Text,
		manualRegistrationPINPrefix,
	)
	require.Len(t, pin, 6, "a verification code is six digits")

	outcome := postVerify(t, verificationID, pin)
	require.Equal(t, http.StatusOK, outcome.status)
	require.NotEmpty(t, outcome.token)

	return verificationID, outcome.token
}

// pendingRegistrationIDForEmail returns the newest registration still pending
// for an address.
func pendingRegistrationIDForEmail(
	t *testing.T,
	emailAddress string,
) uuid.UUID {
	t.Helper()

	db := registrationtestdb.New(testPool)

	history, err := db.GetRegistrationHistory(
		context.Background(),
		emailAddress,
	)
	require.NoError(t, err)

	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Status == string(registration.PendingRegistrationPending) {
			return history[i].ID
		}
	}

	t.Fatalf("no pending registration for %s", emailAddress)

	return uuid.Nil
}

// expireRegistrationContinuation ages the registration's continuation past its
// deadline. This is the state a caller who abandoned the flow reaches on its
// own; the test only removes the wait.
func expireRegistrationContinuation(
	t *testing.T,
	pendingRegistrationID uuid.UUID,
) {
	t.Helper()

	_, err := testPool.Exec(
		context.Background(),
		`UPDATE registration_continuations
		 SET expires_at = NOW() - INTERVAL '1 minute'
		 WHERE pending_registration_id = $1
		   AND consumed_at IS NULL`,
		pendingRegistrationID,
	)
	require.NoError(t, err)
}

type registerManualOutcome struct {
	status             int
	code               string
	verificationID     uuid.UUID
	verificationStatus string
	rawBody            string
	err                error
}

// postRegisterManual calls the registration route and reports everything a
// caller needs. It takes no *testing.T so it is safe from participant
// goroutines; assertions are made by the test's own goroutine.
func postRegisterManual(emailAddress string) registerManualOutcome {
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"`+emailAddress+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	response, err := testApp.Test(request)
	if err != nil {
		return registerManualOutcome{err: err}
	}

	rawBody, err := io.ReadAll(response.Body)
	if err != nil {
		return registerManualOutcome{
			status: response.StatusCode,
			err:    err,
		}
	}

	outcome := registerManualOutcome{
		status:  response.StatusCode,
		rawBody: string(rawBody),
	}

	if outcome.status >= 400 {
		var body registerAPIError

		if err := json.Unmarshal(rawBody, &body); err != nil {
			outcome.err = err

			return outcome
		}

		outcome.code = body.Error.Code

		return outcome
	}

	var body struct {
		Data struct {
			VerificationID     uuid.UUID `json:"verification_id"`
			VerificationStatus string    `json:"verification_status"`
		} `json:"data"`
	}

	if err := json.Unmarshal(rawBody, &body); err != nil {
		outcome.err = err

		return outcome
	}

	outcome.verificationID = body.Data.VerificationID
	outcome.verificationStatus = body.Data.VerificationStatus

	return outcome
}

func getRegistrationContinuationStatus(
	t *testing.T,
	token string,
) (int, string) {
	t.Helper()

	request := httptest.NewRequest(
		http.MethodGet,
		"/auth/register/continuation",
		nil,
	)
	request.Header.Set("X-Registration-Continuation", token)

	response, err := testApp.Test(request)
	require.NoError(t, err)

	if response.StatusCode < 400 {
		return response.StatusCode, ""
	}

	var body registerAPIError

	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))

	return response.StatusCode, body.Error.Code
}

func finalizeManualRegistrationRaw(token string) (int, string, error) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/finalize/manual",
		strings.NewReader(`{"display_name":"Lifecycle User","password":"password123"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Registration-Continuation", token)

	response, err := testApp.Test(request)
	if err != nil {
		return 0, "", err
	}

	if response.StatusCode < 400 {
		return response.StatusCode, "", nil
	}

	var body registerAPIError

	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return response.StatusCode, "", err
	}

	return response.StatusCode, body.Error.Code, nil
}

func finalizeSocialRegistrationRaw(token string) (int, string, error) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/finalize/social",
		strings.NewReader(`{"display_name":"Lifecycle User"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Registration-Continuation", token)
	request.Header.Set("X-Platform", "web")

	response, err := testApp.Test(request)
	if err != nil {
		return 0, "", err
	}

	if response.StatusCode < 400 {
		return response.StatusCode, "", nil
	}

	var body registerAPIError

	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return response.StatusCode, "", err
	}

	return response.StatusCode, body.Error.Code, nil
}

func countSessionsForEmail(t *testing.T, emailAddress string) int64 {
	t.Helper()

	var count int64

	err := testPool.QueryRow(
		context.Background(),
		`SELECT COUNT(*)
		 FROM sessions s
		 JOIN users u ON u.id = s.user_id
		 WHERE u.email = $1`,
		emailAddress,
	).Scan(&count)
	require.NoError(t, err)

	return count
}

// A verified registration with a live continuation is the caller's only
// remaining way to finish. Re-registering must not cancel it: this route is
// unauthenticated, so anyone who knows the address could otherwise expire the
// registration out from under the person who verified it.
func TestRegisterManual_VerifiedWithLiveContinuationIsReused(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "verified-live-manual@example.com"

	verificationID, continuationToken := openVerifiedManualRegistration(
		t,
		emailAddress,
	)

	state, err := db.GetRegistrationState(ctx, emailAddress)
	require.NoError(t, err)
	require.Equal(t, string(registration.PendingRegistrationPending), state.Status)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		state.VerificationStatus,
	)
	require.Equal(t, int32(1), state.PinIssuedCount)

	reused := postRegisterManual(emailAddress)
	require.NoError(t, reused.err)
	require.Equal(t, http.StatusCreated, reused.status)
	require.Equal(
		t,
		verificationID,
		reused.verificationID,
		"a live continuation must retain the registration and its verification id",
	)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		reused.verificationStatus,
		"the response must tell the client that the PIN step is already complete",
	)
	require.NotContains(
		t,
		reused.rawBody,
		"registration_continuation_token",
		"the recovery response must not return a continuation token",
	)
	require.NotContains(
		t,
		reused.rawBody,
		continuationToken,
		"the recovery response must not return the stored token",
	)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(
		t,
		history,
		1,
		"no replacement may be created while the continuation is usable",
	)
	require.Equal(t, string(registration.PendingRegistrationPending), history[0].Status)

	stateAfter, err := db.GetRegistrationState(ctx, emailAddress)
	require.NoError(t, err)
	require.Equal(
		t,
		int32(1),
		stateAfter.PinIssuedCount,
		"the recovery response must not issue a new PIN",
	)

	status, code := getRegistrationContinuationStatus(t, continuationToken)
	require.Equal(t, http.StatusOK, status, "the continuation must stay usable: %s", code)

	finalizeStatus, finalizeCode, err := finalizeManualRegistrationRaw(continuationToken)
	require.NoError(t, err)
	require.Equal(
		t,
		http.StatusCreated,
		finalizeStatus,
		"the retained continuation must still finalize: %s",
		finalizeCode,
	)

	users, credentials, collections := countFinalizedArtifacts(t, emailAddress)
	require.Equal(t, int64(1), users)
	require.Equal(t, int64(1), credentials)
	require.Equal(t, int64(1), collections)
}

// Concurrent requests in state B must all converge on the same verified
// verification and must not replace the registration or cancel its live
// continuation.
func TestRegisterManual_ConcurrentVerifiedReuse(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "concurrent-verified-manual@example.com"

	verificationID, continuationToken := openVerifiedManualRegistration(
		t,
		emailAddress,
	)

	const attempts = 8

	outcomes := make([]registerManualOutcome, attempts)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = postRegisterManual(emailAddress)
	})

	for index, outcome := range outcomes {
		require.NoError(t, outcome.err)
		require.Equalf(
			t,
			http.StatusCreated,
			outcome.status,
			"request %d must succeed: %+v",
			index,
			outcome,
		)
		require.Equalf(
			t,
			verificationID,
			outcome.verificationID,
			"request %d must keep the verified registration",
			index,
		)
		require.Equalf(
			t,
			string(registration.VerificationRequestVerified),
			outcome.verificationStatus,
			"request %d must report that the PIN step is complete",
			index,
		)
	}

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(
		t,
		history,
		1,
		"no replacement may be created while the continuation is usable",
	)

	status, code := getRegistrationContinuationStatus(t, continuationToken)
	require.Equal(t, http.StatusOK, status, "the continuation must stay usable: %s", code)
}

// Once the continuation can no longer be used the registration holds no
// capability. It is expired and replaced so the address is not held for the
// rest of the registration's seven-day lifetime.
func TestRegisterManual_VerifiedWithExpiredContinuationIsReplaced(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "verified-expired-manual@example.com"

	oldVerificationID, continuationToken := openVerifiedManualRegistration(
		t,
		emailAddress,
	)

	expireRegistrationContinuation(
		t,
		pendingRegistrationIDForEmail(t, emailAddress),
	)

	replacement := postRegisterManual(emailAddress)
	require.NoError(t, replacement.err)
	require.Equal(t, http.StatusCreated, replacement.status)
	require.NotEqual(t, oldVerificationID, replacement.verificationID)
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		replacement.verificationStatus,
		"a replacement must ask the client to verify again",
	)

	newVerificationID := replacement.verificationID

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 2, "exactly one replacement may be created")

	require.Equal(
		t,
		string(registration.PendingRegistrationExpired),
		history[0].Status,
	)
	require.Equal(t, oldVerificationID, history[0].VerificationID)

	require.Equal(
		t,
		string(registration.PendingRegistrationPending),
		history[1].Status,
	)
	require.Equal(t, newVerificationID, history[1].VerificationID)
	require.Equal(t, int32(0), history[1].PinIssuedCount)

	// The replacement is the one that can issue a PIN.
	require.Equal(
		t,
		http.StatusCreated,
		requestCreatePIN(t, newVerificationID).StatusCode,
	)

	// The old verification id cannot. Its request is terminal, so every
	// verification route refuses it before any code is considered.
	oldPINResponse := requestCreatePIN(t, oldVerificationID)
	require.Equal(t, http.StatusConflict, oldPINResponse.StatusCode)

	var oldPINBody registerAPIError

	require.NoError(t, json.NewDecoder(oldPINResponse.Body).Decode(&oldPINBody))
	require.Equal(
		t,
		registration.CodeVerificationNotPending,
		oldPINBody.Error.Code,
	)

	status, code := getRegistrationContinuationStatus(t, continuationToken)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(
		t,
		registration.CodeRegistrationContinuationExpired,
		code,
	)

	// Only the old, expired continuation exists; a registration has none until
	// its own verification succeeds.
	require.Equal(t, int64(1), countContinuations(t, emailAddress))
}

// Concurrent replacement requests must converge on one replacement. The partial
// unique index decides the winner at the insert; the loser's single re-run reads
// the winner's committed row and reuses it.
func TestRegisterManual_ConcurrentReplacementConverges(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "concurrent-replacement-manual@example.com"

	oldVerificationID, _ := openVerifiedManualRegistration(t, emailAddress)

	expireRegistrationContinuation(
		t,
		pendingRegistrationIDForEmail(t, emailAddress),
	)

	const attempts = 8

	outcomes := make([]registerManualOutcome, attempts)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = postRegisterManual(emailAddress)
	})

	var replacementID uuid.UUID

	for index, outcome := range outcomes {
		require.NoError(t, outcome.err)
		require.Equalf(
			t,
			http.StatusCreated,
			outcome.status,
			"request %d must be answered with a replacement: %+v",
			index,
			outcome,
		)
		require.NotEqual(t, uuid.Nil, outcome.verificationID)
		require.NotEqual(t, oldVerificationID, outcome.verificationID)
		require.Equalf(
			t,
			string(registration.VerificationRequestPending),
			outcome.verificationStatus,
			"request %d must report a fresh pending verification",
			index,
		)

		if replacementID == uuid.Nil {
			replacementID = outcome.verificationID

			continue
		}

		require.Equalf(
			t,
			replacementID,
			outcome.verificationID,
			"request %d must converge on the one replacement",
			index,
		)
	}

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 2, "exactly one replacement may exist")

	require.Equal(t, oldVerificationID, history[0].VerificationID)
	require.Equal(
		t,
		string(registration.PendingRegistrationExpired),
		history[0].Status,
	)
	require.Equal(t, replacementID, history[1].VerificationID)
	require.Equal(
		t,
		string(registration.PendingRegistrationPending),
		history[1].Status,
	)
}

// A replacement requires an unusable continuation, so it can never race a
// finalization that holds a usable one. This test pins that: the finalization
// wins or the request that runs after it is refused, but the replacement path
// is never entered and nothing partial is written.
func TestRegisterManual_ConcurrentFinalizationAndReplacementLeavesNoOrphans(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "concurrent-finalize-manual@example.com"

	oldVerificationID, continuationToken := openVerifiedManualRegistration(
		t,
		emailAddress,
	)

	expireRegistrationContinuation(
		t,
		pendingRegistrationIDForEmail(t, emailAddress),
	)

	var replacement registerManualOutcome

	var finalizeStatus int

	var finalizeCode string

	var finalizeErr error

	runConcurrently(t, 2, func(index int) {
		if index == 0 {
			replacement = postRegisterManual(emailAddress)

			return
		}

		finalizeStatus, finalizeCode, finalizeErr = finalizeManualRegistrationRaw(
			continuationToken,
		)
	})

	require.NoError(t, replacement.err)
	require.Equal(t, http.StatusCreated, replacement.status)
	require.NotEqual(t, oldVerificationID, replacement.verificationID)
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		replacement.verificationStatus,
	)

	require.NoError(t, finalizeErr)
	require.Equal(t, http.StatusConflict, finalizeStatus)
	require.Equal(
		t,
		registration.CodeRegistrationContinuationExpired,
		finalizeCode,
	)

	users, credentials, collections := countFinalizedArtifacts(t, emailAddress)
	require.Zero(t, users, "a refused finalization must not leave a user")
	require.Zero(t, credentials)
	require.Zero(t, collections)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(
		t,
		string(registration.PendingRegistrationPending),
		history[1].Status,
	)
	require.Equal(t, int64(1), countContinuations(t, emailAddress))
}

// A registration request that races a finalization holding a live continuation
// must not cancel it. The request either reuses the registration or, if the
// finalization already committed, is told the registration is complete. In both
// cases exactly one account exists and the continuation was never spent by the
// registration path.
func TestRegisterManual_ConcurrentFinalizationWithLiveContinuationIsNotCancelled(
	t *testing.T,
) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "concurrent-live-manual@example.com"

	verificationID, continuationToken := openVerifiedManualRegistration(
		t,
		emailAddress,
	)

	var registrationOutcome registerManualOutcome

	var finalizeStatus int

	var finalizeCode string

	var finalizeErr error

	runConcurrently(t, 2, func(index int) {
		if index == 0 {
			registrationOutcome = postRegisterManual(emailAddress)

			return
		}

		finalizeStatus, finalizeCode, finalizeErr = finalizeManualRegistrationRaw(
			continuationToken,
		)
	})

	require.NoError(t, registrationOutcome.err)
	require.NoError(t, finalizeErr)
	require.Equal(
		t,
		http.StatusCreated,
		finalizeStatus,
		"the live continuation must still finalize: %s",
		finalizeCode,
	)

	switch registrationOutcome.status {
	case http.StatusCreated:
		require.Equal(
			t,
			verificationID,
			registrationOutcome.verificationID,
			"the registration must be retained rather than replaced",
		)
		require.Equal(
			t,
			string(registration.VerificationRequestVerified),
			registrationOutcome.verificationStatus,
			"the retained registration must report the completed verification",
		)
	case http.StatusConflict:
		require.Contains(
			t,
			[]string{
				registration.CodeRegistrationAlreadyCompleted,
				registration.CodeRegistrationAlreadyExists,
			},
			registrationOutcome.code,
			"a request that ran after the finalization must be refused as complete",
		)
	default:
		t.Fatalf(
			"unexpected registration status %d: %+v",
			registrationOutcome.status,
			registrationOutcome,
		)
	}

	users, credentials, collections := countFinalizedArtifacts(t, emailAddress)
	require.Equal(t, int64(1), users)
	require.Equal(t, int64(1), credentials)
	require.Equal(t, int64(1), collections)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 1, "the registration must not have been replaced")
	require.Equal(
		t,
		string(registration.PendingRegistrationCompleted),
		history[0].Status,
	)
}

// A verified social registration belongs to the identity that opened it. The
// same identity asking again has chosen to restart, and there is no third party
// to cancel, so the registration is replaced and a fresh verification id is
// returned.
func TestCreateSocialRegistration_SameIdentityOnVerifiedRegistrationReplacesIt(
	t *testing.T,
) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	continuationToken := openSocialRegistrationWithContinuation(
		t,
		socialGoogleEmail,
	)

	before, err := db.GetSocialPendingRegistrationStates(ctx, socialGoogleEmail)
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(
		t,
		string(registration.PendingRegistrationPending),
		before[0].Status,
	)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		before[0].VerificationStatus.String,
	)

	result := postGoogleLogin(t)
	require.Equal(t, http.StatusOK, result.status)
	require.Equal(t, "registration_required", result.outcome)
	require.NotEmpty(t, result.verificationID)
	require.NotEqual(
		t,
		before[0].VerificationID.String(),
		result.verificationID,
	)

	after, err := db.GetSocialPendingRegistrationStates(ctx, socialGoogleEmail)
	require.NoError(t, err)
	require.Len(t, after, 2, "the replacement must be added, not substituted")

	original := after[0]
	replacement := after[1]

	require.Equal(t, string(registration.PendingRegistrationExpired), original.Status)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		original.VerificationStatus.String,
	)
	require.Equal(t, socialGoogleSubject, original.ProviderSubject.String)

	require.Equal(t, string(registration.PendingRegistrationPending), replacement.Status)
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		replacement.VerificationStatus.String,
	)
	require.Equal(t, result.verificationID, replacement.VerificationID.String())
	require.Equal(t, socialGoogleSubject, replacement.ProviderSubject.String)

	require.Equal(
		t,
		int64(2),
		countPendingSocialIdentitiesForRegistration(t, socialGoogleEmail),
	)
	require.Equal(
		t,
		int64(2),
		countVerificationRequestsForRegistration(t, socialGoogleEmail),
	)

	// The old continuation died with the registration it belonged to.
	status, code := getRegistrationContinuationStatus(t, continuationToken)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, registration.CodeRegistrationNotPending, code)

	// The replacement is usable.
	require.Equal(
		t,
		http.StatusCreated,
		requestCreatePIN(t, uuid.MustParse(result.verificationID)).StatusCode,
	)
}

// The identity-mismatch refusal must run before any recovery. A different
// Google identity presenting the same address is refused even when the stored
// registration's verification request is already terminal, and even when there
// is no continuation at all — which is the most permissive state a recovery
// rule could act on.
func TestCreateSocialRegistration_DifferentIdentityOnVerifiedLiveRegistrationIsRefused(
	t *testing.T,
) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	verificationID, err := db.CreateSocialPendingRegistration(
		ctx,
		registrationtestdb.CreateSocialPendingRegistrationParams{
			Email:           socialGoogleEmail,
			Provider:        socialGoogleProvider,
			ProviderSubject: foreignGoogleSubject,
			EmailSnapshot:   pgText(socialGoogleEmail),
			DisplayNameSnapshot: pgText(
				"Someone Else",
			),
		},
	)
	require.NoError(t, err)

	// Make the verification terminal. No continuation is created, so a wrong
	// branch ordering would see "verified and nothing usable" and replace.
	_, err = testPool.Exec(
		ctx,
		`UPDATE verification_requests SET status = 'verified' WHERE id = $1`,
		verificationID,
	)
	require.NoError(t, err)

	before, err := db.GetSocialPendingRegistrationStates(ctx, socialGoogleEmail)
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		before[0].VerificationStatus.String,
	)

	result := postGoogleLogin(t)
	require.Equal(t, http.StatusConflict, result.status)
	require.Equal(t, registration.CodeRegistrationIdentityMismatch, result.code)

	after, err := db.GetSocialPendingRegistrationStates(ctx, socialGoogleEmail)
	require.NoError(t, err)
	require.Len(t, after, 1, "a refusal must not create or replace a registration")

	require.Equal(t, before[0].ID, after[0].ID)
	require.Equal(
		t,
		string(registration.PendingRegistrationPending),
		after[0].Status,
		"the stored registration must stay pending, not be expired",
	)
	require.Equal(t, foreignGoogleSubject, after[0].ProviderSubject.String)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		after[0].VerificationStatus.String,
	)
	require.Equal(
		t,
		int64(1),
		countPendingSocialIdentitiesForRegistration(t, socialGoogleEmail),
	)
	require.Equal(
		t,
		int64(1),
		countVerificationRequestsForRegistration(t, socialGoogleEmail),
	)
}

// Concurrent same-identity requests must produce one replacement and point every
// caller at it. The identity is the app's verified Google fixture, so every
// request passes the mismatch check; the partial unique index and the single
// re-run resolve the rest.
func TestCreateSocialRegistration_ConcurrentSameIdentityOnVerifiedRegistrationConverges(
	t *testing.T,
) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	openSocialRegistrationWithContinuation(t, socialGoogleEmail)

	before, err := db.GetSocialPendingRegistrationStates(ctx, socialGoogleEmail)
	require.NoError(t, err)
	require.Len(t, before, 1)

	const attempts = 8

	outcomes := make([]googleLoginOutcome, attempts)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = postGoogleLogin(t)
	})

	var replacementID string

	for index, outcome := range outcomes {
		require.Equalf(
			t,
			http.StatusOK,
			outcome.status,
			"request %d must succeed: %+v",
			index,
			outcome,
		)
		require.Equal(t, "registration_required", outcome.outcome)
		require.NotEmpty(t, outcome.verificationID)
		require.NotEqual(
			t,
			before[0].VerificationID.String(),
			outcome.verificationID,
		)

		if replacementID == "" {
			replacementID = outcome.verificationID

			continue
		}

		require.Equalf(
			t,
			replacementID,
			outcome.verificationID,
			"request %d must converge on the one replacement",
			index,
		)
	}

	after, err := db.GetSocialPendingRegistrationStates(ctx, socialGoogleEmail)
	require.NoError(t, err)
	require.Len(t, after, 2, "exactly one replacement may exist")

	require.Equal(t, string(registration.PendingRegistrationExpired), after[0].Status)
	require.Equal(t, string(registration.PendingRegistrationPending), after[1].Status)
	require.Equal(t, replacementID, after[1].VerificationID.String())
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		after[1].VerificationStatus.String,
	)
	require.Equal(
		t,
		int64(2),
		countPendingSocialIdentitiesForRegistration(t, socialGoogleEmail),
	)
}

// A same-identity login and a finalization carrying the live continuation race
// for the same registration row. Exactly one of them can win, and the loser must
// leave nothing behind: no account without its credential, collection or
// session, and no pending registration beside a completed one.
func TestFinalizeSocialRegistration_ConcurrentReplacementLeavesConsistentState(
	t *testing.T,
) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	continuationToken := openSocialRegistrationWithContinuation(
		t,
		socialGoogleEmail,
	)

	var login googleLoginOutcome

	var finalizeStatus int

	var finalizeCode string

	var finalizeErr error

	runConcurrently(t, 2, func(index int) {
		if index == 0 {
			login = postGoogleLogin(t)

			return
		}

		finalizeStatus, finalizeCode, finalizeErr = finalizeSocialRegistrationRaw(
			continuationToken,
		)
	})

	require.NoError(t, finalizeErr)

	users, credentials, collections := countFinalizedArtifacts(t, socialGoogleEmail)

	states, err := db.GetSocialPendingRegistrationStates(ctx, socialGoogleEmail)
	require.NoError(t, err)

	switch users {
	case 0:
		// The replacement won. The finalization was refused and its whole
		// transaction rolled back, so no account state exists.
		require.Equal(t, http.StatusConflict, finalizeStatus)
		require.NotEmpty(t, finalizeCode)

		require.Equal(t, http.StatusOK, login.status)
		require.Equal(t, "registration_required", login.outcome)
		require.NotEmpty(t, login.verificationID)

		require.Len(t, states, 2, "the expired original and its replacement")
		require.Equal(t, string(registration.PendingRegistrationExpired), states[0].Status)
		require.Equal(t, string(registration.PendingRegistrationPending), states[1].Status)

	case 1:
		// The finalization won. The completed account is the only registration
		// for the address, and every side record it owns exists exactly once.
		require.Equal(t, http.StatusCreated, finalizeStatus)
		require.Len(t, states, 1, "a completed registration must not be replaced")
		require.Equal(
			t,
			string(registration.PendingRegistrationCompleted),
			states[0].Status,
		)

		require.Equal(t, int64(1), collections)
		require.Equal(
			t,
			int64(1),
			countSessionsForEmail(t, socialGoogleEmail),
		)
		require.Equal(
			t,
			int64(1),
			countAuthIdentitiesForProviderSubject(
				t,
				socialGoogleProvider,
				socialGoogleSubject,
			),
		)

	default:
		t.Fatalf("expected zero or one account, got %d", users)
	}

	require.Zero(t, credentials, "a social registration creates no password credential")

	if users == 0 {
		require.Zero(t, collections)
		require.Zero(t, countSessionsForEmail(t, socialGoogleEmail))
		require.Zero(
			t,
			countAuthIdentitiesForProviderSubject(
				t,
				socialGoogleProvider,
				socialGoogleSubject,
			),
		)
	}

	require.Equal(
		t,
		int64(len(states)),
		countPendingSocialIdentitiesForRegistration(t, socialGoogleEmail),
		"every registration must keep exactly one identity",
	)
}
