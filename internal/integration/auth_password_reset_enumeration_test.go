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

	"github.com/thoriqr/stash-it-backend/internal/testutil"
	passwordresetdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/password_reset/generated"
)

// passwordResetResponse is the whole observable body of the reset request,
// kept as a struct so that a field appearing in one case and not the other is a
// decode failure rather than something a test has to remember to look for.
type passwordResetResponse struct {
	Data *struct {
		VerificationID uuid.UUID `json:"verification_id"`
	} `json:"data"`
	Message string `json:"message"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`

	// Raw is the exact body, kept so two responses can be compared as bytes
	// rather than as a projection of the fields this struct happens to name.
	Raw string `json:"-"`
}

// postPasswordReset requests a reset and returns the status and the full body.
func postPasswordReset(
	t *testing.T,
	emailAddress string,
) (int, passwordResetResponse) {
	t.Helper()

	request := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset",
		strings.NewReader(`{"email":"`+emailAddress+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")

	response, err := testApp.Test(request)
	require.NoError(t, err)

	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	var body passwordResetResponse

	require.NoError(t, json.Unmarshal(raw, &body))

	body.Raw = string(raw)

	return response.StatusCode, body
}

// TestPasswordReset_Request_UnknownEmailIsIndistinguishable is the property the
// placeholder exists to provide: the response for an address nobody holds is the
// response for one that is held.
//
// Both assertions are on the whole observable response rather than on the status
// alone, because a differing status is the easy case to fix and a differing body
// is the one that would survive a status-only fix.
func TestPasswordReset_Request_UnknownEmailIsIndistinguishable(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	const knownEmail = "enumeration-known@example.com"

	_, err := db.CreatePasswordResetUser(ctx, knownEmail)
	require.NoError(t, err)

	knownStatus, known := postPasswordReset(t, knownEmail)
	unknownStatus, unknown := postPasswordReset(
		t,
		"enumeration-unknown@example.com",
	)

	require.Equal(t, http.StatusCreated, knownStatus)
	require.Equal(t, knownStatus, unknownStatus)

	require.Equal(t, known.Message, unknown.Message)
	require.Equal(t, "password reset requested", unknown.Message)

	// Neither side carries an error envelope: an unknown address is answered,
	// not refused.
	require.Nil(t, unknown.Error)
	require.Nil(t, known.Error)

	require.NotNil(t, unknown.Data)
	require.NotNil(t, known.Data)

	require.NotEqual(
		t,
		uuid.Nil,
		unknown.Data.VerificationID,
		"an unknown address must still receive an id",
	)

	require.NotEqual(
		t,
		known.Data.VerificationID,
		unknown.Data.VerificationID,
		"an unknown address must not receive a known account's id",
	)

	// Both ids are UUID v7, matching the ones the database generates. A
	// placeholder with a different version nibble would be distinguishable from
	// a real one by inspection, which would move the signal rather than remove
	// it.
	require.Equal(t, uuid.Version(7), unknown.Data.VerificationID.Version())
	require.Equal(t, uuid.Version(7), known.Data.VerificationID.Version())

	// The raw bodies differ only in the identifier itself. Replacing it with a
	// placeholder makes them byte-identical, which is what "same response shape"
	// has to mean for this to be worth anything.
	require.Equal(
		t,
		strings.ReplaceAll(
			known.Raw,
			known.Data.VerificationID.String(),
			"<id>",
		),
		strings.ReplaceAll(
			unknown.Raw,
			unknown.Data.VerificationID.String(),
			"<id>",
		),
	)
}

// TestPasswordReset_Request_UnknownEmailPersistsNothing is the rule the whole
// approach rests on, asserted against the tables rather than inferred from the
// status code.
//
// A placeholder only stays harmless while nothing backs it. These three counts
// are the statement that it is backed by nothing.
func TestPasswordReset_Request_UnknownEmailPersistsNothing(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	const unknownEmail = "no-state@example.com"

	// Repeated requests, because one could not distinguish "never wrote
	// anything" from "wrote something and cleaned it up".
	for i := 0; i < 3; i++ {
		status, body := postPasswordReset(t, unknownEmail)

		require.Equal(t, http.StatusCreated, status)
		require.NotEqual(t, uuid.Nil, body.Data.VerificationID)
	}

	state, err := db.CountPasswordResetStateForEmail(ctx, unknownEmail)
	require.NoError(t, err)

	require.Zero(t, state.PendingResetCount)
	require.Zero(t, state.VerificationRequestCount)
	require.Zero(t, state.VerificationCodeCount)

	require.Empty(
		t,
		testEmailSender.Messages,
		"an address with no account must not be mailed",
	)
}

// TestPasswordReset_Request_KnownEmailPersistsAndIsUsable is the other half: the
// known-address path must be completely unchanged, because a fix that made
// unknown addresses indistinguishable by making known ones do nothing would
// break the feature outright.
func TestPasswordReset_Request_KnownEmailPersistsAndIsUsable(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	const knownEmail = "still-works@example.com"

	userID, err := db.CreatePasswordResetUser(ctx, knownEmail)
	require.NoError(t, err)

	oldHash, err := testutil.NewPasswordHasher().Hash(
		ctx,
		"old-password",
	)
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

	status, body := postPasswordReset(t, knownEmail)

	require.Equal(t, http.StatusCreated, status)
	require.False(t, body.Data.VerificationID == uuid.Nil)

	state, err := db.CountPasswordResetStateForEmail(ctx, knownEmail)
	require.NoError(t, err)

	require.Equal(t, int64(1), state.PendingResetCount)
	require.Equal(t, int64(1), state.VerificationRequestCount)

	// A second request reuses the open reset rather than opening another, and
	// hands back the same id.
	_, second := postPasswordReset(t, knownEmail)

	require.Equal(
		t,
		body.Data.VerificationID,
		second.Data.VerificationID,
	)

	state, err = db.CountPasswordResetStateForEmail(ctx, knownEmail)
	require.NoError(t, err)

	require.Equal(t, int64(1), state.PendingResetCount)
	require.Equal(t, int64(1), state.VerificationRequestCount)
}

// TestPasswordReset_Request_DummyIDIsRefusedByEveryDownstreamEndpoint walks the
// placeholder through everything that accepts a verification id.
//
// The frontend calls GET first, but that ordering is a client convenience and
// not a security control, so each of these is called directly here. Each must
// refuse through its ordinary unknown-id path without writing anything or
// mailing anything.
func TestPasswordReset_Request_DummyIDIsRefusedByEveryDownstreamEndpoint(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	const unknownEmail = "dummy-id-unusable@example.com"

	_, body := postPasswordReset(t, unknownEmail)

	dummyID := body.Data.VerificationID
	require.NotEqual(t, uuid.Nil, dummyID)

	base := "/auth/password-reset/verification/" + dummyID.String()

	// GET verification: the frontend's own first step.
	getResponse, err := testApp.Test(
		httptest.NewRequest(http.MethodGet, base, nil),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, getResponse.StatusCode)

	// Direct PIN issuance, with no preceding GET.
	for _, path := range []string{base + "/pin", base + "/resend"} {
		postResponse, err := testApp.Test(
			httptest.NewRequest(http.MethodPost, path, nil),
		)
		require.NoError(t, err)
		require.Equalf(
			t,
			http.StatusNotFound,
			postResponse.StatusCode,
			"%s must refuse an unpersisted verification id",
			path,
		)
	}

	// Direct verification.
	verify := httptest.NewRequest(
		http.MethodPost,
		base+"/verify",
		strings.NewReader(`{"pin":"123456"}`),
	)
	verify.Header.Set("Content-Type", "application/json")

	verifyResponse, err := testApp.Test(verify)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, verifyResponse.StatusCode)

	require.Empty(
		t,
		testEmailSender.Messages,
		"a placeholder verification id must never produce a message",
	)

	state, err := db.CountPasswordResetStateForEmail(ctx, unknownEmail)
	require.NoError(t, err)

	require.Zero(t, state.PendingResetCount)
	require.Zero(t, state.VerificationRequestCount)
	require.Zero(t, state.VerificationCodeCount)

	// Finalization needs a continuation token, which is only ever minted by a
	// successful verification against a real verification request. Presenting
	// the placeholder as one must not resolve, and must not reach credentials or
	// sessions.
	finalize := httptest.NewRequest(
		http.MethodPost,
		"/auth/password-reset/finalize",
		strings.NewReader(`{"password":"new-password"}`),
	)
	finalize.Header.Set("Content-Type", "application/json")
	finalize.Header.Set("X-Password-Reset-Continuation", dummyID.String())

	finalizeResponse, err := testApp.Test(finalize)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, finalizeResponse.StatusCode)

	var finalizeBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(
		t,
		json.NewDecoder(finalizeResponse.Body).Decode(&finalizeBody),
	)

	require.NotEmpty(t, finalizeBody.Error.Code)

	// A placeholder never reaches the upsert, so the address has no credential
	// and nothing could have been revoked. Counting on the credential table is
	// what makes "finalize did nothing" observable rather than merely
	// unobserved.
	state, err = db.CountPasswordResetStateForEmail(ctx, unknownEmail)
	require.NoError(t, err)

	require.Zero(t, state.CredentialCount)
}

