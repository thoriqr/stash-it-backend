package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	registrationtestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/registration/generated"
)

type manualRecoveryOutcome struct {
	status             int
	code               string
	verificationID     uuid.UUID
	verificationStatus string
	err                error
}

func postManualRecovery(emailAddress string) manualRecoveryOutcome {
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/recovery/manual",
		strings.NewReader(`{"email":"`+emailAddress+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	response, err := testApp.Test(request)
	if err != nil {
		return manualRecoveryOutcome{err: err}
	}

	rawBody, err := io.ReadAll(response.Body)
	if err != nil {
		return manualRecoveryOutcome{status: response.StatusCode, err: err}
	}

	outcome := manualRecoveryOutcome{status: response.StatusCode}

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

func requestRecoveryPIN(t *testing.T, verificationID uuid.UUID) string {
	t.Helper()

	testEmailSender.Messages = nil

	response := requestCreatePIN(t, verificationID)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.Len(t, testEmailSender.Messages, 1)

	pin := strings.TrimPrefix(
		testEmailSender.Messages[0].Text,
		manualRegistrationPINPrefix,
	)
	require.Len(t, pin, 6, "a verification code is six digits")

	return pin
}

// Recovery initiation must not expire, replace or otherwise disturb the
// registration. The old continuation remains usable until the recovery PIN is
// verified.
func TestRegisterManualRecovery_InitiationPreservesRegistration(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-init@example.com"

	oldVerificationID, oldToken := openVerifiedManualRegistration(t, emailAddress)
	oldPendingID := pendingRegistrationIDForEmail(t, emailAddress)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusCreated, recovery.status)
	require.NotEqual(t, oldVerificationID, recovery.verificationID)
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		recovery.verificationStatus,
	)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 1, "initiation must not create a replacement")
	require.Equal(t, string(registration.PendingRegistrationPending), history[0].Status)
	require.Equal(t, oldVerificationID, history[0].VerificationID)

	status, code := getRegistrationContinuationStatus(t, oldToken)
	require.Equal(
		t,
		http.StatusOK,
		status,
		"initiation must not invalidate the old continuation: %s",
		code,
	)

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 2)

	require.Equal(t, oldVerificationID, requests[0].ID)
	require.Equal(
		t,
		string(registration.VerificationPurposeRegistration),
		requests[0].Purpose,
	)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		requests[0].Status,
	)

	require.Equal(t, recovery.verificationID, requests[1].ID)
	require.Equal(
		t,
		string(registration.VerificationPurposeRegistrationRecovery),
		requests[1].Purpose,
	)
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		requests[1].Status,
	)
	require.Equal(t, oldPendingID, requests[1].SubjectID)

	count, err := db.CountContinuationsForPendingRegistration(ctx, oldPendingID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	second := postManualRecovery(emailAddress)
	require.NoError(t, second.err)
	require.Equal(t, http.StatusCreated, second.status)
	require.Equal(
		t,
		recovery.verificationID,
		second.verificationID,
		"a second initiation must reuse the open recovery request",
	)

	requests, err = db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 2)
}

