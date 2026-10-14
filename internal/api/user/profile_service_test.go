package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	userdb "github.com/thoriqr/stash-it-backend/internal/api/user/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestGetUser_ReturnsTheAuthenticatedUsersProfile(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetUserProfile(ctx, testUserID).
		Return(userdb.GetUserProfileRow{
			ID:                    testUserID,
			Email:                 "owner@example.com",
			DisplayName:           "Owner",
			CreatedAt:             mustTimestamptz("2026-10-01T10:30:00Z"),
			HasPasswordCredential: true,
		}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return([]userdb.ListAuthIdentitiesForUserRow{}, nil)

	profile, err := service.GetUser(ctx, testUserID)
	require.NoError(t, err)
	require.Equal(t, testUserID, profile.ID)
	require.Equal(t, "owner@example.com", profile.Email)
	require.Equal(t, "Owner", profile.DisplayName)
	require.True(t, profile.HasPasswordCredential)
}

// An account with no linked provider is an ordinary account, not an empty
// result. A nil slice here would serialize as null and a client reading
// identities.length would have to check for the key first.
func TestGetUser_NoIdentitiesIsAnEmptySliceNotNil(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetUserProfile(ctx, testUserID).
		Return(userdb.GetUserProfileRow{ID: testUserID}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return(nil, nil)

	profile, err := service.GetUser(ctx, testUserID)
	require.NoError(t, err)
	require.NotNil(t, profile.Identities)
	require.Empty(t, profile.Identities)
}

// The order the query asked for is the order reported. Re-sorting here would be a
// second definition of the rule, and this asserts the service does not have one.
func TestGetUser_PreservesTheOrderTheQueryReturned(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetUserProfile(ctx, testUserID).
		Return(userdb.GetUserProfileRow{ID: testUserID}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return([]userdb.ListAuthIdentitiesForUserRow{
			{
				Provider:            "google",
				LinkedAt:            mustTimestamptz("2026-01-01T00:00:00Z"),
				EmailSnapshot:       testText("first@example.com"),
				DisplayNameSnapshot: testText("First Name"),
			},
			{
				Provider:            "example-provider",
				LinkedAt:            mustTimestamptz("2026-06-01T00:00:00Z"),
				EmailSnapshot:       testText("second@example.com"),
				DisplayNameSnapshot: testText("Second Name"),
			},
		}, nil)

	profile, err := service.GetUser(ctx, testUserID)
	require.NoError(t, err)
	require.Len(t, profile.Identities, 2)
	require.Equal(t, "google", profile.Identities[0].Provider)
	require.Equal(t, "example-provider", profile.Identities[1].Provider)
}

// The snapshots are carried through as pgtype.Text rather than unwrapped, so a
// NULL stays distinguishable from an empty string all the way to the mapper. A
// service that decoded to string here would turn every NULL into "" and the
// response could no longer tell "the provider said nothing" from "the provider
// said nothing at all".
func TestGetUser_PreservesSnapshotNullability(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetUserProfile(ctx, testUserID).
		Return(userdb.GetUserProfileRow{ID: testUserID}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return([]userdb.ListAuthIdentitiesForUserRow{
			{
				// Both present.
				Provider:            "google",
				LinkedAt:            mustTimestamptz("2026-01-01T00:00:00Z"),
				EmailSnapshot:       testText("present@example.com"),
				DisplayNameSnapshot: testText("Present Name"),
			},
			{
				// Both NULL, which is a real state: a provider may assert neither.
				Provider:            "sparse-provider",
				LinkedAt:            mustTimestamptz("2026-02-01T00:00:00Z"),
				EmailSnapshot:       pgtype.Text{},
				DisplayNameSnapshot: pgtype.Text{},
			},
			{
				// Null on one side only.
				Provider:            "half-provider",
				LinkedAt:            mustTimestamptz("2026-03-01T00:00:00Z"),
				EmailSnapshot:       testText("half@example.com"),
				DisplayNameSnapshot: pgtype.Text{},
			},
			{
				// An empty string the provider actually asserted, which is not the
				// same as NULL and must survive as such.
				Provider:            "blank-provider",
				LinkedAt:            mustTimestamptz("2026-04-01T00:00:00Z"),
				EmailSnapshot:       testText(""),
				DisplayNameSnapshot: testText(""),
			},
		}, nil)

	profile, err := service.GetUser(ctx, testUserID)
	require.NoError(t, err)
	require.Len(t, profile.Identities, 4)

	present := profile.Identities[0]
	require.True(t, present.EmailSnapshot.Valid)
	require.Equal(t, "present@example.com", present.EmailSnapshot.String)
	require.True(t, present.DisplayNameSnapshot.Valid)
	require.Equal(t, "Present Name", present.DisplayNameSnapshot.String)

	sparse := profile.Identities[1]
	require.False(t, sparse.EmailSnapshot.Valid)
	require.False(t, sparse.DisplayNameSnapshot.Valid)

	half := profile.Identities[2]
	require.True(t, half.EmailSnapshot.Valid)
	require.Equal(t, "half@example.com", half.EmailSnapshot.String)
	require.False(t, half.DisplayNameSnapshot.Valid)

	blank := profile.Identities[3]
	require.True(t, blank.EmailSnapshot.Valid, "an asserted empty string is not NULL")
	require.Equal(t, "", blank.EmailSnapshot.String)
	require.True(t, blank.DisplayNameSnapshot.Valid)
	require.Equal(t, "", blank.DisplayNameSnapshot.String)
}

// A snapshot must not be silently reconciled with the account's own email or
// display name. They are history, they are allowed to disagree, and the endpoint
// reports both so a client can tell that they do.
func TestGetUser_DoesNotReconcileASnapshotWithTheAccountProfile(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetUserProfile(ctx, testUserID).
		Return(userdb.GetUserProfileRow{
			ID:                    testUserID,
			Email:                 "current@example.com",
			DisplayName:           "Current Name",
			HasPasswordCredential: true,
		}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return([]userdb.ListAuthIdentitiesForUserRow{
			{
				Provider:            "google",
				LinkedAt:            mustTimestamptz("2026-01-01T00:00:00Z"),
				EmailSnapshot:       testText("stale@example.com"),
				DisplayNameSnapshot: testText("Stale Name"),
			},
		}, nil)

	profile, err := service.GetUser(ctx, testUserID)
	require.NoError(t, err)

	require.Equal(t, "current@example.com", profile.Email)
	require.Equal(t, "Current Name", profile.DisplayName)
	require.Equal(t, "stale@example.com", profile.Identities[0].EmailSnapshot.String)
	require.Equal(t, "Stale Name", profile.Identities[0].DisplayNameSnapshot.String)
}

// The provider column is free text with no registry, so an unfamiliar provider
// must survive untouched rather than being dropped or rejected.
func TestGetUser_ReportsAnUnrecognisedProviderVerbatim(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetUserProfile(ctx, testUserID).
		Return(userdb.GetUserProfileRow{ID: testUserID}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return([]userdb.ListAuthIdentitiesForUserRow{
			{Provider: "some-provider-added-next-year", LinkedAt: mustTimestamptz("2026-01-01T00:00:00Z")},
		}, nil)

	profile, err := service.GetUser(ctx, testUserID)
	require.NoError(t, err)
	require.Equal(t, "some-provider-added-next-year", profile.Identities[0].Provider)
}

func TestGetUser_PassesTheRepositoryErrorThrough(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetUserProfile(ctx, testUserID).
		Return(userdb.GetUserProfileRow{}, apperror.NotFoundWith(
			CodeUserNotFound, "user not found", pgx.ErrNoRows,
		))

	profile, err := service.GetUser(ctx, testUserID)
	require.Equal(t, CodeUserNotFound, apperror.FromError(err).Code)
	require.Equal(t, uuid.Nil, profile.ID)
}

// gomock fails on an unexpected call, so this also asserts the identity read is
// skipped rather than attempted once the profile is known to be missing.
func TestGetUser_IdentityFailureIsReportedNotSwallowed(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		GetUserProfile(ctx, testUserID).
		Return(userdb.GetUserProfileRow{ID: testUserID}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return(nil, apperror.Internal(errors.New("connection reset")))

	_, err := service.GetUser(ctx, testUserID)
	require.Equal(t, apperror.CodeInternal, apperror.FromError(err).Code)
}

func TestUpdateUser_WritesTheSubmittedNameAndReturnsTheProfile(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		UpdateUserDisplayName(ctx, testUserID, "Jane Doe").
		Return(userdb.UpdateUserDisplayNameRow{
			ID:                    testUserID,
			Email:                 "owner@example.com",
			DisplayName:           "Jane Doe",
			CreatedAt:             mustTimestamptz("2026-10-01T10:30:00Z"),
			HasPasswordCredential: true,
		}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return([]userdb.ListAuthIdentitiesForUserRow{}, nil)

	profile, err := service.UpdateUser(ctx, testUserID, "Jane Doe")
	require.NoError(t, err)
	require.Equal(t, "Jane Doe", profile.DisplayName)
	require.True(t, profile.HasPasswordCredential)
}

// Registration stores whatever it is given, whitespace included. Normalising here
// would give two paths into one column that disagree about what it holds, so a
// name with surrounding spaces must arrive unchanged.
func TestUpdateUser_DoesNotTrimOrNormalizeTheName(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	const submitted = "  Jane Doe  "

	repository.EXPECT().
		UpdateUserDisplayName(ctx, testUserID, submitted).
		Return(userdb.UpdateUserDisplayNameRow{
			ID:                    testUserID,
			DisplayName:           submitted,
			HasPasswordCredential: false,
		}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return(nil, nil)

	profile, err := service.UpdateUser(ctx, testUserID, submitted)
	require.NoError(t, err)
	require.Equal(t, submitted, profile.DisplayName)
}

// The profile returned is the one the UPDATE wrote. The test asserts the read
// path is not taken by never registering an expectation for GetUserProfile:
// gomock fails on an unexpected call, so a service that committed and then
// re-read would fail here rather than pass by coincidence.
func TestUpdateUser_DoesNotReReadTheRowItJustWrote(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		UpdateUserDisplayName(ctx, testUserID, "Jane Doe").
		Return(userdb.UpdateUserDisplayNameRow{
			ID:                    testUserID,
			DisplayName:           "Jane Doe",
			HasPasswordCredential: false,
		}, nil)

	repository.EXPECT().
		ListAuthIdentitiesForUser(ctx, testUserID).
		Return(nil, nil)

	_, err := service.UpdateUser(ctx, testUserID, "Jane Doe")
	require.NoError(t, err)
}

func TestUpdateUser_ReportsAMissingUserRatherThanAnEmptyProfile(t *testing.T) {
	service, repository, _ := newServiceTestFixture(t)
	ctx := context.Background()

	repository.EXPECT().
		UpdateUserDisplayName(ctx, testUserID, "Jane Doe").
		Return(userdb.UpdateUserDisplayNameRow{}, apperror.NotFoundWith(
			CodeUserNotFound, "user not found", pgx.ErrNoRows,
		))

	_, err := service.UpdateUser(ctx, testUserID, "Jane Doe")
	require.Equal(t, CodeUserNotFound, apperror.FromError(err).Code)
}

func mustTimestamptz(value string) pgtype.Timestamptz {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}

	return pgtype.Timestamptz{Time: parsed, Valid: true}
}
