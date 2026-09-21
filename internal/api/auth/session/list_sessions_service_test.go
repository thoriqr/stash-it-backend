package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
)

func TestService_ListSessions(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()

		sessions := []sessiondb.Session{
			{
				ID:     uuid.New(),
				UserID: userID,
			},
			{
				ID:     uuid.New(),
				UserID: userID,
			},
		}

		repo.EXPECT().
			ListSessions(
				gomock.Any(),
				userID,
				int32(10),
				int32(10),
			).
			Return(sessions, nil)

		repo.EXPECT().
			CountSessions(
				gomock.Any(),
				userID,
			).
			Return(int64(25), nil)

		result, err := svc.ListSessions(
			context.Background(),
			userID,
			2,
			10,
		)

		require.NoError(t, err)
		require.Equal(t, sessions, result.Sessions)
		require.Equal(t, 2, result.Page)
		require.Equal(t, 10, result.Limit)
		require.Equal(t, int64(25), result.Total)
		require.Equal(t, 3, result.TotalPages)
	})

	t.Run("page less than one uses first page", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()

	repo.EXPECT().
		ListSessions(
			gomock.Any(),
			userID,
			int32(0),
			int32(10),
		).
			Return(nil, nil)

		repo.EXPECT().
			CountSessions(
				gomock.Any(),
				userID,
			).
			Return(int64(0), nil)

		result, err := svc.ListSessions(
			context.Background(),
			userID,
			0,
			10,
		)

		require.NoError(t, err)
		require.Equal(t, 1, result.Page)
		require.Equal(t, 10, result.Limit)
		require.Equal(t, int64(0), result.Total)
		require.Equal(t, 0, result.TotalPages)
	})

	t.Run("non-positive limit uses default", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()

		repo.EXPECT().
			ListSessions(
				gomock.Any(),
				userID,
				int32(0),
				int32(session.SessionListDefaultLimit),
			).
			Return(nil, nil)

		repo.EXPECT().
			CountSessions(
				gomock.Any(),
				userID,
			).
			Return(int64(1), nil)

		result, err := svc.ListSessions(
			context.Background(),
			userID,
			1,
			0,
		)

		require.NoError(t, err)
		require.Equal(t, 1, result.Page)
		require.Equal(t, session.SessionListDefaultLimit, result.Limit)
		require.Equal(t, int64(1), result.Total)
		require.Equal(t, 1, result.TotalPages)
	})

	t.Run("limit above maximum uses maximum", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()

		repo.EXPECT().
			ListSessions(
				gomock.Any(),
				userID,
				int32(session.SessionListMaxLimit),
				int32(session.SessionListMaxLimit),
			).
			Return(nil, nil)

		repo.EXPECT().
			CountSessions(
				gomock.Any(),
				userID,
			).
			Return(int64(101), nil)

		result, err := svc.ListSessions(
			context.Background(),
			userID,
			2,
			session.SessionListMaxLimit+10,
		)

		require.NoError(t, err)
		require.Equal(t, 2, result.Page)
		require.Equal(t, session.SessionListMaxLimit, result.Limit)
		require.Equal(t, int64(101), result.Total)
		require.Equal(t, 3, result.TotalPages)
	})

	t.Run("list sessions repository error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()
		expectedErr := errors.New("list sessions error")

		repo.EXPECT().
			ListSessions(
				gomock.Any(),
				userID,
				int32(0),
				int32(10),
			).
			Return(nil, expectedErr)

		result, err := svc.ListSessions(
			context.Background(),
			userID,
			1,
			10,
		)

		require.ErrorIs(t, err, expectedErr)
		require.Zero(t, result)
	})

	t.Run("count sessions repository error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := sessionmocks.NewMockRepository(ctrl)

		svc := session.NewService(repo, nil)

		userID := uuid.New()
		expectedErr := errors.New("count sessions error")

		repo.EXPECT().
			ListSessions(
				gomock.Any(),
				userID,
				int32(0),
				int32(10),
			).
			Return(nil, nil)

		repo.EXPECT().
			CountSessions(
				gomock.Any(),
				userID,
			).
			Return(int64(0), expectedErr)

		result, err := svc.ListSessions(
			context.Background(),
			userID,
			1,
			10,
		)

		require.ErrorIs(t, err, expectedErr)
		require.Zero(t, result)
	})
}