// The full recovery path replaces the old registration only after the recovery
// PIN is verified, then hands back a continuation for the new registration.
func TestRegisterManualRecovery_Success(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-success@example.com"

	oldVerificationID, oldToken := openVerifiedManualRegistration(t, emailAddress)
	oldPendingID := pendingRegistrationIDForEmail(t, emailAddress)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusCreated, recovery.status)

	pin := requestRecoveryPIN(t, recovery.verificationID)

	verified := postVerify(t, recovery.verificationID, pin)
	require.Equal(t, http.StatusOK, verified.status)
	require.NotEmpty(t, verified.token)
	require.NotEqual(t, oldToken, verified.token)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 2)

	require.Equal(t, string(registration.PendingRegistrationExpired), history[0].Status)
	require.Equal(t, oldVerificationID, history[0].VerificationID)

	require.Equal(t, string(registration.PendingRegistrationPending), history[1].Status)
	require.Equal(t, recovery.verificationID, history[1].VerificationID)
	require.Equal(t, int32(1), history[1].PinIssuedCount)

	newPendingID := history[1].ID

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 2)

	require.Equal(t, oldVerificationID, requests[0].ID)
	require.Equal(
		t,
		string(registration.VerificationPurposeRegistration),
		requests[0].Purpose,
	)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		requests[0].Status,
	)
	require.Equal(t, oldPendingID, requests[0].SubjectID)

	require.Equal(t, recovery.verificationID, requests[1].ID)
	require.Equal(
		t,
		string(registration.VerificationPurposeRegistration),
		requests[1].Purpose,
		"the recovery request must become the new registration's normal request",
	)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		requests[1].Status,
	)
	require.Equal(t, newPendingID, requests[1].SubjectID)

	oldCount, err := db.CountContinuationsForPendingRegistration(ctx, oldPendingID)
	require.NoError(t, err)
	require.Equal(t, int64(1), oldCount, "the old continuation must not be deleted")

	newCount, err := db.CountContinuationsForPendingRegistration(ctx, newPendingID)
	require.NoError(t, err)
	require.Equal(t, int64(1), newCount, "the new registration must have exactly one continuation")

	oldStatus, oldCode := getRegistrationContinuationStatus(t, oldToken)
	require.Equal(t, http.StatusConflict, oldStatus)
	require.Equal(t, registration.CodeRegistrationNotPending, oldCode)

	newStatus, newCode := getRegistrationContinuationStatus(t, verified.token)
	require.Equal(t, http.StatusOK, newStatus, newCode)

	oldFinalizeStatus, _, err := finalizeManualRegistrationRaw(oldToken)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, oldFinalizeStatus)

	users, credentials, collections := countFinalizedArtifacts(t, emailAddress)
	require.Zero(t, users, "the old continuation must not finalize the replaced registration")
	require.Zero(t, credentials)
	require.Zero(t, collections)

	newFinalizeStatus, newFinalizeCode, err := finalizeManualRegistrationRaw(verified.token)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, newFinalizeStatus, newFinalizeCode)

	users, credentials, collections = countFinalizedArtifacts(t, emailAddress)
	require.Equal(t, int64(1), users)
	require.Equal(t, int64(1), credentials)
	require.Equal(t, int64(1), collections)
}

// An expired continuation is exactly the case recovery exists for.
func TestRegisterManualRecovery_ExpiredContinuation(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-expired@example.com"

	_, oldToken := openVerifiedManualRegistration(t, emailAddress)

	expireRegistrationContinuation(
		t,
		pendingRegistrationIDForEmail(t, emailAddress),
	)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusCreated, recovery.status)

	pin := requestRecoveryPIN(t, recovery.verificationID)

	verified := postVerify(t, recovery.verificationID, pin)
	require.Equal(t, http.StatusOK, verified.status)
	require.NotEmpty(t, verified.token)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, string(registration.PendingRegistrationExpired), history[0].Status)
	require.Equal(t, string(registration.PendingRegistrationPending), history[1].Status)

	oldStatus, oldCode := getRegistrationContinuationStatus(t, oldToken)
	require.Equal(t, http.StatusConflict, oldStatus)
	require.Equal(
		t,
		registration.CodeRegistrationContinuationExpired,
		oldCode,
	)

	newStatus, newCode := getRegistrationContinuationStatus(t, verified.token)
	require.Equal(t, http.StatusOK, newStatus, newCode)
}

func TestRegisterManualRecovery_CompletedRegistrationRejected(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-completed@example.com"

	_, err := db.CreateCompletedRegistration(ctx, emailAddress)
	require.NoError(t, err)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusConflict, recovery.status)
	require.Equal(
		t,
		registration.CodeRegistrationAlreadyCompleted,
		recovery.code,
	)

	require.Equal(t, uuid.Nil, recovery.verificationID)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, string(registration.PendingRegistrationCompleted), history[0].Status)

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 1, "a refused initiation must not create a recovery request")
	require.Equal(
		t,
		string(registration.VerificationPurposeRegistration),
		requests[0].Purpose,
	)
	require.Equal(
		t,
		string(registration.VerificationRequestVerified),
		requests[0].Status,
	)
}

