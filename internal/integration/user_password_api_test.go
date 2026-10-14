package integration_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/user"
	userdb "github.com/thoriqr/stash-it-backend/internal/api/user/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
)

// --- create branch ---------------------------------------------------------

// An account holding no credential can set one with new_password alone, and the
// stored value is a hash rather than the password.
func TestSetPassword_CreatesTheFirstCredentialWithoutACurrentPassword(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session := seedUserWithoutCredential(t, ctx, "create@example.com", "Create")

	status, body := setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"new_password": "brand-new-password",
	})
	require.Equal(t, http.StatusOK, status)
	require.Equal(
		t,
		"password created successfully",
		decodeObject(t, strings.NewReader(string(body)))["message"],
	)

	stored, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)

	require.NotContains(t, stored, "brand-new-password")
	require.Contains(t, stored, "argon2id")

	// The stored hash is a real credential rather than a well-formed one: it
	// verifies the new password and refuses the wrong one. Signing in through the
	// real endpoint is the external proof of that.
	verification, err := testutil.NewPasswordHasher().Verify(ctx, "brand-new-password", stored)
	require.NoError(t, err)
	require.True(t, verification.Match)

	wrong, err := testutil.NewPasswordHasher().Verify(ctx, "not-the-password", stored)
	require.NoError(t, err)
	require.False(t, wrong.Match)
}

// Signing in with the new password works end to end, and signing in with no
// password did not work before it. This is the proof that the row written is a
// credential rather than something of the right shape.
func TestSetPassword_CreatedCredentialSignsInThroughTheRealLoginEndpoint(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	const emailAddress = "create-then-login@example.com"

	userID, session := seedUserWithoutCredential(t, ctx, emailAddress, "Create Then Login")

	// Before: no credential, so manual login cannot succeed.
	status, _ := loginManually(t, emailAddress, "brand-new-password")
	require.Equal(t, http.StatusUnauthorized, status)

	status, _ = setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"new_password": "brand-new-password",
	})
	require.Equal(t, http.StatusOK, status)

	status, _ = loginManually(t, emailAddress, "brand-new-password")
	require.Equal(t, http.StatusOK, status)

	count, err := db.CountPasswordCredentialsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}

// A supplied current_password for an account holding none is refused rather than
// ignored. Ignoring it would report success for a secret that was never checked.
func TestSetPassword_CreateRejectsASuppliedCurrentPassword(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session := seedUserWithoutCredential(t, ctx, "unexpected@example.com", "Unexpected")

	status, body := setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"current_password": "something-i-made-up",
		"new_password":     "brand-new-password",
	})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(
		t,
		"UNEXPECTED_CURRENT_PASSWORD",
		decodeUserError(t, body).Error.Code,
	)

	// Nothing was written.
	count, err := db.CountPasswordCredentialsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(0), count)
}

// Creating a first password revokes nothing. Nothing was compromised: there was
// no credential to compromise, and an account signed in on a phone and a laptop
// should not be punished for adding one.
func TestSetPassword_CreateRevokesNoSessions(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session := seedUserWithoutCredential(t, ctx, "no-revoke@example.com", "No Revoke")

	// Three other sessions alongside the caller's.
	for range 3 {
		_, err := db.CreateSessionForUser(ctx, userID)
		require.NoError(t, err)
	}

	activeBefore, err := db.CountActiveSessionsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(4), activeBefore)

	status, _ := setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"new_password": "brand-new-password",
	})
	require.Equal(t, http.StatusOK, status)

	activeAfter, err := db.CountActiveSessionsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(4), activeAfter, "no session may be revoked on create")
}

