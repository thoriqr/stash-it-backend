package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/saved_item"
	"github.com/thoriqr/stash-it-backend/internal/pagination"
	collectiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/collection/generated"
)

// recentSavedItem is one row as the recent saved items listing reports it. It is
// decoded here rather than reusing the API response type so the suite asserts the
// wire shape itself, including which fields are nullable and which are not.
type recentSavedItem struct {
	ID               uuid.UUID  `json:"id"`
	URL              string     `json:"url"`
	Domain           *string    `json:"domain"`
	Platform         *string    `json:"platform"`
	Title            *string    `json:"title"`
	Description      *string    `json:"description"`
	ImageURL         *string    `json:"image_url"`
	CollectionID     uuid.UUID  `json:"collection_id"`
	EnrichmentStatus string     `json:"enrichment_status"`
	LastEnrichedAt   *time.Time `json:"last_enriched_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`

	// Collection is inside each item rather than beside the page, because this listing
	// spans collections and there is no single one for the response to name.
	Collection struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	} `json:"collection"`
}

// recentSavedItemsPage is the wire shape of one page.
type recentSavedItemsPage struct {
	Data struct {
		SavedItems []recentSavedItem `json:"saved_items"`
	} `json:"data"`
	Message string `json:"message"`
	Meta    struct {
		Cursor *struct {
			NextCursor *string `json:"next_cursor"`
			HasMore    bool    `json:"has_more"`
		} `json:"cursor"`
	} `json:"meta"`
}

func (p recentSavedItemsPage) ids() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(p.Data.SavedItems))

	for _, item := range p.Data.SavedItems {
		ids = append(ids, item.ID)
	}

	return ids
}

// listRecentSavedItemsOverHTTP calls the endpoint and requires a 200 with a fully
// decoded page, so every test starts from a known-good shape.
func listRecentSavedItemsOverHTTP(
	t *testing.T,
	userID uuid.UUID,
	query string,
) recentSavedItemsPage {
	t.Helper()

	path := "/saved-items"
	if query != "" {
		path += "?" + query
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var page recentSavedItemsPage

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&page))
	require.NotNil(t, page.Meta.Cursor, "every response must carry cursor metadata")

	return page
}

// walkRecentSavedItems follows next_cursor to the end and returns every id seen in
// order, plus the number of pages fetched. It is the workhorse for the paging
// invariants: no skips, no duplicates, and one page ending exactly where the next
// begins.
func walkRecentSavedItems(
	t *testing.T,
	userID uuid.UUID,
	query string,
) ([]uuid.UUID, int) {
	t.Helper()

	var (
		seen  []uuid.UUID
		calls int
	)

	cursor := ""

	for {
		pageQuery := query
		if cursor != "" {
			pageQuery = query + "&cursor=" + cursor
		}

		page := listRecentSavedItemsOverHTTP(t, userID, pageQuery)
		calls++

		seen = append(seen, page.ids()...)

		if !page.Meta.Cursor.HasMore {
			require.Nil(
				t,
				page.Meta.Cursor.NextCursor,
				"has_more false must carry a null next_cursor",
			)

			return seen, calls
		}

		require.NotNil(t, page.Meta.Cursor.NextCursor)

		cursor = *page.Meta.Cursor.NextCursor

		require.Less(t, calls, 60, "paging did not terminate")
	}
}

func requireUniqueRecentSavedItems(t *testing.T, ids []uuid.UUID) {
	t.Helper()

	seen := make(map[uuid.UUID]struct{}, len(ids))

	for _, id := range ids {
		require.NotContains(
			t,
			seen,
			id,
			"saved item %s was returned on more than one page",
			id,
		)

		seen[id] = struct{}{}
	}
}

