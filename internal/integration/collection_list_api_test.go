package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	"github.com/thoriqr/stash-it-backend/internal/pagination"
	collectiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/collection/generated"
)

// listCollectionsPage is the wire shape of one page. Meta is decoded loosely so a
// test asserting on next_cursor can also see whether the key was present at all,
// which is the distinction the cursor contract turns on.
type listCollectionsPage struct {
	Data struct {
		Collections []struct {
			ID        uuid.UUID `json:"id"`
			Name      string    `json:"name"`
			Type      string    `json:"type"`
			SystemKey *string   `json:"system_key"`

			// SavedItemsCount is the bounded count and SavedItemsCountOverflow says
			// whether it was clipped. Both are decoded here rather than reusing the
			// API response type, so the suite asserts the wire shape itself including
			// that a zero count arrives as 0 rather than as null or a missing key.
			SavedItemsCount         int64 `json:"saved_items_count"`
			SavedItemsCountOverflow bool  `json:"saved_items_count_overflow"`

			CreatedAt time.Time `json:"created_at"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"collections"`
	} `json:"data"`
	Message string `json:"message"`
	Meta    struct {
		Cursor *struct {
			NextCursor *string `json:"next_cursor"`
			HasMore    bool    `json:"has_more"`
		} `json:"cursor"`
	} `json:"meta"`
}

// lookupCount finds one collection's reported count pair by name.
//
// It returns found separately from the pair because "the collection was absent" and
// "the collection reported zero" have to be told apart: an inner join instead of a
// left one would report the first while looking like the second.
func (p listCollectionsPage) lookupCount(name string) (count int64, overflow bool, found bool) {
	for _, c := range p.Data.Collections {
		if c.Name == name {
			return c.SavedItemsCount, c.SavedItemsCountOverflow, true
		}
	}

	return 0, false, false
}

// requireCount asserts one collection's reported count pair by name.
//
// Both halves are asserted together because neither means anything on its own: the
// count alone cannot say whether it is exact, and the flag alone cannot say how many
// there are.
func (p listCollectionsPage) requireCount(
	t *testing.T,
	name string,
	wantCount int64,
	wantOverflow bool,
) {
	t.Helper()

	count, overflow, found := p.lookupCount(name)

	require.True(t, found, "collection %q was not in the page", name)
	require.Equal(t, wantCount, count, "collection %q count", name)
	require.Equal(t, wantOverflow, overflow, "collection %q overflow", name)
}

func (p listCollectionsPage) ids() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(p.Data.Collections))

	for _, c := range p.Data.Collections {
		ids = append(ids, c.ID)
	}

	return ids
}

func (p listCollectionsPage) names() []string {
	names := make([]string, 0, len(p.Data.Collections))

	for _, c := range p.Data.Collections {
		names = append(names, c.Name)
	}

	return names
}

// listCollectionsOverHTTP calls the endpoint and requires a 200 with a fully
// decoded page, so every test starts from a known-good shape.
func listCollectionsOverHTTP(
	t *testing.T,
	userID uuid.UUID,
	query string,
) listCollectionsPage {
	t.Helper()

	path := "/collections"
	if query != "" {
		path += "?" + query
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var page listCollectionsPage

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&page))

	return page
}

// createCollectionAt seeds a collection at an exact position in the created_at
// ordering, optionally as a system collection with a key.
func createCollectionAt(
	t *testing.T,
	userID uuid.UUID,
	name string,
	createdAt time.Time,
	systemKey string,
) uuid.UUID {
	t.Helper()

	db := collectiondbtest.New(testPool)

	params := collectiondbtest.CreateTestCollectionAtParams{
		UserID:    userID,
		Name:      name,
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
	}

	if systemKey != "" {
		params.SystemKey = testText(systemKey)
	}

	created, err := db.CreateTestCollectionAt(context.Background(), params)
	require.NoError(t, err)

	return created.ID
}

// walkAllPages follows next_cursor to the end and returns every id seen, in order,
// along with the number of pages fetched. It is the workhorse for the paging
// invariants: no skips, no duplicates, and the pinned row only ever once.
func walkAllPages(
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

		page := listCollectionsOverHTTP(t, userID, pageQuery)
		calls++

		require.NotNil(
			t,
			page.Meta.Cursor,
			"every response must carry cursor metadata",
		)

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

		require.Less(
			t,
			calls,
			50,
			"paging did not terminate, which means next_cursor is not advancing",
		)
	}
}

func requireUnique(t *testing.T, ids []uuid.UUID) {
	t.Helper()

	seen := make(map[uuid.UUID]struct{}, len(ids))

	for _, id := range ids {
		require.NotContains(
			t,
			seen,
			id,
			"collection %s was returned on more than one page",
			id,
		)

		seen[id] = struct{}{}
	}
}

