package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/search"
	searchdb "github.com/thoriqr/stash-it-backend/internal/api/search/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	searchdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/search/generated"
)

// newSearchService builds the real search service against the shared test pool.
// There is no HTTP endpoint for search yet, so these tests drive the service
// directly, which is also where the query length rules and the limits live.
func newSearchService(t *testing.T) search.Service {
	t.Helper()

	return search.NewService(
		search.NewRepository(
			searchdb.New(testPool),
		),
	)
}

func truncateSearchData(t *testing.T) {
	t.Helper()

	db := searchdbtest.New(testPool)

	require.NoError(t, db.TruncateSearchData(context.Background()))
}

func createSearchUser(t *testing.T, email string) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	db := searchdbtest.New(testPool)

	userID, err := db.CreateSearchUser(
		ctx,
		searchdbtest.CreateSearchUserParams{
			Email:       email,
			DisplayName: "Search Test User",
		},
	)
	require.NoError(t, err)

	return userID
}

func createSearchCollection(
	t *testing.T,
	userID uuid.UUID,
	name string,
	collectionType string,
	systemKey string,
) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	db := searchdbtest.New(testPool)

	var key pgtype.Text

	if systemKey != "" {
		key = testText(systemKey)
	}

	collectionID, err := db.CreateSearchCollection(
		ctx,
		searchdbtest.CreateSearchCollectionParams{
			UserID:    userID,
			Name:      name,
			Type:      collectionType,
			SystemKey: key,
		},
	)
	require.NoError(t, err)

	return collectionID
}

// createSearchUserWithUnsorted creates a user and the Unsorted system collection
// every permanent user receives, because search includes system collections and a
// search for "unsorted" has to find it.
func createSearchUserWithUnsorted(t *testing.T, email string) (
	uuid.UUID,
	uuid.UUID,
) {
	t.Helper()

	userID := createSearchUser(t, email)

	unsortedID := createSearchCollection(
		t,
		userID,
		"Unsorted",
		"system",
		"unsorted",
	)

	return userID, unsortedID
}

// createSearchSavedItem seeds a saved item. domain and title are empty for NULL,
// which is what the production schema produces today because nothing populates
// them until enrichment runs.
func createSearchSavedItem(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	url string,
	domain string,
	title string,
) uuid.UUID {
	t.Helper()

	return createSearchSavedItemAt(
		t,
		userID,
		collectionID,
		url,
		domain,
		title,
		nil,
	)
}

// createSearchSavedItemAt is createSearchSavedItem with an explicit created_at, so
// the ordering tests can control recency instead of relying on insert order.
func createSearchSavedItemAt(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	url string,
	domain string,
	title string,
	createdAt *time.Time,
) uuid.UUID {
	t.Helper()

	return createSearchSavedItemFull(
		t,
		userID,
		collectionID,
		url,
		domain,
		title,
		"",
		createdAt,
	)
}

// createSearchSavedItemFull seeds a saved item with every field a test may need
// to control. Empty strings become NULL, which is what the production schema
// produces today for domain, title and platform alike.
func createSearchSavedItemFull(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	url string,
	domain string,
	title string,
	platform string,
	createdAt *time.Time,
) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	db := searchdbtest.New(testPool)

	optional := func(value string) pgtype.Text {
		if value == "" {
			return pgtype.Text{}
		}

		return testText(value)
	}

	var createdAtValue pgtype.Timestamptz

	if createdAt != nil {
		createdAtValue = pgtype.Timestamptz{
			Time:  *createdAt,
			Valid: true,
		}
	}

	savedItemID, err := db.CreateSearchSavedItem(
		ctx,
		searchdbtest.CreateSearchSavedItemParams{
			UserID:       userID,
			Url:          url,
			Domain:       optional(domain),
			Title:        optional(title),
			Platform:     optional(platform),
			CollectionID: collectionID,
			CreatedAt:    createdAtValue,
		},
	)
	require.NoError(t, err)

	return savedItemID
}

func searchSavedItemIDs(items []search.SearchSavedItem) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(items))

	for _, item := range items {
		ids = append(ids, item.ID)
	}

	return ids
}

func searchCollectionIDs(collections []search.SearchCollection) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(collections))

	for _, collection := range collections {
		ids = append(ids, collection.ID)
	}

	return ids
}