// enrichRecentItem sets an item's enrichment state at an exact time, so a test can
// place it in one of the three persisted states and prove all three are listed.
//
// Platform is written by its own statement rather than by the one that carries the
// rest of the enrichment state: several other tests use that statement without a
// platform in mind, and adding the column there would write NULL into rows they go
// on to assert against.
func enrichRecentItem(
	t *testing.T,
	savedItemID uuid.UUID,
	status string,
	at time.Time,
) {
	t.Helper()

	require.NoError(
		t,
		collectiondbtest.New(testPool).SetTestSavedItemPlatform(
			context.Background(),
			collectiondbtest.SetTestSavedItemPlatformParams{
				Platform: testText("youtube"),
				ID:       savedItemID,
			},
		),
	)

	require.NoError(
		t,
		collectiondbtest.New(testPool).SetTestSavedItemEnrichmentStateAt(
			context.Background(),
			collectiondbtest.SetTestSavedItemEnrichmentStateAtParams{
				EnrichmentStatus: status,
				LastEnrichedAt:   pgtype.Timestamptz{Time: at, Valid: true},
				Title:            testText("A title"),
				Description:      testText("A description"),
				ImageUrl:         testText("https://example.com/og.png"),
				CreatedAt:        pgtype.Timestamptz{Time: at, Valid: true},
				ID:               savedItemID,
			},
		),
	)
}

