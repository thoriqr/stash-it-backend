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
	"github.com/thoriqr/stash-it-backend/internal/security"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
)

func TestPasswordReset_EndToEndRevokesSessions(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	email := "reset@example.com"

	userID, err := db.CreatePasswordResetUser(ctx, email)
	require.NoError(t, err)

	oldHash, err := security.NewPasswordHasher().Hash("old-password")
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

	// Request password reset.
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"reset@example.com"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	response, err := testApp.Test(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)

	var requestBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(response.Body).Decode(&requestBody),
	)

	require.NotEqual(
		t,
		uuid.Nil,
		requestBody.Data.VerificationID,
	)

	state, err := db.GetPasswordResetState(ctx, email)
	require.NoError(t, err)

	require.Equal(
		t,
		requestBody.Data.VerificationID,
		state.VerificationID,
	)

	// Create verification PIN.
	createPIN := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+
			requestBody.Data.VerificationID.String()+
			"/pin",
		nil,
	)

	createPINResponse, err := testApp.Test(createPIN)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createPINResponse.StatusCode)

	// Override generated PIN for deterministic integration test.
	codeHash := security.NewVerificationCodeHasher(
		[]byte("integration-test-verification-secret"),
	).Hash("123456")

	require.NoError(
		t,
		db.SetVerificationCodeHash(
			ctx,
			passwordresetdbtest.SetVerificationCodeHashParams{
				CodeHash:              codeHash,
				VerificationRequestID: requestBody.Data.VerificationID,
			},
		),
	)

	// Verify PIN.
	verify := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+
			requestBody.Data.VerificationID.String()+
			"/verify",
		strings.NewReader(`{"pin":"123456"}`),
	)
	verify.Header.Set("Content-Type", "application/json")

	verifyResponse, err := testApp.Test(verify)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, verifyResponse.StatusCode)

	var verifyBody struct {
		Data struct {
			Token string `json:"password_reset_continuation_token"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(verifyResponse.Body).Decode(&verifyBody),
	)

	require.NotEmpty(t, verifyBody.Data.Token)

	// Get continuation.
	continuation := httptest.NewRequest(
		http.MethodGet,
		"/auth/password-reset/continuation",
		nil,
	)
	continuation.Header.Set(
		"X-Password-Reset-Continuation",
		verifyBody.Data.Token,
	)

	continuationResponse, err := testApp.Test(continuation)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, continuationResponse.StatusCode)

	var continuationBody struct {
		Data struct {
			Email                 string `json:"email"`
			HasPasswordCredential bool   `json:"has_password_credential"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(continuationResponse.Body).Decode(&continuationBody),
	)

	require.Equal(t, email, continuationBody.Data.Email)
	require.True(t, continuationBody.Data.HasPasswordCredential)

	// Finalize password reset.
	finalize := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/finalize",
		strings.NewReader(`{"password":"new-password"}`),
	)
	finalize.Header.Set("Content-Type", "application/json")
	finalize.Header.Set(
		"X-Password-Reset-Continuation",
		verifyBody.Data.Token,
	)

	finalizeResponse, err := testApp.Test(finalize)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, finalizeResponse.StatusCode)

	finalState, err := db.GetPasswordResetFinalState(ctx, email)
	require.NoError(t, err)

	passwordResult, err := security.NewPasswordHasher().Verify(
		"new-password",
		finalState.PasswordHash,
	)
	require.NoError(t, err)
	require.True(t, passwordResult.Match)

	require.NotEqual(t, oldHash, finalState.PasswordHash)
	require.Equal(
		t,
		string(passwordreset.PendingPasswordResetCompleted),
		finalState.PasswordResetStatus,
	)
	require.True(t, finalState.ConsumedAt.Valid)
	require.Zero(t, finalState.ActiveSessionCount)

	// Continuation token cannot be reused.
	reused := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/finalize",
		strings.NewReader(`{"password":"another-password"}`),
	)
	reused.Header.Set("Content-Type", "application/json")
	reused.Header.Set(
		"X-Password-Reset-Continuation",
		verifyBody.Data.Token,
	)

	reusedResponse, err := testApp.Test(reused)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, reusedResponse.StatusCode)

	var reusedBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(
		t,
		json.NewDecoder(reusedResponse.Body).Decode(&reusedBody),
	)

	require.Equal(
		t,
		passwordreset.CodePasswordResetContinuationConsumed,
		reusedBody.Error.Code,
	)
}

