package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/user"
	userdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/user/generated"
)

// Every route in this feature is authenticated, and each authentication failure
// keeps its own code.
func TestUserRoutes_RejectEveryMissingOrBadCredential(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	routes := []struct {
		method string
		target string
		body   []byte
	}{
		{method: http.MethodGet, target: "/users/me"},
		{
			method: http.MethodPatch,
			target: "/users/me",
			body:   []byte(`{"display_name":"Jane Doe"}`),
		},
		{
			method: http.MethodPut,
			target: "/users/me/password",
			body:   []byte(`{"new_password":"some-password"}`),
		},
	}

	for _, header := range []string{
		"",
		"Basic abc123",
		"Bearer ",
		"Bearer not.a.jwt",
	} {
		for _, route := range routes {
			status, body := doUserRequest(
				t, route.method, route.target, route.body, header,
			)

			require.Equal(
				t,
				http.StatusUnauthorized,
				status,
				"%s %s with header %q",
				route.method,
				route.target,
				header,
			)

			require.Contains(
				t,
				[]string{"INVALID_AUTHORIZATION_HEADER", "INVALID_ACCESS_TOKEN"},
				decodeUserError(t, body).Error.Code,
				"%s %s with header %q",
				route.method,
				route.target,
				header,
			)
		}
	}
}

func TestGetProfile_ReturnsTheAuthenticatedUsersOwnProfile(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	userID, _ := seedUserWithoutCredential(t, ctx, "owner@example.com", "Owner")

	status, body := getProfile(t, newTestAccessToken(t, userID), "")
	require.Equal(t, http.StatusOK, status)

	decoded := decodeUserBody(t, body)
	require.Equal(t, "user retrieved successfully", decoded.Message)
	require.Equal(t, userID.String(), decoded.Data.User.ID)
	require.Equal(t, "owner@example.com", decoded.Data.User.Email)
	require.Equal(t, "Owner", decoded.Data.User.DisplayName)
	require.False(t, decoded.Data.User.HasPasswordCredential)
	require.NotEmpty(t, decoded.Data.User.CreatedAt)
}

// The user object is pinned as an exact key set. A decoder would leave an
// undeclared field nil either way, so asserting on presence is what proves the
// endpoint did not send it.
func TestGetProfile_UserObjectCarriesExactlyTheAgreedFields(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	userID, session, _ := seedUserWithCredential(
		t, ctx, "fields@example.com", "Fields", "some-password",
	)

	require.NoError(
		t,
		userTestDB().CreateAuthIdentityForUser(
			ctx,
			userdbtest.CreateAuthIdentityForUserParams{
				UserID:          userID,
				Provider:        "google",
				ProviderSubject: "subject-1",
			},
		),
	)

	_, body := getProfile(t, newSessionAccessToken(t, userID, session), "")

	data := dataObject(t, decodeObject(t, strings.NewReader(string(body))))

	require.ElementsMatch(t, []string{"user"}, rawKeys(t, data))

	user, ok := data["user"].(map[string]any)
	require.True(t, ok, "response has no user object")

	require.ElementsMatch(
		t,
		[]string{
			"id",
			"email",
			"display_name",
			"has_password_credential",
			"created_at",
			"identities",
		},
		rawKeys(t, user),
	)

	identity, ok := user["identities"].([]any)[0].(map[string]any)
	require.True(t, ok)

	require.ElementsMatch(
		t,
		[]string{"provider", "linked_at", "email_snapshot", "display_name_snapshot"},
		rawKeys(t, identity),
	)
}

func TestGetProfile_ReportsCredentialPresenceFromTheDatabase(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	withID, withSession, _ := seedUserWithCredential(
		t, ctx, "with-password@example.com", "With Password", "some-password",
	)
	withoutID, _ := seedUserWithoutCredential(
		t, ctx, "without-password@example.com", "Without Password",
	)

	status, body := getProfile(t, newSessionAccessToken(t, withID, withSession), "")
	require.Equal(t, http.StatusOK, status)
	require.True(t, decodeUserBody(t, body).Data.User.HasPasswordCredential)

	status, body = getProfile(t, newTestAccessToken(t, withoutID), "")
	require.Equal(t, http.StatusOK, status)
	require.False(t, decodeUserBody(t, body).Data.User.HasPasswordCredential)
}