func TestRegisterManualRecovery_NoPendingRegistration(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	recovery := postManualRecovery("recovery-missing@example.com")
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusConflict, recovery.status)
	require.Equal(t, registration.CodeRegistrationNotPending, recovery.code)
}

func TestRegisterManualRecovery_SocialRegistrationRejected(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-social@example.com"

	_, err := db.CreateSocialPendingRegistration(
		ctx,
		registrationtestdb.CreateSocialPendingRegistrationParams{
			Email:               emailAddress,
			Provider:            "google",
			ProviderSubject:     "recovery-social-subject",
			EmailSnapshot:       pgText(emailAddress),
			DisplayNameSnapshot: pgText("Social User"),
		},
	)
	require.NoError(t, err)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusConflict, recovery.status)
	require.Equal(t, registration.CodeInvalidRegistrationType, recovery.code)

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(
		t,
		string(registration.VerificationPurposeRegistration),
		requests[0].Purpose,
	)
}

// Recovery is only available after the normal registration verification has
// completed. A registration whose PIN has not been verified is refused and is
// not handed its verification id: that id belongs to the normal flow, and
// answering 201 here would report a recovery that never happened.
func TestRegisterManualRecovery_PendingNormalVerificationRejected(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-pending@example.com"

	primaryID := registerAndReturnVerificationID(t, emailAddress)
	registrationID := pendingRegistrationIDForEmail(t, emailAddress)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusConflict, recovery.status)
	require.Equal(t, registration.CodeRegistrationAlreadyPending, recovery.code)
	require.Equal(
		t,
		uuid.Nil,
		recovery.verificationID,
		"a refused initiation must not return a verification id",
	)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 1, "a refused initiation must not replace the registration")
	require.Equal(t, registrationID, history[0].ID)
	require.Equal(t, string(registration.PendingRegistrationPending), history[0].Status)
	require.Equal(t, primaryID, history[0].VerificationID)
	require.Equal(t, int32(0), history[0].PinIssuedCount)

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 1, "no recovery request may be created")
	require.Equal(t, primaryID, requests[0].ID)
	require.Equal(
		t,
		string(registration.VerificationPurposeRegistration),
		requests[0].Purpose,
	)
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		requests[0].Status,
	)
	require.Equal(t, int32(0), requests[0].PinIssuedCount)

	count, err := db.CountContinuationsForPendingRegistration(ctx, registrationID)
	require.NoError(t, err)
	require.Zero(t, count)
}

// A refused initiation must leave everything it looked at exactly as it found
// it. This registration has already been verified and holds a live
// continuation, so only its normal verification is moved back to pending; that
// is the state the guard is written for, and it shows the refusal does not
// cancel the continuation the caller may still be holding.
func TestRegisterManualRecovery_RejectedInitiationPreservesState(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-rejected-state@example.com"

	verificationID, continuation := openVerifiedManualRegistration(t, emailAddress)
	registrationID := pendingRegistrationIDForEmail(t, emailAddress)

	_, err := testPool.Exec(
		ctx,
		`UPDATE verification_requests
		 SET status = 'pending'
		 WHERE id = $1`,
		verificationID,
	)
	require.NoError(t, err)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusConflict, recovery.status)
	require.Equal(t, registration.CodeRegistrationAlreadyPending, recovery.code)
	require.Equal(t, uuid.Nil, recovery.verificationID)

	state, err := db.GetRegistrationState(ctx, emailAddress)
	require.NoError(t, err)
	require.Equal(t, registrationID, state.ID)
	require.Equal(t, string(registration.PendingRegistrationPending), state.Status)
	require.Equal(t, verificationID, state.VerificationID)
	require.Equal(t, string(registration.VerificationRequestPending), state.VerificationStatus)
	require.Equal(t, int32(1), state.PinIssuedCount)

	status, code := getRegistrationContinuationStatus(t, continuation)
	require.Equal(
		t,
		http.StatusOK,
		status,
		"a refused initiation must not invalidate the continuation: %s",
		code,
	)

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(
		t,
		string(registration.VerificationPurposeRegistration),
		requests[0].Purpose,
	)

	count, err := db.CountContinuationsForPendingRegistration(ctx, registrationID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}

