package saved_item

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// mapCreatedSavedItemResponse builds the minimal create representation.
//
// Only the four fields that mean something at save time are mapped. The remaining
// columns are not read here and not reported: domain is derived locally and is
// obtainable from the URL the caller just submitted, while platform, title,
// description, image_url and last_enriched_at are still NULL because enrichment
// has not run. Returning them would present unlooked-up nulls as metadata.
func mapCreatedSavedItemResponse(savedItem SavedItem) CreatedSavedItemResponse {
	return CreatedSavedItemResponse{
		ID:           savedItem.ID,
		URL:          savedItem.Url,
		CollectionID: savedItem.CollectionID,
		// The stored value, not a constant this mapper assumes. It comes back from
		// the INSERT, so a future change to the column's default is reported rather
		// than silently contradicted here.
		EnrichmentStatus: savedItem.EnrichmentStatus,
	}
}

// mapSavedItemDetailResponse builds the complete saved item.
//
// LastEnrichedAt is a pointer because a pending, never-run or failed enrichment
// leaves it NULL, and reporting the zero time would claim metadata was refreshed
// at a moment when nothing was fetched.
func mapSavedItemDetailResponse(
	savedItem SavedItem,
) SavedItemDetailResponse {
	var lastEnrichedAt *time.Time

	if savedItem.LastEnrichedAt.Valid {
		enrichedAt := savedItem.LastEnrichedAt.Time
		lastEnrichedAt = &enrichedAt
	}

	return SavedItemDetailResponse{
		ID:       savedItem.ID,
		URL:      savedItem.Url,
		Domain:   mapOptionalText(savedItem.Domain),
		Platform: mapOptionalText(savedItem.Platform),
		Title:    mapOptionalText(savedItem.Title),
		// Passed through as stored, including when still NULL. A page is not
		// required to expose either, and one that did not is an ordinary result.
		Description:  mapOptionalText(savedItem.Description),
		ImageURL:     mapOptionalText(savedItem.ImageURL),
		CollectionID: savedItem.CollectionID,
		// EnrichmentStatus is a NOT NULL column with a default, so there is no null
		// case to represent and it is not mapped through mapOptionalText.
		EnrichmentStatus: savedItem.EnrichmentStatus,
		LastEnrichedAt:   lastEnrichedAt,
		CreatedAt:        savedItem.CreatedAt.Time,
		UpdatedAt:        savedItem.UpdatedAt.Time,
	}
}

// mapSavedItemCollectionResponse names the collection the item was filed in.
//
// It is fed the id and name the repository already read alongside the item, so the
// collection reported cannot be one that disagrees with saved_item.collection_id.
func mapSavedItemCollectionResponse(
	collection SavedItemCollection,
) SavedItemCollectionResponse {
	return SavedItemCollectionResponse{
		ID:   collection.ID,
		Name: collection.Name,
	}
}

func mapCreateSavedItemResponse(result CreateResult) CreateSavedItemResponse {
	return CreateSavedItemResponse{
		SavedItem: mapCreatedSavedItemResponse(result.SavedItem),
	}
}

// mapListedSavedItemResponse builds one row of the recent saved items listing.
//
// LastEnrichedAt is a pointer for the same reason it is on the detail response: a
// pending, never-run or failed enrichment leaves it NULL, and reporting the zero
// time would claim metadata was refreshed at a moment when nothing was fetched.
//
// The collection is built from the name the listing's own statement joined in,
// beside the same collection_id, so the two cannot describe different collections.
// Both are reported unconditionally, including for an item that was never enriched:
// where an item lives is a fact about the item, not a consequence of its metadata.
func mapListedSavedItemResponse(
	savedItem ListedSavedItem,
) ListedSavedItemResponse {
	var lastEnrichedAt *time.Time

	if savedItem.LastEnrichedAt.Valid {
		enrichedAt := savedItem.LastEnrichedAt.Time
		lastEnrichedAt = &enrichedAt
	}

	return ListedSavedItemResponse{
		ID:               savedItem.ID,
		URL:              savedItem.Url,
		Domain:           mapOptionalText(savedItem.Domain),
		Platform:         mapOptionalText(savedItem.Platform),
		Title:            mapOptionalText(savedItem.Title),
		Description:      mapOptionalText(savedItem.Description),
		ImageURL:         mapOptionalText(savedItem.ImageURL),
		CollectionID:     savedItem.CollectionID,
		EnrichmentStatus: savedItem.EnrichmentStatus,
		LastEnrichedAt:   lastEnrichedAt,
		CreatedAt:        savedItem.CreatedAt.Time,
		UpdatedAt:        savedItem.UpdatedAt.Time,
		Collection: SavedItemCollectionResponse{
			ID:   savedItem.CollectionID,
			Name: savedItem.CollectionName,
		},
	}
}

// mapListSavedItemsResponse builds one page of the history.
//
// Made with a length rather than left nil, so an empty history serializes as []
// rather than null. A user who has saved nothing is an ordinary state rather than an
// absence of a list, and a list that arrives as null where a list was promised is a
// shape change every client has to defend against.
func mapListSavedItemsResponse(
	result ListSavedItemsResult,
) ListSavedItemsResponse {
	savedItems := make([]ListedSavedItemResponse, 0, len(result.SavedItems))

	for _, savedItem := range result.SavedItems {
		savedItems = append(
			savedItems,
			mapListedSavedItemResponse(savedItem),
		)
	}

	return ListSavedItemsResponse{
		SavedItems: savedItems,
	}
}

func mapGetSavedItemResponse(result GetResult) GetSavedItemResponse {
	return GetSavedItemResponse{
		SavedItem:  mapSavedItemDetailResponse(result.SavedItem),
		Collection: mapSavedItemCollectionResponse(result.Collection),
	}
}

func mapDeleteSavedItemResponse(result DeleteResult) DeleteSavedItemResponse {
	return DeleteSavedItemResponse{
		CollectionID:        result.CollectionID,
		CollectionEmpty:     result.CollectionEmpty,
		CollectionDeletable: result.CollectionDeletable,
	}
}

func mapOptionalText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}

	text := value.String

	return &text
}