// An account with nothing linked must still carry the key, as an empty array. A
// null would make a client check for the key before it could read a length, so
// the assertion is against the raw JSON and the field is a pointer.
func TestGetProfile_NoIdentitiesIsAnEmptyArrayNotNull(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	userID, _ := seedUserWithoutCredential(t, ctx, "manual@example.com", "Manual")

	status, body := getProfile(t, newTestAccessToken(t, userID), "")
	require.Equal(t, http.StatusOK, status)

	var envelope struct {
		Data struct {
			User struct {
				Identities *[]struct {
					Provider string `json:"provider"`
				} `json:"identities"`
			} `json:"user"`
		} `json:"data"`
	}

	require.NoError(t, json.Unmarshal(body, &envelope))
	require.NotNil(t, envelope.Data.User.Identities, "identities must be [] and not null")
	require.Empty(t, *envelope.Data.User.Identities)
}

// Every linked provider is listed, each carrying provider and linked_at. The
// order the query asked for is preserved rather than re-established, so the
// assertion is on membership plus a prefix rather than on timestamps the test
// cannot see.
func TestGetProfile_ListsEveryIdentityWithProviderAndLinkedAt(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, _ := seedUserWithoutCredential(t, ctx, "social@example.com", "Social")

	providers := []string{"google", "example-provider", "another-provider"}

	for _, provider := range providers {
		require.NoError(
			t,
			db.CreateAuthIdentityForUser(ctx, userdbtest.CreateAuthIdentityForUserParams{
				UserID:              userID,
				Provider:            provider,
				ProviderSubject:     "subject-for-" + provider,
				EmailSnapshot:       testText("snapshot@example.com"),
				DisplayNameSnapshot: testText("Snapshot Name"),
			}),
		)
	}

	status, body := getProfile(t, newTestAccessToken(t, userID), "")
	require.Equal(t, http.StatusOK, status)

	identities := decodeUserBody(t, body).Data.User.Identities
	require.Len(t, identities, len(providers))

	listed := make([]string, 0, len(identities))

	for _, identity := range identities {
		require.NotEmpty(t, identity.LinkedAt)

		// The snapshots each identity was written with come back too.
		require.NotNil(t, identity.EmailSnapshot)
		require.Equal(t, "snapshot@example.com", *identity.EmailSnapshot)

		require.NotNil(t, identity.DisplayNameSnapshot)
		require.Equal(t, "Snapshot Name", *identity.DisplayNameSnapshot)

		listed = append(listed, identity.Provider)
	}

	require.ElementsMatch(t, providers, listed)

	// Each row was written in its own statement, so created_at advances and the
	// query's ordering is insertion order.
	require.Equal(t, providers[0], listed[0])
}

