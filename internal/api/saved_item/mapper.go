package saved_item

import (
	"github.com/jackc/pgx/v5/pgtype"
)

func mapSavedItemResponse(savedItem SavedItem) SavedItemResponse {
	return SavedItemResponse{
		ID:        savedItem.ID,
		URL:       savedItem.Url,
		Domain:    mapOptionalText(savedItem.Domain),
		Platform:  mapOptionalText(savedItem.Platform),
		Title:     mapOptionalText(savedItem.Title),
		CreatedAt: savedItem.CreatedAt.Time,
		UpdatedAt: savedItem.UpdatedAt.Time,
	}
}

func mapCreateSavedItemResponse(result CreateResult) CreateSavedItemResponse {
	return CreateSavedItemResponse{
		SavedItem: mapSavedItemResponse(result.SavedItem),
	}
}

func mapGetSavedItemResponse(result GetResult) GetSavedItemResponse {
	return GetSavedItemResponse{
		SavedItem: mapSavedItemResponse(result.SavedItem),
	}
}

func mapListSavedItemsResponse(result ListResult) ListSavedItemsResponse {
	savedItems := make([]SavedItemResponse, 0, len(result.SavedItems))

	for _, savedItem := range result.SavedItems {
		savedItems = append(
			savedItems,
			mapSavedItemResponse(savedItem),
		)
	}

	return ListSavedItemsResponse{
		SavedItems: savedItems,
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
