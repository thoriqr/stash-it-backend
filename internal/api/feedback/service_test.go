package feedback_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/feedback"
	feedbackdb "github.com/thoriqr/stash-it-backend/internal/api/feedback/generated"
	"github.com/thoriqr/stash-it-backend/internal/api/feedback/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// serviceTestUserID is a fixed subject, so a mocked call can assert that the owner
// the handler resolved is the one the statement was given.
var serviceTestUserID = uuid.MustParse("01a0f359-093b-737a-963a-80f7ca6768ed")

// newService builds the service under test over a mock repository.
func newService(t *testing.T) (feedback.Service, *mocks.MockRepository) {
	t.Helper()

	ctrl := gomock.NewController(t)

	repository := mocks.NewMockRepository(ctrl)

	return feedback.NewService(repository), repository
}

// captureCreate installs one expectation that records the parameters the service
// passed to the insert, and answers with a row the database could have written.
//
// Assertions belong in the callback rather than after the call because the
// parameters never leave it: the service hands them straight to the repository.
func captureCreate(
	t *testing.T,
	repository *mocks.MockRepository,
	assert func(feedbackdb.CreateFeedbackParams),
) {
	t.Helper()

	repository.EXPECT().
		CreateFeedback(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			params feedbackdb.CreateFeedbackParams,
		) (feedback.Feedback, error) {
			if assert != nil {
				assert(params)
			}

			return feedback.Feedback{
				ID:      uuid.New(),
				UserID:  params.UserID,
				Type:    params.Type,
				Message: params.Message,
				Status:  "new",
			}, nil
		})
}

// expectNoWrite fails the test if the service reaches the repository at all.
func expectNoWrite(repository *mocks.MockRepository) {
	repository.EXPECT().
		CreateFeedback(gomock.Any(), gomock.Any()).
		Times(0)
}

func TestCreate_TrimsMessage(t *testing.T) {
	service, repository := newService(t)

	captureCreate(t, repository, func(params feedbackdb.CreateFeedbackParams) {
		require.Equal(t, "Saving a reel leaves the title empty.", params.Message)
	})

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "bug",
			Message: "  Saving a reel leaves the title empty.  \n",
		},
	)
	require.NoError(t, err)
}

// A blank message is rejected before any statement runs.
//
// The request validator cannot cover this: `required` treats a string of spaces as
// present. Rejecting it here is what keeps a row that reads as a report from being
// stored while saying nothing.
func TestCreate_RejectsBlankMessageWithoutWriting(t *testing.T) {
	for _, message := range []string{
		"",
		" ",
		"   \n\t ",
	} {
		t.Run("blank message", func(t *testing.T) {
			service, repository := newService(t)

			expectNoWrite(repository)

			_, err := service.Create(
				context.Background(),
				serviceTestUserID,
				feedback.NewFeedbackParams{
					Type:    "bug",
					Message: message,
				},
			)

			assertValidationField(t, err, "message")
		})
	}
}

func TestCreate_RejectsOverLongMessageWithoutWriting(t *testing.T) {
	service, repository := newService(t)

	expectNoWrite(repository)

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "idea",
			Message: strings.Repeat("a", feedback.MessageMaxLength+1),
		},
	)

	assertValidationField(t, err, "message")
}

// The bound is measured in runes, so a multibyte message of the same character
// count is accepted.
func TestCreate_AcceptsMessageAtRuneLimit(t *testing.T) {
	service, repository := newService(t)

	captureCreate(t, repository, nil)

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "other",
			Message: strings.Repeat("é", feedback.MessageMaxLength),
		},
	)
	require.NoError(t, err)
}

// An absent context is normalized to absent, not to empty strings.
func TestCreate_TreatsAbsentContextAsNull(t *testing.T) {
	service, repository := newService(t)

	captureCreate(t, repository, func(params feedbackdb.CreateFeedbackParams) {
		require.False(t, params.Platform.Valid)
		require.False(t, params.AppVersion.Valid)
		require.False(t, params.AppBuild.Valid)
	})

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "bug",
			Message: "no context",
		},
	)
	require.NoError(t, err)
}

// A blank context field is absent rather than stored as an empty string, so "the
// client said nothing" stays distinguishable from "the client said something empty".
func TestCreate_TreatsBlankContextFieldsAsNull(t *testing.T) {
	service, repository := newService(t)

	blank := "   "

	captureCreate(t, repository, func(params feedbackdb.CreateFeedbackParams) {
		require.False(t, params.Platform.Valid)
		require.False(t, params.AppVersion.Valid)
	})

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "bug",
			Message: "blank context",
			Context: feedback.Context{
				Platform:   &blank,
				AppVersion: &blank,
			},
		},
	)
	require.NoError(t, err)
}