func indexOfUUID(haystack []uuid.UUID, needle uuid.UUID) int {
	for i, candidate := range haystack {
		if candidate == needle {
			return i
		}
	}

	return -1
}

func TestSearch_Matching(t *testing.T) {
	t.Run("matches a saved item by substring in the url", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "url@example.com")

		matchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://example.org/guides/traveljournal",
			"example.org",
			"",
		)

		result, err := svc.Search(context.Background(), userID, "traveljournal", 0)
		require.NoError(t, err)

		// The query appears only in the url, so this row entered the result set
		// because of the url clause.
		require.Equal(t, []uuid.UUID{matchID}, searchSavedItemIDs(result.SavedItems))
	})

	t.Run("matches a saved item by substring in the domain", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "domain@example.com")

		matchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://photography.example/articles/1",
			"photography.example",
			"",
		)

		result, err := svc.Search(context.Background(), userID, "photography.example", 0)
		require.NoError(t, err)

		require.Equal(t, []uuid.UUID{matchID}, searchSavedItemIDs(result.SavedItems))
	})

	t.Run("matches a saved item by substring in a populated title", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "title@example.com")

		matchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://blog.test/posts/7",
			"blog.test",
			"Best Trip Cameras Of 2026",
		)

		result, err := svc.Search(context.Background(), userID, "Trip Cameras", 0)
		require.NoError(t, err)

		// title is NULL for every item saved in production today, so it is only
		// searchable once enrichment has run. This proves the clause works when
		// there is something in it.
		require.Equal(t, []uuid.UUID{matchID}, searchSavedItemIDs(result.SavedItems))
		require.True(t, result.SavedItems[0].Title.Valid)
		require.Equal(t, "Best Trip Cameras Of 2026", result.SavedItems[0].Title.String)
	})

	t.Run("matches a saved item by fuzzy word similarity after a typo", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "typo@example.com")

		matchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://sourdough.example/guides/bread",
			"sourdough.example",
			"",
		)

		// "sourdogh" is not a substring of anything here. The row can only be in the
		// result because word_similarity cleared 0.3.
		result, err := svc.Search(context.Background(), userID, "sourdogh", 0)
		require.NoError(t, err)

		require.Equal(t, []uuid.UUID{matchID}, searchSavedItemIDs(result.SavedItems))
	})

	t.Run("matches a collection by name substring", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, _ := createSearchUserWithUnsorted(t, "collection@example.com")

		matchID := createSearchCollection(
			t,
			userID,
			"Camera Gear",
			"user",
			"",
		)

		result, err := svc.Search(context.Background(), userID, "Camera Gear", 0)
		require.NoError(t, err)

		require.Equal(
			t,
			[]uuid.UUID{matchID},
			searchCollectionIDs(result.Collections),
		)
	})

	t.Run("matches a collection partially by name", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, _ := createSearchUserWithUnsorted(t, "partial@example.com")

		matchID := createSearchCollection(t, userID, "Recipes", "user", "")

		result, err := svc.Search(context.Background(), userID, "rec", 0)
		require.NoError(t, err)

		require.Equal(
			t,
			[]uuid.UUID{matchID},
			searchCollectionIDs(result.Collections),
		)
	})

	t.Run("returns an empty result when nothing is a meaningful match", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "noise@example.com")

		createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://example.com/articles/1",
			"example.com",
			"Camera Buying Guide",
		)
		createSearchCollection(t, userID, "Wishlist", "user", "")

		result, err := svc.Search(context.Background(), userID, "zzzqqq", 0)
		require.NoError(t, err)

		// The fuzzy threshold is what keeps this empty. Without it every row would
		// come back as a weak match, which is not something a user can act on.
		require.Empty(t, result.SavedItems)
		require.Empty(t, result.Collections)
	})

	t.Run("does not match on a populated platform", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "platform@example.com")

		// platform is populated with exactly the query, and none of the three
		// searchable fields contains it. platform is not a searchable field, so this
		// row must not come back.
		createSearchSavedItemFull(
			t,
			userID,
			unsortedID,
			"https://example.com/watch/abc",
			"example.com",
			"",
			"youtube",
			nil,
		)

		result, err := svc.Search(context.Background(), userID, "youtube", 0)
		require.NoError(t, err)

		require.Empty(t, result.SavedItems)
	})

	t.Run("carries the collection id on every saved item result", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, _ := createSearchUserWithUnsorted(t, "collectionid@example.com")

		gearID := createSearchCollection(t, userID, "Camera Gear", "user", "")

		createSearchSavedItem(
			t,
			userID,
			gearID,
			"https://example.com/cameras",
			"example.com",
			"",
		)

		result, err := svc.Search(context.Background(), userID, "cameras", 0)
		require.NoError(t, err)

		// The client needs to show where an item currently lives, so the result
		// carries the collection the item is actually in.
		require.Len(t, result.SavedItems, 1)
		require.Equal(t, gearID, result.SavedItems[0].CollectionID)
	})
}