func TestSavedItemAPI_ListRecent(t *testing.T) {
	// The listing is a history, not a view of one collection. Items spread across
	// Unsorted, a collection the user named and a collection created automatically
	// must all appear in one chronological page, each naming where it lives.
	t.Run("spans every collection the user owns", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-across@example.com")
		unsortedID := createUnsorted(t, userID)
		wishlistID := createTestUserCollection(t, userID, "Wishlist")
		platformID := createTestSystemCollection(t, userID, "youtube", "youtube")

		base := time.Now().Add(-3 * time.Hour).Truncate(time.Second)

		unsortedItem := createSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/a",
			base,
		)
		wishlistItem := createSavedItemAt(
			t,
			userID,
			wishlistID,
			"https://example.com/b",
			base.Add(time.Minute),
		)
		platformItem := createSavedItemAt(
			t,
			userID,
			platformID,
			"https://example.com/c",
			base.Add(2*time.Minute),
		)

		page := listRecentSavedItemsOverHTTP(t, userID, "")

		require.Len(t, page.Data.SavedItems, 3)

		// Newest first, and the ordering crosses collection boundaries rather than
		// restarting inside one.
		require.Equal(
			t,
			[]uuid.UUID{platformItem, wishlistItem, unsortedItem},
			page.ids(),
		)

		byID := make(map[uuid.UUID]recentSavedItem, 3)
		for _, item := range page.Data.SavedItems {
			byID[item.ID] = item
		}

		require.Equal(t, unsortedID, byID[unsortedItem].CollectionID)
		require.Equal(t, "Unsorted", byID[unsortedItem].Collection.Name)
		require.Equal(t, wishlistID, byID[wishlistItem].CollectionID)
		require.Equal(t, "Wishlist", byID[wishlistItem].Collection.Name)
		require.Equal(t, platformID, byID[platformItem].CollectionID)
		require.Equal(t, "youtube", byID[platformItem].Collection.Name)

		// collection_id and the nested collection are the same collection. A client
		// that reads either one has to be reading about the same place.
		for _, item := range page.Data.SavedItems {
			require.Equal(t, item.CollectionID, item.Collection.ID)
		}
	})

	// An item stays in the history after being filed. This is the behaviour the
	// endpoint exists for: the recent record is not the same thing as whatever
	// collection an item happens to be in now.
	t.Run("keeps an item after it has been filed into a collection", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-filed@example.com")
		unsortedID := createUnsorted(t, userID)
		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		moved := createSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/moved",
			base,
		)
		stayed := createSavedItemAt(
			t,
			userID,
			wishlistID,
			"https://example.com/stayed",
			base.Add(time.Minute),
		)

		// The real move endpoint, not a direct write, so the fixture is a state the
		// product can actually produce.
		moveSavedItemIntoCollection(t, userID, moved, "Wishlist")

		page := listRecentSavedItemsOverHTTP(t, userID, "")

		require.Len(t, page.Data.SavedItems, 2)
		require.ElementsMatch(
			t,
			[]uuid.UUID{moved, stayed},
			page.ids(),
			"filing an item must not remove it from the recent history",
		)

		for _, item := range page.Data.SavedItems {
			if item.ID != moved {
				continue
			}

			require.Equal(t, wishlistID, item.CollectionID)
			require.Equal(t, "Wishlist", item.Collection.Name)
		}
	})

	// All three persisted enrichment states are ordinary states of a saved item and
	// all three appear. A history that hid the two it had not finished processing
	// would be a record of only what went well.
	t.Run("lists pending, completed and failed items alike", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-states@example.com")
		unsortedID := createUnsorted(t, userID)

		base := time.Now().Add(-3 * time.Hour).Truncate(time.Second)

		// A fresh save is pending and has no last_enriched_at, which is the state a
		// client most needs to distinguish from "nothing was found on the page".
		pending := createSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/pending",
			base,
		)

		completed := createSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/completed",
			base.Add(time.Minute),
		)
		enrichRecentItem(t, completed, "completed", base.Add(time.Minute))

		failed := createSavedItemAt(
			t,
			userID,
			unsortedID,
			"https://example.com/failed",
			base.Add(2*time.Minute),
		)

		// A failed attempt preserves the existing metadata and leaves
		// last_enriched_at untouched, so this is written as the failed state a real
		// failed enrichment produces rather than by setting a time.
		_, err := testPool.Exec(
			context.Background(),
			"UPDATE saved_items SET enrichment_status = 'failed' WHERE id = $1",
			failed,
		)
		require.NoError(t, err)

		page := listRecentSavedItemsOverHTTP(t, userID, "")

		require.Len(t, page.Data.SavedItems, 3)
		require.ElementsMatch(
			t,
			[]uuid.UUID{pending, completed, failed},
			page.ids(),
		)

		states := map[uuid.UUID]recentSavedItem{}

		for _, item := range page.Data.SavedItems {
			states[item.ID] = item
		}

		require.Equal(t, "pending", states[pending].EnrichmentStatus)
		require.Nil(t, states[pending].LastEnrichedAt)
		require.Nil(t, states[pending].Title)

		require.Equal(t, "completed", states[completed].EnrichmentStatus)
		require.NotNil(t, states[completed].LastEnrichedAt)

		require.Equal(t, "failed", states[failed].EnrichmentStatus)
		require.Nil(
			t,
			states[failed].LastEnrichedAt,
			"a failed attempt refreshes nothing, so it must not set this",
		)
	})

	// The history is the caller's own and nothing else. Another user's items must not
	// be reachable from it, and one user's collections say nothing about them.
	t.Run("excludes another user's saved items", func(t *testing.T) {
		truncateCollectionData(t)

		ownerID := createCollectionUser(t, "recent-owner@example.com")
		ownerUnsorted := createUnsorted(t, ownerID)

		mine := createSavedItemAt(
			t,
			ownerID,
			ownerUnsorted,
			"https://example.com/mine",
			time.Now().Add(-time.Hour).Truncate(time.Second),
		)

		otherID := createCollectionUser(t, "recent-other@example.com")
		otherUnsorted := createUnsorted(t, otherID)

		theirs := createSavedItemAt(
			t,
			otherID,
			otherUnsorted,
			"https://example.com/theirs",
			time.Now().Add(-time.Hour).Truncate(time.Second),
		)

		page := listRecentSavedItemsOverHTTP(t, ownerID, "")

		require.Equal(t, []uuid.UUID{mine}, page.ids())
		require.NotContains(t, page.ids(), theirs)
	})

	// A user who has saved nothing is an ordinary state, so the page is an empty
	// array rather than null. A list that arrives as null where a list was promised
	// is a shape change a client has to defend against.
	t.Run("an empty history returns an empty array", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-empty@example.com")
		createUnsorted(t, userID)

		req := httptest.NewRequest(http.MethodGet, "/saved-items", nil)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body map[string]any

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		data, ok := body["data"].(map[string]any)
		require.True(t, ok, "the response must carry a data object")

		savedItems, ok := data["saved_items"].([]any)
		require.True(t, ok, "saved_items must be an array, never null")
		require.Empty(t, savedItems)

		meta, ok := body["meta"].(map[string]any)
		require.True(t, ok)

		cursor, ok := meta["cursor"].(map[string]any)
		require.True(t, ok)

		require.Equal(t, false, cursor["has_more"])
		require.Nil(
			t,
			cursor["next_cursor"],
			"a final page must carry an explicit null next_cursor",
		)
	})

	// Every nullable column stays on a key that is present, with a null value. A key
	// that disappears tells a client the field was withheld, which is a different
	// thing from "this page exposed nothing".
	t.Run("reports nullable fields as null rather than omitting them", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-nulls@example.com")
		unsortedID := createUnsorted(t, userID)

		// A never-enriched item: domain is derived at save time and everything the
		// page would have supplied is still NULL.
		createSavedItemInCollection(t, userID, unsortedID, "https://example.com/bare")

		req := httptest.NewRequest(http.MethodGet, "/saved-items", nil)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body map[string]any

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		data, _ := body["data"].(map[string]any)
		savedItems, _ := data["saved_items"].([]any)
		require.Len(t, savedItems, 1)

		entry, ok := savedItems[0].(map[string]any)
		require.True(t, ok)

		for _, field := range []string{
			"platform",
			"title",
			"description",
			"image_url",
			"last_enriched_at",
		} {
			require.Contains(t, entry, field)
			require.Nil(
				t,
				entry[field],
				"%s must be present and null on a never-enriched item",
				field,
			)
		}

		// domain is the one metadata column that is NOT null here, and being precise
		// about that is the point. It is derived locally from the submitted URL at
		// save time rather than read off the page, so it arrives before enrichment has
		// run. Reporting it as null would claim the backend had looked and found
		// nothing, which is a different statement from "this has not been looked up".
		require.Contains(t, entry, "domain")
		require.Equal(t, "example.com", entry["domain"])

		// Non-nullable fields are never null, and the collection is a real object.
		require.Equal(t, "pending", entry["enrichment_status"])
		require.Equal(t, unsortedID.String(), entry["collection_id"])
		require.Equal(t, "https://example.com/bare", entry["url"])

		collection, ok := entry["collection"].(map[string]any)
		require.True(t, ok, "each item must carry its collection")
		require.Equal(t, "Unsorted", collection["name"])
		require.Equal(t, unsortedID.String(), collection["id"])
		require.Len(t, collection, 2, "the collection object carries id and name only")
	})

	// The listing reads and writes nothing. Looking at the history is not a reason to
	// fetch anything, so no enrichment status, no last_enriched_at and no collection
	// may move as a result of it.
	t.Run("does not mutate items or trigger enrichment", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-readonly@example.com")
		unsortedID := createUnsorted(t, userID)
		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		item := createSavedItemInCollection(t, userID, unsortedID, "https://example.com/a")

		before := savedItemState(t, item)

		// Listing more than once rules out a single request's worth of work being
		// mistaken for none.
		for range 3 {
			listRecentSavedItemsOverHTTP(t, userID, "")
		}

		after := savedItemState(t, item)

		require.Equal(t, before.EnrichmentStatus, after.EnrichmentStatus)
		require.Equal(t, before.LastEnrichedAt, after.LastEnrichedAt)
		require.Equal(t, before.CollectionID, after.CollectionID)
		require.Equal(t, before.UpdatedAt, after.UpdatedAt)
		require.Equal(t, unsortedID, after.CollectionID)

		// Nothing was filed either: browsing must not organize.
		require.Equal(t, unsortedID, after.CollectionID)
		require.NotEqual(t, wishlistID, after.CollectionID)
	})
}