// Two concurrent creators produce exactly one credential and one success. The
// loser is refused rather than overwriting the winner, which is the whole reason
// this branch uses a plain INSERT instead of the password-reset upsert.
//
// The participant count is deliberately far smaller than the sixty-four the
// registration-creation race uses, and the difference is the point. That race has
// to catch a window between a locking SELECT returning no rows and the INSERT —
// microseconds. This one has a window between the credential read and the INSERT
// that is *one full Argon2id derivation* wide, which is hundreds of milliseconds
// and orders of magnitude easier to enter. Four participants contend reliably in
// it while costing four derivations; sixty-four would cost sixty-four and time
// the test client out, turning a status code under test into a transport error.
func TestSetPassword_ConcurrentCreatorsProduceExactlyOneCredential(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session := seedUserWithoutCredential(
		t, ctx, "create-race@example.com", "Create Race",
	)

	const participants = 4

	passwords := make([]string, participants)
	statuses := make([]int, participants)
	bodies := make([][]byte, participants)

	var wg sync.WaitGroup

	wg.Add(participants)

	for index := range participants {
		go func() {
			defer wg.Done()

			passwords[index] = "concurrent-password-" + strings.Repeat(
				string(rune('a'+index%26)), index+8,
			)
			statuses[index], bodies[index] = setPassword(
				t,
				newSessionAccessToken(t, userID, session),
				map[string]string{"new_password": passwords[index]},
			)
		}()
	}

	wg.Wait()

	var succeeded, conflicted int
	winnerIndex := -1

	for index, status := range statuses {
		switch status {
		case http.StatusOK:
			succeeded++
			winnerIndex = index
		case http.StatusConflict:
			conflicted++
			require.Equal(
				t,
				"PASSWORD_CREDENTIAL_ALREADY_EXISTS",
				decodeUserError(t, bodies[index]).Error.Code,
			)
		default:
			t.Fatalf(
				"participant %d answered %d: %s",
				index,
				status,
				string(bodies[index]),
			)
		}
	}

	require.Equal(t, participants, succeeded+conflicted)
	require.Equal(t, 1, succeeded, "exactly one creator may succeed")
	require.Equal(t, participants-1, conflicted)
	require.NotEqual(t, -1, winnerIndex)

	// One credential, not one per request.
	count, err := db.CountPasswordCredentialsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	// And the row holds the winner's password. A loser's password would verify
	// here, which is exactly the silent overwrite this branch refuses to permit.
	stored, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)

	winner, err := testutil.NewPasswordHasher().Verify(ctx, passwords[winnerIndex], stored)
	require.NoError(t, err)
	require.True(t, winner.Match, "the stored hash must be the winner's password")

	for index, password := range passwords {
		if index == winnerIndex {
			continue
		}

		loser, err := testutil.NewPasswordHasher().Verify(ctx, password, stored)
		require.NoError(t, err)
		require.False(t, loser.Match, "a losing request's password must not survive")
	}
}

// --- change branch ---------------------------------------------------------

func TestSetPassword_ChangesTheCredentialAndReportsTheChange(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session, oldHash := seedUserWithCredential(
		t, ctx, "change@example.com", "Change", "old-password-value",
	)

	status, body := setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"current_password": "old-password-value",
		"new_password":     "new-password-value",
	})
	require.Equal(t, http.StatusOK, status)
	require.Equal(
		t,
		"password changed successfully",
		decodeObject(t, strings.NewReader(string(body)))["message"],
	)

	stored, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)
	require.NotEqual(t, oldHash, stored)

	current, err := testutil.NewPasswordHasher().Verify(ctx, "new-password-value", stored)
	require.NoError(t, err)
	require.True(t, current.Match)

	previous, err := testutil.NewPasswordHasher().Verify(ctx, "old-password-value", stored)
	require.NoError(t, err)
	require.False(t, previous.Match)

	count, err := db.CountPasswordCredentialsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}

