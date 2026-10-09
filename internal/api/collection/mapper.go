package collection

import (
	"time"

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

func mapListedSavedItemResponse(
	savedItem ListedSavedItem,
) ListedSavedItemResponse {
	// LastEnrichedAt is a pointer because a failed or never-run enrichment leaves it
	// NULL, and reporting the zero time would claim metadata was refreshed then.
	var lastEnrichedAt *time.Time

	if savedItem.LastEnrichedAt.Valid {
		enrichedAt := savedItem.LastEnrichedAt.Time
		lastEnrichedAt = &enrichedAt
	}

	return ListedSavedItemResponse{
		ID:           savedItem.ID,
		URL:          savedItem.URL,
		Domain:       mapOptionalText(savedItem.Domain),
		Platform:     mapOptionalText(savedItem.Platform),
		Title:        mapOptionalText(savedItem.Title),
		Description:  mapOptionalText(savedItem.Description),
		ImageURL:     mapOptionalText(savedItem.ImageURL),
		CollectionID: savedItem.CollectionID,
		// EnrichmentStatus is a NOT NULL column with a default, so it is always a real
		// value here rather than an absent one. It is not mapped through
		// mapOptionalText because there is no null case to represent.
		EnrichmentStatus: savedItem.EnrichmentStatus,
		LastEnrichedAt:   lastEnrichedAt,
		CreatedAt:        savedItem.CreatedAt.Time,
		UpdatedAt:        savedItem.UpdatedAt.Time,
	}
}

// mapListedSavedItemsCollectionResponse names the collection a page came from.
//
// It takes the row the ownership check already returned rather than being handed a
// name separately, so the id and the name reported together cannot describe two
// different collections.
func mapListedSavedItemsCollectionResponse(
	collection collectiondb.Collection,
) ListedSavedItemsCollectionResponse {
	return ListedSavedItemsCollectionResponse{
		ID:   collection.ID,
		Name: collection.Name,
	}
}

func mapListSavedItemsInCollectionResponse(
	result ListSavedItemsInCollectionResult,
) ListSavedItemsInCollectionResponse {
	savedItems := make([]ListedSavedItemResponse, 0, len(result.SavedItems))

	for _, savedItem := range result.SavedItems {
		savedItems = append(
			savedItems,
			mapListedSavedItemResponse(savedItem),
		)
	}

	// Mapped unconditionally rather than only when the page is non-empty. An empty
	// collection is still the collection the caller asked about, and omitting the
	// field there would make "nothing in it" look like "nothing matched".
	return ListSavedItemsInCollectionResponse{
		Collection: mapListedSavedItemsCollectionResponse(result.Collection),
		SavedItems: savedItems,
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