// The context is stored as it was sent. A platform this deployment has never heard
// of is recorded rather than refused, which is why the column has no CHECK.
func TestCreate_StoresUnknownPlatform(t *testing.T) {
	service, repository := newService(t)

	platform := "SomeFuturePlatform"

	captureCreate(t, repository, func(params feedbackdb.CreateFeedbackParams) {
		require.True(t, params.Platform.Valid)
		require.Equal(t, "somefutureplatform", params.Platform.String)
	})

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "bug",
			Message: "unknown platform",
			Context: feedback.Context{
				Platform: &platform,
			},
		},
	)
	require.NoError(t, err)
}

// A version is reported as the client wrote it. Only the platform is lowercased,
// because it is the value grouping happens on.
func TestCreate_PreservesAppVersionCase(t *testing.T) {
	service, repository := newService(t)

	version := "1.3.1-Beta"

	captureCreate(t, repository, func(params feedbackdb.CreateFeedbackParams) {
		require.True(t, params.AppVersion.Valid)
		require.Equal(t, "1.3.1-Beta", params.AppVersion.String)
	})

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "bug",
			Message: "version case",
			Context: feedback.Context{
				AppVersion: &version,
			},
		},
	)
	require.NoError(t, err)
}

// The build names the installed build, so anything that cannot be one is refused
// rather than stored.
func TestCreate_RejectsNonPositiveBuild(t *testing.T) {
	for _, build := range []int32{0, -1} {
		t.Run("non-positive build", func(t *testing.T) {
			service, repository := newService(t)

			expectNoWrite(repository)

			_, err := service.Create(
				context.Background(),
				serviceTestUserID,
				feedback.NewFeedbackParams{
					Type:    "bug",
					Message: "bad build",
					Context: feedback.Context{AppBuild: &build},
				},
			)

			assertValidationField(t, err, "app_build")
		})
	}
}

// An over-long context is a client bug and the report is not written, because a
// truncated value would record something nobody can match against a release.
func TestCreate_RejectsOverLongPlatform(t *testing.T) {
	service, repository := newService(t)

	expectNoWrite(repository)

	platform := strings.Repeat("a", feedback.PlatformMaxBytes+1)

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "bug",
			Message: "long platform",
			Context: feedback.Context{Platform: &platform},
		},
	)

	assertValidationField(t, err, "platform")
}

// The owner comes from the caller, never from the payload.
func TestCreate_UsesTheCallerAsOwner(t *testing.T) {
	service, repository := newService(t)

	captureCreate(t, repository, func(params feedbackdb.CreateFeedbackParams) {
		require.Equal(t, serviceTestUserID, params.UserID)
	})

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "bug",
			Message: "owner",
		},
	)
	require.NoError(t, err)
}

// The stored status is whatever the database wrote, not a constant this code
// assumes, so a change to the column default is reported rather than contradicted.
func TestCreate_ReportsStoredStatus(t *testing.T) {
	service, repository := newService(t)

	repository.EXPECT().
		CreateFeedback(gomock.Any(), gomock.Any()).
		Return(feedback.Feedback{
			ID:      uuid.New(),
			Message: "status",
			Status:  "new",
		}, nil)

	result, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "bug",
			Message: "status",
		},
	)
	require.NoError(t, err)
	require.Equal(t, "new", result.Status)
}

func TestCreate_PassesRepositoryErrorThrough(t *testing.T) {
	service, repository := newService(t)

	repository.EXPECT().
		CreateFeedback(gomock.Any(), gomock.Any()).
		Return(feedback.Feedback{}, apperror.Internal(nil))

	_, err := service.Create(
		context.Background(),
		serviceTestUserID,
		feedback.NewFeedbackParams{
			Type:    "bug",
			Message: "fails",
		},
	)

	require.Error(t, err)
	require.Equal(t, apperror.CodeInternal, apperror.FromError(err).Code)
}

// assertValidationField requires a VALIDATION_ERROR naming the given field.
func assertValidationField(t *testing.T, err error, field string) {
	t.Helper()

	require.Error(t, err)

	appErr := apperror.FromError(err)
	require.Equal(t, apperror.CodeValidation, appErr.Code)
	require.Len(t, appErr.Fields, 1)
	require.Equal(t, field, appErr.Fields[0].Field)
}