// A change is visible through the real login endpoint: the old password stops
// working and the new one starts.
func TestSetPassword_ChangedCredentialIsVisibleThroughTheLoginEndpoint(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	const emailAddress = "change-login@example.com"

	userID, session, _ := seedUserWithCredential(
		t, ctx, emailAddress, "Change Login", "old-password-value",
	)

	status, _ := loginManually(t, emailAddress, "old-password-value")
	require.Equal(t, http.StatusOK, status)

	status, _ = setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"current_password": "old-password-value",
		"new_password":     "new-password-value",
	})
	require.Equal(t, http.StatusOK, status)

	status, _ = loginManually(t, emailAddress, "old-password-value")
	require.Equal(t, http.StatusUnauthorized, status)

	status, _ = loginManually(t, emailAddress, "new-password-value")
	require.Equal(t, http.StatusOK, status)
}

// A wrong current password is refused and changes nothing: not the credential,
// not the sessions.
func TestSetPassword_WrongCurrentPasswordChangesNothing(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session, storedBefore := seedUserWithCredential(
		t, ctx, "wrong-current@example.com", "Wrong Current", "correct-password-here",
	)

	_, err := db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	activeBefore, err := db.CountActiveSessionsForUser(ctx, userID)
	require.NoError(t, err)

	status, body := setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"current_password": "not-the-password",
		"new_password":     "attacker-chosen-password",
	})
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "CURRENT_PASSWORD_INVALID", decodeUserError(t, body).Error.Code)

	storedAfter, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, storedBefore, storedAfter, "the stored hash must be untouched")

	activeAfter, err := db.CountActiveSessionsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, activeBefore, activeAfter, "no session may be revoked")

	// The original password still works, which is the external proof that the
	// credential was not replaced.
	status, _ = loginManually(t, "wrong-current@example.com", "correct-password-here")
	require.Equal(t, http.StatusOK, status)
}

func TestSetPassword_ChangeRequiresTheCurrentPassword(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session, storedBefore := seedUserWithCredential(
		t, ctx, "missing-current@example.com", "Missing Current", "correct-password-here",
	)

	status, body := setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"new_password": "brand-new-password",
	})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(
		t,
		"CURRENT_PASSWORD_REQUIRED",
		decodeUserError(t, body).Error.Code,
	)

	storedAfter, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, storedBefore, storedAfter)
}

// --- session revocation ----------------------------------------------------

// Changing a password evicts every other active session and spares the caller's
// own. The caller's session is identified by the verified token's sid claim, so
// it is the one row that must survive.
func TestSetPassword_ChangeRevokesOtherSessionsAndPreservesTheCurrent(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session, _ := seedUserWithCredential(
		t, ctx, "revoke@example.com", "Revoke", "old-password-value",
	)

	// Three more sessions, and one that was already revoked so the count can
	// distinguish "was matched and revoked" from "was already gone".
	otherSessions := make([]uuid.UUID, 0, 3)

	for range 3 {
		other, err := db.CreateSessionForUser(ctx, userID)
		require.NoError(t, err)

		otherSessions = append(otherSessions, other)
	}

	alreadyRevoked, err := db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	require.NoError(t, db.RevokeSession(ctx, alreadyRevoked))

	activeBefore, err := db.CountActiveSessionsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(4), activeBefore)

	status, _ := setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"current_password": "old-password-value",
		"new_password":     "new-password-value",
	})
	require.Equal(t, http.StatusOK, status)

	// Exactly the caller's session survives.
	activeAfter, err := db.CountActiveSessionsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(1), activeAfter)

	currentRevoked, err := db.IsSessionRevoked(ctx, session)
	require.NoError(t, err)
	require.False(t, currentRevoked, "the caller's own session must be preserved")

	for _, other := range otherSessions {
		wasRevoked, err := db.IsSessionRevoked(ctx, other)
		require.NoError(t, err)
		require.True(t, wasRevoked, "every other session must be revoked")
	}
}

// The caller's session survives, so its own access token keeps working. This is
// the stateless design: revocation writes sessions.revoked_at and nothing ever
// consults it when a token is verified.
func TestSetPassword_TheCallersAccessTokenStillWorksAfterAChange(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	userID, session, _ := seedUserWithCredential(
		t, ctx, "still-valid@example.com", "Still Valid", "old-password-value",
	)

	token := newSessionAccessToken(t, userID, session)

	status, _ := setPassword(t, token, map[string]string{
		"current_password": "old-password-value",
		"new_password":     "new-password-value",
	})
	require.Equal(t, http.StatusOK, status)

	status, _ = getProfile(t, token, "")
	require.Equal(t, http.StatusOK, status, "the caller's own token must survive")
}