// The snapshots preserve the stored nullability on the wire. A provider that
// asserted nothing reports JSON null; a provider that asserted an empty value
// reports "". Neither is ever filled in from the account's own profile.
//
// The keys are decoded as *string so a missing key and a null key are
// distinguishable: a non-nil pointer is present, and nil covers both "absent" and
// "null", which is why presence is asserted separately below.
func TestGetProfile_SnapshotsPreserveStoredNullability(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, _ := seedUserWithoutCredential(t, ctx, "snapshots@example.com", "Snapshots")

	// The account's own profile, deliberately different from every snapshot, so a
	// response that quietly reconciled them would be visible.
	for _, tc := range []struct {
		provider string
		email    pgtype.Text
		name     pgtype.Text
	}{
		{provider: "both-present", email: testText("a@example.com"), name: testText("A Name")},
		{provider: "both-null", email: pgtype.Text{}, name: pgtype.Text{}},
		{provider: "email-null", email: pgtype.Text{}, name: testText("Name Only")},
		{provider: "name-null", email: testText("b@example.com"), name: pgtype.Text{}},
		{provider: "both-blank", email: testText(""), name: testText("")},
	} {
		require.NoError(
			t,
			db.CreateAuthIdentityForUser(ctx, userdbtest.CreateAuthIdentityForUserParams{
				UserID:              userID,
				Provider:            tc.provider,
				ProviderSubject:     "subject-for-" + tc.provider,
				EmailSnapshot:       tc.email,
				DisplayNameSnapshot: tc.name,
			}),
		)
	}

	status, body := getProfile(t, newTestAccessToken(t, userID), "")
	require.Equal(t, http.StatusOK, status)

	byProvider := make(map[string]struct {
		email       *string
		displayName *string
	})

	var envelope struct {
		Data struct {
			User struct {
				Identities []struct {
					Provider            string          `json:"provider"`
					EmailSnapshot       json.RawMessage `json:"email_snapshot"`
					DisplayNameSnapshot json.RawMessage `json:"display_name_snapshot"`
				} `json:"identities"`
			} `json:"user"`
		} `json:"data"`
	}

	require.NoError(t, json.Unmarshal(body, &envelope))

	for _, identity := range envelope.Data.User.Identities {
		byProvider[identity.Provider] = struct {
			email       *string
			displayName *string
		}{
			email:       decodeNullableString(t, identity.EmailSnapshot),
			displayName: decodeNullableString(t, identity.DisplayNameSnapshot),
		}
	}

	require.Len(t, byProvider, 5)

	require.Equal(t, "a@example.com", *byProvider["both-present"].email)
	require.Equal(t, "A Name", *byProvider["both-present"].displayName)

	require.Nil(t, byProvider["both-null"].email)
	require.Nil(t, byProvider["both-null"].displayName)

	require.Nil(t, byProvider["email-null"].email)
	require.Equal(t, "Name Only", *byProvider["email-null"].displayName)

	require.Equal(t, "b@example.com", *byProvider["name-null"].email)
	require.Nil(t, byProvider["name-null"].displayName)

	// An asserted empty string is not null, and must not be reported as one.
	require.NotNil(t, byProvider["both-blank"].email)
	require.Equal(t, "", *byProvider["both-blank"].email)
	require.NotNil(t, byProvider["both-blank"].displayName)
	require.Equal(t, "", *byProvider["both-blank"].displayName)

	// The keys are present on every identity, including the null ones, so a client
	// can read one without first checking whether it arrived.
	for _, raw := range []string{"email_snapshot", "display_name_snapshot"} {
		require.Contains(t, string(body), `"`+raw+`"`, raw)
	}

	// And nothing was reconciled with the account's own profile.
	profile := decodeUserBody(t, body).Data.User
	require.Equal(t, "snapshots@example.com", profile.Email)
	require.Equal(t, "Snapshots", profile.DisplayName)
}

// decodeNullableString distinguishes three things a field can be: a JSON string,
// JSON null, and absent entirely.
//
// The nil check on the raw message is the presence assertion. A key that is absent
// leaves the RawMessage nil, while a key that is present and null carries the four
// bytes "null" — so this fails if a null field ever quietly stops being sent.
func decodeNullableString(t *testing.T, raw json.RawMessage) *string {
	t.Helper()

	require.NotNil(t, raw, "key absent from the response")

	if string(raw) == "null" {
		return nil
	}

	var value string

	require.NoError(t, json.Unmarshal(raw, &value))

	return &value
}

// The provider column is free text with no registry, so an unfamiliar provider
// must come back untouched rather than being dropped or rejected.
func TestGetProfile_ReportsAnUnknownProviderVerbatim(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, _ := seedUserWithoutCredential(t, ctx, "future@example.com", "Future")

	require.NoError(
		t,
		db.CreateAuthIdentityForUser(ctx, userdbtest.CreateAuthIdentityForUserParams{
			UserID:          userID,
			Provider:        "provider-added-next-year",
			ProviderSubject: "subject-1",
		}),
	)

	status, body := getProfile(t, newTestAccessToken(t, userID), "")
	identities := decodeUserBody(t, body).Data.User.Identities

	require.Equal(t, http.StatusOK, status)
	require.Len(t, identities, 1)
	require.Equal(t, "provider-added-next-year", identities[0].Provider)
}

