package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
	registrationtestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/registration/generated"
)

func TestRegisterManual_Success(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	email := "user@example.com"

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"user@example.com"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.NotEqual(t, uuid.Nil, body.Data.VerificationID)

	state, err := db.GetRegistrationState(ctx, email)
	require.NoError(t, err)

	require.Equal(t, email, state.Email)
	require.Equal(
		t,
		registration.RegistrationTypeManual,
		registration.RegistrationType(state.RegistrationType),
	)
	require.Equal(
		t,
		registration.PendingRegistrationPending,
		registration.PendingRegistrationStatus(state.Status),
	)
	require.Equal(t, body.Data.VerificationID, state.VerificationID)
	require.Equal(
		t,
		registration.VerificationRequestPending,
		registration.VerificationRequestStatus(state.VerificationStatus),
	)
	require.Equal(t, int32(0), state.PinIssuedCount)
}

func TestRegisterManual_AlreadyPending(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	firstReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"pending@example.com"}`),
	)
	firstReq.Header.Set("Content-Type", "application/json")

	firstResp, err := testApp.Test(firstReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, firstResp.StatusCode)

	var firstBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(firstResp.Body).Decode(&firstBody))
	require.NotEqual(t, uuid.Nil, firstBody.Data.VerificationID)

	secondReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"pending@example.com"}`),
	)
	secondReq.Header.Set("Content-Type", "application/json")

	secondResp, err := testApp.Test(secondReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, secondResp.StatusCode)

	var secondBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(secondResp.Body).Decode(&secondBody))

	require.Equal(t, firstBody.Data.VerificationID, secondBody.Data.VerificationID)
}

func TestRegisterManual_AlreadyCompleted(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	email := "completed@example.com"

	_, err := db.CreateCompletedRegistration(ctx, email)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"completed@example.com"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode)

	var body struct {
		Error struct {
			Code   string `json:"code"`
			Fields []any  `json:"fields"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, registration.CodeRegistrationAlreadyCompleted, body.Error.Code)
}

// registerAndReturnVerificationID opens a manual registration and returns the
// verification id its caller would use for the rest of the flow.
func registerAndReturnVerificationID(
	t *testing.T,
	email string,
) uuid.UUID {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"`+email+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
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

// requestCreatePIN calls the pin endpoint for a verification id.
func requestCreatePIN(
	t *testing.T,
	verificationID uuid.UUID,
) *http.Response {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+verificationID.String()+"/pin",
		nil,
	)

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp
}

// Issuing a PIN and immediately asking for another is the same act twice, and it
// has to be refused: the endpoint is unauthenticated, so without the cooldown it
// is a way to mail an address as fast as the endpoint can be called, and a way to
// reset the per-code attempt limit by having a fresh code issued.
func TestCreatePIN_Cooldown(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))
	testEmailSender.Messages = nil

	verificationID := registerAndReturnVerificationID(t, "pincooldown@example.com")

	// The first issuance is the one a caller starting a registration expects and
	// is never subject to a cooldown: nothing has been sent yet.
	first := requestCreatePIN(t, verificationID)
	require.Equal(t, http.StatusCreated, first.StatusCode)
	require.Len(t, testEmailSender.Messages, 1)

	// The second one is refused, and refused before anything is issued, so no
	// second message goes out.
	second := requestCreatePIN(t, verificationID)
	require.Equal(t, http.StatusConflict, second.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(second.Body).Decode(&body))
	require.Equal(t, registration.CodeVerificationResendCooldown, body.Error.Code)

	require.Len(
		t,
		testEmailSender.Messages,
		1,
		"a refused issuance must not send a second email",
	)

	// Exactly one code was ever issued for this verification, which is the same
	// thing said about the database rather than about the mailer.
	state, err := db.GetRegistrationState(ctx, "pincooldown@example.com")
	require.NoError(t, err)
	require.Equal(t, int32(1), state.PinIssuedCount)
}