func TestGetPasswordResetContinuation_MissingHeader(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	req := httptest.NewRequest(
		http.MethodGet,
		"/auth/password-reset/continuation",
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

	require.Equal(
		t,
		passwordreset.CodePasswordResetContinuationRequired,
		body.Error.Code,
	)
}

func TestGetPasswordResetContinuation_InvalidToken(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	req := httptest.NewRequest(
		http.MethodGet,
		"/auth/password-reset/continuation",
		nil,
	)
	req.Header.Set(
		"X-Password-Reset-Continuation",
		"invalid-token",
	)

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

func TestPasswordReset_CreatePIN(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	email := "create-pin@example.com"

	_, err := db.CreatePasswordResetUser(ctx, email)
	require.NoError(t, err)

	// Request password reset.
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"create-pin@example.com"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	response, err := testApp.Test(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)

	var requestBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(response.Body).Decode(&requestBody),
	)

	verificationID := requestBody.Data.VerificationID
	require.NotEqual(t, uuid.Nil, verificationID)

	// PIN has not been issued yet.
	state, err := db.GetPasswordResetVerificationState(
		ctx,
		verificationID,
	)
	require.NoError(t, err)

	require.Equal(t, verificationID, state.VerificationID)
	require.Equal(t, "pending", state.VerificationStatus)
	require.Equal(t, int32(0), state.PinIssuedCount)

	// Create verification PIN.
	createPIN := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+
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

	// PIN should now be issued.
	state, err = db.GetPasswordResetVerificationState(
		ctx,
		verificationID,
	)
	require.NoError(t, err)

	require.Equal(t, verificationID, state.VerificationID)
	require.Equal(t, "pending", state.VerificationStatus)
	require.Equal(t, int32(1), state.PinIssuedCount)
}

func TestPasswordReset_GetVerification(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	email := "get-verification@example.com"

	_, err := db.CreatePasswordResetUser(ctx, email)
	require.NoError(t, err)

	// Request password reset.
	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"get-verification@example.com"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	response, err := testApp.Test(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)

	var requestBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(response.Body).Decode(&requestBody),
	)

	verificationID := requestBody.Data.VerificationID
	require.NotEqual(t, uuid.Nil, verificationID)

	// Get verification before PIN is issued.
	getVerification := httptest.NewRequest(
		http.MethodGet,
		"/auth/password-reset/verification/"+verificationID.String(),
		nil,
	)

	getResponse, err := testApp.Test(getVerification)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResponse.StatusCode)

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
		json.NewDecoder(getResponse.Body).Decode(&getBody),
	)

	require.Equal(t, verificationID.String(), getBody.Data.VerificationID)
	require.Equal(
		t,
		string(passwordreset.VerificationRequestPending),
		getBody.Data.Status,
	)
	require.False(t, getBody.Data.PINIssued)
	require.Equal(t, 0, getBody.Data.ResendInSeconds)

	// Create verification PIN.
	createPIN := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+
			verificationID.String()+
			"/pin",
		nil,
	)

	createPINResponse, err := testApp.Test(createPIN)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createPINResponse.StatusCode)

	// Get verification after PIN is issued.
	getVerification = httptest.NewRequest(
		http.MethodGet,
		"/auth/password-reset/verification/"+verificationID.String(),
		nil,
	)

	getResponse, err = testApp.Test(getVerification)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResponse.StatusCode)

	require.NoError(
		t,
		json.NewDecoder(getResponse.Body).Decode(&getBody),
	)

	require.Equal(t, verificationID.String(), getBody.Data.VerificationID)
	require.Equal(
		t,
		string(passwordreset.VerificationRequestPending),
		getBody.Data.Status,
	)
	require.True(t, getBody.Data.PINIssued)
	require.Greater(t, getBody.Data.ResendInSeconds, 0)
}

func TestPasswordReset_ResendVerification_Cooldown(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	email := "reset@example.com"

	_, err := db.CreatePasswordResetUser(
		ctx,
		email,
	)
	require.NoError(t, err)

	// Request password reset.
	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"reset@example.com"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var requestBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(resp.Body).Decode(&requestBody),
	)

	require.NotEqual(
		t,
		uuid.Nil,
		requestBody.Data.VerificationID,
	)

	verificationID := requestBody.Data.VerificationID

	// Create the first PIN.
	createPINReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+verificationID.String()+"/pin",
		nil,
	)

	createPINResp, err := testApp.Test(createPINReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createPINResp.StatusCode)

	// Make the verification request eligible for resend.
	require.NoError(
		t,
		db.MakeVerificationResendable(ctx, verificationID),
	)

	resendURL := "/auth/password-reset/verification/" +
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

	// Two emails should have been sent:
	// one for the initial PIN and one for the resend.
	require.Len(t, testEmailSender.Messages, 2)

	firstMessage := testEmailSender.Messages[0]
	secondMessage := testEmailSender.Messages[1]

	require.Equal(t, email, firstMessage.To.Email)
	require.Equal(t, email, secondMessage.To.Email)

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
		passwordreset.CodeVerificationResendCooldown,
		body.Error.Code,
	)

	// The rejected resend should not send another email.
	require.Len(t, testEmailSender.Messages, 2)
}