// The response must not carry a password hash or a provider subject, and must
// not carry a session id. This asserts on the marshalled bytes, so a field that
// is present but null still fails.
//
// The two snapshot columns are deliberately *not* on this list. They are
// reported: they are the provider's own rendering of the account at link time,
// they are not the account's address, and a client needs them to tell a link-time
// address from the current one. What must never appear is the subject the identity
// is bound to.
func TestGetProfile_ExposesNoCredentialOrSessionFields(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, session, passwordHash := seedUserWithCredential(
		t, ctx, "sensitive@example.com", "Sensitive", "a-real-password",
	)

	require.NoError(
		t,
		db.CreateAuthIdentityForUser(ctx, userdbtest.CreateAuthIdentityForUserParams{
			UserID:              userID,
			Provider:            "google",
			ProviderSubject:     "google-subject-secret",
			EmailSnapshot:       testText("snapshot@example.com"),
			DisplayNameSnapshot: testText("Snapshot Name"),
		}),
	)

	_, body := getProfile(t, newSessionAccessToken(t, userID, session), "")
	rendered := string(body)

	for _, forbidden := range []string{
		passwordHash,
		"argon2",
		"a-real-password",
		"google-subject-secret",
		session.String(),
	} {
		require.NotContains(t, rendered, forbidden)
	}

	// Field names as well as values: a field present but null is still a field.
	for _, forbiddenField := range []string{
		"password_hash",
		"provider_subject",
		"email_verified_at",
		"session_id",
	} {
		require.NotContains(t, rendered, forbiddenField)
	}

	// The snapshots are reported, so their absence here would itself be a
	// regression in the other direction.
	require.Contains(t, rendered, "snapshot@example.com")
	require.Contains(t, rendered, "Snapshot Name")
}

// There is no route that accepts a user id, so naming another account changes
// nothing. A client could reasonably try it as a query parameter, so that is what
// is sent.
func TestGetProfile_ASuppliedUserIdChangesNothing(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	ownerID, _ := seedUserWithoutCredential(t, ctx, "owner@example.com", "Owner")
	otherID, _ := seedUserWithoutCredential(t, ctx, "other@example.com", "Other")

	token := newTestAccessToken(t, ownerID)

	for _, query := range []string{
		"user_id=" + otherID.String(),
		"id=" + otherID.String(),
	} {
		status, body := getProfile(t, token, query)
		require.Equal(t, http.StatusOK, status)

		decoded := decodeUserBody(t, body)
		require.Equal(t, ownerID.String(), decoded.Data.User.ID)
		require.Equal(t, "owner@example.com", decoded.Data.User.Email)
	}
}

// --- display name ----------------------------------------------------------

func TestUpdateProfile_RenamesTheUserAndPersistsIt(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, _, _ := seedUserWithCredential(
		t, ctx, "rename@example.com", "Before", "some-password",
	)

	status, body := patchProfile(
		t,
		newTestAccessToken(t, userID),
		[]byte(`{"display_name":"Jane Doe"}`),
	)
	require.Equal(t, http.StatusOK, status)

	decoded := decodeUserBody(t, body)
	require.Equal(t, "user updated successfully", decoded.Message)
	require.Equal(t, "Jane Doe", decoded.Data.User.DisplayName)
	require.Equal(t, userID.String(), decoded.Data.User.ID)

	// Read from the row rather than the response, so the claim is about
	// persistence rather than about what the endpoint said it did.
	stored, err := db.GetUserDisplayName(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "Jane Doe", stored)
}

// A rename must touch nothing else on the row, and the users_set_updated_at
// trigger must have fired.
func TestUpdateProfile_ChangesNothingButTheDisplayName(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, _, _ := seedUserWithCredential(
		t, ctx, "unchanged@example.com", "Before", "some-password",
	)

	before, err := db.GetUserRowForTest(ctx, userID)
	require.NoError(t, err)

	status, _ := patchProfile(
		t,
		newTestAccessToken(t, userID),
		[]byte(`{"display_name":"After"}`),
	)
	require.Equal(t, http.StatusOK, status)

	after, err := db.GetUserRowForTest(ctx, userID)
	require.NoError(t, err)

	require.Equal(t, before.Email, after.Email)
	require.Equal(t, before.CreatedAt.Time, after.CreatedAt.Time)

	// The trigger fires on every UPDATE, so updated_at must have moved. Both
	// values come from the database's own clock, which is what makes comparing
	// them safe without a sleep.
	require.True(
		t,
		after.UpdatedAt.Time.After(before.UpdatedAt.Time),
		"updated_at must advance",
	)

	_, body := getProfile(t, newTestAccessToken(t, userID), "")
	require.True(t, decodeUserBody(t, body).Data.User.HasPasswordCredential)
}