// The cooldown has to expire on its own: a caller who lost a code must be able to
// ask for another, and the refusal must not be permanent.
func TestCreatePIN_SucceedsAfterCooldown(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))
	testEmailSender.Messages = nil

	verificationID := registerAndReturnVerificationID(t, "pinafter@example.com")

	require.Equal(t, http.StatusCreated, requestCreatePIN(t, verificationID).StatusCode)
	require.Len(t, testEmailSender.Messages, 1)

	// Age the last send past the cooldown instead of waiting it out, so the test
	// is about the rule rather than about the clock.
	require.NoError(t, db.MakeVerificationResendable(ctx, verificationID))

	third := requestCreatePIN(t, verificationID)
	require.Equal(t, http.StatusCreated, third.StatusCode)

	require.Len(t, testEmailSender.Messages, 2)

	state, err := db.GetRegistrationState(ctx, "pinafter@example.com")
	require.NoError(t, err)
	require.Equal(t, int32(2), state.PinIssuedCount)
}

// Both issuance paths send the same kind of message to the same address, so they
// obey the same rule. Resend is asserted here as well because the shared check
// could otherwise pass this test while leaving resend broken.
func TestCreatePIN_AndResendShareOneCooldown(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))
	testEmailSender.Messages = nil

	verificationID := registerAndReturnVerificationID(t, "pinafterresend@example.com")

	require.Equal(t, http.StatusCreated, requestCreatePIN(t, verificationID).StatusCode)

	// A pin issuance starts the same cooldown a resend does.
	resendURL := "/auth/register/verification/" + verificationID.String() + "/resend"

	resendResp, err := testApp.Test(
		httptest.NewRequest(http.MethodPost, resendURL, nil),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, resendResp.StatusCode)

	// And a resend starts the same cooldown a pin issuance does.
	require.NoError(t, db.MakeVerificationResendable(ctx, verificationID))

	afterCooldownResp, err := testApp.Test(
		httptest.NewRequest(http.MethodPost, resendURL, nil),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, afterCooldownResp.StatusCode)

	require.Equal(
		t,
		http.StatusConflict,
		requestCreatePIN(t, verificationID).StatusCode,
	)
}

// Several requests for the same address that has never registered must all come
// back the same way. Only one of them can create the registration, so the rest
// have to be answered the way the second sequential request would be answered,
// rather than as a conflict that means something different to a caller.
func TestRegisterManual_ConcurrentSameNewEmail(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	const attempts = 8

	type outcome struct {
		status         int
		verificationID uuid.UUID
	}

	results := make([]outcome, attempts)

	// The barrier is the synchronization: every request is built and parked
	// before any of them is allowed to run, so they contend for the same window
	// rather than being issued one after another.
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup

	ready.Add(attempts)
	done.Add(attempts)

	for i := range attempts {
		go func() {
			defer done.Done()

			req := httptest.NewRequest(
				http.MethodPost,
				"/auth/register/manual",
				strings.NewReader(`{"email":"concurrent@example.com"}`),
			)
			req.Header.Set("Content-Type", "application/json")

			ready.Done()
			<-start

			resp, err := testApp.Test(req)
			if err != nil {
				return
			}

			var body struct {
				Data struct {
					VerificationID uuid.UUID `json:"verification_id"`
				} `json:"data"`
			}

			if json.NewDecoder(resp.Body).Decode(&body) != nil {
				return
			}

			results[i] = outcome{
				status:         resp.StatusCode,
				verificationID: body.Data.VerificationID,
			}
		}()
	}

	ready.Wait()
	close(start)

	done.Wait()

	// Every caller sees the same thing: a successful registration pointing at one
	// verification. There is no losing caller, and in particular none is told the
	// address is already taken, which is what a caller cannot act on here.
	var verificationID uuid.UUID

	for i, result := range results {
		require.Equal(
			t,
			http.StatusCreated,
			result.status,
			"request %d must be answered like the second sequential request",
			i,
		)

		require.NotEqual(
			t,
			uuid.Nil,
			result.verificationID,
			"request %d returned no verification id",
			i,
		)

		if verificationID == uuid.Nil {
			verificationID = result.verificationID
		}

		require.Equal(
			t,
			verificationID,
			result.verificationID,
			"request %d was pointed at a different registration",
			i,
		)
	}

	// One registration, not several. The unique index is what guarantees this
	// whatever the callers did, and it is asserted here rather than assumed.
	history, err := db.GetRegistrationHistory(ctx, "concurrent@example.com")
	require.NoError(t, err)
	require.Len(t, history, 1)

	require.Equal(t, "pending", history[0].Status)
	require.Equal(t, verificationID, history[0].VerificationID)
}