func TestCollectionAPI_ListCollections(t *testing.T) {
	// Unsorted is identified by system_key and must lead the list under every sort,
	// including where its own created_at would put it last.
	t.Run("unsorted is first under every sort", func(t *testing.T) {
		for _, sort := range []string{"newest", "oldest", "name"} {
			t.Run(sort, func(t *testing.T) {
				truncateCollectionData(t)

				userID := createCollectionUser(t, "collection-list-pinned-"+sort+"@example.com")

				// Unsorted is created first, so under oldest it would sort first
				// anyway and under newest last. Pinning must not depend on that.
				unsortedID := createUnsorted(t, userID)

				base := time.Now().Add(-time.Hour)

				// A user collection with a NULL system_key must still be listed,
				// which is what IS DISTINCT FROM protects.
				createCollectionAt(t, userID, "Aardvark", base, "")
				createCollectionAt(t, userID, "Zebra", base.Add(time.Minute), "")

				page := listCollectionsOverHTTP(
					t, userID, "sort="+sort+"&limit=50",
				)

				ids := page.ids()

				require.NotEmpty(t, ids)
				require.Equal(
					t,
					unsortedID,
					ids[0],
					"unsorted must be first under sort=%s",
					sort,
				)

				// Every collection the user has is present, system and user alike:
				// Unsorted, Aardvark and Zebra.
				require.Len(t, ids, 3)
				requireUnique(t, ids)
			})
		}
	})

	// The pinned row must not come back once a later page is requested.
	t.Run("unsorted appears on no page after the first", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-pinned-once@example.com")
		unsortedID := createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour)

		for i := range 4 {
			createCollectionAt(
				t,
				userID,
				fmt.Sprintf("Collection %d", i),
				base.Add(time.Duration(i)*time.Minute),
				"",
			)
		}

		first := listCollectionsOverHTTP(t, userID, "sort=newest&limit=2")

		require.Equal(t, unsortedID, first.ids()[0])
		require.True(t, first.Meta.Cursor.HasMore)
		require.NotNil(t, first.Meta.Cursor.NextCursor)

		seen, calls := walkAllPages(t, userID, "sort=newest&limit=2")

		require.Greater(t, calls, 1, "this dataset needs more than one page")
		requireUnique(t, seen)

		// Unsorted plus the four seeded collections.
		require.Len(t, seen, 5)

		// Exactly one appearance of the pinned collection across the whole walk.
		appearances := 0

		for _, id := range seen {
			if id == unsortedID {
				appearances++
			}
		}

		require.Equal(
			t,
			1,
			appearances,
			"unsorted must appear exactly once across all pages",
		)
	})

	// limit=1 is the case where the pinned row is the only result and the next page
	// must move on rather than repeat it.
	t.Run("limit one transitions from unsorted to the rest", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-limit-one@example.com")
		unsortedID := createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour)

		firstID := createCollectionAt(t, userID, "First", base, "")
		secondID := createCollectionAt(t, userID, "Second", base.Add(time.Minute), "")

		pageOne := listCollectionsOverHTTP(t, userID, "sort=newest&limit=1")

		require.Len(t, pageOne.ids(), 1)
		require.Equal(t, unsortedID, pageOne.ids()[0])
		require.True(t, pageOne.Meta.Cursor.HasMore)

		pageTwo := listCollectionsOverHTTP(
			t,
			userID,
			"sort=newest&limit=1&cursor="+*pageOne.Meta.Cursor.NextCursor,
		)

		require.Len(t, pageTwo.ids(), 1)
		require.NotEqual(
			t,
			unsortedID,
			pageTwo.ids()[0],
			"the second page must not repeat unsorted",
		)

		pageThree := listCollectionsOverHTTP(
			t,
			userID,
			"sort=newest&limit=1&cursor="+*pageTwo.Meta.Cursor.NextCursor,
		)

		require.Len(t, pageThree.ids(), 1)
		require.False(t, pageThree.Meta.Cursor.HasMore)
		require.Nil(t, pageThree.Meta.Cursor.NextCursor)

		requireUnique(
			t,
			[]uuid.UUID{
				pageOne.ids()[0],
				pageTwo.ids()[0],
				pageThree.ids()[0],
			},
		)

		// The ordering below the pinned row is newest first, whichever row each
		// individual page happened to carry.
		require.Equal(
			t,
			[]uuid.UUID{unsortedID, secondID, firstID},
			append(
				[]uuid.UUID{pageOne.ids()[0]},
				append(pageTwo.ids(), pageThree.ids()...)...,
			),
		)
	})

	t.Run("sorts by newest and oldest", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-sorts@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		oldest := createCollectionAt(t, userID, "Oldest", base, "")
		middle := createCollectionAt(t, userID, "Middle", base.Add(time.Minute), "")
		newest := createCollectionAt(t, userID, "Newest", base.Add(2*time.Minute), "")

		newestFirst := listCollectionsOverHTTP(
			t, userID, "sort=newest&limit=50",
		).ids()

		require.Equal(t, []uuid.UUID{newest, middle, oldest}, newestFirst[1:])

		oldestFirst := listCollectionsOverHTTP(
			t, userID, "sort=oldest&limit=50",
		).ids()

		require.Equal(t, []uuid.UUID{oldest, middle, newest}, oldestFirst[1:])
	})

	t.Run("sorts by name ignoring case and padding", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-names@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour)

		// Deliberately mixed case and created in an order that does not match the
		// expected result, so a raw-byte sort would fail this.
		charlie := createCollectionAt(t, userID, "charlie", base, "")
		alpha := createCollectionAt(t, userID, "Alpha", base, "")
		bravo := createCollectionAt(t, userID, "BRAVO", base, "")

		page := listCollectionsOverHTTP(t, userID, "sort=name&limit=50")

		require.Equal(
			t,
			[]uuid.UUID{alpha, bravo, charlie},
			page.ids()[1:],
			"name sort must be case-insensitive",
		)
	})

	// created_at defaults to NOW(), which is constant within a transaction, so ties
	// are a real state. Without a unique tie-break a page boundary can skip or repeat
	// a row, and that failure is silent.
	t.Run("keeps a stable order when timestamps are equal", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-ties@example.com")
		createUnsorted(t, userID)

		tiedAt := time.Now().Add(-time.Hour).Truncate(time.Second)

		tied := make([]uuid.UUID, 0, 5)

		for i := range 5 {
			tied = append(
				tied,
				createCollectionAt(
					t,
					userID,
					fmt.Sprintf("Tied %d", i),
					tiedAt,
					"",
				),
			)
		}

		// limit=2 pages straight through the tied block, so the boundary lands inside
		// it three times over.
		seen, calls := walkAllPages(t, userID, "sort=newest&limit=2")

		require.Greater(t, calls, 2)
		requireUnique(t, seen)

		require.Equal(
			t,
			len(tied)+1,
			len(seen),
			"every tied collection must be listed exactly once",
		)

		for _, id := range tied {
			require.Contains(t, seen, id)
		}
	})

	// The static-dataset invariant: walking every page yields the complete set with
	// nothing skipped or repeated.
	t.Run("pages a static dataset without skipping or repeating", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-walk@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		expected := make([]uuid.UUID, 0, 8)

		for i := range 7 {
			expected = append(
				expected,
				createCollectionAt(
					t,
					userID,
					fmt.Sprintf("Item %02d", i),
					base.Add(time.Duration(i)*time.Minute),
					"",
				),
			)
		}

		seen, _ := walkAllPages(t, userID, "sort=oldest&limit=3")

		require.Len(t, seen, len(expected)+1)
		requireUnique(t, seen)

		for _, id := range expected {
			require.Contains(t, seen, id)
		}
	})

	t.Run("lists collections automatic organization created", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-system@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour)

		// A system collection the organization worker would have created. It belongs
		// to one user like any other and must not be filtered out.
		youtubeID := createCollectionAt(t, userID, "YouTube", base, "youtube")
		spotifyID := createCollectionAt(t, userID, "Spotify", base.Add(time.Minute), "spotify")

		page := listCollectionsOverHTTP(t, userID, "sort=oldest&limit=50")

		ids := page.ids()

		require.Len(t, ids, 3)
		require.Contains(t, ids, youtubeID)
		require.Contains(t, ids, spotifyID)

		for _, c := range page.Data.Collections {
			if c.ID == youtubeID {
				require.Equal(t, "system", c.Type)
				require.NotNil(t, c.SystemKey)
				require.Equal(t, "youtube", *c.SystemKey)
			}
		}
	})

	// A user collection's system_key is NULL and must be reported as null, not as an
	// empty string, which would claim a key value this schema never produces.
	t.Run("reports a user collection with a null system key", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-null-key@example.com")
		unsortedID := createUnsorted(t, userID)

		userCollectionID := createCollectionAt(
			t,
			userID,
			"Wishlist",
			time.Now().Add(-time.Hour),
			"",
		)

		page := listCollectionsOverHTTP(t, userID, "sort=newest&limit=50")

		for _, c := range page.Data.Collections {
			switch c.ID {
			case unsortedID:
				require.NotNil(
					t,
					c.SystemKey,
					"unsorted is identified by its system_key",
				)
				require.Equal(
					t,
					string(collection.CollectionSystemKeyUnsorted),
					*c.SystemKey,
				)
				require.Equal(t, "system", c.Type)

			case userCollectionID:
				require.Nil(
					t,
					c.SystemKey,
					"a user collection has no system key and must report null",
				)
				require.Equal(t, "user", c.Type)
			}
		}
	})

	t.Run("lists only the authenticated user's collections", func(t *testing.T) {
		truncateCollectionData(t)

		firstUser := createCollectionUser(t, "collection-list-scope-a@example.com")
		secondUser := createCollectionUser(t, "collection-list-scope-b@example.com")

		firstUnsorted := createUnsorted(t, firstUser)
		createUnsorted(t, secondUser)

		base := time.Now().Add(-time.Hour)

		firstID := createCollectionAt(t, firstUser, "Mine", base, "")
		secondID := createCollectionAt(t, secondUser, "Theirs", base, "")

		page := listCollectionsOverHTTP(t, firstUser, "sort=newest&limit=50")

		ids := page.ids()

		require.Len(t, ids, 2)
		require.Contains(t, ids, firstUnsorted)
		require.Contains(t, ids, firstID)
		require.NotContains(t, ids, secondID)
	})

	// An empty list is an ordinary result and must serialize as [], never null.
	t.Run("returns an empty array when there is nothing to list", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-empty@example.com")

		resp := httptest.NewRequest(http.MethodGet, "/collections", nil)
		resp.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		httpResp, err := testApp.Test(resp)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, httpResp.StatusCode)

		raw, err := io.ReadAll(httpResp.Body)
		require.NoError(t, err)

		require.Contains(
			t,
			string(raw),
			`"collections":[]`,
			"an empty list must be [] rather than null",
		)

		var page listCollectionsPage

		require.NoError(t, json.Unmarshal(raw, &page))
		require.Empty(t, page.Data.Collections)
		require.False(t, page.Meta.Cursor.HasMore)
		require.Nil(t, page.Meta.Cursor.NextCursor)
	})

	// next_cursor must be present and explicitly null on the final page, because a
	// client reading the field has to be able to tell "no next page" from "this build
	// does not send the field".
	t.Run("serializes next_cursor as null on the final page", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-null-cursor@example.com")
		createUnsorted(t, userID)

		createCollectionAt(t, userID, "Only", time.Now().Add(-time.Hour), "")

		req := httptest.NewRequest(http.MethodGet, "/collections?limit=50", nil)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Contains(
			t,
			string(raw),
			`"next_cursor":null`,
			"the final page must serialize next_cursor as an explicit null",
		)

		require.NotContains(
			t,
			string(raw),
			`"page"`,
			"cursor pagination reports no offset fields",
		)
	})

	t.Run("defaults to newest first and the default limit", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-defaults@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		newest := createCollectionAt(t, userID, "Newest", base.Add(time.Minute), "")
		createCollectionAt(t, userID, "Oldest", base, "")

		page := listCollectionsOverHTTP(t, userID, "")

		require.Equal(t, "collections retrieved successfully", page.Message)
		require.Equal(t, newest, page.ids()[1], "absent sort means newest first")

		// More than the default limit of 20 rows proves the default was applied
		// rather than unbounded.
		for i := range 25 {
			createCollectionAt(
				t,
				userID,
				fmt.Sprintf("Bulk %02d", i),
				base.Add(time.Duration(i)*time.Second),
				"",
			)
		}

		paged := listCollectionsOverHTTP(t, userID, "")

		require.Len(t, paged.Data.Collections, 20)
		require.True(t, paged.Meta.Cursor.HasMore)
	})
}