// TestPasswordReset_Request_DummyIDCannotDistinguishFromRandomID checks the
// claim the design rests on: a caller holding a placeholder learns nothing about
// the address beyond what holding any made-up id would tell them.
//
// The placeholder and a random UUID are both absent from the database, so every
// endpoint must answer them identically. If they ever diverge, the placeholder
// has become a signal in its own right.
func TestPasswordReset_Request_DummyIDCannotDistinguishFromRandomID(t *testing.T) {
	ctx := context.Background()

	db := passwordresetdbtest.New(testPool)
	require.NoError(t, db.TruncatePasswordResetData(ctx))

	// Reset captured emails from previous tests.
	testEmailSender.Messages = nil

	_, body := postPasswordReset(t, "no-account-here@example.com")

	dummyID := body.Data.VerificationID
	require.NotEqual(t, uuid.Nil, dummyID)

	neverIssued := uuid.New()

	// Keyed by endpoint as well as by id. Keying by id alone let the second
	// endpoint's result overwrite the first's, so the comparison below would have
	// covered one route while appearing to cover two.
	type endpointOutcome struct {
		status int
		code   string
	}

	outcomes := make(map[string]map[string]endpointOutcome)

	endpoints := []struct {
		name   string
		method string
		suffix string
		body   string
	}{
		{name: "GET", method: http.MethodGet},
		{name: "POST /pin", method: http.MethodPost, suffix: "/pin"},
		{name: "POST /resend", method: http.MethodPost, suffix: "/resend"},
		{
			name:   "POST /verify",
			method: http.MethodPost,
			suffix: "/verify",
			body:   `{"pin":"123456"}`,
		},
	}

	for _, id := range []uuid.UUID{dummyID, neverIssued} {
		base := "/auth/password-reset/verification/" + id.String()
		outcomes[id.String()] = make(map[string]endpointOutcome, len(endpoints))

		for _, endpoint := range endpoints {
			req := httptest.NewRequest(
				endpoint.method,
				base+endpoint.suffix,
				strings.NewReader(endpoint.body),
			)

			if endpoint.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}

			response, err := testApp.Test(req)
			require.NoError(t, err)

			var decoded struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}

			require.NoError(
				t,
				json.NewDecoder(response.Body).Decode(&decoded),
			)

			outcomes[id.String()][endpoint.name] = endpointOutcome{
				status: response.StatusCode,
				code:   decoded.Error.Code,
			}
		}
	}

	for endpoint, dummy := range outcomes[dummyID.String()] {
		other, compared := outcomes[neverIssued.String()][endpoint]
		require.Truef(t, compared, "endpoint %s was not compared", endpoint)

		require.Equalf(
			t,
			other.status,
			dummy.status,
			"a placeholder must be refused exactly as any unknown id is on %s",
			endpoint,
		)

		require.Equalf(
			t,
			other.code,
			dummy.code,
			"endpoint %s answered a placeholder and a random id differently",
			endpoint,
		)
	}

	require.Equal(
		t,
		http.StatusNotFound,
		outcomes[dummyID.String()]["GET"].status,
	)

	require.Empty(t, testEmailSender.Messages)
}
