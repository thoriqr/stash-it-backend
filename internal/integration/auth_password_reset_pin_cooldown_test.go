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
	"github.com/thoriqr/stash-it-backend/internal/security"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
)

// The two routes below are the whole of PIN issuance. They are separate
// endpoints with separate response contracts that happen to be the same act, and
// they enforce one policy between them. These helpers exist because asserting
// that property needs several calls against the same verification, and spelling
// the URLs out each time is how one direction ends up untested.

func pinIssueURL(verificationID uuid.UUID) string {
	return "/auth/password-reset/verification/" +
		verificationID.String() +
		"/pin"
}

func resendIssueURL(verificationID uuid.UUID) string {
	return "/auth/password-reset/verification/" +
		verificationID.String() +
		"/resend"
}

// requestPasswordResetID opens a password reset for emailAddress and returns the
// verification id the caller is handed. Every test here starts here, because on
// these unauthenticated routes that id is the only credential there is.
func requestPasswordResetID(t *testing.T, emailAddress string) uuid.UUID {
	t.Helper()

	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"`+emailAddress+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	response, err := testApp.Test(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)

	var body struct {
		Data struct {
			VerificationID uuid.UUID `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.NotEqual(t, uuid.Nil, body.Data.VerificationID)

	return body.Data.VerificationID
}

func postToURL(t *testing.T, url string) *http.Response {
	t.Helper()

	response, err := testApp.Test(
		httptest.NewRequest(http.MethodPost, url, nil),
	)
	require.NoError(t, err)

	return response
}

// requireCooldownConflict asserts the one refusal both issuance routes share.
func requireCooldownConflict(t *testing.T, response *http.Response) {
	t.Helper()

	require.Equal(t, http.StatusConflict, response.StatusCode)

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))

	require.Equal(
		t,
		passwordreset.CodeVerificationResendCooldown,
		body.Error.Code,
	)
}

// issuedPINCount reads back the database's own record of how many codes have
// been handed out for a verification. A refused issuance must not move it.
func issuedPINCount(
	t *testing.T,
	db *passwordresetdbtest.Queries,
	verificationID uuid.UUID,
) int32 {
	t.Helper()

	state, err := db.GetPasswordResetVerificationState(
		context.Background(),
		verificationID,
	)
	require.NoError(t, err)

	return state.PinIssuedCount
}

// TestPasswordReset_PINIssuance_CooldownIsSharedBetweenEndpoints is the
// invariant the consolidation exists to establish: neither route is the way
// around the other, whichever one issued the code that started the window.
//
// /pin used to issue regardless of last_sent_at, so it is called first here. If
// the gap were open, that call would return 201 and send a message rather than
// the 409 the assertions below require.
func TestPasswordReset_PINIssuance_CooldownIsSharedBetweenEndpoints(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	const emailAddress = "cooldown-shared@example.com"

	_, err := db.CreatePasswordResetUser(ctx, emailAddress)
	require.NoError(t, err)

	// A verification that has never sent a code has no cooldown to observe, so
	// the first issuance is allowed on either route. /pin is used here because it
	// is the one a caller reaches first.
	pinFirstID := requestPasswordResetID(t, emailAddress)

	require.Equal(
		t,
		http.StatusCreated,
		postToURL(t, pinIssueURL(pinFirstID)).StatusCode,
	)
	require.Len(t, testEmailSender.Messages, 1)
	require.Equal(t, int32(1), issuedPINCount(t, db, pinFirstID))

	requireCooldownConflict(t, postToURL(t, pinIssueURL(pinFirstID)))
	requireCooldownConflict(t, postToURL(t, resendIssueURL(pinFirstID)))

	require.Len(t, testEmailSender.Messages, 1)
	require.Equal(t, int32(1), issuedPINCount(t, db, pinFirstID))

	// The same two calls, with the window started by the other route instead.
	// /resend issuing a first code when none has been sent is the other half of
	// the two being one act, and it is what a caller who never reached /pin
	// actually does.
	const resendFirstEmail = "cooldown-shared-resend-first@example.com"

	_, err = db.CreatePasswordResetUser(ctx, resendFirstEmail)
	require.NoError(t, err)

	resendFirstID := requestPasswordResetID(t, resendFirstEmail)

	require.Equal(
		t,
		http.StatusOK,
		postToURL(t, resendIssueURL(resendFirstID)).StatusCode,
	)
	require.Len(t, testEmailSender.Messages, 2)
	require.Equal(t, int32(1), issuedPINCount(t, db, resendFirstID))

	requireCooldownConflict(t, postToURL(t, pinIssueURL(resendFirstID)))
	requireCooldownConflict(t, postToURL(t, resendIssueURL(resendFirstID)))

	require.Len(t, testEmailSender.Messages, 2)
	require.Equal(t, int32(1), issuedPINCount(t, db, resendFirstID))
}

// TestPasswordReset_PINIssuance_RefusalDoesNotInvalidateIssuedCode covers the
// part of a refusal that a status code cannot show: the code the user is already
// holding has to survive it.
//
// Issuance invalidates the previous active code before writing the new one. A
// cooldown check placed after that step instead of before it would look
// identical from outside — same 409, same error code, no message sent — while
// leaving the user holding no usable PIN and unable to ask for one until the
// window they are already waiting out has passed.
func TestPasswordReset_PINIssuance_RefusalDoesNotInvalidateIssuedCode(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	const emailAddress = "cooldown-keeps-code@example.com"

	_, err := db.CreatePasswordResetUser(ctx, emailAddress)
	require.NoError(t, err)

	verificationID := requestPasswordResetID(t, emailAddress)

	require.Equal(
		t,
		http.StatusCreated,
		postToURL(t, pinIssueURL(verificationID)).StatusCode,
	)

	// Pin the issued code to a known value so verification is deterministic.
	require.NoError(
		t,
		db.SetVerificationCodeHash(
			ctx,
			passwordresetdbtest.SetVerificationCodeHashParams{
				CodeHash: security.NewVerificationCodeHasher(
					[]byte("integration-test-verification-secret"),
				).Hash("123456"),
				VerificationRequestID: verificationID,
			},
		),
	)

	requireCooldownConflict(t, postToURL(t, pinIssueURL(verificationID)))
	requireCooldownConflict(t, postToURL(t, resendIssueURL(verificationID)))

	require.Len(t, testEmailSender.Messages, 1)
	require.Equal(t, int32(1), issuedPINCount(t, db, verificationID))

	// The live code still verifies. Had either refusal invalidated it, this would
	// come back as "verification code is no longer available" instead.
	verify := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/verification/"+verificationID.String()+"/verify",
		strings.NewReader(`{"pin":"123456"}`),
	)
	verify.Header.Set("Content-Type", "application/json")

	verifyResponse, err := testApp.Test(verify)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, verifyResponse.StatusCode)
}

// TestPasswordReset_PINIssuance_CooldownElapsesForBothEndpoints checks that the
// shared policy is a cooldown and not a lockout.
//
// A rule that could not be outlived would make the second issuance permanently
// unreachable, and since the reset itself expires in fifteen minutes that would
// leave anyone who lost one message with no way to ask for another.
func TestPasswordReset_PINIssuance_CooldownElapsesForBothEndpoints(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	const emailAddress = "cooldown-elapsed@example.com"

	_, err := db.CreatePasswordResetUser(ctx, emailAddress)
	require.NoError(t, err)

	verificationID := requestPasswordResetID(t, emailAddress)

	require.Equal(
		t,
		http.StatusCreated,
		postToURL(t, pinIssueURL(verificationID)).StatusCode,
	)
	require.Len(t, testEmailSender.Messages, 1)

	require.NoError(
		t,
		db.MakeVerificationResendable(ctx, verificationID),
	)

	require.Equal(
		t,
		http.StatusOK,
		postToURL(t, resendIssueURL(verificationID)).StatusCode,
	)
	require.Len(t, testEmailSender.Messages, 2)

	require.NoError(
		t,
		db.MakeVerificationResendable(ctx, verificationID),
	)

	require.Equal(
		t,
		http.StatusCreated,
		postToURL(t, pinIssueURL(verificationID)).StatusCode,
	)
	require.Len(t, testEmailSender.Messages, 3)

	// Both routes moved the same counter, which is the database-side statement
	// that they are one act under two names.
	require.Equal(t, int32(3), issuedPINCount(t, db, verificationID))
}
