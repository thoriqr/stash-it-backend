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

func mapListedCollectionResponse(
	collection collectiondb.Collection,
) ListedCollectionResponse {
	response := ListedCollectionResponse{
		ID:        collection.ID,
		Name:      collection.Name,
		Type:      collection.Type,
		CreatedAt: collection.CreatedAt.Time,
		UpdatedAt: collection.UpdatedAt.Time,
	}

	// Valid is checked before the value, so a user collection's absent key is
	// reported as null rather than as an empty string. The two are different things:
	// null means "not a system collection", and an empty string would be a key value
	// this schema does not produce.
	if collection.SystemKey.Valid {
		systemKey := collection.SystemKey.String
		response.SystemKey = &systemKey
	}

	return response
}

func mapListCollectionsResponse(
	result ListCollectionsResult,
) ListCollectionsResponse {
	// Made with a length rather than left nil, so an empty page serializes as []
	// rather than null. A list that arrives as null where a list was promised is a
	// shape change a client has to defend against, and an empty collection list is
	// an ordinary result rather than an absence of one.
	collections := make(
		[]ListedCollectionResponse,
		0,
		len(result.Collections),
	)

	for _, collection := range result.Collections {
		collections = append(
			collections,
			mapListedCollectionResponse(collection),
		)
	}

	return ListCollectionsResponse{
		Collections: collections,
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