func TestSearch_UserIsolation(t *testing.T) {
	t.Run("does not return another user's saved items", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)

		ownerID, ownerUnsorted := createSearchUserWithUnsorted(t, "owner@example.com")
		otherID, otherUnsorted := createSearchUserWithUnsorted(t, "other@example.com")

		ownerItemID := createSearchSavedItem(
			t,
			ownerID,
			ownerUnsorted,
			"https://shared.example/cameras",
			"shared.example",
			"",
		)

		otherItemID := createSearchSavedItem(
			t,
			otherID,
			otherUnsorted,
			"https://shared.example/cameras",
			"shared.example",
			"",
		)

		require.NotEqual(t, ownerItemID, otherItemID)

		result, err := svc.Search(context.Background(), ownerID, "cameras", 0)
		require.NoError(t, err)

		// Both users own an item with identical searchable text. Owner scoping is
		// decided in SQL, so the other user's row cannot leak in.
		require.Equal(
			t,
			[]uuid.UUID{ownerItemID},
			searchSavedItemIDs(result.SavedItems),
		)
	})

	t.Run("does not return another user's collections", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)

		ownerID, _ := createSearchUserWithUnsorted(t, "owner2@example.com")
		otherID, _ := createSearchUserWithUnsorted(t, "other2@example.com")

		ownerCollectionID := createSearchCollection(t, ownerID, "Camera Gear", "user", "")
		otherCollectionID := createSearchCollection(t, otherID, "Camera Gear", "user", "")

		require.NotEqual(t, ownerCollectionID, otherCollectionID)

		result, err := svc.Search(context.Background(), ownerID, "Camera Gear", 0)
		require.NoError(t, err)

		require.Equal(
			t,
			[]uuid.UUID{ownerCollectionID},
			searchCollectionIDs(result.Collections),
		)
	})
}

func TestSearch_SystemCollections(t *testing.T) {
	t.Run("finds the Unsorted system collection", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "system@example.com")

		result, err := svc.Search(context.Background(), userID, "unsorted", 0)
		require.NoError(t, err)

		require.Contains(t, searchCollectionIDs(result.Collections), unsortedID)
	})

	t.Run("returns the system collection with its type and key", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "systemkey@example.com")

		result, err := svc.Search(context.Background(), userID, "unsorted", 0)
		require.NoError(t, err)

		index := indexOfUUID(searchCollectionIDs(result.Collections), unsortedID)
		require.NotEqual(t, -1, index)

		found := result.Collections[index]

		// Without the type and the system key the client cannot tell a system
		// collection from one of the user's own.
		require.Equal(t, "system", found.Type)
		require.True(t, found.SystemKey.Valid)
		require.Equal(t, "unsorted", found.SystemKey.String)
	})

	t.Run("finds a named system collection", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, _ := createSearchUserWithUnsorted(t, "namedsystem@example.com")

		youtubeID := createSearchCollection(
			t,
			userID,
			"YouTube",
			"system",
			"youtube",
		)

		result, err := svc.Search(context.Background(), userID, "youtube", 0)
		require.NoError(t, err)

		require.Contains(t, searchCollectionIDs(result.Collections), youtubeID)
	})
}

