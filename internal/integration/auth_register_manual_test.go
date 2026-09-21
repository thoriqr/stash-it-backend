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
	require.NotEqual(t, uuid.Nil, state.VerificationCodeID)
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
	require.GreaterOrEqual(t, getBody.Data.ResendInSeconds, 0)
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

	resendURL := "/auth/register/verification/" +
		verificationID.String() +
		"/resend"

	// First resend should succeed and start the cooldown.
	firstResendReq := httptest.NewRequest(
		http.MethodPost,
		resendURL,
		nil,
	)

	firstResendResp, err := testApp.Test(firstResendReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, firstResendResp.StatusCode)

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
	require.NotEqual(t, uuid.Nil, history[1].VerificationCodeID)
}