func TestVerifyRegistration_InvalidPIN(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	registerReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"verify@example.com"}`),
	)
	registerReq.Header.Set("Content-Type", "application/json")

	registerResp, err := testApp.Test(registerReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, registerResp.StatusCode)

	var registerBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(registerResp.Body).Decode(&registerBody),
	)

	require.NotEqual(t, uuid.Nil, registerBody.Data.VerificationID)

	createPINReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+registerBody.Data.VerificationID.String()+"/pin",
		nil,
	)

	createPINResp, err := testApp.Test(createPINReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createPINResp.StatusCode)

	verifyReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+registerBody.Data.VerificationID.String()+"/verify",
		strings.NewReader(`{"pin":"000000"}`),
	)
	verifyReq.Header.Set("Content-Type", "application/json")

	verifyResp, err := testApp.Test(verifyReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, verifyResp.StatusCode)

	var verifyBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(
		t,
		json.NewDecoder(verifyResp.Body).Decode(&verifyBody),
	)

	require.Equal(
		t,
		registration.CodeInvalidVerificationCode,
		verifyBody.Error.Code,
	)
}

func TestVerifyRegistration_AttemptsExceeded(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	registerReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"attempts@example.com"}`),
	)
	registerReq.Header.Set("Content-Type", "application/json")

	registerResp, err := testApp.Test(registerReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, registerResp.StatusCode)

	var registerBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(registerResp.Body).Decode(&registerBody),
	)

	verificationID := registerBody.Data.VerificationID
	require.NotEqual(t, uuid.Nil, verificationID)

	createPINReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+verificationID.String()+"/pin",
		nil,
	)

	createPINResp, err := testApp.Test(createPINReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createPINResp.StatusCode)

	verifyURL := "/auth/register/verification/" +
		verificationID.String() +
		"/verify"

	for attempt := 1; attempt <= int(registration.VerificationCodeMaxAttempts); attempt++ {
		verifyReq := httptest.NewRequest(
			http.MethodPost,
			verifyURL,
			strings.NewReader(`{"pin":"000000"}`),
		)
		verifyReq.Header.Set("Content-Type", "application/json")

		verifyResp, err := testApp.Test(verifyReq)
		require.NoError(t, err)

		var verifyBody struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}

		require.NoError(
			t,
			json.NewDecoder(verifyResp.Body).Decode(&verifyBody),
		)

		if attempt < int(registration.VerificationCodeMaxAttempts) {
			require.Equal(t, http.StatusConflict, verifyResp.StatusCode)
			require.Equal(
				t,
				registration.CodeInvalidVerificationCode,
				verifyBody.Error.Code,
			)
			continue
		}

		require.Equal(t, http.StatusConflict, verifyResp.StatusCode)
		require.Equal(
			t,
			registration.CodeVerificationCodeAttemptsExceeded,
			verifyBody.Error.Code,
		)
	}
}

