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
	savedItems := make([]SearchSavedItemResponse, 0, len(result.SavedItems))
	for _, savedItem := range result.SavedItems {
		savedItems = append(savedItems, mapSearchSavedItemResponse(savedItem))
	}

	collections := make([]SearchCollectionResponse, 0, len(result.Collections))
	for _, collection := range result.Collections {
		collections = append(collections, mapSearchCollectionResponse(collection))
	}

	return SearchResponse{
		SavedItems:  savedItems,
		Collections: collections,
	}
}

func mapSearchSavedItemResponse(savedItem SearchSavedItem) SearchSavedItemResponse {
	return SearchSavedItemResponse{
		ID:               savedItem.ID,
		Title:            mapOptionalText(savedItem.Title),
		URL:              savedItem.URL,
		Domain:           mapOptionalText(savedItem.Domain),
		ImageURL:         mapOptionalText(savedItem.ImageURL),
		EnrichmentStatus: savedItem.EnrichmentStatus,
		// The collection is reported as the object it is rather than as a bare
		// id, so a result says both which collection it is in and what that
		// collection is called. Nothing is looked up here: the name came from the
		// same statement that produced the item.
		Collection: SearchSavedItemCollectionInfo{
			ID:   savedItem.Collection.ID,
			Name: savedItem.Collection.Name,
		},
		CreatedAt: savedItem.CreatedAt.Time,
	}
}

func mapSearchCollectionResponse(
	collection SearchCollection,
) SearchCollectionResponse {
	return SearchCollectionResponse{
		ID:   collection.ID,
		Name: collection.Name,
	}
}

// mapOptionalText turns a nullable text column into an optional JSON value.
//
// A column that is NULL in the database becomes JSON null rather than being
// dropped from the payload, so the response shape is the same whether or not
// enrichment has run. Nothing else is ever substituted for the column's value:
// a missing title stays missing rather than becoming the domain or the URL.
func mapOptionalText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}

	text := value.String

	return &text
}
