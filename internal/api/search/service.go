package search

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// Service searches the user's saved items and collections.
//
// Search is one call returning both kinds of result, because the user typed one
// query and the client shows them together. Fuzzy matching is not a separate
// call: it is a fallback inside the one SQL statement, and the ordering it
// produces is what puts a substring match above a typo match.
type Service interface {
	Search(
		ctx context.Context,
		userID uuid.UUID,
		query string,
		limit int,
	) (SearchResult, error)
}

type service struct {
	repository Repository
}

func NewService(
	repository Repository,
) *service {
	return &service{
		repository: repository,
	}
}

// SearchResult holds both halves of one search, each already ordered by
// relevance.
//
// Limit is the saved item limit that was applied, so the caller can report it.
type SearchResult struct {
	SavedItems  []SearchSavedItem
	Collections []SearchCollection
	Limit       int
}

// Search finds the user's saved items and collections matching the query.
//
// The service owns only the query rules and the limits. It does not match, rank
// or filter: the predicate and the score live in the two SQL statements, so
// there is no second place where the same rules could disagree.
func (s *service) Search(
	ctx context.Context,
	userID uuid.UUID,
	query string,
	limit int,
) (SearchResult, error) {
	normalizedQuery, err := normalizeQuery(query)
	if err != nil {
		return SearchResult{}, err
	}

	savedItemLimit := normalizeSavedItemLimit(limit)

	// The two searches are independent reads, so they are issued sequentially
	// against the same pool rather than concurrently. Concurrency here would buy
	// nothing at this scale and would make error handling and connection use
	// harder to reason about.
	savedItems, err := s.repository.SearchSavedItems(
		ctx,
		SearchSavedItemsParams{
			UserID: userID,
			Query:  normalizedQuery,
			Limit:  int32(savedItemLimit),
		},
	)
	if err != nil {
		return SearchResult{}, err
	}

	collections, err := s.repository.SearchCollections(
		ctx,
		SearchCollectionsParams{
			UserID: userID,
			Query:  normalizedQuery,
			// Fixed, never caller controlled.
			Limit: int32(CollectionLimit),
		},
	)
	if err != nil {
		return SearchResult{}, err
	}

	return SearchResult{
		SavedItems:  savedItems,
		Collections: collections,
		Limit:       savedItemLimit,
	}, nil
}

// normalizeQuery trims the submitted query and validates its length.
//
// Surrounding whitespace is stripped, following the convention the collection
// feature already uses for collection names and the saved item feature uses for
// URLs: user input is trimmed, every kind of Unicode whitespace, rather than
// only the plain spaces btrim() removes. A query of nothing but whitespace is
// blank, so it is rejected the same way an empty query is.
//
// The query is NOT lowercased and not otherwise rewritten. Both the substring
// and the fuzzy comparisons are already case-insensitive in PostgreSQL, ILIKE for
// the first and pg_trgm for the second, so lowercasing here would only duplicate
// that. It would also be wrong for the fuzzy side: folding case in Go changes the
// query string that is scored, and the URL substring comparison would then see a
// rewritten needle. URLs and collection names are not altered, only the whitespace
// around the query is removed.
//
// Length is measured in runes after trimming, so a multi-byte query is not
// rejected for being long in bytes. This matches how the collection feature
// measures a name.
func normalizeQuery(query string) (string, error) {
	trimmed := strings.TrimSpace(query)

	if trimmed == "" {
		return "", apperror.BadRequestWith(
			CodeInvalidSearchQuery,
			"search query must not be blank",
			nil,
		)
	}

	length := utf8.RuneCountInString(trimmed)

	if length < QueryMinLength {
		return "", apperror.BadRequestWith(
			CodeInvalidSearchQuery,
			"search query is too short",
			nil,
		)
	}

	if length > QueryMaxLength {
		return "", apperror.BadRequestWith(
			CodeInvalidSearchQuery,
			"search query is too long",
			nil,
		)
	}

	return trimmed, nil
}

// normalizeSavedItemLimit resolves the requested saved item limit.
//
// A limit that is absent or not positive becomes SavedItemDefaultLimit, and a
// limit above SavedItemMaxLimit is clamped down to it. Clamping rather than
// rejecting follows the convention the saved item list already uses for its own
// page limit: the limit bounds the work one request can cause, and an oversized
// request is answered with the largest allowed answer rather than refused. The
// query itself is still validated strictly, because a short or long query has no
// sensible default.
func normalizeSavedItemLimit(limit int) int {
	if limit <= 0 {
		return SavedItemDefaultLimit
	}

	if limit > SavedItemMaxLimit {
		return SavedItemMaxLimit
	}

	return limit
}