func TestCollectionAPI_ListCollections_CursorBehaviour(t *testing.T) {
	// A cursor is a position, not a row reference. Nothing in the resumed query reads
	// the row the cursor came from, so deleting it must not stop the walk: it simply
	// continues with whatever sorts after that position. This is the property offset
	// pagination cannot offer, since a deleted row shifts everything after it and the
	// client receives a duplicate.
	t.Run("a cursor keeps working after its anchor row is deleted", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-deleted-anchor@example.com")

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		unsortedID := createUnsorted(t, userID)

		first := createCollectionAt(t, userID, "First", base, "")
		anchor := createCollectionAt(t, userID, "Anchor", base.Add(time.Minute), "")
		last := createCollectionAt(t, userID, "Last", base.Add(2*time.Minute), "")

		// limit 2 so the first page covers Unsorted and First, putting the cursor on
		// First. The next page with limit 1 then lands exactly on the anchor.
		firstPage := listCollectionsOverHTTP(t, userID, "sort=oldest&limit=2")

		require.Equal(
			t,
			[]uuid.UUID{unsortedID, first},
			firstPage.ids(),
		)
		require.True(t, firstPage.Meta.Cursor.HasMore)
		require.NotNil(t, firstPage.Meta.Cursor.NextCursor)

		anchorPage := listCollectionsOverHTTP(
			t,
			userID,
			"sort=oldest&limit=1&cursor="+*firstPage.Meta.Cursor.NextCursor,
		)

		require.Equal(t, anchor, anchorPage.ids()[0])
		require.True(t, anchorPage.Meta.Cursor.HasMore)
		require.NotNil(t, anchorPage.Meta.Cursor.NextCursor)

		// The anchor row disappears before the next page is requested.
		deleteCollectionRow(t, anchor)

		nextPage := listCollectionsOverHTTP(
			t,
			userID,
			"sort=oldest&limit=1&cursor="+*anchorPage.Meta.Cursor.NextCursor,
		)

		// It returns what sorts after the recorded position, not an empty page and
		// not an error.
		require.Equal(
			t,
			[]uuid.UUID{last},
			nextPage.ids(),
			"a valid cursor must continue past a deleted anchor",
		)

		require.False(t, nextPage.Meta.Cursor.HasMore)
		require.Nil(t, nextPage.Meta.Cursor.NextCursor)

		require.NotContains(t, nextPage.ids(), anchor)
		require.NotContains(t, nextPage.ids(), first)
	})

	// The after-Unsorted cursor holds no position, so nothing can invalidate it.
	t.Run("a cursor survives unsorted being deleted", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-deleted-unsorted@example.com")
		unsortedID := createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		first := createCollectionAt(t, userID, "First", base, "")

		// limit 1 returns only Unsorted, so its cursor is the positionless one.
		pageOne := listCollectionsOverHTTP(t, userID, "sort=newest&limit=1")

		require.Equal(t, unsortedID, pageOne.ids()[0])
		require.True(t, pageOne.Meta.Cursor.HasMore)

		deleteCollectionRow(t, unsortedID)

		pageTwo := listCollectionsOverHTTP(
			t,
			userID,
			"sort=newest&limit=1&cursor="+*pageOne.Meta.Cursor.NextCursor,
		)

		require.Equal(
			t,
			[]uuid.UUID{first},
			pageTwo.ids(),
			"a positionless cursor must resume the regular ordering",
		)
	})

	// A cursor records a stored position, so renaming the row it came from does not
	// move the walk forward or backward.
	t.Run("a cursor resumes from the stored name, not a renamed row", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-renamed-anchor@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour)

		createCollectionAt(t, userID, "Anchor", base, "")
		last := createCollectionAt(t, userID, "Zebra", base, "")

		firstPage := listCollectionsOverHTTP(t, userID, "sort=name&limit=2")

		require.Equal(t, "Anchor", firstPage.names()[1])
		require.True(t, firstPage.Meta.Cursor.HasMore)

		renameCollection(t, "Anchor", "Aardvark")

		nextPage := listCollectionsOverHTTP(
			t,
			userID,
			"sort=name&limit=2&cursor="+*firstPage.Meta.Cursor.NextCursor,
		)

		require.Contains(
			t,
			nextPage.ids(),
			last,
			"the walk continues from the position the cursor recorded",
		)
	})

	t.Run("a cursor past the end returns an empty final page", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-past-end@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		createCollectionAt(t, userID, "Only", base, "")

		// Walk to the final page, then ask once more with the same cursor.
		finalPage := listCollectionsOverHTTP(t, userID, "sort=newest&limit=1")

		require.True(t, finalPage.Meta.Cursor.HasMore)

		secondPage := listCollectionsOverHTTP(
			t,
			userID,
			"sort=newest&limit=1&cursor="+*finalPage.Meta.Cursor.NextCursor,
		)

		require.Len(t, secondPage.Data.Collections, 1)
		require.False(t, secondPage.Meta.Cursor.HasMore)
		require.NotNil(t, secondPage.Meta.Cursor)
		require.Nil(t, secondPage.Meta.Cursor.NextCursor)

		// Exhausting the list is a successful empty page, not an error: a cursor
		// outlives the rows it points past, and a client paging a list whose contents
		// shifted should see "no more" rather than a failure.
		pageThree := listCollectionsOverHTTP(
			t,
			userID,
			"sort=newest&limit=50",
		)

		require.False(t, pageThree.Meta.Cursor.HasMore)
		require.Nil(t, pageThree.Meta.Cursor.NextCursor)
	})
}

