package search

import (
	"github.com/jackc/pgx/v5/pgtype"
)

// mapSearchResponse maps the service result onto the API response.
//
// The projection is taken straight from the service and never re-ordered here.
// Relevance and order are decided by the search SQL, and re-sorting in Go would
// be a second place where the same rules could disagree with the database.
//
// The relevance score is read by the search SQL to decide this order and is then
// dropped. It is internal to how the results were ranked, and it is not part of
// the response contract.
func mapSearchResponse(result SearchResult) SearchResponse {
	collections := make([]SearchCollectionResponse, 0, len(result.Collections))
	for _, collection := range result.Collections {
		collections = append(collections, mapSearchCollectionResponse(collection))
	}

	savedItems := make([]SearchSavedItemResponse, 0, len(result.SavedItems))
	for _, savedItem := range result.SavedItems {
		savedItems = append(savedItems, mapSearchSavedItemResponse(savedItem))
	}

	return SearchResponse{
		Collections: collections,
		SavedItems:  savedItems,
	}
}

func mapSearchSavedItemResponse(savedItem SearchSavedItem) SearchSavedItemResponse {
	return SearchSavedItemResponse{
		ID:           savedItem.ID,
		URL:          savedItem.URL,
		Domain:       mapOptionalText(savedItem.Domain),
		Title:        mapOptionalText(savedItem.Title),
		CollectionID: savedItem.CollectionID,
		CreatedAt:    savedItem.CreatedAt.Time,
		UpdatedAt:    savedItem.UpdatedAt.Time,
	}
}

func mapSearchCollectionResponse(
	collection SearchCollection,
) SearchCollectionResponse {
	return SearchCollectionResponse{
		ID:        collection.ID,
		Name:      collection.Name,
		Type:      collection.Type,
		SystemKey: mapOptionalText(collection.SystemKey),
		CreatedAt: collection.CreatedAt.Time,
		UpdatedAt: collection.UpdatedAt.Time,
	}
}

// mapOptionalText turns a nullable text column into an optional JSON value.
//
// A column that is NULL in the database becomes JSON null rather than being
// dropped from the payload, so the response shape is the same whether or not
// enrichment has run.
func mapOptionalText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}

	text := value.String

	return &text
}
