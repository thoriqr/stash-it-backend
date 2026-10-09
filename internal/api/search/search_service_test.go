package search_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/search"
	searchmocks "github.com/thoriqr/stash-it-backend/internal/api/search/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// expectNoRepositoryCalls asserts that neither search runs.
//
// A query that fails validation must not reach the database at all, so nothing is
// matched, nothing is scored and no work is done for a query that cannot mean
// anything.
func expectNoRepositoryCalls(
	t *testing.T,
	repo *searchmocks.MockRepository,
) {
	t.Helper()

	repo.EXPECT().
		SearchSavedItems(gomock.Any(), gomock.Any()).
		Times(0)
	repo.EXPECT().
		SearchCollections(gomock.Any(), gomock.Any()).
		Times(0)
}

func TestService_Search(t *testing.T) {
	t.Run("returns saved items and collections for a valid query", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		userID := uuid.New()
		collectionID := uuid.New()

		expectedSavedItem := search.SearchSavedItem{
			ID:     uuid.New(),
			Title:  testText("Camera Buying Guide"),
			URL:    "https://example.org/notes/camera-buying-guide",
			Domain: testText("example.org"),
			Collection: search.SearchCollectionRef{
				ID:   collectionID,
				Name: "Camera Gear",
			},
			EnrichmentStatus: "completed",
			Score:            10.9,
		}

		expectedCollection := search.SearchCollection{
			ID:    collectionID,
			Name:  "Camera Gear",
			Score: 10.95,
		}

		repo.EXPECT().
			SearchSavedItems(
				gomock.Any(),
				search.SearchSavedItemsParams{
					UserID: userID,
					Query:  "camera",
					Limit:  search.SavedItemDefaultLimit,
				},
			).
			Return([]search.SearchSavedItem{expectedSavedItem}, nil)

		repo.EXPECT().
			SearchCollections(
				gomock.Any(),
				search.SearchCollectionsParams{
					UserID: userID,
					Query:  "camera",
					Limit:  search.CollectionLimit,
				},
			).
			Return([]search.SearchCollection{expectedCollection}, nil)

		result, err := svc.Search(
			context.Background(),
			userID,
			"camera",
			0,
		)

		require.NoError(t, err)
		require.Equal(t, []search.SearchSavedItem{expectedSavedItem}, result.SavedItems)
		require.Equal(
			t,
			[]search.SearchCollection{expectedCollection},
			result.Collections,
		)
		require.Equal(t, search.SavedItemDefaultLimit, result.Limit)
	})

	t.Run("rejects a query shorter than the minimum", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		// One rune is rejected: it matches too much to be a useful search and
		// forces the fuzzy threshold to admit noise.
		expectNoRepositoryCalls(t, repo)

		_, err := svc.Search(
			context.Background(),
			uuid.New(),
			"a",
			0,
		)

		require.Error(t, err)
		require.Equal(t, search.CodeInvalidSearchQuery, apperror.FromError(err).Code)
		require.Equal(t, 400, apperror.FromError(err).Status)
	})

	t.Run("rejects a blank query", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		blankQueries := []string{
			"",
			" ",
			"\t",
			"\n",
			"   \t\n  ",
			" ",
			"　",
		}

		for _, rawQuery := range blankQueries {
			t.Run(
				"blank "+strings.ReplaceAll(rawQuery, "\n", `\n`),
				func(t *testing.T) {
					expectNoRepositoryCalls(t, repo)

					_, err := svc.Search(
						context.Background(),
						uuid.New(),
						rawQuery,
						0,
					)

					require.Error(t, err)
					require.Equal(
						t,
						search.CodeInvalidSearchQuery,
						apperror.FromError(err).Code,
					)
					require.Equal(t, 400, apperror.FromError(err).Status)
				},
			)
		}
	})

	t.Run("accepts a query exactly at the minimum length", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		userID := uuid.New()

		atMinimum := strings.Repeat("a", search.QueryMinLength)

		repo.EXPECT().
			SearchSavedItems(
				gomock.Any(),
				search.SearchSavedItemsParams{
					UserID: userID,
					Query:  atMinimum,
					Limit:  search.SavedItemDefaultLimit,
				},
			).
			Return([]search.SearchSavedItem{}, nil)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil)

		_, err := svc.Search(
			context.Background(),
			userID,
			atMinimum,
			0,
		)

		require.NoError(t, err)
	})

	t.Run("accepts a query exactly at the maximum length", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		userID := uuid.New()

		atMaximum := strings.Repeat("a", search.QueryMaxLength)

		repo.EXPECT().
			SearchSavedItems(
				gomock.Any(),
				search.SearchSavedItemsParams{
					UserID: userID,
					Query:  atMaximum,
					Limit:  search.SavedItemDefaultLimit,
				},
			).
			Return([]search.SearchSavedItem{}, nil)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil)

		_, err := svc.Search(
			context.Background(),
			userID,
			atMaximum,
			0,
		)

		require.NoError(t, err)
	})

	t.Run("rejects a query longer than the maximum", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		expectNoRepositoryCalls(t, repo)

		_, err := svc.Search(
			context.Background(),
			uuid.New(),
			strings.Repeat("a", search.QueryMaxLength+1),
			0,
		)

		require.Error(t, err)
		require.Equal(t, search.CodeInvalidSearchQuery, apperror.FromError(err).Code)
		require.Equal(t, 400, apperror.FromError(err).Status)
	})

	t.Run("measures the query length in runes, not bytes", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		userID := uuid.New()

		// Every rune here is multiple bytes, so a byte based limit would reject a
		// query that is well within the rune limit.
		multibyte := strings.Repeat("日", search.QueryMaxLength)

		repo.EXPECT().
			SearchSavedItems(
				gomock.Any(),
				search.SearchSavedItemsParams{
					UserID: userID,
					Query:  multibyte,
					Limit:  search.SavedItemDefaultLimit,
				},
			).
			Return([]search.SearchSavedItem{}, nil)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil)

		_, err := svc.Search(
			context.Background(),
			userID,
			multibyte,
			0,
		)

		require.NoError(t, err)
	})

	t.Run("trims surrounding whitespace from the query", func(t *testing.T) {
		cases := map[string]string{
			"leading and trailing spaces": "   camera   ",
			"leading tab":                 "\tcamera",
			"trailing newline":            "camera\n",
			"all unicode whitespace":      " camera ",
			"padded tab and newline":      "\n\t camera \t\n",
		}

		for name, rawQuery := range cases {
			t.Run(name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := searchmocks.NewMockRepository(ctrl)

				svc := search.NewService(repo)

				userID := uuid.New()

				repo.EXPECT().
					SearchSavedItems(
						gomock.Any(),
						search.SearchSavedItemsParams{
							UserID: userID,
							Query:  "camera",
							Limit:  search.SavedItemDefaultLimit,
						},
					).
					Return([]search.SearchSavedItem{}, nil)

				repo.EXPECT().
					SearchCollections(
						gomock.Any(),
						search.SearchCollectionsParams{
							UserID: userID,
							Query:  "camera",
							Limit:  search.CollectionLimit,
						},
					).
					Return([]search.SearchCollection{}, nil)

				_, err := svc.Search(
					context.Background(),
					userID,
					rawQuery,
					0,
				)

				require.NoError(t, err)
			})
		}
	})

	t.Run("does not lowercase the query", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		userID := uuid.New()

		// ILIKE and pg_trgm are both case-insensitive already, so the query is
		// forwarded as submitted. Lowercasing it in Go would change the string
		// that is scored and would not change what matches.
		repo.EXPECT().
			SearchSavedItems(
				gomock.Any(),
				search.SearchSavedItemsParams{
					UserID: userID,
					Query:  "CaMeRa",
					Limit:  search.SavedItemDefaultLimit,
				},
			).
			Return([]search.SearchSavedItem{}, nil)

		repo.EXPECT().
			SearchCollections(
				gomock.Any(),
				search.SearchCollectionsParams{
					UserID: userID,
					Query:  "CaMeRa",
					Limit:  search.CollectionLimit,
				},
			).
			Return([]search.SearchCollection{}, nil)

		_, err := svc.Search(
			context.Background(),
			userID,
			"CaMeRa",
			0,
		)

		require.NoError(t, err)
	})

	t.Run("applies the default saved item limit when none is requested", func(t *testing.T) {
		nonPositiveLimits := map[string]int{
			"zero":     0,
			"negative": -1,
		}

		for name, requested := range nonPositiveLimits {
			t.Run(name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := searchmocks.NewMockRepository(ctrl)

				svc := search.NewService(repo)

				userID := uuid.New()

				repo.EXPECT().
					SearchSavedItems(
						gomock.Any(),
						search.SearchSavedItemsParams{
							UserID: userID,
							Query:  "camera",
							Limit:  search.SavedItemDefaultLimit,
						},
					).
					Return([]search.SearchSavedItem{}, nil)

				repo.EXPECT().
					SearchCollections(gomock.Any(), gomock.Any()).
					Return([]search.SearchCollection{}, nil)

				result, err := svc.Search(
					context.Background(),
					userID,
					"camera",
					requested,
				)

				require.NoError(t, err)
				require.Equal(t, search.SavedItemDefaultLimit, result.Limit)
			})
		}
	})

	t.Run("clamps a requested saved item limit above the maximum", func(t *testing.T) {
		oversizedLimits := map[string]int{
			"just above the maximum": search.SavedItemMaxLimit + 1,
			"far above the maximum":  10000,
		}

		for name, requested := range oversizedLimits {
			t.Run(name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := searchmocks.NewMockRepository(ctrl)

				svc := search.NewService(repo)

				userID := uuid.New()

				// Clamping rather than rejecting, matching how the saved item list
				// already handles its own page limit. The query is still validated
				// strictly; only the bound on work is relaxed.
				repo.EXPECT().
					SearchSavedItems(
						gomock.Any(),
						search.SearchSavedItemsParams{
							UserID: userID,
							Query:  "camera",
							Limit:  search.SavedItemMaxLimit,
						},
					).
					Return([]search.SearchSavedItem{}, nil)

				repo.EXPECT().
					SearchCollections(gomock.Any(), gomock.Any()).
					Return([]search.SearchCollection{}, nil)

				result, err := svc.Search(
					context.Background(),
					userID,
					"camera",
					requested,
				)

				require.NoError(t, err)
				require.Equal(t, search.SavedItemMaxLimit, result.Limit)
			})
		}
	})

	t.Run("passes a requested saved item limit inside the bounds through", func(t *testing.T) {
		withinBounds := map[string]int{
			"minimum useful": 1,
			"mid range":      25,
			"exactly max":    search.SavedItemMaxLimit,
		}

		for name, requested := range withinBounds {
			t.Run(name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := searchmocks.NewMockRepository(ctrl)

				svc := search.NewService(repo)

				userID := uuid.New()

				repo.EXPECT().
					SearchSavedItems(
						gomock.Any(),
						search.SearchSavedItemsParams{
							UserID: userID,
							Query:  "camera",
							Limit:  int32(requested),
						},
					).
					Return([]search.SearchSavedItem{}, nil)

				repo.EXPECT().
					SearchCollections(gomock.Any(), gomock.Any()).
					Return([]search.SearchCollection{}, nil)

				result, err := svc.Search(
					context.Background(),
					userID,
					"camera",
					requested,
				)

				require.NoError(t, err)
				require.Equal(t, requested, result.Limit)
			})
		}
	})

	t.Run("keeps the collection limit fixed regardless of the requested limit", func(t *testing.T) {
		requestedLimits := map[string]int{
			"none":      0,
			"small":     1,
			"oversized": search.SavedItemMaxLimit + 100,
		}

		for name, requested := range requestedLimits {
			t.Run(name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				repo := searchmocks.NewMockRepository(ctrl)

				svc := search.NewService(repo)

				userID := uuid.New()

				repo.EXPECT().
					SearchSavedItems(gomock.Any(), gomock.Any()).
					Return([]search.SearchSavedItem{}, nil)

				// Collections are a navigation aid shown beside the results, not a
				// list to page through, so the limit is not caller controlled.
				repo.EXPECT().
					SearchCollections(
						gomock.Any(),
						search.SearchCollectionsParams{
							UserID: userID,
							Query:  "camera",
							Limit:  search.CollectionLimit,
						},
					).
					Return([]search.SearchCollection{}, nil)

				_, err := svc.Search(
					context.Background(),
					userID,
					"camera",
					requested,
				)

				require.NoError(t, err)
			})
		}
	})

	t.Run("returns saved item repository errors unchanged", func(t *testing.T) {
		repoErr := apperror.Internal(errors.New("connection reset"))

		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return(nil, repoErr)

		// The collection search is never reached once the saved item search fails.
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Times(0)

		_, err := svc.Search(
			context.Background(),
			uuid.New(),
			"camera",
			0,
		)

		require.Equal(t, repoErr, err)
	})

	t.Run("returns collection repository errors unchanged", func(t *testing.T) {
		repoErr := apperror.Internal(errors.New("connection reset"))

		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{}, nil)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return(nil, repoErr)

		_, err := svc.Search(
			context.Background(),
			uuid.New(),
			"camera",
			0,
		)

		require.Equal(t, repoErr, err)
	})

	t.Run("returns an empty result when nothing matches", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{}, nil)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil)

		result, err := svc.Search(
			context.Background(),
			uuid.New(),
			"zzzqqq",
			0,
		)

		require.NoError(t, err)
		require.Empty(t, result.SavedItems)
		require.Empty(t, result.Collections)
	})

	t.Run("passes the authenticated user id unchanged to both searches", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		userID := uuid.New()

		// userID comes from the access token and is the only identity search has,
		// so it is what scopes both halves of the result.
		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params search.SearchSavedItemsParams,
				) ([]search.SearchSavedItem, error) {
					require.Equal(t, userID, params.UserID)

					return nil, nil
				},
			)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params search.SearchCollectionsParams,
				) ([]search.SearchCollection, error) {
					require.Equal(t, userID, params.UserID)

					return nil, nil
				},
			)

		_, err := svc.Search(
			context.Background(),
			userID,
			"camera",
			0,
		)

		require.NoError(t, err)
	})

	t.Run("passes the same normalized query to both searches", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		// One query, one normalized value, both halves. Normalizing twice could let
		// the two searches see different queries.
		savedItemQueries := []string{}
		collectionQueries := []string{}

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params search.SearchSavedItemsParams,
				) ([]search.SearchSavedItem, error) {
					savedItemQueries = append(savedItemQueries, params.Query)

					return nil, nil
				},
			)

		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					params search.SearchCollectionsParams,
				) ([]search.SearchCollection, error) {
					collectionQueries = append(collectionQueries, params.Query)

					return nil, nil
				},
			)

		_, err := svc.Search(
			context.Background(),
			uuid.New(),
			"  sourdogh \n",
			0,
		)

		require.NoError(t, err)
		require.Equal(t, []string{"sourdogh"}, savedItemQueries)
		require.Equal(t, []string{"sourdogh"}, collectionQueries)
	})

	t.Run("does no matching or ranking in Go", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		// Both results are returned in the order the repository produced them. The
		// service never re-sorts them, so there is no second place where the
		// relevance rules could disagree with the SQL that applied them.
		first := search.SearchSavedItem{ID: uuid.New(), Score: 10.0}
		second := search.SearchSavedItem{ID: uuid.New(), Score: 0.5}

		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{first, second}, nil)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil)

		result, err := svc.Search(
			context.Background(),
			uuid.New(),
			"camera",
			0,
		)

		require.NoError(t, err)
		require.Equal(
			t,
			[]search.SearchSavedItem{first, second},
			result.SavedItems,
		)
	})

	t.Run("does nothing beyond two repository calls", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := searchmocks.NewMockRepository(ctrl)

		svc := search.NewService(repo)

		// Search has no writes and no transaction, so the service composes nothing:
		// it validates, defaults, calls each search once, and returns.
		repo.EXPECT().
			SearchSavedItems(gomock.Any(), gomock.Any()).
			Return([]search.SearchSavedItem{}, nil).
			Times(1)
		repo.EXPECT().
			SearchCollections(gomock.Any(), gomock.Any()).
			Return([]search.SearchCollection{}, nil).
			Times(1)

		_, err := svc.Search(
			context.Background(),
			uuid.New(),
			"camera",
			0,
		)

		require.NoError(t, err)
	})
}

func testText(value string) pgtype.Text {
	return pgtype.Text{
		String: value,
		Valid:  true,
	}
}
