package collection

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

type Service interface {
	// PutSavedItem puts one of the user's saved items into the user collection
	// with the given name, creating that collection if it does not exist yet.
	PutSavedItem(
		ctx context.Context,
		userID uuid.UUID,
		savedItemID uuid.UUID,
		collectionName string,
	) (PutSavedItemResult, error)
}

type service struct {
	repository Repository
}

func NewService(
	repository Repository,
) *service {
	return &service{
		repository: repository,
	}
}

// PutSavedItemResult reports what the operation did.
//
// CollectionCreated and AlreadyInCollection are mutually exclusive: a collection
// that was just created cannot already hold the saved item.
type PutSavedItemResult struct {
	Collection          collectiondb.Collection
	CollectionCreated   bool
	AlreadyInCollection bool
	SavedItem           SavedItem
}

// PutSavedItem puts a saved item into the user's collection with the given name,
// creating the collection if it does not exist yet.
//
// The operation is idempotent. When the saved item is already in the target
// collection nothing is written, and this is reported through AlreadyInCollection
// rather than reported as a conflict.
//
// The service owns only the name rules and the single call into the repository.
// The transaction, the create-or-get and the move all belong to the repository,
// because they cannot be split without risking a collection that holds no item.
func (s *service) PutSavedItem(
	ctx context.Context,
	userID uuid.UUID,
	savedItemID uuid.UUID,
	collectionName string,
) (PutSavedItemResult, error) {
	name, err := normalizeCollectionName(collectionName)
	if err != nil {
		return PutSavedItemResult{}, err
	}

	result, err := s.repository.PutSavedItemIntoUserCollection(
		ctx,
		PutSavedItemIntoUserCollectionParams{
			UserID:      userID,
			SavedItemID: savedItemID,
			Name:        name,
		},
	)
	if err != nil {
		return PutSavedItemResult{}, err
	}

	return PutSavedItemResult{
		Collection:          result.Collection,
		CollectionCreated:   result.CollectionCreated,
		AlreadyInCollection: result.AlreadyInCollection,
		SavedItem:           result.SavedItem,
	}, nil
}

// normalizeCollectionName trims the submitted name and validates it.
//
// Surrounding whitespace is stripped so the stored display name and the
// collections_user_name_unique expression, which compares lower(btrim(name)),
// agree on what the name is. Trimming every kind of Unicode whitespace, rather
// than only the plain spaces btrim() removes, also keeps the stored name from
// looking different from the name the unique index matched.
//
// The name is not lowercased: it is the user's display name and is stored as
// submitted. Case-insensitive uniqueness is PostgreSQL's job, decided by the
// unique index, so no existence check is performed here. Deciding it in Go would
// mean two sources of truth that can disagree with the index.
func normalizeCollectionName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)

	if trimmed == "" {
		return "", apperror.BadRequestWith(
			CodeInvalidCollectionName,
			"collection name must not be blank",
			nil,
		)
	}

	// Measured in runes, so a multi-byte name is not rejected for being long in
	// bytes.
	if utf8.RuneCountInString(trimmed) > CollectionNameMaxLength {
		return "", apperror.BadRequestWith(
			CodeInvalidCollectionName,
			"collection name is too long",
			nil,
		)
	}

	return trimmed, nil
}
