package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	registration "github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	registrationtestdb "github.com/thoriqr/stash-it-backend/internal/testutil/db/registration/generated"
)

// These tests cover the two decisions CreateSocialRegistration makes about state
// that already exists for an address: a concurrent pair contending for an address
// with nothing on it, and a verified identity arriving for a registration another
// identity opened.
//
// Both are asserted against real PostgreSQL rather than mocks. The behaviour under
// test is entirely the interaction between the transaction, the FOR UPDATE lookup
// and pending_registrations_email_idx, and a mock repository cannot express any of
// that: it would pass identically whichever way the branch was written.
//
// The integration app's Google verifier always asserts the same identity, so a
// "different identity" case is arranged by seeding the stored side with a subject
// the app does not present. That is the same technique TestConfirmAccountLink
// uses, and it has the property that the presented token is always the app's real
// fixture rather than something a test wrote.

// The socialGoogleEmail, socialGoogleProvider and socialGoogleSubject constants
// below are the identity the integration app's verifier always asserts. They name
// the fixture, not a credential: nothing verifies them.
const (
	socialGoogleEmail    = "google@example.com"
	socialGoogleProvider = "google"
	socialGoogleSubject  = "google-subject-123"
)

// foreignGoogleSubject is a provider subject the app never presents. Storing it
// is what makes a mismatch without needing a second verifier.
const foreignGoogleSubject = "a-different-google-subject"

// pgText is the non-null pgtype.Text the seed queries take.
//
// The suite writes these inline elsewhere; it is spelled once here because these
// cases seed the same snapshot shape several times and the field name alone does
// not say whether it is set.
func pgText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

// googleLoginOutcome is what one Google login told the caller.
//
// VerificationID is carried so a concurrent pair can be shown to resolve to the
// same persisted state, which is the property the race is really about: two
// callers, one registration.
type googleLoginOutcome struct {
	status         int
	code           string
	outcome        string
	verificationID string
}