func TestSearch_Ranking(t *testing.T) {
	t.Run("ranks an exact substring match above a fuzzy-only match", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "rank@example.com")

		// "sourdough" appears verbatim in this url, so it is a substring match.
		substringMatchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://example.com/sourdough-guide",
			"example.com",
			"",
		)

		// "sourdogh" is a typo and appears in no field of this row, so it can only
		// match fuzzily.
		fuzzyMatchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://recipecard.io/sourdogh-starter",
			"recipecard.io",
			"",
		)

		result, err := svc.Search(context.Background(), userID, "sourdough", 0)
		require.NoError(t, err)

		ids := searchSavedItemIDs(result.SavedItems)

		require.Contains(t, ids, substringMatchID)
		require.Contains(t, ids, fuzzyMatchID)

		substringIndex := indexOfUUID(ids, substringMatchID)
		fuzzyIndex := indexOfUUID(ids, fuzzyMatchID)

		// The exact substring result comes first. This is the whole point of the
		// 10.0 bonus: the largest possible fuzzy-only score is 2.4, so no amount of
		// fuzzy agreement can push a typo match above a verbatim one.
		require.Less(t, substringIndex, fuzzyIndex)
	})

	t.Run("ranks a title substring match above a url fuzzy-only match", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "ranktitle@example.com")

		substringMatchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://blog.test/posts/1",
			"blog.test",
			"Sourdough Baking Notes",
		)

		fuzzyMatchID := createSearchSavedItem(
			t,
			userID,
			unsortedID,
			"https://sourdogh.example/intro",
			"sourdogh.example",
			"",
		)

		result, err := svc.Search(context.Background(), userID, "sourdough", 0)
		require.NoError(t, err)

		ids := searchSavedItemIDs(result.SavedItems)

		require.Less(
			t,
			indexOfUUID(ids, substringMatchID),
			indexOfUUID(ids, fuzzyMatchID),
		)
	})

	t.Run("ranks a substring collection above a fuzzy-only collection", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, _ := createSearchUserWithUnsorted(t, "rankcoll@example.com")

		substringMatchID := createSearchCollection(
			t,
			userID,
			"Camera Gear",
			"user",
			"",
		)
		fuzzyMatchID := createSearchCollection(
			t,
			userID,
			"Cama Gear",
			"user",
			"",
		)

		result, err := svc.Search(context.Background(), userID, "Camera", 0)
		require.NoError(t, err)

		ids := searchCollectionIDs(result.Collections)

		require.Contains(t, ids, substringMatchID)
		require.Contains(t, ids, fuzzyMatchID)
		require.Less(
			t,
			indexOfUUID(ids, substringMatchID),
			indexOfUUID(ids, fuzzyMatchID),
		)
	})

	t.Run("does not let recency beat relevance", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "recency@example.com")

		now := time.Now()

		// The fuzzy-only match is much newer than the substring match. Recency is a
		// tiebreaker only, so it must not lift the weaker match to the top.
		substringMatchID := createSearchSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/sourdough-guide",
			"example.com",
			"",
			ptrTime(now.Add(-90*24*time.Hour)),
		)
		fuzzyMatchID := createSearchSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://recipecard.io/sourdogh-starter",
			"recipecard.io",
			"",
			ptrTime(now),
		)

		result, err := svc.Search(context.Background(), userID, "sourdough", 0)
		require.NoError(t, err)

		ids := searchSavedItemIDs(result.SavedItems)

		require.Less(
			t,
			indexOfUUID(ids, substringMatchID),
			indexOfUUID(ids, fuzzyMatchID),
		)
	})
}

