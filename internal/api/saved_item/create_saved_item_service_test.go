package saved_item_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	saveditemmocks "github.com/thoriqr/stash-it-backend/internal/api/saved_item/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestService_Create(t *testing.T) {
	t.Run("derives domain and leaves platform and title null", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()

		created := saveditemdb.SavedItem{
			ID:     uuid.New(),
			UserID: userID,
			Url:    "https://example.com/articles/1",
			Domain: pgtype.Text{String: "example.com", Valid: true},
		}

		repo.EXPECT().
			CreateSavedItem(
				gomock.Any(),
				saveditemdb.CreateSavedItemParams{
					UserID:   userID,
					Url:      "https://example.com/articles/1",
					Domain:   pgtype.Text{String: "example.com", Valid: true},
					Platform: pgtype.Text{},
					Title:    pgtype.Text{},
				},
			).
			Return(created, nil)

		result, err := svc.Create(
			context.Background(),
			userID,
			"https://example.com/articles/1",
		)

		require.NoError(t, err)
		require.Equal(t, created, result.SavedItem)
	})

	t.Run("never populates platform from the url host", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()

		repo.EXPECT().
			CreateSavedItem(
				gomock.Any(),
				gomock.Any(),
			).
			DoAndReturn(
				func(
					_ context.Context,
					params saveditemdb.CreateSavedItemParams,
				) (saveditemdb.SavedItem, error) {
					require.Equal(
						t,
						"youtube.com",
						params.Domain.String,
					)
					require.False(
						t,
						params.Platform.Valid,
						"platform must stay null in Phase A",
					)
					require.False(t, params.Title.Valid)

					return saveditemdb.SavedItem{}, nil
				},
			)

		_, err := svc.Create(
			context.Background(),
			userID,
			"https://www.youtube.com/watch?v=abc",
		)

		require.NoError(t, err)
	})

	t.Run("keeps subdomain in domain", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		userID := uuid.New()

		repo.EXPECT().
			CreateSavedItem(
				gomock.Any(),
				gomock.Any(),
			).
			DoAndReturn(
				func(
					_ context.Context,
					params saveditemdb.CreateSavedItemParams,
				) (saveditemdb.SavedItem, error) {
					require.Equal(
						t,
						"shop.example.com",
						params.Domain.String,
					)

					return saveditemdb.SavedItem{}, nil
				},
			)

		_, err := svc.Create(
			context.Background(),
			userID,
			"https://shop.example.com:8443/a/b?c=d#e",
		)

		require.NoError(t, err)
	})

	t.Run("normalizes domain", func(t *testing.T) {
		cases := []struct {
			name     string
			rawURL   string
			expected string
		}{
			{
				name:     "strips www prefix",
				rawURL:   "https://www.youtube.com/watch?v=abc",
				expected: "youtube.com",
			},
			{
				name:     "leaves bare host unchanged",
				rawURL:   "https://youtube.com/watch?v=abc",
				expected: "youtube.com",
			},
			{
				name:     "keeps m subdomain",
				rawURL:   "https://m.youtube.com/watch?v=abc",
				expected: "m.youtube.com",
			},
			{
				name:     "strips www from tiktok",
				rawURL:   "https://www.tiktok.com/@user/video/1",
				expected: "tiktok.com",
			},
			{
				name:     "lowercases host",
				rawURL:   "https://WWW.YouTube.COM/watch",
				expected: "youtube.com",
			},
			{
				name:     "strips only one www prefix",
				rawURL:   "https://www.www.example.com/a",
				expected: "www.example.com",
			},
			{
				name:     "ignores port and userinfo",
				rawURL:   "https://user:pw@Shop.Example.com:8443/a",
				expected: "shop.example.com",
			},
			{
				name:     "lowercases without stripping non-www subdomain",
				rawURL:   "https://Shop.Example.com/a",
				expected: "shop.example.com",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := saveditemmocks.NewMockRepository(ctrl)

				svc := saved_item.NewService(repo)

				repo.EXPECT().
					CreateSavedItem(
						gomock.Any(),
						gomock.Any(),
					).
					DoAndReturn(
						func(
							_ context.Context,
							params saveditemdb.CreateSavedItemParams,
						) (saveditemdb.SavedItem, error) {
							require.Equal(
								t,
								tc.expected,
								params.Domain.String,
							)

							return saveditemdb.SavedItem{}, nil
						},
					)

				_, err := svc.Create(
					context.Background(),
					uuid.New(),
					tc.rawURL,
				)

				require.NoError(t, err)
			})
		}
	})

	t.Run("rejects url without host", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		_, err := svc.Create(
			context.Background(),
			uuid.New(),
			"not-a-url",
		)

		require.Error(t, err)
		require.Equal(
			t,
			saved_item.CodeInvalidURL,
			apperror.FromError(err).Code,
		)
	})

	t.Run("rejects url with control characters", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		_, err := svc.Create(
			context.Background(),
			uuid.New(),
			"https://example.com/\x7f",
		)

		require.Error(t, err)
		require.Equal(
			t,
			saved_item.CodeInvalidURL,
			apperror.FromError(err).Code,
		)
	})

	t.Run("returns repository error unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo)

		repoErr := apperror.Internal(errors.New("insert failed"))

		repo.EXPECT().
			CreateSavedItem(gomock.Any(), gomock.Any()).
			Return(saveditemdb.SavedItem{}, repoErr)

		_, err := svc.Create(
			context.Background(),
			uuid.New(),
			"https://example.com",
		)

		require.Equal(t, repoErr, err)
	})
}