// The conditional update is the whole concurrency rule for a password change,
// and the window it guards lives *inside* a request: between the hash it read
// and the UPDATE it issues. That window cannot be opened from outside the
// service — any write made before the request begins is the hash the request
// will read, and so is the one it will match.
//
// So the guard is driven directly against the repository, which is where it
// lives. Both calls carry the same previousPasswordHash, as two requests that
// verified against the same row would. The second must match no row, and because
// the guard sits before the revocation inside the same transaction, it must
// revoke nothing.
func TestSetPassword_AConditionalUpdateOnAStaleHashWritesAndRevokesNothing(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session, storedHash := seedUserWithCredential(
		t, ctx, "stale-hash@example.com", "Stale Hash", "old-password-value",
	)

	other, err := db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	repository := user.NewRepository(userdb.New(testPool), testPool)

	// The first call succeeds and revokes the other session, exactly as the
	// endpoint's change branch does.
	require.NoError(
		t,
		repository.SetPasswordAndRevokeOtherSessions(
			ctx,
			userID,
			session,
			storedHash,
			"first-replacement-hash",
		),
	)

	storedAfterFirst, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "first-replacement-hash", storedAfterFirst)

	activeAfterFirst, err := db.CountActiveSessionsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(1), activeAfterFirst, "the first change revokes the others")

	otherRevoked, err := db.IsSessionRevoked(ctx, other)
	require.NoError(t, err)
	require.True(t, otherRevoked)

	// A second session, and a second call carrying the hash that is no longer
	// stored. This is the lost update.
	late, err := db.CreateSessionForUser(ctx, userID)
	require.NoError(t, err)

	err = repository.SetPasswordAndRevokeOtherSessions(
		ctx,
		userID,
		session,
		storedHash,
		"second-replacement-hash",
	)
	require.Equal(
		t,
		user.CodePasswordAlreadyChanged,
		apperror.FromError(err).Code,
	)
	require.Equal(t, 409, apperror.FromError(err).Status)

	// Nothing was written and nothing was revoked.
	storedAfterSecond, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, storedAfterFirst, storedAfterSecond)

	lateRevoked, err := db.IsSessionRevoked(ctx, late)
	require.NoError(t, err)
	require.False(t, lateRevoked, "a refused change must revoke nothing")
}

// Two concurrent changes from the same valid credential produce exactly one
// success. The loser is told its basis expired rather than silently overwriting
// the winner, which is what the conditional update is for.
func TestSetPassword_ConcurrentChangesProduceExactlyOneSuccess(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session, _ := seedUserWithCredential(
		t, ctx, "change-race@example.com", "Change Race", "old-password-value",
	)

	const participants = 2

	passwords := []string{
		"first-concurrent-password",
		"second-concurrent-password",
	}

	statuses := make([]int, participants)
	bodies := make([][]byte, participants)

	var wg sync.WaitGroup

	wg.Add(participants)

	for index := range participants {
		go func() {
			defer wg.Done()

			statuses[index], bodies[index] = setPassword(
				t,
				newSessionAccessToken(t, userID, session),
				map[string]string{
					"current_password": "old-password-value",
					"new_password":     passwords[index],
				},
			)
		}()
	}

	wg.Wait()

	var succeeded, conflicted int
	winnerIndex := -1

	for index, status := range statuses {
		switch status {
		case http.StatusOK:
			succeeded++
			winnerIndex = index
		case http.StatusConflict:
			conflicted++
			require.Equal(
				t,
				"PASSWORD_ALREADY_CHANGED",
				decodeUserError(t, bodies[index]).Error.Code,
			)
		default:
			t.Fatalf(
				"participant %d answered %d: %s",
				index,
				status,
				string(bodies[index]),
			)
		}
	}

	require.Equal(t, 1, succeeded, "exactly one change may succeed")
	require.Equal(t, 1, conflicted)

	// The stored hash is one of the two submitted and never a mixture.
	stored, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)

	matches := 0

	for _, password := range passwords {
		verification, err := testutil.NewPasswordHasher().Verify(ctx, password, stored)
		require.NoError(t, err)

		if verification.Match {
			matches++
		}
	}

	require.Equal(t, 1, matches, "exactly one of the submitted passwords must be stored")
	require.Equal(t, passwords[winnerIndex], mustMatchPassword(t, ctx, stored, passwords))

	count, err := db.CountPasswordCredentialsForUser(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}