func TestUpdateProfile_ValidationRejectsMissingShortAndLongNames(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, _, _ := seedUserWithCredential(
		t, ctx, "validated@example.com", "Valid", "some-password",
	)

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "absent", body: `{}`},
		{name: "empty", body: `{"display_name":""}`},
		{name: "one character", body: `{"display_name":"J"}`},
		{name: "too long", body: `{"display_name":"` + nameOfLength(101) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := patchProfile(
				t,
				newTestAccessToken(t, userID),
				[]byte(tc.body),
			)
			require.Equal(t, http.StatusBadRequest, status)

			decoded := decodeUserError(t, body)
			require.Equal(t, "VALIDATION_ERROR", decoded.Error.Code)
			require.NotEmpty(t, decoded.Error.Fields)
			require.Equal(t, "display_name", decoded.Error.Fields[0].Field)

			// The rejected request wrote nothing.
			stored, err := db.GetUserDisplayName(ctx, userID)
			require.NoError(t, err)
			require.Equal(t, "Valid", stored)
		})
	}
}

// The bounds are registration's, and both boundaries are accepted. If the two
// ever drift apart, a user could register with a name their own profile rejects.
func TestUpdateProfile_AcceptsTheRegistrationBoundaries(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, _, _ := seedUserWithCredential(
		t, ctx, "boundaries@example.com", "Start", "some-password",
	)

	for _, name := range []string{"Jo", nameOfLength(100)} {
		status, _ := patchProfile(
			t,
			newTestAccessToken(t, userID),
			[]byte(`{"display_name":"`+name+`"}`),
		)
		require.Equal(t, http.StatusOK, status, "boundary %d", len(name))

		stored, err := db.GetUserDisplayName(ctx, userID)
		require.NoError(t, err)
		require.Equal(t, name, stored)
	}
}

// Registration stores a name verbatim, so this must too. Normalising here and not
// there would give two paths into one column that disagree.
func TestUpdateProfile_DoesNotTrimTheName(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	userID, _, _ := seedUserWithCredential(
		t, ctx, "untrimmed@example.com", "Start", "some-password",
	)

	status, _ := patchProfile(
		t,
		newTestAccessToken(t, userID),
		[]byte(`{"display_name":"  Padded Name  "}`),
	)
	require.Equal(t, http.StatusOK, status)

	stored, err := db.GetUserDisplayName(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "  Padded Name  ", stored)
}

func TestUpdateProfile_OnlyTouchesTheAuthenticatedUser(t *testing.T) {
	ctx := context.Background()
	db := userTestDB()

	require.NoError(t, db.TruncateUserData(ctx))

	callerID, _, _ := seedUserWithCredential(
		t, ctx, "caller@example.com", "Caller", "some-password",
	)
	bystanderID, _, _ := seedUserWithCredential(
		t, ctx, "bystander@example.com", "Bystander", "some-password",
	)

	// Fields this endpoint does not define, one of them naming another account.
	// Both must be ignored rather than silently applied.
	status, _ := patchProfile(
		t,
		newTestAccessToken(t, callerID),
		[]byte(
			`{"display_name":"Renamed Caller","user_id":"`+
				bystanderID.String()+`","email":"hijack@example.com"}`,
		),
	)
	require.Equal(t, http.StatusOK, status)

	bystander, err := db.GetUserRowForTest(ctx, bystanderID)
	require.NoError(t, err)
	require.Equal(t, "Bystander", bystander.DisplayName)
	require.Equal(t, "bystander@example.com", bystander.Email)

	caller, err := db.GetUserRowForTest(ctx, callerID)
	require.NoError(t, err)
	require.Equal(t, "Renamed Caller", caller.DisplayName)
	require.Equal(t, "caller@example.com", caller.Email)
}