func TestSearch_Ordering(t *testing.T) {
	t.Run("orders ties by created_at descending then by id descending", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "tie@example.com")

		now := time.Now().Truncate(time.Microsecond)

		// Every row below contains the query as a substring, so every row earns the
		// same 10.0 bonus and the three are ordered by the tiebreakers alone. The
		// fuzzy contributions differ only by length, so created_at is what decides
		// the first two.
		olderID := createSearchSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/zebra",
			"example.com",
			"",
			ptrTime(now.Add(-48*time.Hour)),
		)
		newerID := createSearchSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/zebra-two",
			"example.com",
			"",
			ptrTime(now.Add(-24*time.Hour)),
		)

		result, err := svc.Search(context.Background(), userID, "zebra", 0)
		require.NoError(t, err)

		ids := searchSavedItemIDs(result.SavedItems)

		require.Equal(t, []uuid.UUID{newerID, olderID}, ids)
	})

	t.Run("breaks a full tie on score and recency with id descending", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "fulltie@example.com")

		sameInstant := time.Now().Truncate(time.Microsecond)

		// Identical url, identical created_at, so score and recency both tie across
		// all three rows. Only id can order them, and it must do so descending every
		// time.
		firstID := createSearchSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/zebra",
			"example.com",
			"",
			ptrTime(sameInstant),
		)
		secondID := createSearchSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/zebra",
			"example.com",
			"",
			ptrTime(sameInstant),
		)
		thirdID := createSearchSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/zebra",
			"example.com",
			"",
			ptrTime(sameInstant),
		)

		// ids are uuidv7, so they are time ordered as well as random. Sorting them
		// descending here is what "ordered by id DESC" means, expressed as the
		// expectation rather than as a hardcoded sequence.
		expected := []uuid.UUID{firstID, secondID, thirdID}
		slices.SortFunc(expected, func(a, b uuid.UUID) int {
			return bytes.Compare(b[:], a[:])
		})

		for attempt := range 5 {
			result, err := svc.Search(
				context.Background(),
				userID,
				"zebra",
				0,
			)
			require.NoError(t, err, "attempt %d", attempt)

			require.Equal(
				t,
				expected,
				searchSavedItemIDs(result.SavedItems),
				"attempt %d",
				attempt,
			)
		}
	})
}

func TestSearch_Limits(t *testing.T) {
	t.Run("returns the default number of saved items when no limit is given", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "default@example.com")

		// More matches than the default limit, so a default that was not applied
		// would return all of them.
		seedSearchSavedItems(t, userID, unsortedID, "zebra", 25)

		result, err := svc.Search(context.Background(), userID, "zebra", 0)
		require.NoError(t, err)

		require.Len(t, result.SavedItems, search.SavedItemDefaultLimit)
		require.Equal(t, search.SavedItemDefaultLimit, result.Limit)
	})

	t.Run("never returns more than the maximum number of saved items", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "max@example.com")

		seedSearchSavedItems(t, userID, unsortedID, "zebra", 60)

		// An oversized request is clamped rather than rejected, so the answer is the
		// largest one allowed rather than an error.
		result, err := svc.Search(
			context.Background(),
			userID,
			"zebra",
			search.SavedItemMaxLimit+100,
		)
		require.NoError(t, err)

		require.Len(t, result.SavedItems, search.SavedItemMaxLimit)
		require.Equal(t, search.SavedItemMaxLimit, result.Limit)
	})

	t.Run("honours a requested saved item limit within the bounds", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "honour@example.com")

		seedSearchSavedItems(t, userID, unsortedID, "zebra", 25)

		result, err := svc.Search(context.Background(), userID, "zebra", 5)
		require.NoError(t, err)

		require.Len(t, result.SavedItems, 5)
		require.Equal(t, 5, result.Limit)
	})

	t.Run("never returns more than five collections", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, _ := createSearchUserWithUnsorted(t, "collmax@example.com")

		for i := range 8 {
			createSearchCollection(
				t,
				userID,
				"Zebra Collection "+string(rune('a'+i)),
				"user",
				"",
			)
		}

		// Even the largest request the service accepts leaves collections capped,
		// because the collection limit is fixed and not caller controlled.
		result, err := svc.Search(
			context.Background(),
			userID,
			"Zebra",
			search.SavedItemMaxLimit,
		)
		require.NoError(t, err)

		require.Len(t, result.Collections, search.CollectionLimit)
	})

	t.Run("rejects a query shorter than the minimum without querying", func(t *testing.T) {
		truncateSearchData(t)

		svc := newSearchService(t)
		userID, unsortedID := createSearchUserWithUnsorted(t, "short@example.com")

		seedSearchSavedItems(t, userID, unsortedID, "zebra", 5)

		_, err := svc.Search(context.Background(), userID, "a", 0)
		require.Error(t, err)
		require.Equal(t, search.CodeInvalidSearchQuery, apperror.FromError(err).Code)
	})
}

// seedSearchSavedItems creates count saved items that all contain match in their
// url, so a limit test has more matches than any limit it will ask for.
func seedSearchSavedItems(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	match string,
	count int,
) {
	t.Helper()

	for i := range count {
		createSearchSavedItem(
			t,
			userID,
			collectionID,
			fmt.Sprintf("https://example.com/%s/%d", match, i),
			"example.com",
			"",
		)
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
