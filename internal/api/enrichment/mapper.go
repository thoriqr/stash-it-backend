package enrichment

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func mapSavedItemResponse(savedItem SavedItem) SavedItemResponse {
	return SavedItemResponse{
		ID:               savedItem.ID,
		URL:              savedItem.Url,
		Domain:           mapOptionalText(savedItem.Domain),
		Platform:         mapOptionalText(savedItem.Platform),
		Title:            mapOptionalText(savedItem.Title),
		Description:      mapOptionalText(savedItem.Description),
		ImageURL:         mapOptionalText(savedItem.ImageURL),
		CollectionID:     savedItem.CollectionID,
		EnrichmentStatus: savedItem.EnrichmentStatus,
		LastEnrichedAt:   mapOptionalTimestamp(savedItem.LastEnrichedAt),
		CreatedAt:        savedItem.CreatedAt.Time,
		UpdatedAt:        savedItem.UpdatedAt.Time,
	}
}

func mapEnrichSavedItemResponse(result EnrichResult) EnrichSavedItemResponse {
	return EnrichSavedItemResponse{
		SavedItem: mapSavedItemResponse(result.SavedItem),
	}
}

func mapOptionalText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}

	text := value.String

	return &text
}

func mapOptionalTimestamp(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}

	timestamp := value.Time

	return &timestamp
}
