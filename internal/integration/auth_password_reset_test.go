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

const integrationVerificationCode = "123456"

func TestPasswordReset_EndToEndRevokesSessions(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	email := "reset@example.com"
	userID, err := db.CreatePasswordResetUser(ctx, email)
	require.NoError(t, err)
	oldHash, err := security.NewPasswordHasher().Hash("old-password")
	require.NoError(t, err)
	require.NoError(t, db.CreatePasswordCredentialForUser(ctx, passwordresetdbtest.CreatePasswordCredentialForUserParams{UserID: userID, PasswordHash: oldHash}))
	_, err = db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/auth/password-reset", strings.NewReader(`{"email":"reset@example.com"}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := testApp.Test(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)

	var requestBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&requestBody))
	state, err := db.GetPasswordResetState(ctx, email)
	require.NoError(t, err)
	require.Equal(t, requestBody.Data.VerificationID, state.VerificationID)

	codeHash := security.NewVerificationCodeHasher([]byte("integration-test-verification-secret")).Hash(integrationVerificationCode)
	require.NoError(t, db.SetVerificationCodeHash(ctx, passwordresetdbtest.SetVerificationCodeHashParams{VerificationRequestID: state.VerificationID, CodeHash: codeHash}))

	verify := httptest.NewRequest(http.MethodPost, "/auth/password-reset/verification/"+state.VerificationID.String()+"/verify", strings.NewReader(`{"pin":"123456"}`))
	verify.Header.Set("Content-Type", "application/json")
	verifyResponse, err := testApp.Test(verify)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, verifyResponse.StatusCode)
	var verifyBody struct {
		Data struct {
			Token string `json:"password_reset_continuation_token"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(verifyResponse.Body).Decode(&verifyBody))
	require.NotEmpty(t, verifyBody.Data.Token)

	continuation := httptest.NewRequest(http.MethodGet, "/auth/password-reset/continuation", nil)
	continuation.Header.Set("X-Password-Reset-Continuation", verifyBody.Data.Token)
	continuationResponse, err := testApp.Test(continuation)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, continuationResponse.StatusCode)
	var continuationBody struct {
		Data struct {
			Email                 string `json:"email"`
			HasPasswordCredential bool   `json:"has_password_credential"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(continuationResponse.Body).Decode(&continuationBody))
	require.Equal(t, email, continuationBody.Data.Email)
	require.True(t, continuationBody.Data.HasPasswordCredential)

	finalize := httptest.NewRequest(http.MethodPost, "/auth/password-reset/finalize", strings.NewReader(`{"password":"new-password"}`))
	finalize.Header.Set("Content-Type", "application/json")
	finalize.Header.Set("X-Password-Reset-Continuation", verifyBody.Data.Token)
	finalizeResponse, err := testApp.Test(finalize)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, finalizeResponse.StatusCode)

	finalState, err := db.GetPasswordResetFinalState(ctx, email)
	require.NoError(t, err)
	passwordResult, err := security.NewPasswordHasher().Verify("new-password", finalState.PasswordHash)
	require.NoError(t, err)
	require.True(t, passwordResult.Match)
	require.NotEqual(t, oldHash, finalState.PasswordHash)
	require.Equal(t, string(passwordreset.PendingPasswordResetCompleted), finalState.PasswordResetStatus)
	require.True(t, finalState.ConsumedAt.Valid)
	require.Zero(t, finalState.ActiveSessionCount)

	reused := httptest.NewRequest(http.MethodPost, "/auth/password-reset/finalize", strings.NewReader(`{"password":"another-password"}`))
	reused.Header.Set("Content-Type", "application/json")
	reused.Header.Set("X-Password-Reset-Continuation", verifyBody.Data.Token)
	reusedResponse, err := testApp.Test(reused)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, reusedResponse.StatusCode)
	var reusedBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(reusedResponse.Body).Decode(&reusedBody))
	require.Equal(t, passwordreset.CodePasswordResetContinuationConsumed, reusedBody.Error.Code)
}

func TestPasswordReset_AlreadyPendingAndInvalidPIN(t *testing.T) {
	ctx := context.Background()
	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))
	_, err := db.CreatePasswordResetUser(ctx, "pending-reset@example.com")
	require.NoError(t, err)

	request := func() *http.Response {
		req := httptest.NewRequest(http.MethodPost, "/auth/password-reset", strings.NewReader(`{"email":"pending-reset@example.com"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := testApp.Test(req)
		require.NoError(t, err)
		return resp
	}
	first := request()
	var firstBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(first.Body).Decode(&firstBody))
	second := request()
	require.Equal(t, http.StatusOK, second.StatusCode)
	var secondBody struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(second.Body).Decode(&secondBody))
	require.Equal(t, firstBody.Data.VerificationID, secondBody.Data.VerificationID)

	invalid := httptest.NewRequest(http.MethodPost, "/auth/password-reset/verification/"+firstBody.Data.VerificationID.String()+"/verify", strings.NewReader(`{"pin":"000000"}`))
	invalid.Header.Set("Content-Type", "application/json")
	invalidResponse, err := testApp.Test(invalid)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, invalidResponse.StatusCode)
	var invalidBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(invalidResponse.Body).Decode(&invalidBody))
	require.Equal(t, passwordreset.CodeInvalidVerificationCode, invalidBody.Error.Code)

	unknown := httptest.NewRequest(http.MethodPost, "/auth/password-reset", strings.NewReader(`{"email":"unknown@example.com"}`))
	unknown.Header.Set("Content-Type", "application/json")
	unknownResponse, err := testApp.Test(unknown)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, unknownResponse.StatusCode)
	var unknownBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(unknownResponse.Body).Decode(&unknownBody))
	require.Equal(t, apperror.CodeNotFound, unknownBody.Error.Code)
}
