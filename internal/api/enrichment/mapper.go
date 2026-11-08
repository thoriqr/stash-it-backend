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
		PriceAmount:      mapOptionalNumeric(savedItem.PriceAmount),
		PriceCurrency:    mapOptionalText(savedItem.PriceCurrency),
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

// mapOptionalNumeric renders an exact numeric as its decimal string.
//
// The response carries a price amount as a string rather than a number so a client
// never routes money through a floating point type. pgtype.Numeric already holds the
// exact value and its own decimal rendering, so no recalculation happens here, and a
// NULL column stays null in the response.
func mapOptionalNumeric(value pgtype.Numeric) *string {
	if !value.Valid {
		return nil
	}

	rendered, err := value.Value()
	if err != nil {
		return nil
	}

	text, ok := rendered.(string)
	if !ok {
		return nil
	}

	return &text
}
