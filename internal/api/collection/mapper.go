package collection

import (
	"github.com/jackc/pgx/v5/pgtype"

	collectiondb "github.com/thoriqr/stash-it-backend/internal/api/collection/generated"
)

func mapCollectionResponse(collection collectiondb.Collection) CollectionResponse {
	return CollectionResponse{
		ID:        collection.ID,
		Name:      collection.Name,
		Type:      collection.Type,
		CreatedAt: collection.CreatedAt.Time,
		UpdatedAt: collection.UpdatedAt.Time,
	}
}

func mapPutSavedItemSavedItemResponse(savedItem SavedItem) PutSavedItemSavedItemResponse {
	return PutSavedItemSavedItemResponse{
		ID:           savedItem.ID,
		URL:          savedItem.URL,
		Domain:       mapOptionalText(savedItem.Domain),
		Platform:     mapOptionalText(savedItem.Platform),
		Title:        mapOptionalText(savedItem.Title),
		CollectionID: savedItem.CollectionID,
		CreatedAt:    savedItem.CreatedAt.Time,
		UpdatedAt:    savedItem.UpdatedAt.Time,
	}
}

func mapPutSavedItemIntoCollectionResponse(
	result PutSavedItemResult,
) PutSavedItemIntoCollectionResponse {
	return PutSavedItemIntoCollectionResponse{
		Collection:          mapCollectionResponse(result.Collection),
		SavedItem:           mapPutSavedItemSavedItemResponse(result.SavedItem),
		CollectionCreated:   result.CollectionCreated,
		AlreadyInCollection: result.AlreadyInCollection,
	}
}

func mapOptionalText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}

	text := value.String

	return &text
}