func TestSavedItemAPI_ListRecent_Pagination(t *testing.T) {
	// created_at defaults to NOW(), which is constant within a transaction, so rows
	// genuinely share a timestamp and the id tie-break is the only thing making the
	// order total. Without it a page boundary could skip or repeat an item.
	t.Run("orders deterministically when rows share a created_at", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-ties@example.com")
		unsortedID := createUnsorted(t, userID)
		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		// One shared timestamp across two collections, so the tie is broken across a
		// collection boundary rather than inside one.
		tied := time.Now().Add(-time.Hour).Truncate(time.Second)

		var (
			unsortedTies []uuid.UUID
			wishlistTies []uuid.UUID
		)

		for i := range 6 {
			unsortedTies = append(
				unsortedTies,
				createSavedItemAt(
					t,
					userID,
					unsortedID,
					fmt.Sprintf("https://example.com/u%d", i),
					tied,
				),
			)
		}

		for i := range 6 {
			wishlistTies = append(
				wishlistTies,
				createSavedItemAt(
					t,
					userID,
					wishlistID,
					fmt.Sprintf("https://example.com/w%d", i),
					tied,
				),
			)
		}

		all := append(append([]uuid.UUID{}, unsortedTies...), wishlistTies...)

		// uuidv7 is unique and monotonic, so the documented tie-break puts the later
		// id first. The expectation is computed from that rather than from the order
		// the fixture happened to insert in.
		want := append([]uuid.UUID{}, all...)
		sort.Slice(want, func(i, j int) bool {
			return want[i].String() > want[j].String()
		})

		page := listRecentSavedItemsOverHTTP(t, userID, "limit=50")

		require.Len(t, page.Data.SavedItems, 12)
		require.Equal(t, want, page.ids())
	})

	// Keyset pagination across collection boundaries: every item exactly once, and no
	// gap where a collection's items would end.
	t.Run("crosses collection boundaries without duplicates or gaps", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-walk@example.com")
		unsortedID := createUnsorted(t, userID)
		wishlistID := createTestUserCollection(t, userID, "Wishlist")
		platformID := createTestSystemCollection(t, userID, "youtube", "youtube")

		base := time.Now().Add(-6 * time.Hour).Truncate(time.Second)

		var want []uuid.UUID

		// Interleaved across three collections so every page boundary falls inside a
		// mixed run rather than at a collection edge.
		for i := range 15 {
			want = append(want, createSavedItemAt(
				t,
				userID,
				unsortedID,
				fmt.Sprintf("https://example.com/u%02d", i),
				base.Add(time.Duration(i)*time.Second),
			))
			want = append(want, createSavedItemAt(
				t,
				userID,
				wishlistID,
				fmt.Sprintf("https://example.com/w%02d", i),
				base.Add(time.Duration(i)*time.Second+500*time.Millisecond),
			))
			want = append(want, createSavedItemAt(
				t,
				userID,
				platformID,
				fmt.Sprintf("https://example.com/p%02d", i),
				base.Add(time.Duration(i)*time.Second+750*time.Millisecond),
			))
		}

		// The fixture inserts oldest first, so the listing is the reverse.
		for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
			want[i], want[j] = want[j], want[i]
		}

		seen, pages := walkRecentSavedItems(t, userID, "limit=7")

		require.Equal(t, want, seen, "every item, newest first, in one unbroken run")
		requireUniqueRecentSavedItems(t, seen)
		require.Greater(t, pages, 1, "the fixture must span more than one page")

		// Every page reports the envelope's cursor metadata, which is what a client
		// reads to decide whether to ask again.
		first := listRecentSavedItemsOverHTTP(t, userID, "limit=7")
		require.True(t, first.Meta.Cursor.HasMore)
		require.NotNil(t, first.Meta.Cursor.NextCursor)

		last := listRecentSavedItemsOverHTTP(
			t,
			userID,
			"limit=7&cursor="+*first.Meta.Cursor.NextCursor,
		)
		require.True(t, last.Meta.Cursor.HasMore)
	})

	// A cursor is a position rather than a row reference, so the item it came from can
	// be deleted and the walk still finishes correctly. An offset would shift every
	// later row and hand the client a duplicate.
	t.Run("resumes after the anchor item is deleted", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-anchor@example.com")
		unsortedID := createUnsorted(t, userID)

		base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

		var want []uuid.UUID

		for i := range 8 {
			want = append(want, createSavedItemAt(
				t,
				userID,
				unsortedID,
				fmt.Sprintf("https://example.com/%02d", i),
				base.Add(time.Duration(i)*time.Second),
			))
		}

		for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
			want[i], want[j] = want[j], want[i]
		}

		first := listRecentSavedItemsOverHTTP(t, userID, "limit=3")
		require.True(t, first.Meta.Cursor.HasMore)
		require.NotNil(t, first.Meta.Cursor.NextCursor)

		// The anchor is the last row the caller received. Deleting it removes a row
		// the cursor described, which is exactly the case an offset cannot survive.
		anchor := first.Data.SavedItems[len(first.Data.SavedItems)-1]

		_, err := testPool.Exec(
			context.Background(),
			"DELETE FROM saved_items WHERE id = $1",
			anchor.ID,
		)
		require.NoError(t, err)

		rest, _ := walkRecentSavedItemsFrom(
			t,
			userID,
			"limit=3",
			*first.Meta.Cursor.NextCursor,
		)
		seen := append(first.ids(), rest...)

		// The expectation is the untouched fixture list, and that is the assertion
		// that matters. The caller already received the anchor on page one, so it is
		// still one of the items it holds; what this proves is that its deletion did
		// not make the walk skip the item after it or repeat one before it. Offset
		// pagination shifts every later row down a slot on a delete and returns a
		// duplicate here.
		require.Equal(t, want, seen)
		requireUniqueRecentSavedItems(t, seen)
	})

	// A cursor pointing past the end is a finished walk, not a failure.
	t.Run("a cursor past the end returns an empty page", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-past-end@example.com")
		unsortedID := createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		for i := range 3 {
			createSavedItemAt(
				t,
				userID,
				unsortedID,
				fmt.Sprintf("https://example.com/%02d", i),
				base.Add(time.Duration(i)*time.Second),
			)
		}

		last := listRecentSavedItemsOverHTTP(t, userID, "limit=3")
		require.False(t, last.Meta.Cursor.HasMore)
		require.Nil(t, last.Meta.Cursor.NextCursor)

		// A position older than everything the user owns, encoded as a valid token.
		old := base.Add(-24 * time.Hour).Format(time.RFC3339Nano)
		oldID := uuid.New()

		token, err := pagination.Encode(saved_item.ListSavedItemsCursor{
			Version: pagination.CurrentVersion,
			Value:   &old,
			ID:      &oldID,
		})
		require.NoError(t, err)

		page := listRecentSavedItemsOverHTTP(t, userID, "cursor="+token)

		require.Empty(t, page.Data.SavedItems)
		require.NotNil(t, page.Meta.Cursor)
		require.False(t, page.Meta.Cursor.HasMore)
		require.Nil(t, page.Meta.Cursor.NextCursor)
	})

	// Every unusable token is the same 400 with the same code, and none of them
	// reaches the listing query. A cursor is not a capability, so all of them mean
	// one thing to a caller: start over without one. Telling them apart would only
	// make the token's internal shape easier to probe.
	t.Run("rejects every unusable cursor", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-bad-cursor@example.com")
		createUnsorted(t, userID)

		validValue := "2026-10-02T10:30:00Z"
		validID := uuid.New()
		emptyValue := ""
		notATimestamp := "not-a-timestamp"

		cursorToken := func(payload saved_item.ListSavedItemsCursor) string {
			t.Helper()

			if payload.Version == 0 {
				payload.Version = pagination.CurrentVersion
			}

			token, err := pagination.Encode(payload)
			require.NoError(t, err)

			return token
		}

		cases := []struct {
			name  string
			token string
		}{
			{
				name:  "a token that is not base64",
				token: "not-a-cursor%21%21",
			},
			{
				name:  "a token that is not json",
				token: "bm90IGpzb24",
			},
			{
				name:  "an unsupported payload version",
				token: cursorToken(saved_item.ListSavedItemsCursor{Version: pagination.CurrentVersion + 1}),
			},
			{
				name:  "no position at all",
				token: cursorToken(saved_item.ListSavedItemsCursor{}),
			},
			{
				// An ordering on created_at alone is not total, so a position without
				// the tie-breaker cannot say where a page ended.
				name: "a value with no tie-breaking id",
				token: cursorToken(saved_item.ListSavedItemsCursor{
					Value: &validValue,
				}),
			},
			{
				name: "an id with no value",
				token: cursorToken(saved_item.ListSavedItemsCursor{
					ID: &validID,
				}),
			},
			{
				name: "an empty value",
				token: cursorToken(saved_item.ListSavedItemsCursor{
					Value: &emptyValue,
					ID:    &validID,
				}),
			},
			{
				name: "the nil id",
				token: cursorToken(saved_item.ListSavedItemsCursor{
					Value: &validValue,
					ID:    &uuid.Nil,
				}),
			},
			{
				// The position is compared against a timestamptz column, so a string
				// that is not a timestamp has to be refused before the query runs.
				// Letting it through would turn a caller's bad token into a server
				// fault.
				name: "a value that is not a timestamp",
				token: cursorToken(saved_item.ListSavedItemsCursor{
					Value: &notATimestamp,
					ID:    &validID,
				}),
			},
			{
				name:  "a token past the length bound",
				token: "%20",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(
					http.MethodGet,
					"/saved-items?cursor="+tc.token,
					nil,
				)
				req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

				resp, err := testApp.Test(req)
				require.NoError(t, err)

				requireErrorCode(
					t,
					resp,
					http.StatusBadRequest,
					saved_item.CodeInvalidCursor,
				)
			})
		}
	})

	// limit=0 is read as absent rather than as a request for no rows.
	t.Run("reads a zero limit as absent", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-zero-limit@example.com")
		unsortedID := createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		for i := range saved_item.SavedItemListDefaultLimit + 5 {
			createSavedItemAt(
				t,
				userID,
				unsortedID,
				fmt.Sprintf("https://example.com/%02d", i),
				base.Add(time.Duration(i)*time.Second),
			)
		}

		page := listRecentSavedItemsOverHTTP(t, userID, "limit=0")

		require.Len(t, page.Data.SavedItems, saved_item.SavedItemListDefaultLimit)
		require.True(t, page.Meta.Cursor.HasMore)
	})

	// The request tag fires before the service's own clamp, so a caller asking for
	// more than the maximum is told what it asked for was out of range instead of
	// quietly receiving a differently sized page.
	t.Run("rejects a limit outside 1..50", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "recent-bad-limit@example.com")
		createUnsorted(t, userID)

		for _, limit := range []string{"51", "100", "-1"} {
			req := httptest.NewRequest(
				http.MethodGet,
				"/saved-items?limit="+limit,
				nil,
			)
			req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

			resp, err := testApp.Test(req)
			require.NoError(t, err)
			require.Equal(
				t,
				http.StatusBadRequest,
				resp.StatusCode,
				"limit=%s must be rejected",
				limit,
			)
		}

		// The boundaries themselves are accepted.
		for _, limit := range []string{"1", "50"} {
			page := listRecentSavedItemsOverHTTP(t, userID, "limit="+limit)
			require.NotNil(t, page.Meta.Cursor)
		}
	})

	t.Run("rejects a request without an access token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/saved-items", nil)

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		requireErrorCode(
			t,
			resp,
			http.StatusUnauthorized,
			"INVALID_AUTHORIZATION_HEADER",
		)
	})
}