func TestCollectionAPI_ListCollections_Errors(t *testing.T) {
	listError := func(
		t *testing.T,
		userID uuid.UUID,
		query string,
		expectedStatus int,
		expectedCode string,
	) {
		t.Helper()

		req := httptest.NewRequest(
			http.MethodGet,
			"/collections?"+query,
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		requireErrorCode(t, resp, expectedStatus, expectedCode)
	}

	t.Run("rejects an unsupported sort", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-bad-sort@example.com")

		listError(
			t,
			userID,
			"sort=random",
			http.StatusBadRequest,
			"VALIDATION_ERROR",
		)
	})

	t.Run("rejects a limit above the maximum or below one", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-bad-limit@example.com")

		listError(t, userID, "limit=51", http.StatusBadRequest, "VALIDATION_ERROR")
		listError(t, userID, "limit=-1", http.StatusBadRequest, "VALIDATION_ERROR")

		// A limit that is not a number never reaches validation: the binder itself
		// fails converting it, which BindQuery reports as a plain bad request. That
		// is the existing behaviour of the shared helper, not something this endpoint
		// redefines.
		listError(t, userID, "limit=abc", http.StatusBadRequest, "BAD_REQUEST")
	})

	// limit=0 is absent, not invalid. omitempty on the request tag and the service's
	// own default both treat it as "no limit given", which is the same reading the
	// saved item and session list endpoints give it.
	t.Run("treats a zero limit as absent and applies the default", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-zero-limit@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		for i := range 25 {
			createCollectionAt(
				t,
				userID,
				fmt.Sprintf("Bulk %02d", i),
				base.Add(time.Duration(i)*time.Second),
				"",
			)
		}

		page := listCollectionsOverHTTP(t, userID, "limit=0")

		require.Len(
			t,
			page.Data.Collections,
			collection.CollectionListDefaultLimit,
		)
		require.True(t, page.Meta.Cursor.HasMore)
	})

	t.Run("rejects a malformed cursor", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-bad-cursor@example.com")

		listError(
			t,
			userID,
			"cursor=not-a-cursor%21%21",
			http.StatusBadRequest,
			collection.CodeInvalidCursor,
		)
	})

	t.Run("rejects a cursor issued for another sort", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-reused-cursor@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		createCollectionAt(t, userID, "First", base, "")
		createCollectionAt(t, userID, "Second", base.Add(time.Minute), "")

		newestPage := listCollectionsOverHTTP(t, userID, "sort=newest&limit=1")

		require.True(t, newestPage.Meta.Cursor.HasMore)
		require.NotNil(t, newestPage.Meta.Cursor.NextCursor)

		// Reusing a position recorded under a different ordering would resume
		// somewhere meaningless, so it is refused rather than misread.
		listError(
			t,
			userID,
			"sort=oldest&cursor="+*newestPage.Meta.Cursor.NextCursor,
			http.StatusBadRequest,
			collection.CodeInvalidCursor,
		)
	})

	t.Run("rejects a request without an access token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/collections", nil)

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		requireErrorCode(
			t,
			resp,
			http.StatusUnauthorized,
			"INVALID_AUTHORIZATION_HEADER",
		)
	})

	// A token that decodes cleanly and satisfies every structural rule can still
	// carry a value the time-based sorts cannot compare against a timestamptz column.
	// It must be reported as the bad cursor it is, not as a server fault: the value
	// came from the caller and nothing about the request reached the database.
	t.Run("rejects a decodable cursor holding an invalid timestamp", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-bad-timestamp@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		createCollectionAt(t, userID, "First", base, "")
		createCollectionAt(t, userID, "Second", base.Add(time.Minute), "")

		// Both values are non-empty, so they clear the emptiness check, and both fail
		// time.Parse.
		for _, value := range []string{
			"not-a-timestamp",
			"2026-13-45T99:99:99Z",
			"1750000000",
			"z",
		} {
			listError(
				t,
				userID,
				"sort=newest&cursor="+encodeTimestampCursor(
					t,
					collection.CollectionSortNewest,
					value,
				),
				http.StatusBadRequest,
				collection.CodeInvalidCursor,
			)
		}
	})

	// The same token under oldest, which is a separate query with its own generated
	// parameter type.
	t.Run("rejects an invalid timestamp under the oldest sort", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-bad-ts-oldest@example.com")
		createUnsorted(t, userID)

		createCollectionAt(t, userID, "Only", time.Now().Add(-time.Hour), "")

		listError(
			t,
			userID,
			"sort=oldest&cursor="+encodeTimestampCursor(
				t,
				collection.CollectionSortOldest,
				"not-a-timestamp",
			),
			http.StatusBadRequest,
			collection.CodeInvalidCursor,
		)
	})

	// A name sort position is text, so a collection name is a legitimate value there
	// and must not be mistaken for an unusable timestamp.
	t.Run("accepts a collection name as the cursor value under name sort", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-list-name-value@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		createCollectionAt(t, userID, "Apple", base, "")
		createCollectionAt(t, userID, "Banana", base, "")
		createCollectionAt(t, userID, "Cherry", base, "")

		page := listCollectionsOverHTTP(
			t,
			userID,
			"sort=name&limit=2&cursor="+encodeTimestampCursor(
				t,
				collection.CollectionSortName,
				"apple",
			),
		)

		require.NotEmpty(t, page.Data.Collections)

		// Resuming from "apple" yields what sorts after it, never the pinned row.
		for _, c := range page.Data.Collections {
			if c.SystemKey != nil {
				require.NotEqual(
					t,
					string(collection.CollectionSystemKeyUnsorted),
					*c.SystemKey,
					"the pinned row must not reappear on a resumed page",
				)
			}
		}
	})
}

