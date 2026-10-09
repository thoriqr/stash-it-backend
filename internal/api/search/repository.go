package search

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	searchdb "github.com/thoriqr/stash-it-backend/internal/api/search/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// SearchSavedItemsParams is what a saved item search is asked for.
//
// The params are this package's own type rather than the generated
// SearchSavedItemsParams, so the service does not depend on sqlc's naming or on
// the generated struct staying shaped the same way.
//
// Query is a string here even though sqlc infers pgtype.Text for it, because the
// query is a validated non-empty value by the time it reaches here. The
// repository is what adapts it for the driver.
type SearchSavedItemsParams struct {
	UserID uuid.UUID
	Query  string
	Limit  int32
}

// SearchCollectionsParams is what a collection search is asked for.
type SearchCollectionsParams struct {
	UserID uuid.UUID
	Query  string
	Limit  int32
}

type Repository interface {
	SearchSavedItems(
		ctx context.Context,
		params SearchSavedItemsParams,
	) ([]SearchSavedItem, error)

	SearchCollections(
		ctx context.Context,
		params SearchCollectionsParams,
	) ([]SearchCollection, error)
}

type repository struct {
	queries *searchdb.Queries
}

// NewRepository builds the search repository.
//
// No *pgxpool.Pool is taken and no transaction is used: both queries are
// read-only, single statements, and neither needs a consistent snapshot across
// more than one statement.
func NewRepository(
	queries *searchdb.Queries,
) Repository {
	return &repository{
		queries: queries,
	}
}

// SearchSavedItems returns the user's saved items matching the query, best match
// first.
//
// Matching and ranking both happen in SQL. The predicate decides whether a row
// is a result at all, and the score decides the order, so the service cannot
// disagree with the database about either.
//
// The collection each result lives in is joined in by the same statement, so
// naming it costs no extra query per result and cannot be answered from a
// different snapshot than the item it belongs to.
func (r *repository) SearchSavedItems(
	ctx context.Context,
	params SearchSavedItemsParams,
) ([]SearchSavedItem, error) {
	rows, err := r.queries.SearchSavedItems(
		ctx,
		searchdb.SearchSavedItemsParams{
			UserID:      params.UserID,
			Query:       searchText(params.Query),
			ResultLimit: params.Limit,
		},
	)
	if err != nil {
		return nil, apperror.Internal(err)
	}

	// An empty result is an empty slice, never nil, so a caller can range over it
	// and marshal it to an empty JSON array rather than null.
	savedItems := make([]SearchSavedItem, 0, len(rows))
	for _, row := range rows {
		savedItems = append(savedItems, SearchSavedItem{
			ID:    row.ID,
			Title: row.Title,
			URL:   row.Url,
			// Domain and ImageURL are passed through with the nullability the
			// columns have. Neither is ever replaced by a value derived from the
			// URL, so an unread page reports what is stored and nothing else.
			Domain:           row.Domain,
			ImageURL:         row.ImageUrl,
			EnrichmentStatus: row.EnrichmentStatus,
			Collection: SearchCollectionRef{
				ID:   row.CollectionID,
				Name: row.CollectionName,
			},
			CreatedAt: row.CreatedAt,
			Score:     row.Score,
		})
	}

	return savedItems, nil
}

// SearchCollections returns the user's collections matching the query, best match
// first.
//
// System collections are included. Matching and ranking are decided in SQL, as
// for saved items.
func (r *repository) SearchCollections(
	ctx context.Context,
	params SearchCollectionsParams,
) ([]SearchCollection, error) {
	rows, err := r.queries.SearchCollections(
		ctx,
		searchdb.SearchCollectionsParams{
			UserID:      params.UserID,
			Query:       searchText(params.Query),
			ResultLimit: params.Limit,
		},
	)
	if err != nil {
		return nil, apperror.Internal(err)
	}

	collections := make([]SearchCollection, 0, len(rows))
	for _, row := range rows {
		collections = append(collections, SearchCollection{
			ID:    row.ID,
			Name:  row.Name,
			Score: row.Score,
		})
	}

	return collections, nil
}

// searchText adapts the validated query string for the driver.
//
// sqlc infers pgtype.Text for the query parameter because it is only ever seen
// concatenated with a literal in the SQL. The service guarantees the query is
// non-empty by the time it arrives, so it is sent as a present value rather than
// NULL, which keeps the SQL's word_similarity and ILIKE arguments non-null.
func searchText(query string) pgtype.Text {
	return pgtype.Text{
		String: query,
		Valid:  true,
	}
}
