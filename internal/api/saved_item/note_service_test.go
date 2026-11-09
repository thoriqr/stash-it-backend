package saved_item_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	saveditemdb "github.com/thoriqr/stash-it-backend/internal/api/saved_item/generated"
	saveditemmocks "github.com/thoriqr/stash-it-backend/internal/api/saved_item/mocks"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// expectCreatedSavedItem wires the two repository calls a successful save makes and
// lets the test inspect the parameters the note produced.
func expectCreatedSavedItem(
	t *testing.T,
	repo *saveditemmocks.MockRepository,
	userID uuid.UUID,
	inspect func(saveditemdb.CreateSavedItemParams),
) {
	t.Helper()

	repo.EXPECT().
		GetUnsortedCollectionByUser(gomock.Any(), userID).
		Return(uuid.New(), nil)

	repo.EXPECT().
		CreateSavedItem(gomock.Any(), gomock.Any()).
		DoAndReturn(
			func(
				_ context.Context,
				params saveditemdb.CreateSavedItemParams,
			) (saved_item.SavedItem, error) {
				inspect(params)

				return saved_item.SavedItem{}, nil
			},
		)
}

func TestService_Create_Note(t *testing.T) {
	t.Run("stores a submitted note", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()
		note := "Read the section about database indexing later."

		expectCreatedSavedItem(t, repo, userID, func(params saveditemdb.CreateSavedItemParams) {
			require.True(t, params.Note.Valid)
			require.Equal(t, note, params.Note.String)
		})

		_, err := svc.Create(
			context.Background(),
			userID,
			"https://example.com/articles/1",
			&note,
		)

		require.NoError(t, err)
	})

	t.Run("omitted note leaves the column null", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()

		expectCreatedSavedItem(t, repo, userID, func(params saveditemdb.CreateSavedItemParams) {
			require.False(t, params.Note.Valid)
		})

		_, err := svc.Create(
			context.Background(),
			userID,
			"https://example.com/articles/1",
			nil,
		)

		require.NoError(t, err)
	})

	t.Run("an empty note leaves the column null", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()
		note := ""

		expectCreatedSavedItem(t, repo, userID, func(params saveditemdb.CreateSavedItemParams) {
			require.False(t, params.Note.Valid)
		})

		_, err := svc.Create(
			context.Background(),
			userID,
			"https://example.com/articles/1",
			&note,
		)

		require.NoError(t, err)
	})

	t.Run("normalizes a whitespace-only note to null", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()
		note := " \n\t  "

		expectCreatedSavedItem(t, repo, userID, func(params saveditemdb.CreateSavedItemParams) {
			require.False(
				t,
				params.Note.Valid,
				"whitespace-only content is not a note",
			)
		})

		_, err := svc.Create(
			context.Background(),
			userID,
			"https://example.com/articles/1",
			&note,
		)

		require.NoError(t, err)
	})

	t.Run("preserves multiline plain text", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()
		note := "First line\nSecond line\n\nFourth line"

		expectCreatedSavedItem(t, repo, userID, func(params saveditemdb.CreateSavedItemParams) {
			require.True(t, params.Note.Valid)
			require.Equal(t, note, params.Note.String)
		})

		_, err := svc.Create(
			context.Background(),
			userID,
			"https://example.com/articles/1",
			&note,
		)

		require.NoError(t, err)
	})

	t.Run("rejects a note longer than 5000 characters before writing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()
		note := strings.Repeat("a", saved_item.NoteMaxLength+1)

		repo.EXPECT().GetUnsortedCollectionByUser(gomock.Any(), gomock.Any()).Times(0)
		repo.EXPECT().CreateSavedItem(gomock.Any(), gomock.Any()).Times(0)

		_, err := svc.Create(
			context.Background(),
			userID,
			"https://example.com/articles/1",
			&note,
		)

		require.Error(t, err)
		require.Equal(t, saved_item.CodeInvalidNote, apperror.FromError(err).Code)
	})

	t.Run("counts characters rather than bytes", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()

		// 5000 runes but 10000 bytes in UTF-8. A byte-counting limit would reject
		// this even though it is exactly at the documented character bound.
		note := strings.Repeat("é", saved_item.NoteMaxLength)
		require.Greater(t, len(note), saved_item.NoteMaxLength)
		require.Equal(t, saved_item.NoteMaxLength, utf8.RuneCountInString(note))

		expectCreatedSavedItem(t, repo, userID, func(params saveditemdb.CreateSavedItemParams) {
			require.True(t, params.Note.Valid)
			require.Equal(t, note, params.Note.String)
		})

		_, err := svc.Create(
			context.Background(),
			userID,
			"https://example.com/articles/1",
			&note,
		)

		require.NoError(t, err)
	})
}