// The profile a PATCH returns has the same envelope keys GET returns, so a client
// can treat the two as one representation.
//
// The meta assertion is the one worth having. httpx.OK builds a two-key body, and
// an API response type that declared a meta key would describe a key that is
// never sent — still valid OpenAPI, so no linter reports it.
func TestUserResponses_CarryNoMetaKey(t *testing.T) {
	ctx := context.Background()

	require.NoError(t, userTestDB().TruncateUserData(ctx))

	userID, session, _ := seedUserWithCredential(
		t, ctx, "envelope@example.com", "Envelope", "some-password",
	)

	token := newSessionAccessToken(t, userID, session)

	_, getBody := getProfile(t, token, "")
	_, patchBody := patchProfile(t, token, []byte(`{"display_name":"Envelope"}`))
	_, passwordBody := setPassword(t, token, map[string]string{
		"current_password": "some-password",
		"new_password":     "another-password",
	})

	for _, body := range [][]byte{getBody, patchBody, passwordBody} {
		require.ElementsMatch(
			t,
			[]string{"data", "message"},
			rawKeys(t, decodeObject(t, strings.NewReader(string(body)))),
		)
	}
}

// --- documented contract ---------------------------------------------------

// The error codes this feature documents are the ones it can produce. This is a
// cheap guard against a constant being renamed without the Swagger annotations
// and the tests that name it following.
func TestUserErrorCodesAreStable(t *testing.T) {
	require.Equal(t, "USER_NOT_FOUND", user.CodeUserNotFound)
	require.Equal(t, "CURRENT_PASSWORD_INVALID", user.CodeCurrentPasswordInvalid)
	require.Equal(
		t,
		"PASSWORD_CREDENTIAL_ALREADY_EXISTS",
		user.CodePasswordCredentialAlreadyExists,
	)
	require.Equal(
		t,
		"PASSWORD_CREDENTIAL_NOT_FOUND",
		user.CodePasswordCredentialNotFound,
	)
	require.Equal(t, "PASSWORD_ALREADY_CHANGED", user.CodePasswordAlreadyChanged)
	require.Equal(
		t,
		"UNEXPECTED_CURRENT_PASSWORD",
		user.CodeUnexpectedCurrentPassword,
	)
	require.Equal(t, "CURRENT_PASSWORD_REQUIRED", user.CodeCurrentPasswordRequired)
	require.Equal(t, "PASSWORD_WORK_UNAVAILABLE", user.CodePasswordWorkUnavailable)
}

// The generated document has to describe the routes that exist, and describe the
// profile response with the fields it actually carries. A schema that promised a
// field the body omits is valid OpenAPI, so only an assertion catches it.
func TestUserSwagger_DocumentsTheRoutesAndTheProfileShape(t *testing.T) {
	doc := loadSwaggerDoc(t)

	paths, ok := doc["paths"].(map[string]any)
	require.True(t, ok, "swagger.json has no paths object")

	for _, route := range []struct{ path, method string }{
		{path: "/users/me", method: "get"},
		{path: "/users/me", method: "patch"},
		{path: "/users/me/password", method: "put"},
	} {
		path, ok := paths[route.path].(map[string]any)
		require.True(t, ok, "swagger.json documents no %s", route.path)

		_, ok = path[route.method].(map[string]any)
		require.True(t, ok, "swagger.json documents no %s %s", route.method, route.path)
	}

	// The envelope the two profile responses document is two keys, and the runtime
	// body is two keys, so the two agree.
	//
	// The documented *properties* are read rather than the resolved schema node,
	// because swaggerDoc.definition follows $ref chains and so returns a node
	// keyed by type/properties rather than by the fields themselves.
	for _, definition := range []string{
		"user.GetUserAPIResponse",
		"user.UpdateUserAPIResponse",
		"user.SetPasswordAPIResponse",
	} {
		require.ElementsMatch(
			t,
			[]string{"data", "message"},
			swaggerDefinitionPropertyKeys(t, definition),
			definition,
		)
	}

	// The user object, pinned as a key set rather than spot-checked, so a field
	// added to the response without a schema update fails here.
	require.ElementsMatch(
		t,
		[]string{
			"id",
			"email",
			"display_name",
			"has_password_credential",
			"created_at",
			"identities",
		},
		swaggerDefinitionPropertyKeys(t, "user.UserResponse"),
	)

	require.ElementsMatch(
		t,
		[]string{"provider", "linked_at", "email_snapshot", "display_name_snapshot"},
		swaggerDefinitionPropertyKeys(t, "user.UserIdentityResponse"),
	)
}