func TestRegisterManualRecovery_ExpiredRegistrationRejected(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-expired-registration@example.com"

	verificationID, err := db.CreateExpiredPendingRegistration(ctx, emailAddress)
	require.NoError(t, err)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusConflict, recovery.status)
	require.Equal(t, registration.CodeRegistrationExpired, recovery.code)
	require.Equal(t, uuid.Nil, recovery.verificationID)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 1, "a refused initiation must not replace the registration")
	require.Equal(t, string(registration.PendingRegistrationPending), history[0].Status)
	require.Equal(t, verificationID, history[0].VerificationID)
	require.Equal(t, int32(0), history[0].PinIssuedCount)

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 1, "no recovery request may be created")
	require.Equal(
		t,
		string(registration.VerificationPurposeRegistration),
		requests[0].Purpose,
	)
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		requests[0].Status,
	)
}

// A registration whose status is no longer pending is ineligible even though it
// still has a row and a verification request. The locked lookup excludes it, so
// the refusal is the same one an address with no registration receives.
func TestRegisterManualRecovery_NonPendingRegistrationRejected(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-expired-status@example.com"

	verificationID, err := db.CreateExpiredPendingRegistration(ctx, emailAddress)
	require.NoError(t, err)

	_, err = testPool.Exec(
		ctx,
		`UPDATE pending_registrations
		 SET status = 'expired'
		 WHERE email = $1`,
		emailAddress,
	)
	require.NoError(t, err)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusConflict, recovery.status)
	require.Equal(t, registration.CodeRegistrationNotPending, recovery.code)
	require.Equal(t, uuid.Nil, recovery.verificationID)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, string(registration.PendingRegistrationExpired), history[0].Status)
	require.Equal(t, verificationID, history[0].VerificationID)

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 1, "no recovery request may be created")
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		requests[0].Status,
	)
}

// An open recovery request is reused only after every eligibility check has
// passed. Moving the registration's deadline into the past while its recovery
// request is open must turn the next initiation into a refusal, and the open
// request must not be handed back.
func TestRegisterManualRecovery_OpenRecoveryReusedOnlyWhenEligible(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-reuse-eligibility@example.com"

	_, _ = openVerifiedManualRegistration(t, emailAddress)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusCreated, recovery.status)

	registrationID := pendingRegistrationIDForEmail(t, emailAddress)

	_, err := testPool.Exec(
		ctx,
		`UPDATE pending_registrations
		 SET expires_at = NOW() - INTERVAL '1 minute'
		 WHERE id = $1`,
		registrationID,
	)
	require.NoError(t, err)

	rejected := postManualRecovery(emailAddress)
	require.NoError(t, rejected.err)
	require.Equal(t, http.StatusConflict, rejected.status)
	require.Equal(t, registration.CodeRegistrationExpired, rejected.code)
	require.Equal(
		t,
		uuid.Nil,
		rejected.verificationID,
		"an ineligible registration must not have its open recovery request reused",
	)

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 2, "the refusal must not create another request")

	var recoveryRequests int

	for _, request := range requests {
		if request.Purpose != string(registration.VerificationPurposeRegistrationRecovery) {
			continue
		}

		recoveryRequests++

		require.Equal(t, recovery.verificationID, request.ID)
		require.Equal(t, string(registration.VerificationRequestPending), request.Status)
	}

	require.Equal(t, 1, recoveryRequests)
}

// verification_requests_status_check permits exactly `pending` and `verified`,
// so "not verified" has one reachable value and the eligibility guard has no
// third branch to exercise. Assert the domain here so the missing case is a
// property of the schema rather than an untested path.
func TestRegisterManualRecovery_VerificationStatusDomainIsPendingOrVerified(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-status-domain@example.com"

	registerAndReturnVerificationID(t, emailAddress)

	registrationID := pendingRegistrationIDForEmail(t, emailAddress)

	_, err := testPool.Exec(
		ctx,
		`INSERT INTO verification_requests (
			subject_type,
			subject_id,
			purpose,
			status
		) VALUES (
			'pending_registration',
			$1,
			'registration_recovery',
			'expired'
		)`,
		registrationID,
	)
	require.Error(t, err, "the schema must refuse a verification status other than pending or verified")
}