// TestCollectionAPI_ListCollections_SavedItemsCount covers the bounded count the
// listing reports for every collection.
//
// The bound is a product decision rather than a cost one, so the cases below walk the
// two edges it creates and the state just inside each: nothing held, an exact count,
// the bound itself reported exactly, and the first counts above it. Reporting the
// bound without saying it was clipped would make "99 items" and "99 or more" the same
// answer, and a client would have to hard-code the threshold to tell them apart.
func TestCollectionAPI_ListCollections_SavedItemsCount(t *testing.T) {
	// A collection holding nothing is an ordinary state in this product, so it is
	// reported as zero rather than as an absence. It is also the case an inner join
	// instead of a left one would drop from the listing entirely.
	t.Run("an empty collection reports zero with no overflow", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-count-empty@example.com")
		createUnsorted(t, userID)

		emptyID := createTestUserCollection(t, userID, "Empty")

		require.Zero(
			t,
			countSavedItemsInCollectionForUser(t, emptyID, userID),
			"the fixture must start from a collection holding nothing",
		)

		page := listCollectionsOverHTTP(t, userID, "")

		page.requireCount(t, "Empty", 0, false)
	})

	t.Run("an exact count below the bound is reported exactly", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-count-one@example.com")
		createUnsorted(t, userID)

		oneID := createTestUserCollection(t, userID, "One")

		createSavedItemInCollection(t, userID, oneID, "https://example.com/one")

		page := listCollectionsOverHTTP(t, userID, "")

		page.requireCount(t, "One", 1, false)
	})

	// The bound itself is a real count and is not overflow. A client rendering "99"
	// and "99+" has to be able to tell them apart, and only this can.
	t.Run("a count at the bound is reported exactly with no overflow", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-count-99@example.com")
		createUnsorted(t, userID)

		exactID := createTestUserCollection(t, userID, "Exact")

		seedSavedItems(t, userID, exactID, collection.CollectionSavedItemsCountCap)

		page := listCollectionsOverHTTP(t, userID, "")

		page.requireCount(
			t,
			"Exact",
			collection.CollectionSavedItemsCountCap,
			false,
		)
	})

	// One above the bound is the first clipped count, and 100 and 101 must be
	// reported identically: the response says "99 or more" in both cases, which is
	// the whole point of the flag rather than exposing a total nobody renders.
	t.Run("counts above the bound report the cap with overflow", func(t *testing.T) {
		for _, extra := range []struct {
			name  string
			total int
		}{
			{name: "one above the bound", total: collection.CollectionSavedItemsCountCap + 1},
			{name: "well above the bound", total: collection.CollectionSavedItemsCountCap + 2},
			{name: "far above the bound", total: collection.CollectionSavedItemsCountCap * 3},
		} {
			t.Run(extra.name, func(t *testing.T) {
				truncateCollectionData(t)

				userID := createCollectionUser(
					t,
					"collection-count-overflow-"+strconv.Itoa(extra.total)+"@example.com",
				)
				createUnsorted(t, userID)

				bigID := createTestUserCollection(
					t,
					userID,
					"Overflow "+strconv.Itoa(extra.total),
				)

				seedSavedItems(t, userID, bigID, extra.total)

				page := listCollectionsOverHTTP(t, userID, "")

				page.requireCount(
					t,
					"Overflow "+strconv.Itoa(extra.total),
					collection.CollectionSavedItemsCountCap,
					true,
				)
			})
		}
	})

	// Unsorted is a collection like any other for this purpose. It holds everything a
	// user saved before filing it, so it is where a count above the bound is most
	// likely to appear, and the pinned row must carry it like every other row.
	t.Run("unsorted reports its own count", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-count-unsorted@example.com")
		unsortedID := createUnsorted(t, userID)

		createTestUserCollection(t, userID, "Wishlist")

		seedSavedItems(t, userID, unsortedID, collection.CollectionSavedItemsCountCap+5)

		page := listCollectionsOverHTTP(t, userID, "")

		require.Len(t, page.Data.Collections, 2)
		require.Equal(t, "Unsorted", page.Data.Collections[0].Name)
		page.requireCount(
			t,
			"Unsorted",
			collection.CollectionSavedItemsCountCap,
			true,
		)
		page.requireCount(t, "Wishlist", 0, false)
	})

	// A collection automatic organization created is one the user owns, so its
	// contents are counted exactly like a collection they named. The count is about
	// what a collection holds, not about how it came to be.
	t.Run("an automatically created system collection is counted", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-count-system@example.com")
		createUnsorted(t, userID)

		platformID := createTestSystemCollection(t, userID, "youtube", "youtube")

		seedSavedItems(t, userID, platformID, 3)

		page := listCollectionsOverHTTP(t, userID, "")

		page.requireCount(t, "youtube", 3, false)
	})

	// Another user's items must never be counted. They are in a different collection
	// with a different owner, so this proves the count is scoped by the collection's
	// own rows rather than by anything about the caller.
	t.Run("another user's items are not counted", func(t *testing.T) {
		truncateCollectionData(t)

		ownerID := createCollectionUser(t, "collection-count-owner@example.com")
		createUnsorted(t, ownerID)
		ownerCollectionID := createTestUserCollection(t, ownerID, "Mine")

		createSavedItemInCollection(t, ownerID, ownerCollectionID, "https://example.com/mine")

		otherID := createCollectionUser(t, "collection-count-other@example.com")
		createUnsorted(t, otherID)
		otherCollectionID := createTestUserCollection(t, otherID, "Theirs")

		createSavedItemInCollection(t, otherID, otherCollectionID, "https://example.com/theirs")
		createSavedItemInCollection(t, otherID, otherCollectionID, "https://example.com/theirs2")

		page := listCollectionsOverHTTP(t, ownerID, "")

		page.requireCount(t, "Mine", 1, false)

		// The other user's collection is not reachable at all, which is the same
		// non-disclosure guarantee every listing here makes.
		_, _, found := page.lookupCount("Theirs")
		require.False(t, found, "another user's collection must not be listed")
	})

	// The count describes the collection, not the page. A badge that changed as the
	// user scrolled would not be a count of anything, and the first page reporting a
	// different number from the second is the failure this asserts against.
	t.Run("every cursor page reports the collection's whole contents", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-count-paged@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		firstID := createCollectionAt(t, userID, "First", base, "")
		secondID := createCollectionAt(t, userID, "Second", base.Add(time.Minute), "")
		thirdID := createCollectionAt(t, userID, "Third", base.Add(2*time.Minute), "")

		// The counts deliberately straddle the bound across two pages: the exact
		// one, the clipped one and an empty one are all on different pages, so a
		// count that meant "the rows on this page" would be visibly wrong.
		seedSavedItems(t, userID, secondID, collection.CollectionSavedItemsCountCap+3)
		seedSavedItems(t, userID, thirdID, 7)

		require.NotEqual(t, firstID, secondID)
		require.NotEqual(t, secondID, thirdID)

		seen := map[string][2]int64{}
		pages := 0

		cursor := ""

		for {
			query := "limit=1"
			if cursor != "" {
				query += "&cursor=" + cursor
			}

			page := listCollectionsOverHTTP(t, userID, query)
			pages++

			for _, c := range page.Data.Collections {
				seen[c.Name] = [2]int64{c.SavedItemsCount, boolToInt64(c.SavedItemsCountOverflow)}
			}

			if !page.Meta.Cursor.HasMore {
				break
			}

			require.NotNil(t, page.Meta.Cursor.NextCursor)

			cursor = *page.Meta.Cursor.NextCursor

			require.Less(t, pages, 20, "paging did not terminate")
		}

		require.GreaterOrEqual(t, pages, 3, "the fixture must span more than one page")

		require.Equal(t, [2]int64{0, 0}, seen["First"])
		require.Equal(
			t,
			[2]int64{collection.CollectionSavedItemsCountCap, 1},
			seen["Second"],
		)
		require.Equal(t, [2]int64{7, 0}, seen["Third"])
	})

	// Every statement in the listing projects the pair, and a statement that quietly
	// dropped it would report zero for every collection under one sort, or on one
	// resume point only. Walking all three pages of each sort is what covers all
	// three statement groups: the first page, the page after the pinned Unsorted row,
	// and the resumed page after a position.
	t.Run("the pair is reported under every sort and on every resume point", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-count-sorts@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		wishlistID := createCollectionAt(t, userID, "Wishlist", base, "")
		readLaterID := createCollectionAt(t, userID, "Read later", base.Add(time.Minute), "")

		// The counts deliberately straddle the bound, so a statement that dropped the
		// overflow flag or clipped at the wrong number is visibly wrong.
		seedSavedItems(t, userID, wishlistID, collection.CollectionSavedItemsCountCap+1)
		seedSavedItems(t, userID, readLaterID, 4)

		for _, sort := range []collection.CollectionSort{
			collection.CollectionSortNewest,
			collection.CollectionSortOldest,
			collection.CollectionSortName,
		} {
			t.Run(string(sort), func(t *testing.T) {
				counts := map[string][2]int64{}
				pages := 0

				cursor := ""

				for {
					query := "sort=" + string(sort) + "&limit=1"
					if cursor != "" {
						query += "&cursor=" + cursor
					}

					page := listCollectionsOverHTTP(t, userID, query)
					pages++

					for _, c := range page.Data.Collections {
						counts[c.Name] = [2]int64{
							c.SavedItemsCount,
							boolToInt64(c.SavedItemsCountOverflow),
						}
					}

					if !page.Meta.Cursor.HasMore {
						break
					}

					require.NotNil(t, page.Meta.Cursor.NextCursor)

					cursor = *page.Meta.Cursor.NextCursor

					require.Less(t, pages, 20, "paging did not terminate")
				}

				require.Equal(t, 3, pages, "three collections at one per page")
				require.Equal(
					t,
					[2]int64{collection.CollectionSavedItemsCountCap, 1},
					counts["Wishlist"],
				)
				require.Equal(t, [2]int64{4, 0}, counts["Read later"])
				require.Equal(t, [2]int64{0, 0}, counts["Unsorted"])
			})
		}
	})

	// The count is additive. Nothing about ordering, pinning or paging may move, and
	// this asserts that directly rather than trusting that a projection cannot. The
	// expected order differs per sort, so it is stated per sort rather than once.
	t.Run("ordering and pinning are unchanged by the count", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-count-order@example.com")
		createUnsorted(t, userID)

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		// Aardvark is older and alphabetically first, so the two sorts disagree about
		// the order after Unsorted. Both are asserted, so a sort that silently became
		// another one is caught.
		oldest := createCollectionAt(t, userID, "Aardvark", base, "")
		middle := createCollectionAt(t, userID, "Mammal", base.Add(time.Minute), "")

		seedSavedItems(t, userID, oldest, collection.CollectionSavedItemsCountCap+9)
		seedSavedItems(t, userID, middle, collection.CollectionSavedItemsCountCap+10)

		for _, tc := range []struct {
			sort collection.CollectionSort
			want []string
		}{
			{
				sort: collection.CollectionSortNewest,
				want: []string{"Unsorted", "Mammal", "Aardvark"},
			},
			{
				sort: collection.CollectionSortOldest,
				want: []string{"Unsorted", "Aardvark", "Mammal"},
			},
			{
				sort: collection.CollectionSortName,
				want: []string{"Unsorted", "Aardvark", "Mammal"},
			},
		} {
			page := listCollectionsOverHTTP(t, userID, "sort="+string(tc.sort))

			require.Equal(
				t,
				tc.want,
				page.names(),
				"sort %q must keep Unsorted pinned first and the documented order",
			)
		}
	})
}