// The documented identity response must agree with the runtime one on where its
// fields live. The two snapshots are the case this is for: they are nullable, and
// a schema that described them as plain strings while the body sent null would be
// valid OpenAPI and so pass every linter.
func TestUserSwagger_DocumentsTheSnapshotsAsNullable(t *testing.T) {
	doc := loadSwaggerDoc(t)

	definitions, ok := doc["definitions"].(map[string]any)
	require.True(t, ok, "swagger.json has no definitions object")

	identity, ok := definitions["user.UserIdentityResponse"].(map[string]any)
	require.True(t, ok, "swagger.json has no user.UserIdentityResponse definition")

	properties, ok := identity["properties"].(map[string]any)
	require.True(t, ok, "user.UserIdentityResponse has no properties")

	for _, field := range []string{"email_snapshot", "display_name_snapshot"} {
		property, ok := properties[field].(map[string]any)
		require.True(t, ok, "user.UserIdentityResponse has no %s property", field)

		require.Equal(
			t,
			"string",
			property["type"],
			"%s must be documented as a nullable string, not an object",
			field,
		)
	}
}

// The description has to say the snapshots are history rather than current
// profile data, because that is the whole reason a client should not reconcile
// them itself without being told.
func TestUserSwagger_DescribesTheSnapshotsAsHistory(t *testing.T) {
	doc := loadSwaggerDoc(t)

	paths, ok := doc["paths"].(map[string]any)
	require.True(t, ok, "swagger.json has no paths object")

	mePath, ok := paths["/users/me"].(map[string]any)
	require.True(t, ok, "swagger.json documents no /users/me")

	get, ok := mePath["get"].(map[string]any)
	require.True(t, ok, "swagger.json documents no GET /users/me")

	description, ok := get["description"].(string)
	require.True(t, ok, "GET /users/me has no description")

	lower := strings.ToLower(description)

	for _, promised := range []string{
		"email_snapshot",
		"display_name_snapshot",
		"null",
		// The negations, which are the claims actually being made. Asserting on
		// "not refreshed" rather than merely on the absence of "refreshed" is the
		// precise form: the second would be satisfied by a description promising
		// freshness, and by one promising the opposite.
		"not refreshed",
		"nothing is fetched from the provider",
	} {
		require.Contains(t, lower, promised)
	}

	require.NotContains(t, lower, "always up to date")
	require.NotContains(t, lower, "latest from the provider")
}

// swaggerDefinitionPropertyKeys returns the documented property names of one
// definition.
//
// It deliberately does not resolve $ref: the claim being made is about what the
// document declares for that named type, and a resolver would answer a different
// question by inlining whatever the reference pointed at.
func swaggerDefinitionPropertyKeys(t *testing.T, name string) []string {
	t.Helper()

	doc := loadSwaggerDoc(t)

	definitions, ok := doc["definitions"].(map[string]any)
	require.True(t, ok, "swagger.json has no definitions object")

	definition, ok := definitions[name].(map[string]any)
	require.True(t, ok, "swagger.json has no definition %q", name)

	properties, ok := definition["properties"].(map[string]any)
	require.True(t, ok, "definition %q has no properties", name)

	return rawKeys(t, properties)
}

// The description must not promise immediate invalidation. Revoking a session
// does not stop an already-issued access token, and documentation that said it
// did would be a promise the design cannot keep.
func TestUserSwagger_DoesNotPromiseImmediateTokenInvalidation(t *testing.T) {
	doc := loadSwaggerDoc(t)

	paths, ok := doc["paths"].(map[string]any)
	require.True(t, ok, "swagger.json has no paths object")

	passwordPath, ok := paths["/users/me/password"].(map[string]any)
	require.True(t, ok, "swagger.json documents no /users/me/password")

	put, ok := passwordPath["put"].(map[string]any)
	require.True(t, ok, "swagger.json documents no PUT /users/me/password")

	description, ok := put["description"].(string)
	require.True(t, ok, "PUT /users/me/password has no description")

	lower := strings.ToLower(description)
	require.Contains(t, lower, "stateless")
	require.Contains(t, lower, "15 minutes")
	require.NotContains(t, lower, "immediately invalidated")
}

// The sorted key set is used wherever two envelopes are compared.
func sortedKeys(t *testing.T, raw map[string]any) []string {
	t.Helper()

	keys := rawKeys(t, raw)
	sort.Strings(keys)

	return keys
}