func TestGetVerification_Success(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	email := "get-verification@example.com"

	registerReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"get-verification@example.com"}`),
	)
	registerReq.Header.Set("Content-Type", "application/json")

	registerResp, err := testApp.Test(registerReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, registerResp.StatusCode)

	var registerBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(registerResp.Body).Decode(&registerBody),
	)

	verificationID := registerBody.Data.VerificationID
	require.NotEqual(t, uuid.Nil, verificationID)

	// Get verification before PIN is issued.
	getReq := httptest.NewRequest(
		http.MethodGet,
		"/auth/register/verification/"+verificationID.String(),
		nil,
	)

	getResp, err := testApp.Test(getReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	var getBody struct {
		Data struct {
			VerificationID  string `json:"verification_id"`
			Status          string `json:"status"`
			PINIssued       bool   `json:"pin_issued"`
			ResendInSeconds int    `json:"resend_in_seconds"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(getResp.Body).Decode(&getBody),
	)

	require.Equal(
		t,
		verificationID.String(),
		getBody.Data.VerificationID,
	)
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		getBody.Data.Status,
	)
	require.False(t, getBody.Data.PINIssued)
	require.Equal(t, 0, getBody.Data.ResendInSeconds)

	// Create verification PIN.
	createPIN := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+
			verificationID.String()+
			"/pin",
		nil,
	)

	createPINResponse, err := testApp.Test(createPIN)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createPINResponse.StatusCode)

	// Email should have been sent.
	require.Len(t, testEmailSender.Messages, 1)

	message := testEmailSender.Messages[0]

	require.Equal(t, email, message.To.Email)

	// Get verification after PIN is issued.
	getReq = httptest.NewRequest(
		http.MethodGet,
		"/auth/register/verification/"+verificationID.String(),
		nil,
	)

	getResp, err = testApp.Test(getReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	require.NoError(
		t,
		json.NewDecoder(getResp.Body).Decode(&getBody),
	)

	require.Equal(t, verificationID.String(), getBody.Data.VerificationID)
	require.Equal(
		t,
		string(registration.VerificationRequestPending),
		getBody.Data.Status,
	)
	require.True(t, getBody.Data.PINIssued)
	require.Greater(t, getBody.Data.ResendInSeconds, 0)
}

func TestGetVerification_NotFound(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodGet,
		"/auth/register/verification/"+uuid.New().String(),
		nil,
	)

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, apperror.CodeNotFound, body.Error.Code)
}

func TestResendVerification_Cooldown(t *testing.T) {
	ctx := context.Background()

	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	email := "resend@example.com"

	registerReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"resend@example.com"}`),
	)
	registerReq.Header.Set("Content-Type", "application/json")

	registerResp, err := testApp.Test(registerReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, registerResp.StatusCode)

	var registerBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(registerResp.Body).Decode(&registerBody),
	)

	verificationID := registerBody.Data.VerificationID
	require.NotEqual(t, uuid.Nil, verificationID)

	createPINReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+verificationID.String()+"/pin",
		nil,
	)

	createPINResp, err := testApp.Test(createPINReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createPINResp.StatusCode)

	// CreatePIN should send one email.
	require.Len(t, testEmailSender.Messages, 1)
	require.Equal(t, email, testEmailSender.Messages[0].To.Email)

	require.NoError(
		t,
		db.MakeVerificationResendable(ctx, verificationID),
	)

	resendURL := "/auth/register/verification/" +
		verificationID.String() +
		"/resend"

	// First resend should succeed and send another email.
	firstResendReq := httptest.NewRequest(
		http.MethodPost,
		resendURL,
		nil,
	)

	firstResendResp, err := testApp.Test(firstResendReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, firstResendResp.StatusCode)

	require.Len(t, testEmailSender.Messages, 2)
	require.Equal(t, email, testEmailSender.Messages[1].To.Email)

	// Second resend should be rejected by the cooldown.
	secondResendReq := httptest.NewRequest(
		http.MethodPost,
		resendURL,
		nil,
	)

	secondResendResp, err := testApp.Test(secondResendReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, secondResendResp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(
		t,
		json.NewDecoder(secondResendResp.Body).Decode(&body),
	)

	require.Equal(
		t,
		registration.CodeVerificationResendCooldown,
		body.Error.Code,
	)

	// Cooldown rejection should not send another email.
	require.Len(t, testEmailSender.Messages, 2)
}

func TestResendVerification_NotFound(t *testing.T) {
	verificationID := uuid.New()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/verification/"+verificationID.String()+"/resend",
		nil,
	)

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, apperror.CodeNotFound, body.Error.Code)
}

func TestGetRegistrationContinuation_Success(t *testing.T) {
	ctx := context.Background()

	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	token := "test-registration-continuation-token"
	tokenHash := security.HashToken(token)

	email := "continuation@example.com"

	_, err := db.CreateRegistrationContinuation(
		ctx,
		registrationtestdb.CreateRegistrationContinuationParams{
			Email:     email,
			TokenHash: tokenHash,
		},
	)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodGet,
		"/auth/register/continuation",
		nil,
	)

	req.Header.Set("X-Registration-Continuation", token)

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Data struct {
			Email            string `json:"email"`
			RegistrationType string `json:"registration_type"`
			RequiresPassword bool   `json:"requires_password"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, email, body.Data.Email)
	require.Equal(
		t,
		string(registration.RegistrationTypeManual),
		body.Data.RegistrationType,
	)
	require.True(t, body.Data.RequiresPassword)
}