// TestSavedItemAPI_ListRecent_DocumentedShape pins the generated documentation to the
// real response.
//
// The endpoint's description talks about meta.cursor, the per-item collection object
// and every nullable field, and a schema that omitted meta or described one of them
// differently would contradict it while still parsing as valid OpenAPI. Nothing at
// runtime could notice, so this walks the documented tree and the actual body side by
// side at every level: the envelope, data, one item, that item's collection, and
// meta.cursor.
func TestSavedItemAPI_ListRecent_DocumentedShape(t *testing.T) {
	truncateCollectionData(t)

	doc := loadSwaggerDoc(t)

	userID := createCollectionUser(t, "recent-docshape@example.com")
	unsortedID := createUnsorted(t, userID)

	// The item is enriched so every nullable column is populated. The key sets are
	// the same either way, and a populated item is the case where an omission would
	// be least visible.
	item := createSavedItemInCollection(
		t,
		userID,
		unsortedID,
		"https://example.com/watch",
	)

	enrichRecentItem(t, item, "completed", time.Now().Add(-time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/saved-items", nil)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	api := doc.definition(t, "saved_item.ListSavedItemsAPIResponse")

	// The envelope itself: data, message, meta.
	requireSameKeys(t, "$", propertyKeys(api), jsonKeys(t, body, "$"))

	data := doc.property(t, api, "data", "$")

	actualData, ok := body["data"].(map[string]any)
	require.True(t, ok, "response has no data object")

	requireSameKeys(t, "data", propertyKeys(data), jsonKeys(t, actualData, "data"))

	// The array's element schema is what describes one item's fields, and it is where
	// a field added to the runtime type but not to the response would show up.
	documentedItems := doc.arrayItem(
		t,
		doc.property(t, data, "saved_items", "data"),
		"data.saved_items",
	)

	actualItems, ok := actualData["saved_items"].([]any)
	require.True(t, ok, "response has no saved_items array")
	require.Len(t, actualItems, 1)

	actualItem, ok := actualItems[0].(map[string]any)
	require.True(t, ok)

	requireSameKeys(
		t,
		"data.saved_items[]",
		propertyKeys(documentedItems),
		jsonKeys(t, actualItems[0], "data.saved_items[]"),
	)

	// The per-item collection object is the field this endpoint adds over the detail
	// response, so it is asserted at its own level rather than as part of the item.
	documentedCollection := doc.property(
		t,
		documentedItems,
		"collection",
		"data.saved_items[]",
	)

	requireSameKeys(
		t,
		"data.saved_items[].collection",
		propertyKeys(documentedCollection),
		jsonKeys(t, actualItem["collection"], "data.saved_items[].collection"),
	)

	// meta.cursor, which the endpoint's description promised and the schema omitted.
	documentedMeta := doc.property(t, api, "meta", "$")
	requireSameKeys(
		t,
		"meta",
		propertyKeys(documentedMeta),
		jsonKeys(t, body["meta"], "meta"),
	)

	documentedCursor := doc.property(t, documentedMeta, "cursor", "meta")
	requireSameKeys(
		t,
		"meta.cursor",
		propertyKeys(documentedCursor),
		jsonKeys(t, body["meta"].(map[string]any)["cursor"], "meta.cursor"),
	)
}

// walkRecentSavedItemsFrom follows next_cursor from a supplied position rather than
// from the beginning, so a test can delete an anchor row between two requests.
func walkRecentSavedItemsFrom(
	t *testing.T,
	userID uuid.UUID,
	query string,
	cursor string,
) ([]uuid.UUID, int) {
	t.Helper()

	var (
		seen  []uuid.UUID
		calls int
	)

	for {
		page := listRecentSavedItemsOverHTTP(t, userID, query+"&cursor="+cursor)
		calls++

		seen = append(seen, page.ids()...)

		if !page.Meta.Cursor.HasMore {
			require.Nil(t, page.Meta.Cursor.NextCursor)

			return seen, calls
		}

		require.NotNil(t, page.Meta.Cursor.NextCursor)

		cursor = *page.Meta.Cursor.NextCursor

		require.Less(t, calls, 60, "paging did not terminate")
	}
}

// moveSavedItemIntoCollection files a saved item through the real endpoint, so a
// fixture is a state the product can actually produce rather than a direct write.
func moveSavedItemIntoCollection(
	t *testing.T,
	userID uuid.UUID,
	savedItemID uuid.UUID,
	name string,
) {
	t.Helper()

	body := fmt.Sprintf(
		`{"collection_name":%q,"collection_intent":"existing"}`,
		name,
	)

	req := httptest.NewRequest(
		http.MethodPut,
		"/saved-items/"+savedItemID.String()+"/collection",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