func TestRegisterManualRecovery_InvalidAndExhaustedPIN(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-bad-pin@example.com"

	oldVerificationID, _ := openVerifiedManualRegistration(t, emailAddress)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusCreated, recovery.status)

	pin := requestRecoveryPIN(t, recovery.verificationID)

	wrongPIN := "000000"
	if pin == wrongPIN {
		wrongPIN = "000001"
	}

	first := postVerify(t, recovery.verificationID, wrongPIN)
	require.Equal(t, http.StatusConflict, first.status)
	require.Equal(t, registration.CodeInvalidVerificationCode, first.code)

	for attempt := int32(2); attempt <= registration.VerificationCodeMaxAttempts; attempt++ {
		outcome := postVerify(t, recovery.verificationID, wrongPIN)
		require.Equal(t, http.StatusConflict, outcome.status)

		if attempt < registration.VerificationCodeMaxAttempts {
			require.Equal(t, registration.CodeInvalidVerificationCode, outcome.code)
		} else {
			require.Equal(
				t,
				registration.CodeVerificationCodeAttemptsExceeded,
				outcome.code,
			)
		}
	}

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 1, "a failed recovery PIN must not replace the registration")
	require.Equal(t, oldVerificationID, history[0].VerificationID)
	require.Equal(t, string(registration.PendingRegistrationPending), history[0].Status)
}

func TestRegisterManualRecovery_ExpiredCode(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-expired-code@example.com"

	_, _ = openVerifiedManualRegistration(t, emailAddress)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusCreated, recovery.status)

	pin := requestRecoveryPIN(t, recovery.verificationID)

	_, err := testPool.Exec(
		ctx,
		`UPDATE verification_codes
		 SET expires_at = NOW() - INTERVAL '1 minute'
		 WHERE verification_request_id = $1
		   AND consumed_at IS NULL
		   AND invalidated_at IS NULL`,
		recovery.verificationID,
	)
	require.NoError(t, err)

	outcome := postVerify(t, recovery.verificationID, pin)
	require.Equal(t, http.StatusConflict, outcome.status)
	require.Equal(t, apperror.CodeConflict, outcome.code)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, string(registration.PendingRegistrationPending), history[0].Status)
}

func TestRegisterManualRecovery_ConcurrentInitiation(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-concurrent-init@example.com"

	openVerifiedManualRegistration(t, emailAddress)

	const attempts = 8

	outcomes := make([]manualRecoveryOutcome, attempts)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = postManualRecovery(emailAddress)
	})

	var recoveryID uuid.UUID

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
		require.NotEqual(t, uuid.Nil, outcome.verificationID)

		if recoveryID == uuid.Nil {
			recoveryID = outcome.verificationID

			continue
		}

		require.Equalf(
			t,
			recoveryID,
			outcome.verificationID,
			"request %d must reuse the one open recovery request",
			index,
		)
	}

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 1, "initiation must not replace the registration")

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, requests, 2)

	var recoveryCount int
	for _, request := range requests {
		if request.Purpose == string(registration.VerificationPurposeRegistrationRecovery) &&
			request.Status == string(registration.VerificationRequestPending) {
			recoveryCount++
		}
	}
	require.Equal(t, 1, recoveryCount)
}