func TestService_SetNote(t *testing.T) {
	t.Run("stores and returns the note", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()
		savedItemID := uuid.New()
		note := "My updated context for saving this URL."

		repo.EXPECT().
			UpdateSavedItemNote(
				gomock.Any(),
				userID,
				savedItemID,
				pgtype.Text{String: note, Valid: true},
			).
			Return(pgtype.Text{String: note, Valid: true}, nil)

		result, err := svc.SetNote(
			context.Background(),
			userID,
			savedItemID,
			note,
		)

		require.NoError(t, err)
		require.Equal(t, note, result.Note)
	})

	t.Run("scopes the write by both the item and the authenticated user", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()
		savedItemID := uuid.New()
		otherUserID := uuid.New()

		repo.EXPECT().
			UpdateSavedItemNote(gomock.Any(), userID, savedItemID, gomock.Any()).
			DoAndReturn(
				func(
					_ context.Context,
					gotUserID uuid.UUID,
					gotSavedItemID uuid.UUID,
					_ pgtype.Text,
				) (pgtype.Text, error) {
					require.Equal(t, userID, gotUserID)
					require.Equal(t, savedItemID, gotSavedItemID)
					require.NotEqual(t, otherUserID, gotUserID)

					return pgtype.Text{String: "note", Valid: true}, nil
				},
			)

		_, err := svc.SetNote(
			context.Background(),
			userID,
			savedItemID,
			"note",
		)

		require.NoError(t, err)
	})

	t.Run("rejects a blank note without writing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		repo.EXPECT().
			UpdateSavedItemNote(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)

		_, err := svc.SetNote(
			context.Background(),
			uuid.New(),
			uuid.New(),
			"",
		)

		require.Error(t, err)
		require.Equal(t, saved_item.CodeInvalidNote, apperror.FromError(err).Code)
	})

	t.Run("rejects a whitespace-only note without writing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		repo.EXPECT().
			UpdateSavedItemNote(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)

		_, err := svc.SetNote(
			context.Background(),
			uuid.New(),
			uuid.New(),
			" \n\t ",
		)

		require.Error(t, err)
		require.Equal(t, saved_item.CodeInvalidNote, apperror.FromError(err).Code)
	})

	t.Run("rejects a note longer than 5000 characters without writing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())

		repo.EXPECT().
			UpdateSavedItemNote(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Times(0)

		_, err := svc.SetNote(
			context.Background(),
			uuid.New(),
			uuid.New(),
			strings.Repeat("a", saved_item.NoteMaxLength+1),
		)

		require.Error(t, err)
		require.Equal(t, saved_item.CodeInvalidNote, apperror.FromError(err).Code)
	})

	t.Run("accepts 5000 characters", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		note := strings.Repeat("a", saved_item.NoteMaxLength)

		repo.EXPECT().
			UpdateSavedItemNote(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(pgtype.Text{String: note, Valid: true}, nil)

		result, err := svc.SetNote(
			context.Background(),
			uuid.New(),
			uuid.New(),
			note,
		)

		require.NoError(t, err)
		require.Equal(t, note, result.Note)
	})

	t.Run("returns the not found error unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		repoErr := apperror.NotFound(errors.New("no rows"))

		repo.EXPECT().
			UpdateSavedItemNote(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(pgtype.Text{}, repoErr)

		_, err := svc.SetNote(
			context.Background(),
			uuid.New(),
			uuid.New(),
			"note",
		)

		require.Equal(t, repoErr, err)
	})
}

func TestService_DeleteNote(t *testing.T) {
	t.Run("clears the note through the repository", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		userID := uuid.New()
		savedItemID := uuid.New()

		repo.EXPECT().
			ClearSavedItemNote(gomock.Any(), userID, savedItemID).
			Return(nil)

		require.NoError(
			t,
			svc.DeleteNote(context.Background(), userID, savedItemID),
		)
	})

	t.Run("returns the not found error unchanged", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		repo := saveditemmocks.NewMockRepository(ctrl)

		svc := saved_item.NewService(repo, nil, zap.NewNop())
		repoErr := apperror.NotFound(errors.New("no rows"))

		repo.EXPECT().
			ClearSavedItemNote(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(repoErr)

		err := svc.DeleteNote(context.Background(), uuid.New(), uuid.New())

		require.Equal(t, repoErr, err)
	})
}