func TestGetRegistrationContinuation_MissingHeader(t *testing.T) {
	ctx := context.Background()

	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	req := httptest.NewRequest(
		http.MethodGet,
		"/auth/register/continuation",
		nil,
	)

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, registration.CodeRegistrationContinuationRequired, body.Error.Code)
}

func TestGetRegistrationContinuation_InvalidToken(t *testing.T) {
	ctx := context.Background()

	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	req := httptest.NewRequest(
		http.MethodGet,
		"/auth/register/continuation",
		nil,
	)
	req.Header.Set("X-Registration-Continuation", "invalid-token")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, apperror.CodeConflict, body.Error.Code)
}

func TestFinalizeManualRegistration_Success(t *testing.T) {
	ctx := context.Background()

	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	email := "finalize@example.com"
	token := "test-registration-continuation-token"
	tokenHash := security.HashToken(token)

	_, err := db.CreateRegistrationContinuation(
		ctx,
		registrationtestdb.CreateRegistrationContinuationParams{
			Email:     email,
			TokenHash: tokenHash,
		},
	)
	require.NoError(t, err)

	reqBody := `{
		"display_name": "Test User",
		"password": "password123"
	}`

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/finalize/manual",
		strings.NewReader(reqBody),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registration-Continuation", token)

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body struct {
		Data struct {
			UserID      uuid.UUID `json:"user_id"`
			Email       string    `json:"email"`
			DisplayName string    `json:"display_name"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, email, body.Data.Email)
	require.Equal(t, "Test User", body.Data.DisplayName)

	state, err := db.GetFinalizedRegistrationState(ctx, email)
	require.NoError(t, err)

	require.Equal(t, email, state.Email)
	require.Equal(t, "Test User", state.DisplayName)
	require.Equal(t, state.UserID, state.PasswordUserID)
	require.Equal(t, "completed", state.RegistrationStatus)
	require.True(t, state.ConsumedAt.Valid)

	requireSingleUnsortedCollection(t, ctx, db, state.UserID)
}

// requireSingleUnsortedCollection asserts the per-user Unsorted invariant: a
// permanent user owns exactly one system collection keyed 'unsorted'.
func requireSingleUnsortedCollection(
	t *testing.T,
	ctx context.Context,
	db *registrationtestdb.Queries,
	userID uuid.UUID,
) {
	t.Helper()

	collections, err := db.GetUnsortedCollectionsForUser(ctx, userID)
	require.NoError(t, err)

	require.Len(
		t,
		collections,
		1,
		"user must own exactly one Unsorted collection",
	)

	require.Equal(t, userID, collections[0].UserID)
	require.Equal(t, "Unsorted", collections[0].Name)
	require.Equal(t, "system", collections[0].Type)

	require.True(t, collections[0].SystemKey.Valid)
	require.Equal(t, "unsorted", collections[0].SystemKey.String)
}

// TestRegisterManual_PendingHasNoUnsortedCollection pins the lifecycle
// distinction: a pending registration is not a permanent user yet, so no
// collection may exist for it. The collection belongs to user finalization.
func TestRegisterManual_PendingHasNoUnsortedCollection(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/manual",
		strings.NewReader(`{"email":"pending-only@example.com"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	count, err := db.CountAllCollections(ctx)
	require.NoError(t, err)

	require.Equal(t, int64(0), count)
}

func TestFinalizeManualRegistration_UserAlreadyExists(t *testing.T) {
	ctx := context.Background()

	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	email := "existing@example.com"
	token := "test-registration-continuation-token"
	tokenHash := security.HashToken(token)

	_, err := db.CreateRegistrationContinuation(
		ctx,
		registrationtestdb.CreateRegistrationContinuationParams{
			Email:     email,
			TokenHash: tokenHash,
		},
	)
	require.NoError(t, err)

	_, err = db.CreateExistingUser(ctx, email)
	require.NoError(t, err)

	reqBody := `{
		"display_name": "New User",
		"password": "password123"
	}`

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/finalize/manual",
		strings.NewReader(reqBody),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registration-Continuation", token)

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(t, registration.CodeUserAlreadyExists, body.Error.Code)
}

func TestRegisterManual_ExpiredPendingIsReconciled(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	email := "expired@example.com"
	oldVerificationID, err := db.CreateExpiredPendingRegistration(ctx, email)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/auth/register/manual", strings.NewReader(`{"email":"expired@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEqual(t, oldVerificationID, body.Data.VerificationID)

	history, err := db.GetRegistrationHistory(ctx, email)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, "expired", history[0].Status)
	require.Equal(t, oldVerificationID, history[0].VerificationID)
	require.Equal(t, "pending", history[1].Status)
	require.Equal(t, body.Data.VerificationID, history[1].VerificationID)
	require.Equal(t, int32(0), history[1].PinIssuedCount)
}

func TestFinalizeSocialRegistration_Success(t *testing.T) {
	ctx := context.Background()

	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	email := "google@example.com"
	provider := "google"
	providerSubject := "google-subject-123"
	socialDisplayName := "Google User"
	displayName := "Test User"

	continuationToken := "test-social-continuation-token"
	tokenHash := security.HashToken(continuationToken)

	_, err := db.CreateSocialRegistrationContinuation(
		ctx,
		registrationtestdb.CreateSocialRegistrationContinuationParams{
			TokenHash:       tokenHash,
			Email:           email,
			Provider:        provider,
			ProviderSubject: providerSubject,
			EmailSnapshot: pgtype.Text{
				String: email,
				Valid:  true,
			},
			DisplayNameSnapshot: pgtype.Text{
				String: socialDisplayName,
				Valid:  true,
			},
		},
	)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/finalize/social",
		strings.NewReader(`{
			"display_name": "Test User"
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registration-Continuation", continuationToken)
	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body struct {
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			User         struct {
				ID          string `json:"id"`
				Email       string `json:"email"`
				DisplayName string `json:"display_name"`
			} `json:"user"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.NotEmpty(t, body.Data.AccessToken)
	require.NotEmpty(t, body.Data.RefreshToken)

	require.NotEmpty(t, body.Data.User.ID)
	require.Equal(t, email, body.Data.User.Email)
	require.Equal(t, displayName, body.Data.User.DisplayName)

	userID, err := uuid.Parse(body.Data.User.ID)
	require.NoError(t, err)

	state, err := db.GetFinalizedSocialRegistrationState(ctx, email)
	require.NoError(t, err)

	require.Equal(t, userID, state.UserID)
	require.Equal(t, email, state.Email)
	require.Equal(t, displayName, state.DisplayName)

	require.True(t, state.EmailVerifiedAt.Valid)

	require.NotEqual(t, uuid.Nil, state.AuthIdentityID)
	require.Equal(t, provider, state.Provider)
	require.Equal(t, providerSubject, state.ProviderSubject)

	require.True(t, state.EmailSnapshot.Valid)
	require.Equal(t, email, state.EmailSnapshot.String)

	require.True(t, state.DisplayNameSnapshot.Valid)
	require.Equal(
		t,
		socialDisplayName,
		state.DisplayNameSnapshot.String,
	)

	require.Equal(
		t,
		"completed",
		state.RegistrationStatus,
	)

	require.True(t, state.ConsumedAt.Valid)

	sessionCount, err := db.CountUserSessions(ctx, userID)
	require.NoError(t, err)

	require.Equal(t, int64(1), sessionCount)

	requireSingleUnsortedCollection(t, ctx, db, userID)
}

func TestFinalizeSocialRegistration_UserAlreadyExists(t *testing.T) {
	ctx := context.Background()

	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	email := "existing-social@example.com"
	token := "test-social-continuation-token"
	tokenHash := security.HashToken(token)

	_, err := db.CreateSocialRegistrationContinuation(
		ctx,
		registrationtestdb.CreateSocialRegistrationContinuationParams{
			TokenHash:       tokenHash,
			Email:           email,
			Provider:        "google",
			ProviderSubject: "google-subject-123",
			EmailSnapshot: pgtype.Text{
				String: email,
				Valid:  true,
			},
			DisplayNameSnapshot: pgtype.Text{
				String: "Google User",
				Valid:  true,
			},
		},
	)
	require.NoError(t, err)

	_, err = db.CreateExistingUser(ctx, email)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/finalize/social",
		strings.NewReader(`{
			"display_name": "New User"
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registration-Continuation", token)
	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(
		t,
		registration.CodeUserAlreadyExists,
		body.Error.Code,
	)
}

func TestFinalizeSocialRegistration_AuthIdentityAlreadyExists(t *testing.T) {
	ctx := context.Background()

	db := registrationtestdb.New(testPool)
	require.NoError(t, db.TruncateRegistrationData(ctx))

	email := "social@example.com"
	token := "test-social-continuation-token"
	tokenHash := security.HashToken(token)

	provider := "google"
	providerSubject := "google-subject-123"

	_, err := db.CreateSocialRegistrationContinuation(
		ctx,
		registrationtestdb.CreateSocialRegistrationContinuationParams{
			TokenHash:       tokenHash,
			Email:           email,
			Provider:        provider,
			ProviderSubject: providerSubject,
			EmailSnapshot: pgtype.Text{
				String: email,
				Valid:  true,
			},
			DisplayNameSnapshot: pgtype.Text{
				String: "Google User",
				Valid:  true,
			},
		},
	)
	require.NoError(t, err)

	existingUserID, err := db.CreateExistingUser(ctx, "existing@example.com")
	require.NoError(t, err)

	err = db.CreateExistingAuthIdentity(
		ctx,
		registrationtestdb.CreateExistingAuthIdentityParams{
			UserID:          existingUserID,
			Provider:        provider,
			ProviderSubject: providerSubject,
			EmailSnapshot: pgtype.Text{
				String: "existing@example.com",
				Valid:  true,
			},
			DisplayNameSnapshot: pgtype.Text{
				String: "Existing User",
				Valid:  true,
			},
		},
	)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/register/finalize/social",
		strings.NewReader(`{
			"display_name": "New User"
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registration-Continuation", token)
	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	require.Equal(
		t,
		registration.CodeAuthIdentityAlreadyExists,
		body.Error.Code,
	)
}