func TestRegisterManualRecovery_ConcurrentVerification(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-concurrent-verify@example.com"

	oldVerificationID, _ := openVerifiedManualRegistration(t, emailAddress)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusCreated, recovery.status)

	pin := requestRecoveryPIN(t, recovery.verificationID)

	const attempts = 8

	outcomes := make([]verificationRaceOutcome, attempts)

	runConcurrently(t, len(outcomes), func(index int) {
		outcomes[index] = postVerify(t, recovery.verificationID, pin)
	})

	var winner verificationRaceOutcome

	var successes int

	for _, outcome := range outcomes {
		if outcome.status == http.StatusOK {
			successes++
			winner = outcome

			continue
		}

		require.Equal(t, http.StatusConflict, outcome.status)
	}

	require.Equal(t, 1, successes, "exactly one recovery verification may succeed")
	require.NotEmpty(t, winner.token)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 2)

	require.Equal(t, string(registration.PendingRegistrationExpired), history[0].Status)
	require.Equal(t, oldVerificationID, history[0].VerificationID)
	require.Equal(t, string(registration.PendingRegistrationPending), history[1].Status)
	require.Equal(t, recovery.verificationID, history[1].VerificationID)

	newPendingID := history[1].ID

	count, err := db.CountContinuationsForPendingRegistration(ctx, newPendingID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	status, code := getRegistrationContinuationStatus(t, winner.token)
	require.Equal(t, http.StatusOK, status, code)
}

// Recovery verification and finalization both serialize on the old registration.
// Exactly one can win, and the loser must leave no partial account or
// replacement behind.
func TestRegisterManualRecovery_RaceWithFinalization(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-finalize-race@example.com"

	_, oldToken := openVerifiedManualRegistration(t, emailAddress)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusCreated, recovery.status)

	pin := requestRecoveryPIN(t, recovery.verificationID)

	var finalizeStatus int

	var finalizeCode string

	var finalizeErr error

	var verify verificationRaceOutcome

	runConcurrently(t, 2, func(index int) {
		if index == 0 {
			finalizeStatus, finalizeCode, finalizeErr = finalizeManualRegistrationRaw(
				oldToken,
			)

			return
		}

		verify = postVerify(t, recovery.verificationID, pin)
	})

	require.NoError(t, finalizeErr)

	users, credentials, collections := countFinalizedArtifacts(t, emailAddress)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)

	switch {
	case users == 1:
		// Finalization won. The recovery verification must have been refused and
		// its transaction rolled back; the account is the only registration.
		require.Equal(t, http.StatusCreated, finalizeStatus, finalizeCode)
		require.Equal(t, http.StatusConflict, verify.status)
		require.Len(t, history, 1)
		require.Equal(t, string(registration.PendingRegistrationCompleted), history[0].Status)
		require.Equal(t, int64(1), credentials)
		require.Equal(t, int64(1), collections)

	case users == 0:
		// Recovery won. Finalization must have been refused and rolled back,
		// leaving the old registration expired and exactly one replacement.
		require.Equal(t, http.StatusOK, verify.status)
		require.NotEmpty(t, verify.token)
		require.Equal(t, http.StatusConflict, finalizeStatus)
		require.Len(t, history, 2)
		require.Equal(t, string(registration.PendingRegistrationExpired), history[0].Status)
		require.Equal(t, string(registration.PendingRegistrationPending), history[1].Status)
		require.Zero(t, credentials)
		require.Zero(t, collections)

	default:
		t.Fatalf("expected zero or one account, got %d", users)
	}
}

// A normal registration request must not replace the registration while a
// recovery request is open; it should hand back that request instead.
func TestRegisterManualRecovery_NormalRegistrationDoesNotReplaceOpenRecovery(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-normal-race@example.com"

	_, oldToken := openVerifiedManualRegistration(t, emailAddress)

	recovery := postManualRecovery(emailAddress)
	require.NoError(t, recovery.err)
	require.Equal(t, http.StatusCreated, recovery.status)

	pin := requestRecoveryPIN(t, recovery.verificationID)

	var normal registerManualOutcome

	var verify verificationRaceOutcome

	runConcurrently(t, 2, func(index int) {
		if index == 0 {
			normal = postRegisterManual(emailAddress)

			return
		}

		verify = postVerify(t, recovery.verificationID, pin)
	})

	require.NoError(t, normal.err)
	require.Equal(t, http.StatusCreated, normal.status)
	require.Equal(
		t,
		recovery.verificationID,
		normal.verificationID,
		"a normal registration request must point at the open recovery request",
	)

	require.Equal(t, http.StatusOK, verify.status)
	require.NotEmpty(t, verify.token)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.Len(t, history, 2, "the recovery must create one replacement")
	require.Equal(t, string(registration.PendingRegistrationExpired), history[0].Status)
	require.Equal(t, string(registration.PendingRegistrationPending), history[1].Status)
	require.Equal(t, recovery.verificationID, history[1].VerificationID)

	status, code := getRegistrationContinuationStatus(t, oldToken)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, registration.CodeRegistrationNotPending, code)
}