func mustMatchPassword(
	t *testing.T,
	ctx context.Context,
	stored string,
	candidates []string,
) string {
	t.Helper()

	for _, candidate := range candidates {
		verification, err := testutil.NewPasswordHasher().Verify(ctx, candidate, stored)
		require.NoError(t, err)

		if verification.Match {
			return candidate
		}
	}

	t.Fatal("no candidate password verified against the stored hash")

	return ""
}

// --- validation ------------------------------------------------------------

func TestSetPassword_ValidatesTheNewPassword(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	userID, session := seedUserWithoutCredential(t, ctx, "invalid@example.com", "Invalid")

	for _, tc := range []struct {
		name        string
		newPassword string
	}{
		{name: "absent", newPassword: ""},
		{name: "too short", newPassword: "short"},
		{name: "too long", newPassword: nameOfLength(129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]string{}

			if tc.newPassword != "" {
				body["new_password"] = tc.newPassword
			}

			status, responseBody := setPassword(
				t,
				newSessionAccessToken(t, userID, session),
				body,
			)
			require.Equal(t, http.StatusBadRequest, status)

			decoded := decodeUserError(t, responseBody)
			require.Equal(t, "VALIDATION_ERROR", decoded.Error.Code)
			require.NotEmpty(t, decoded.Error.Fields)
			require.Equal(t, "new_password", decoded.Error.Fields[0].Field)

			count, err := userTestDB().CountPasswordCredentialsForUser(ctx, userID)
			require.NoError(t, err)
			require.Equal(t, int64(0), count)
		})
	}
}

// The bounds match login's LoginManualRequest, so a password login accepts a
// value this endpoint accepts.
func TestSetPassword_AcceptsTheLoginLengthBoundaries(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session := seedUserWithoutCredential(
		t, ctx, "boundaries@example.com", "Boundaries",
	)

	status, _ := setPassword(t, newSessionAccessToken(t, userID, session), map[string]string{
		"new_password": nameOfLength(8),
	})
	require.Equal(t, http.StatusOK, status)

	stored, err := db.GetPasswordCredentialHashForUser(ctx, userID)
	require.NoError(t, err)

	verification, err := testutil.NewPasswordHasher().Verify(
		ctx, nameOfLength(8), stored,
	)
	require.NoError(t, err)
	require.True(t, verification.Match)
}

// A malformed body is refused before anything is read or written.
func TestSetPassword_RejectsAMalformedBody(t *testing.T) {
	status, _ := doUserRequest(
		t,
		http.MethodPut,
		"/users/me/password",
		[]byte(`{"new_password":`),
		bearer(newTestAccessToken(t, uuid.New())),
	)
	require.Equal(t, http.StatusBadRequest, status)
}

// --- helper ----------------------------------------------------------------

// loginManually drives POST /auth/login so a test can prove a password is really
// a credential rather than something of the right shape.
func loginManually(t *testing.T, email string, password string) (int, []byte) {
	t.Helper()

	body := `{"email":"` + email + `","password":"` + password + `"}`

	return doUserRequest(
		t,
		http.MethodPost,
		"/auth/login",
		[]byte(body),
		"",
	)
}