// postGoogleLogin presents the app's Google token and reports the result.
//
// The body is malformed-free on purpose: the caller here is a client that has
// just authenticated with Google, not an unauthenticated prober.
func postGoogleLogin(t *testing.T) googleLoginOutcome {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/login/google",
		strings.NewReader(`{
			"id_token": "test-google-id-token"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Platform", "web")

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	result := googleLoginOutcome{status: resp.StatusCode}

	if result.status >= 400 {
		var errBody registerAPIError

		require.NoError(
			t,
			json.NewDecoder(resp.Body).Decode(&errBody),
		)

		result.code = errBody.Error.Code

		return result
	}

	var body struct {
		Data struct {
			Outcome        string `json:"outcome"`
			VerificationID string `json:"verification_id"`
		} `json:"data"`
	}

	require.NoError(
		t,
		json.NewDecoder(resp.Body).Decode(&body),
	)

	result.outcome = body.Data.Outcome
	result.verificationID = body.Data.VerificationID

	return result
}

// Two Google logins for an address with no registration yet cannot both be decided
// by the lookup, and they must not be: GetPendingRegistrationByEmailForUpdate
// locks a row, and there is no row yet. Both proceed to the insert and one loses
// on pending_registrations_email_idx.
//
// Before the fix the loser was answered 409 REGISTRATION_ALREADY_EXISTS with no
// verification id — a registration that existed and that the caller could not
// reach. The re-run inside CreateSocialRegistration now re-reads the winner's
// committed row, finds the social registration and the matching identity, and
// returns the verification id the winner created.
//
// Both callers therefore succeed, and both resolve to the same state. The counts
// are what distinguish "the loser reused the winner's registration" from "the loser
// created a second one that happened to share the email", which the unique index
// would otherwise have permitted had it not been the authority.
//
// The rounds are not padding, and that is measured rather than assumed. The window
// between "the lookup found no row" and "the insert claims the address" is short,
// and a single round of eight requests often misses it entirely: the losers find
// the winner's committed row and take the reuse path, so the race is never entered.
// Repeating the race on a truncated table drives the hit rate from "sometimes" to
// "nearly every round". Removing the re-run from CreateSocialRegistration and
// running this exact arrangement produced 272 refused requests across 40 rounds;
// with the re-run in place it produces zero.
func TestCreateSocialRegistration_ConcurrentSameNewEmailReusesOneRegistration(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	// Eight per round, for the reason behind the manual flow's equivalent: enough
	// requests to keep the insert window genuinely contended.
	//
	// Forty rounds, matching the arrangement the removal experiment was actually
	// run on rather than extrapolating past it. Twenty also fails on a removed
	// re-run, but the margin there is an extrapolation from a single 40-round
	// sample; at forty it is the measurement itself. The whole test costs about a
	// second.
	const attempts = 8
	const rounds = 40

	for round := range rounds {
		require.NoError(t, db.TruncateRegistrationData(ctx))

		outcomes := make([]googleLoginOutcome, attempts)

		runConcurrently(t, len(outcomes), func(index int) {
			outcomes[index] = postGoogleLogin(t)
		})

		var succeeded int

		var verificationIDs []string

		for _, outcome := range outcomes {
			// No request may be refused, and in particular none may be told the
			// registration already exists: that is the defect being fixed, and it
			// is the response a caller has no way to recover from.
			require.Equal(
				t,
				http.StatusOK,
				outcome.status,
				"round %d: every concurrent request must succeed: %+v",
				round,
				outcomes,
			)
			require.Empty(
				t,
				outcome.code,
				"round %d: the loser must not be handed an error code: %+v",
				round,
				outcomes,
			)
			require.Equal(
				t,
				"registration_required",
				outcome.outcome,
			)

			succeeded++
			verificationIDs = append(
				verificationIDs,
				outcome.verificationID,
			)
		}

		require.Equal(t, attempts, succeeded)
		require.NotEmpty(t, verificationIDs[0])

		// Every response must name the same verification request, or one of them
		// is pointing at state the others cannot see.
		for _, id := range verificationIDs {
			require.Equal(
				t,
				verificationIDs[0],
				id,
				"round %d: every response must resolve to the same "+
					"verification request",
				round,
			)
		}

		states, err := db.GetSocialPendingRegistrationStates(
			ctx,
			socialGoogleEmail,
		)
		require.NoError(t, err)

		require.Len(
			t,
			states,
			1,
			"round %d: exactly one pending registration may exist",
			round,
		)
		require.Equal(
			t,
			string(registration.RegistrationTypeSocial),
			states[0].RegistrationType,
		)
		require.Equal(
			t,
			string(registration.PendingRegistrationPending),
			states[0].Status,
		)

		// Exactly one identity and one verification request on it, read from the
		// joined row so a duplicate on a second registration could not hide behind
		// a :one query.
		require.Equal(
			t,
			socialGoogleProvider,
			states[0].Provider.String,
		)
		require.Equal(
			t,
			socialGoogleSubject,
			states[0].ProviderSubject.String,
		)
		require.Equal(
			t,
			verificationIDs[0],
			states[0].VerificationID.String(),
		)
		require.Equal(
			t,
			string(registration.VerificationRequestPending),
			states[0].VerificationStatus.String,
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
}

// countVerificationRequestsForRegistration counts the registration verification
// requests opened for an address, across every registration it has had.
//
// A registration may legitimately be replaced, so this is deliberately not the
// same as counting requests on the single pending registration: the property under
// test is that a concurrent pair opens one, not that history was never written.
func countVerificationRequestsForRegistration(
	t *testing.T,
	email string,
) int64 {
	t.Helper()

	var count int64

	err := testPool.QueryRow(
		context.Background(),
		`SELECT COUNT(*)
		 FROM verification_requests vr
		 JOIN pending_registrations pr
		   ON pr.id = vr.subject_id
		 WHERE pr.email = $1
		   AND vr.subject_type = 'pending_registration'
		   AND vr.purpose = 'registration'`,
		email,
	).Scan(&count)
	require.NoError(t, err)

	return count
}

// A verified identity arriving for a registration another identity opened is
// refused, and the stored registration is left exactly as it was.
//
// The refusal is the conservative half of the policy. The capability to finalize
// lives in the mailbox rather than in the row, so this caller could already
// complete the stored registration if it chose to; what expire-and-replace would
// add is the ability to cancel it, which is a denial primitive against whoever
// really holds the address. Refusing removes that and reports the truth.
func TestCreateSocialRegistration_DifferentIdentityOnLiveRegistrationIsRefused(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	_, err := db.CreateSocialPendingRegistration(
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

	before, err := db.GetSocialPendingRegistrationStates(
		ctx,
		socialGoogleEmail,
	)
	require.NoError(t, err)
	require.Len(t, before, 1)

	result := postGoogleLogin(t)

	require.Equal(
		t,
		http.StatusConflict,
		result.status,
		"a live registration held by another identity must be refused",
	)
	require.Equal(
		t,
		registration.CodeRegistrationIdentityMismatch,
		result.code,
	)

	// The stored identity must not have been rewritten, and the registration must
	// still be the one the other identity opened.
	after, err := db.GetSocialPendingRegistrationStates(
		ctx,
		socialGoogleEmail,
	)
	require.NoError(t, err)

	require.Len(
		t,
		after,
		1,
		"a refusal must not create a registration",
	)

	require.Equal(t, before[0].ID, after[0].ID)
	require.Equal(
		t,
		string(registration.PendingRegistrationPending),
		after[0].Status,
		"the stored registration must stay pending, not be expired",
	)
	require.Equal(
		t,
		foreignGoogleSubject,
		after[0].ProviderSubject.String,
		"the stored subject must not be overwritten by the caller's",
	)
	require.Equal(
		t,
		socialGoogleProvider,
		after[0].Provider.String,
	)
	require.Equal(
		t,
		before[0].VerificationID.String(),
		after[0].VerificationID.String(),
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

// An expired registration is the other half of the policy, and it is the case that
// was previously unrecoverable: the mismatch check ran before the expiry branch, so
// a registration past its deadline returned 500 forever and nothing could clear it.
//
// Past its deadline the registration cannot be finalized —
// validateVerificationPending, validateRegistrationContinuation and
// CompletePendingRegistration all refuse it on expires_at — so it holds no
// capability and expiring it grants this caller nothing the passage of time had
// not already granted. The original is left expired and its identity is left
// attached to it, so this is a replacement and not a transfer.
func TestCreateSocialRegistration_DifferentIdentityOnExpiredRegistrationReplacesIt(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	_, err := db.CreateExpiredSocialPendingRegistration(
		ctx,
		registrationtestdb.CreateExpiredSocialPendingRegistrationParams{
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

	result := postGoogleLogin(t)

	require.Equal(
		t,
		http.StatusOK,
		result.status,
		"an expired registration must not produce a server fault: %+v",
		result,
	)
	require.Equal(t, "registration_required", result.outcome)
	require.NotEmpty(t, result.verificationID)

	states, err := db.GetSocialPendingRegistrationStates(
		ctx,
		socialGoogleEmail,
	)
	require.NoError(t, err)

	require.Len(
		t,
		states,
		2,
		"the expired registration must be kept alongside its replacement, "+
			"not overwritten",
	)

	// Ordered by creation, so the original is first.
	original := states[0]
	replacement := states[1]

	require.Equal(
		t,
		string(registration.PendingRegistrationExpired),
		original.Status,
		"the original must be expired, not left pending",
	)
	require.Equal(
		t,
		foreignGoogleSubject,
		original.ProviderSubject.String,
		"the original identity must stay attached to the original registration",
	)

	require.NotEqual(t, original.ID, replacement.ID)
	require.Equal(
		t,
		string(registration.PendingRegistrationPending),
		replacement.Status,
	)
	require.Equal(
		t,
		socialGoogleSubject,
		replacement.ProviderSubject.String,
		"the replacement must carry the incoming identity",
	)
	require.Equal(
		t,
		result.verificationID,
		replacement.VerificationID.String(),
	)

	// Two registrations exist, so the unique index is doing its job only because
	// the original left the pending set. Asserting the row count is what proves
	// the replacement did not arrive by displacing the original.
	//
	// The tally is across the address, not per registration, so the pairing with
	// the row checks above is what carries the "exactly one identity each" claim:
	// the join is a LEFT JOIN, so a registration that had lost or gained an
	// identity would show an empty subject here rather than being invisible.
	for _, state := range states {
		require.True(
			t,
			state.ProviderSubject.Valid,
			"every registration must carry exactly one identity",
		)
		require.True(t, state.Provider.Valid)
		require.True(t, state.VerificationID.Valid)
	}

	require.Equal(
		t,
		int64(2),
		countPendingSocialIdentitiesForRegistration(t, socialGoogleEmail),
		"each registration must hold exactly one identity, so two registrations "+
			"mean two identities and not one reused",
	)
}

// The reuse path is untouched by either fix: a second login for the same identity
// must still find and return the registration already open, without creating a
// second one and without extending its lifetime.
func TestCreateSocialRegistration_SameIdentityOnLiveRegistrationReusesIt(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	first := postGoogleLogin(t)
	require.Equal(t, http.StatusOK, first.status)
	require.Equal(t, "registration_required", first.outcome)
	require.NotEmpty(t, first.verificationID)

	before, err := db.GetSocialPendingRegistrationStates(
		ctx,
		socialGoogleEmail,
	)
	require.NoError(t, err)
	require.Len(t, before, 1)

	second := postGoogleLogin(t)

	require.Equal(t, http.StatusOK, second.status)
	require.Equal(
		t,
		first.verificationID,
		second.verificationID,
		"a sequential login must reuse the open registration",
	)

	after, err := db.GetSocialPendingRegistrationStates(
		ctx,
		socialGoogleEmail,
	)
	require.NoError(t, err)

	require.Len(t, after, 1, "reuse must not create a second registration")
	require.Equal(t, before[0].ID, after[0].ID)
	require.Equal(t, before[0].ExpiresAt, after[0].ExpiresAt)

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

// The manual↔social transition is the other direction and is owned by
// CreateManualRegistration. Asserting it here is what shows the social-side
// changes did not disturb it: a manual registration still supersedes a pending
// social one, and the social identity that was attached to the superseded
// registration stays attached to it.
func TestCreateManualRegistration_SupersedesPendingSocialRegistration(t *testing.T) {
	ctx := context.Background()
	db := registrationtestdb.New(testPool)

	require.NoError(t, db.TruncateRegistrationData(ctx))

	_, err := db.CreateSocialPendingRegistration(
		ctx,
		registrationtestdb.CreateSocialPendingRegistrationParams{
			Email:           socialGoogleEmail,
			Provider:        socialGoogleProvider,
			ProviderSubject: socialGoogleSubject,
			EmailSnapshot:   pgText(socialGoogleEmail),
			DisplayNameSnapshot: pgText(
				"Google User",
			),
		},
	)
	require.NoError(t, err)

	manualID := registerAndReturnVerificationID(t, socialGoogleEmail)

	states, err := db.GetSocialPendingRegistrationStates(
		ctx,
		socialGoogleEmail,
	)
	require.NoError(t, err)

	require.Len(
		t,
		states,
		2,
		"the manual registration must be added, not substituted in place",
	)

	require.Equal(
		t,
		string(registration.PendingRegistrationExpired),
		states[0].Status,
		"the manual flow must expire the pending social registration",
	)
	require.Equal(
		t,
		socialGoogleSubject,
		states[0].ProviderSubject.String,
		"the superseded identity must stay with the superseded registration",
	)
	require.Equal(
		t,
		string(registration.RegistrationTypeSocial),
		states[0].RegistrationType,
	)

	require.Equal(
		t,
		manualID,
		uuid.UUID(states[1].VerificationID.Bytes),
		"the manual verification request must belong to the manual registration",
	)
	require.Equal(
		t,
		string(registration.RegistrationTypeManual),
		states[1].RegistrationType,
	)
}