func TestPasswordReset_VerifyPIN_AttemptsExceeded(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	_, err := db.CreatePasswordResetUser(
		ctx,
		"attempts@example.com",
	)
	require.NoError(t, err)

	// Request password reset.
	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"attempts@example.com"}`),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var requestBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(resp.Body).Decode(&requestBody),
	)

	verificationID := requestBody.Data.VerificationID
	require.NotEqual(t, uuid.Nil, verificationID)

	// Create the PIN.
	createPINReq := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+verificationID.String()+"/pin",
		nil,
	)

	createPINResp, err := testApp.Test(createPINReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createPINResp.StatusCode)

	verifyURL := "/auth/password-reset/verification/" +
		verificationID.String() +
		"/verify"

	for attempt := 1; attempt <= int(passwordreset.VerificationCodeMaxAttempts); attempt++ {
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

		if attempt < int(passwordreset.VerificationCodeMaxAttempts) {
			require.Equal(
				t,
				http.StatusConflict,
				verifyResp.StatusCode,
			)
			require.Equal(
				t,
				passwordreset.CodeInvalidVerificationCode,
				verifyBody.Error.Code,
			)
			continue
		}

		require.Equal(
			t,
			http.StatusConflict,
			verifyResp.StatusCode,
		)
		require.Equal(
			t,
			passwordreset.CodeVerificationCodeAttemptsExceeded,
			verifyBody.Error.Code,
		)
	}
}

func TestPasswordReset_AlreadyPendingAndInvalidPIN(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	_, err := db.CreatePasswordResetUser(
		ctx,
		"pending-reset@example.com",
	)
	require.NoError(t, err)

	request := func() *http.Response {
		req := httptest.NewRequest(
			http.MethodPost,
			"/auth/password-reset",
			strings.NewReader(`{"email":"pending-reset@example.com"}`),
		)
		req.Header.Set("Content-Type", "application/json")

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		return resp
	}

	first := request()
	require.Equal(t, http.StatusCreated, first.StatusCode)

	var firstBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(first.Body).Decode(&firstBody),
	)

	require.NotEqual(
		t,
		uuid.Nil,
		firstBody.Data.VerificationID,
	)

	// Second request reuses the existing pending reset.
	second := request()
	require.Equal(t, http.StatusCreated, second.StatusCode)

	var secondBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(second.Body).Decode(&secondBody),
	)

	require.Equal(
		t,
		firstBody.Data.VerificationID,
		secondBody.Data.VerificationID,
	)

	// Create PIN before verifying.
	createPIN := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+
			firstBody.Data.VerificationID.String()+
			"/pin",
		nil,
	)

	createPINResponse, err := testApp.Test(createPIN)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createPINResponse.StatusCode)

	// Invalid PIN.
	invalid := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+
			firstBody.Data.VerificationID.String()+
			"/verify",
		strings.NewReader(`{"pin":"000000"}`),
	)
	invalid.Header.Set("Content-Type", "application/json")

	invalidResponse, err := testApp.Test(invalid)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, invalidResponse.StatusCode)

	var invalidBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(
		t,
		json.NewDecoder(invalidResponse.Body).Decode(&invalidBody),
	)

	require.Equal(
		t,
		passwordreset.CodeInvalidVerificationCode,
		invalidBody.Error.Code,
	)

	// Unknown email.
	unknown := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"unknown@example.com"}`),
	)
	unknown.Header.Set("Content-Type", "application/json")

	unknownResponse, err := testApp.Test(unknown)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, unknownResponse.StatusCode)

	var unknownBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(
		t,
		json.NewDecoder(unknownResponse.Body).Decode(&unknownBody),
	)

	require.Equal(
		t,
		apperror.CodeNotFound,
		unknownBody.Error.Code,
	)
}

func TestPasswordReset_ExpiredPendingIsReconciled(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)

	require.NoError(t, db.TruncatePasswordResetData(ctx))

	email := "expired@example.com"

	_, err := db.CreatePasswordResetUser(ctx, email)
	require.NoError(t, err)

	oldVerificationID, err := db.CreateExpiredPendingPasswordReset(
		ctx,
		email,
	)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"expired@example.com"}`),
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

	require.NoError(
		t,
		json.NewDecoder(resp.Body).Decode(&body),
	)

	require.NotEqual(
		t,
		uuid.Nil,
		body.Data.VerificationID,
	)

	require.NotEqual(
		t,
		oldVerificationID,
		body.Data.VerificationID,
	)
}