// TestCollectionAPI_ListCollections_DocumentedShape pins the generated documentation
// to the real response, the way TestCollectionAPI_ListSavedItems_DocumentedShape does
// for the other two cursor listings.
//
// The endpoint's own description talks about saved_items_count,
// saved_items_count_overflow and meta.cursor, and a schema that omitted meta — or
// documented one count field and not the other — would contradict it while still
// parsing as valid OpenAPI. Nothing else in the suite catches that, so this walks
// the documented tree and the actual body side by side at every level: the envelope,
// data, one collection, and meta.cursor.
func TestCollectionAPI_ListCollections_DocumentedShape(t *testing.T) {
	truncateCollectionData(t)

	doc := loadSwaggerDoc(t)

	userID := createCollectionUser(t, "collection-list-docshape@example.com")
	createUnsorted(t, userID)

	// One collection of each interesting kind, so the documented element schema is
	// compared against both a system collection carrying a key and a user collection
	// whose key is null.
	wishlistID := createTestUserCollection(t, userID, "YouTube")
	createTestSystemCollection(t, userID, "reddit", "reddit")

	seedSavedItems(t, userID, wishlistID, 3)

	req := httptest.NewRequest(http.MethodGet, "/collections", nil)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	api := doc.definition(t, "collection.ListCollectionsAPIResponse")

	// The envelope itself: data, message, meta.
	requireSameKeys(t, "$", propertyKeys(api), jsonKeys(t, body, "$"))

	data := doc.property(t, api, "data", "$")

	actualData, ok := body["data"].(map[string]any)
	require.True(t, ok, "response has no data object")

	requireSameKeys(t, "data", propertyKeys(data), jsonKeys(t, actualData, "data"))

	// Resolving the array's element schema rather than the array itself is the point:
	// the element schema is what describes one collection's fields, and it is where a
	// count field added to the runtime type but not to the response would show up.
	documentedCollections := doc.arrayItem(
		t,
		doc.property(t, data, "collections", "data"),
		"data.collections",
	)

	actualCollections, ok := actualData["collections"].([]any)
	require.True(t, ok, "response has no collections array")
	require.NotEmpty(t, actualCollections)

	requireSameKeys(
		t,
		"data.collections[]",
		propertyKeys(documentedCollections),
		jsonKeys(t, actualCollections[0], "data.collections[]"),
	)

	// Both count keys are named explicitly and with the right type, because this is
	// the assertion that a schema documenting one of the pair as something else
	// fails. Types rather than a key-set comparison: these are scalars, so what
	// matters is that a client reading the document learns a number and a boolean.
	for field, wantType := range map[string]string{
		"saved_items_count":          "integer",
		"saved_items_count_overflow": "boolean",
	} {
		documented := doc.property(t, documentedCollections, field, "data.collections[]")

		gotType, ok := documented["type"].(string)
		require.True(t, ok, field+" must document a type")
		require.Equal(t, wantType, gotType, field)
	}

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

// seedSavedItems puts count saved items into a collection in one statement.
func seedSavedItems(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	count int,
) {
	t.Helper()

	require.NoError(
		t,
		collectiondbtest.New(testPool).CreateTestSavedItemsInCollection(
			context.Background(),
			collectiondbtest.CreateTestSavedItemsInCollectionParams{
				UserID:       userID,
				CollectionID: collectionID,
				ItemCount:    int32(count),
			},
		),
	)

	// Asserted from the database rather than from the listing, so a count that came
	// back wrong cannot be explained by a fixture that did not seed what it claimed.
	require.Equal(
		t,
		int64(count),
		countSavedItemsInCollectionForUser(t, collectionID, userID),
	)
}

func countSavedItemsInCollectionForUser(
	t *testing.T,
	collectionID uuid.UUID,
	userID uuid.UUID,
) int64 {
	t.Helper()

	count, err := collectiondbtest.New(testPool).CountSavedItemsInCollectionForUser(
		context.Background(),
		collectiondbtest.CountSavedItemsInCollectionForUserParams{
			CollectionID: collectionID,
			UserID:       userID,
		},
	)
	require.NoError(t, err)

	return count
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}

	return 0
}

// encodeTimestampCursor builds a structurally valid cursor carrying an arbitrary
// position value. It is how a test produces a token that decodes and passes every
// structural check while still holding something unusable.
func encodeTimestampCursor(
	t *testing.T,
	sort collection.CollectionSort,
	value string,
) string {
	t.Helper()

	group := 1
	id := uuid.New()

	token, err := pagination.Encode(collection.ListCollectionsCursor{
		Version: pagination.CurrentVersion,
		Sort:    sort,
		Group:   &group,
		Value:   &value,
		ID:      &id,
	})
	require.NoError(t, err)

	return token
}

// deleteCollectionRow removes a collection directly, bypassing the API so a test can
// delete a collection that still holds no constraint-relevant state.
func deleteCollectionRow(t *testing.T, collectionID uuid.UUID) {
	t.Helper()

	_, err := testPool.Exec(
		context.Background(),
		"DELETE FROM collections WHERE id = $1",
		collectionID,
	)
	require.NoError(t, err)
}

func renameCollection(t *testing.T, from string, to string) {
	t.Helper()

	_, err := testPool.Exec(
		context.Background(),
		"UPDATE collections SET name = $1 WHERE name = $2",
		to,
		from,
	)
	require.NoError(t, err)
}