// Recovery initiation and normal registration serialize on the old registration.
// The result must be one pending registration, with no recovery request left on
// an expired registration and no duplicate replacement.
func TestRegisterManualRecovery_InitiationRacesWithNormalRegistration(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const emailAddress = "recovery-init-normal-race@example.com"

	openVerifiedManualRegistration(t, emailAddress)

	expireRegistrationContinuation(
		t,
		pendingRegistrationIDForEmail(t, emailAddress),
	)

	var recovery manualRecoveryOutcome

	var normal registerManualOutcome

	runConcurrently(t, 2, func(index int) {
		if index == 0 {
			recovery = postManualRecovery(emailAddress)

			return
		}

		normal = postRegisterManual(emailAddress)
	})

	require.NoError(t, normal.err)
	require.Equal(t, http.StatusCreated, normal.status, "the normal request must always resolve")
	require.NoError(t, recovery.err)

	history, err := db.GetRegistrationHistory(ctx, emailAddress)
	require.NoError(t, err)
	require.NotEmpty(t, history)

	var pendingRegistrations int
	for _, item := range history {
		if item.Status == string(registration.PendingRegistrationPending) {
			pendingRegistrations++
		}
	}
	require.Equal(t, 1, pendingRegistrations, "exactly one registration may remain pending")

	requests, err := db.GetVerificationRequestsForEmail(ctx, emailAddress)
	require.NoError(t, err)

	var pendingRecovery int
	for _, request := range requests {
		if request.Purpose == string(registration.VerificationPurposeRegistrationRecovery) {
			pendingRecovery++

			require.Equal(
				t,
				string(registration.PendingRegistrationPending),
				request.RegistrationStatus,
				"an open recovery request must belong to a pending registration",
			)
		}
	}

	require.LessOrEqual(t, pendingRecovery, 1)

	if pendingRecovery == 1 {
		require.Len(t, history, 1, "a recovery request only exists on the retained registration")
	}

	if recovery.err == nil && recovery.status == http.StatusCreated {
		require.Equal(
			t,
			normal.verificationID,
			recovery.verificationID,
			"both requests must resolve to the same verification",
		)
	}
}

func TestRegisterManualRecovery_IPRateLimit(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	app, _ := testutil.NewAppWithLimiter(
		testPool,
		&testutil.CountingPinRateLimiter{
			Allowed:    false,
			RetryAfter: 42 * time.Second,
		},
	)

	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/recovery/manual",
		strings.NewReader(`{"email":"recovery-rate-limited@example.com"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	response, err := app.Test(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, response.StatusCode)

	var body registerAPIError

	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Equal(t, "IP_RATE_LIMIT_EXCEEDED", body.Error.Code)

	unavailableApp, _ := testutil.NewAppWithLimiter(
		testPool,
		testutil.UnavailablePinRateLimiter(errors.New("redis unavailable")),
	)

	unavailableRequest := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/recovery/manual",
		strings.NewReader(`{"email":"recovery-rate-unavailable@example.com"}`),
	)
	unavailableRequest.Header.Set("Content-Type", "application/json")

	unavailableResponse, err := unavailableApp.Test(unavailableRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, unavailableResponse.StatusCode)

	var unavailableBody registerAPIError

	require.NoError(t, json.NewDecoder(unavailableResponse.Body).Decode(&unavailableBody))
	require.Equal(t, "IP_RATE_LIMIT_UNAVAILABLE", unavailableBody.Error.Code)
}
