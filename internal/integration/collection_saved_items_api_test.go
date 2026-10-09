package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/api/collection"
	"github.com/thoriqr/stash-it-backend/internal/pagination"
	collectiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/collection/generated"
)

// listedSavedItem is one item as the collection listing reports it. It is decoded
// here rather than reusing the API response type so the suite asserts the wire shape
// itself, including which fields are nullable.
type listedSavedItem struct {
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
}

// listedSavedItemsCollection is the collection a page was read from. It is decoded
// separately from the API response type so the suite asserts the wire shape itself,
// including that nothing but id and name ever appears here.
type listedSavedItemsCollection struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type listSavedItemsPage struct {
	Data struct {
		Collection listedSavedItemsCollection `json:"collection"`
		SavedItems []listedSavedItem          `json:"saved_items"`
	} `json:"data"`
	Message string `json:"message"`
	Meta    struct {
		Cursor *struct {
			NextCursor *string `json:"next_cursor"`
			HasMore    bool    `json:"has_more"`
		} `json:"cursor"`
	} `json:"meta"`
}

func (p listSavedItemsPage) ids() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(p.Data.SavedItems))

	for _, item := range p.Data.SavedItems {
		ids = append(ids, item.ID)
	}

	return ids
}

// listCollectionSavedItemsOverHTTP calls the endpoint and requires a 200 with a fully
// decoded page.
func listCollectionSavedItemsOverHTTP(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	query string,
) listSavedItemsPage {
	t.Helper()

	path := "/collections/" + collectionID.String() + "/saved-items"
	if query != "" {
		path += "?" + query
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var page listSavedItemsPage

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&page))

	require.NotNil(t, page.Meta.Cursor, "every response must carry cursor metadata")

	return page
}

// walkCollectionSavedItems follows next_cursor to the end and returns every id seen
// in order, plus the page count. It is the workhorse for the paging invariants.
func walkCollectionSavedItems(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
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

		page := listCollectionSavedItemsOverHTTP(
			t,
			userID,
			collectionID,
			pageQuery,
		)
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

		require.Less(t, calls, 50, "paging did not terminate")
	}
}

func requireUniqueSavedItems(t *testing.T, ids []uuid.UUID) {
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

// createSavedItemAt seeds a saved item in a collection at an exact created_at, so a
// test can place it at a chosen position in the ordering or make rows genuinely tie.
func createSavedItemAt(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	url string,
	createdAt time.Time,
) uuid.UUID {
	t.Helper()

	db := collectiondbtest.New(testPool)

	item := createSavedItemInCollection(t, userID, collectionID, url)

	require.NoError(
		t,
		db.SetTestSavedItemEnrichmentStateAt(
			context.Background(),
			collectiondbtest.SetTestSavedItemEnrichmentStateAtParams{
				EnrichmentStatus: "pending",
				CreatedAt:        pgtype.Timestamptz{Time: createdAt, Valid: true},
				ID:               item,
			},
		),
	)

	return item
}

func TestCollectionAPI_ListSavedItems_Collection(t *testing.T) {
	// The response names the collection it read from, so a caller that arrived with
	// only an id has something to render a heading from.
	t.Run("reports the collection id and name", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-meta@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "YouTube")

		createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/watch",
		)

		page := listCollectionSavedItemsOverHTTP(t, userID, wishlistID, "")

		require.Equal(t, wishlistID, page.Data.Collection.ID)
		require.Equal(t, "YouTube", page.Data.Collection.Name)
	})

	// Exactly id and name. A field added here for symmetry with another endpoint's
	// collection shape would be a second contract for a client to read, so the test
	// pins the set rather than spot-checking two names.
	t.Run("reports nothing beyond id and name", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-onlyidname@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/watch",
		)

		req := httptest.NewRequest(
			http.MethodGet,
			"/collections/"+wishlistID.String()+"/saved-items",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var raw map[string]any

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))

		data, ok := raw["data"].(map[string]any)
		require.True(t, ok)

		collection, ok := data["collection"].(map[string]any)
		require.True(t, ok)

		keys := make([]string, 0, len(collection))
		for key := range collection {
			keys = append(keys, key)
		}

		require.ElementsMatch(t, []string{"id", "name"}, keys)
	})

	// Unsorted is not special-cased. It arrives through the same field with the same
	// two keys, which is the only way a client can treat the inbox as a collection.
	t.Run("reports unsorted through the same contract", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-metainbox@example.com")
		unsortedID := createUnsorted(t, userID)

		createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/inbox/1",
		)

		page := listCollectionSavedItemsOverHTTP(t, userID, unsortedID, "")

		require.Equal(t, unsortedID, page.Data.Collection.ID)
		require.NotEmpty(t, page.Data.Collection.Name)
		require.Equal(t, "Unsorted", page.Data.Collection.Name)
	})

	// An empty collection is still a collection. Omitting the field there would make
	// "nothing in it" indistinguishable from "no such collection", which is precisely
	// the distinction the 404 already draws.
	t.Run("reports the collection when saved_items is empty", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-metempty@example.com")
		createUnsorted(t, userID)

		emptyID := createTestUserCollection(t, userID, "Reading List")

		page := listCollectionSavedItemsOverHTTP(t, userID, emptyID, "")

		require.Empty(t, page.Data.SavedItems)
		require.Equal(t, emptyID, page.Data.Collection.ID)
		require.Equal(t, "Reading List", page.Data.Collection.Name)
	})

	// The collection is reported on every page, not only the first, so a resumed walk
	// carries the same heading without the client having to remember it.
	t.Run("reports the collection on every page", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-metapages@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		for i := range 5 {
			createSavedItemAt(
				t,
				userID,
				wishlistID,
				"https://example.com/page/"+string(rune('a'+i)),
				base.Add(time.Duration(i)*time.Minute),
			)
		}

		first := listCollectionSavedItemsOverHTTP(t, userID, wishlistID, "limit=2")
		require.True(t, first.Meta.Cursor.HasMore)
		require.Equal(t, wishlistID, first.Data.Collection.ID)

		second := listCollectionSavedItemsOverHTTP(
			t,
			userID,
			wishlistID,
			"limit=2&cursor="+*first.Meta.Cursor.NextCursor,
		)
		require.True(t, second.Meta.Cursor.HasMore)
		require.Equal(t, wishlistID, second.Data.Collection.ID)

		third := listCollectionSavedItemsOverHTTP(
			t,
			userID,
			wishlistID,
			"limit=2&cursor="+*second.Meta.Cursor.NextCursor,
		)

		// The final page still names the collection, and still carries the explicit
		// null cursor alongside it.
		require.False(t, third.Meta.Cursor.HasMore)
		require.Nil(t, third.Meta.Cursor.NextCursor)
		require.Equal(t, wishlistID, third.Data.Collection.ID)
		require.Equal(t, "Wishlist", third.Data.Collection.Name)
	})

	// Every item keeps its own collection_id, and it agrees with data.collection. That
	// redundancy is deliberate: collection_id is what makes an item self-describing
	// once it is carried out of this response.
	t.Run("keeps collection_id on every item, matching data.collection", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-metaitem@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")
		otherID := createTestUserCollection(t, userID, "Recipes")

		inWishlist := createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/wishlist/1",
		)
		inRecipes := createSavedItemInCollection(
			t,
			userID,
			otherID,
			"https://example.com/recipes/1",
		)

		wishlistPage := listCollectionSavedItemsOverHTTP(t, userID, wishlistID, "")
		require.Equal(t, wishlistID, wishlistPage.Data.Collection.ID)
		require.Equal(t, []uuid.UUID{inWishlist}, wishlistPage.ids())

		for _, item := range wishlistPage.Data.SavedItems {
			require.Equal(t, wishlistID, item.CollectionID)
		}

		recipesPage := listCollectionSavedItemsOverHTTP(t, userID, otherID, "")
		require.Equal(t, otherID, recipesPage.Data.Collection.ID)
		require.Equal(t, []uuid.UUID{inRecipes}, recipesPage.ids())

		for _, item := range recipesPage.Data.SavedItems {
			require.Equal(t, otherID, item.CollectionID)
		}
	})

	// Ownership isolation is unchanged: a foreign collection is a 404, so there is no
	// collection object to leak. There is nothing to assert beyond the error, and that
	// the error is the ordinary one.
	t.Run("reports no collection for a foreign collection", func(t *testing.T) {
		truncateCollectionData(t)

		ownerID := createCollectionUser(t, "collection-items-metaowner@example.com")
		otherID := createCollectionUser(t, "collection-items-metaother@example.com")

		createUnsorted(t, ownerID)
		createUnsorted(t, otherID)

		ownerCollectionID := createTestUserCollection(t, ownerID, "Private Secrets")

		req := httptest.NewRequest(
			http.MethodGet,
			"/collections/"+ownerCollectionID.String()+"/saved-items",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, otherID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)

		var body map[string]any

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

		// The collection's name does not appear anywhere in a response the caller was
		// not entitled to.
		require.NotContains(
			t,
			body,
			"data",
			"a 404 must not carry a data object",
		)

		raw, err := json.Marshal(body)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "Private Secrets")
	})
}

// swaggerDoc is the generated OpenAPI document, read so a test can check that what is
// documented is what the endpoint returns.
type swaggerDoc map[string]any

// loadSwaggerDoc reads docs/swagger.json.
//
// The path is relative to this package, so the test fails loudly rather than silently
// skipping if the file moves. A missing file means the generated docs were never
// regenerated, which is exactly the drift this test exists to catch.
func loadSwaggerDoc(t *testing.T) swaggerDoc {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "swagger.json"))
	require.NoError(
		t,
		err,
		"docs/swagger.json must exist; run swag init -g cmd/api/main.go -parseInternal",
	)

	var doc swaggerDoc

	require.NoError(t, json.Unmarshal(raw, &doc))

	return doc
}

// definition resolves a named definition to its schema, following $ref chains.
//
// The bound is a safety net rather than a real expectation: it stops a malformed
// document from spinning here forever and turns that into a readable failure.
func (d swaggerDoc) definition(t *testing.T, name string) map[string]any {
	t.Helper()

	definitions, ok := d["definitions"].(map[string]any)
	require.True(t, ok, "swagger.json has no definitions object")

	node, ok := definitions[name]
	require.True(t, ok, "swagger.json has no definition %q", name)

	return d.resolve(t, node, name)
}

func (d swaggerDoc) resolve(
	t *testing.T,
	node any,
	path string,
) map[string]any {
	t.Helper()

	for range 10 {
		schema, ok := node.(map[string]any)
		require.True(t, ok, "%s is not an object in swagger.json", path)

		if ref, ok := schema["$ref"].(string); ok {
			node, path = d.follow(t, ref, path)
			continue
		}

		// swag wraps a $ref that also carries a description in a single-element allOf,
		// so the description does not hide the reference behind it.
		if allOf, ok := schema["allOf"].([]any); ok && len(allOf) == 1 {
			node = allOf[0]
			continue
		}

		return schema
	}

	t.Fatalf("%s: swagger $ref chain did not terminate", path)

	return nil
}

func (d swaggerDoc) follow(
	t *testing.T,
	ref string,
	path string,
) (any, string) {
	t.Helper()

	const prefix = "#/definitions/"

	require.True(
		t,
		strings.HasPrefix(ref, prefix),
		"%s: only local definition refs are supported, got %q",
		path,
		ref,
	)

	name := strings.TrimPrefix(ref, prefix)

	definitions, ok := d["definitions"].(map[string]any)
	require.True(t, ok, "swagger.json has no definitions object")

	node, ok := definitions[name]
	require.True(t, ok, "swagger.json has no definition %q", name)

	return node, path + " -> " + name
}

// property resolves a named property of a schema.
func (d swaggerDoc) property(
	t *testing.T,
	schema map[string]any,
	name string,
	path string,
) map[string]any {
	t.Helper()

	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "%s has no properties", path)

	node, ok := properties[name]
	require.True(t, ok, "%s has no property %q", path, name)

	return d.resolve(t, node, path+"."+name)
}

// arrayItem resolves the schema of an array property's elements.
func (d swaggerDoc) arrayItem(
	t *testing.T,
	schema map[string]any,
	path string,
) map[string]any {
	t.Helper()

	items, ok := schema["items"]
	require.True(t, ok, "%s is not an array", path)

	return d.resolve(t, items, path+"[]")
}

// propertyKeys lists what a schema documents.
func propertyKeys(schema map[string]any) []string {
	properties, _ := schema["properties"].(map[string]any)

	keys := make([]string, 0, len(properties))

	for key := range properties {
		keys = append(keys, key)
	}

	return keys
}

// jsonKeys lists what a decoded response body actually carries.
func jsonKeys(t *testing.T, value any, path string) []string {
	t.Helper()

	object, ok := value.(map[string]any)
	require.True(t, ok, "%s is not an object in the response", path)

	keys := make([]string, 0, len(object))

	for key := range object {
		keys = append(keys, key)
	}

	return keys
}

// requireSameKeys asserts the documented key set and the actual one are identical, in
// both directions.
//
// Both halves are needed. Documentation naming a field the API omits sends a client
// looking for something that is not there, and a field the API returns but the schema
// does not is the one a client is silently missing. Asserting only that documented
// names exist would pass for both.
func requireSameKeys(
	t *testing.T,
	path string,
	documented []string,
	actual []string,
) {
	t.Helper()

	require.ElementsMatch(
		t,
		documented,
		actual,
		"%s: generated documentation and actual response disagree",
		path,
	)
}

// TestCollectionAPI_ListSavedItems_DocumentedShape pins the generated documentation to
// the real response.
//
// The endpoint's own @Description talks about meta.cursor, and a schema that omitted
// meta entirely would contradict it while still parsing as valid OpenAPI. Nothing else
// in the suite catches that, so this walks the documented tree and the actual body side
// by side at every level: the envelope, data, data.collection, one saved item, and
// meta.cursor.
func TestCollectionAPI_ListSavedItems_DocumentedShape(t *testing.T) {
	truncateCollectionData(t)

	doc := loadSwaggerDoc(t)

	userID := createCollectionUser(t, "collection-items-docshape@example.com")
	createUnsorted(t, userID)

	wishlistID := createTestUserCollection(t, userID, "YouTube")

	// One item is enough, but it must be enriched so every nullable column is
	// populated: the key sets are the same either way, and a populated item is the
	// case where an omission would be least visible.
	createSavedItemInCollection(t, userID, wishlistID, "https://example.com/watch")

	require.NoError(
		t,
		collectiondbtest.New(testPool).SetTestSavedItemEnrichmentStateAt(
			context.Background(),
			collectiondbtest.SetTestSavedItemEnrichmentStateAtParams{
				EnrichmentStatus: "completed",
				LastEnrichedAt: pgtype.Timestamptz{
					Time:  time.Now().Add(-time.Hour),
					Valid: true,
				},
				Title:       testText("A video"),
				Description: testText("A description"),
				ImageUrl:    testText("https://example.com/og.png"),
				CreatedAt: pgtype.Timestamptz{
					Time:  time.Now().Add(-time.Hour),
					Valid: true,
				},
				ID: mustOnlyItemID(t, userID, wishlistID),
			},
		),
	)

	req := httptest.NewRequest(
		http.MethodGet,
		"/collections/"+wishlistID.String()+"/saved-items",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	api := doc.definition(t, "collection.ListSavedItemsInCollectionAPIResponse")

	// The envelope itself: data, message, meta.
	requireSameKeys(t, "$", propertyKeys(api), jsonKeys(t, body, "$"))

	data := doc.property(t, api, "data", "$")

	actualData, ok := body["data"].(map[string]any)
	require.True(t, ok, "response has no data object")

	requireSameKeys(t, "data", propertyKeys(data), jsonKeys(t, actualData, "data"))

	// data.collection, which is the field this change added.
	documentedCollection := doc.property(t, data, "collection", "data")
	requireSameKeys(
		t,
		"data.collection",
		propertyKeys(documentedCollection),
		jsonKeys(t, actualData["collection"], "data.collection"),
	)

	// A saved item. Resolving the array's element schema rather than the array itself
	// is the point: the element schema is what describes one item's fields.
	documentedItems := doc.arrayItem(
		t,
		doc.property(t, data, "saved_items", "data"),
		"data.saved_items",
	)

	actualItems, ok := actualData["saved_items"].([]any)
	require.True(t, ok, "response has no saved_items array")
	require.Len(t, actualItems, 1)

	requireSameKeys(
		t,
		"data.saved_items[]",
		propertyKeys(documentedItems),
		jsonKeys(t, actualItems[0], "data.saved_items[]"),
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

// mustOnlyItemID returns the single item in a collection, for a fixture that needs its
// id after the fact.
func mustOnlyItemID(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
) uuid.UUID {
	t.Helper()

	page := listCollectionSavedItemsOverHTTP(t, userID, collectionID, "")
	require.Len(t, page.Data.SavedItems, 1)

	return page.Data.SavedItems[0].ID
}

func TestCollectionAPI_ListSavedItems(t *testing.T) {
	// The whole representation has to be present, including the enrichment columns.
	// A listing that omitted them could not tell a caller whether an item's metadata
	// had arrived.
	t.Run("returns the full saved item representation", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-full@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		item := createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/wishlist/full",
		)

		enrichedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)

		require.NoError(
			t,
			collectiondbtest.New(testPool).SetTestSavedItemEnrichmentStateAt(
				context.Background(),
				collectiondbtest.SetTestSavedItemEnrichmentStateAtParams{
					EnrichmentStatus: "completed",
					LastEnrichedAt: pgtype.Timestamptz{
						Time:  enrichedAt,
						Valid: true,
					},
					Title:       testText("An interesting article"),
					Description: testText("A short summary of the page"),
					ImageUrl:    testText("https://example.com/og.png"),
					CreatedAt:   pgtype.Timestamptz{Time: enrichedAt, Valid: true},
					ID:          item,
				},
			),
		)

		page := listCollectionSavedItemsOverHTTP(t, userID, wishlistID, "")

		require.Len(t, page.Data.SavedItems, 1)

		got := page.Data.SavedItems[0]

		require.Equal(t, item, got.ID)
		require.Equal(t, "https://example.com/wishlist/full", got.URL)
		require.NotNil(t, got.Domain)
		require.NotNil(t, got.Title)
		require.NotNil(t, got.Description)
		require.NotNil(t, got.ImageURL)
		require.Equal(t, wishlistID, got.CollectionID)
		require.Equal(t, "completed", got.EnrichmentStatus)

		require.NotNil(t, got.LastEnrichedAt)
		require.True(t, got.LastEnrichedAt.Equal(enrichedAt))

		require.False(t, got.CreatedAt.IsZero())
		require.False(t, got.UpdatedAt.IsZero())
	})

	// A null title and a failed enrichment are ordinary states of an item, not reasons
	// to omit it from a listing.
	t.Run("returns items with a null title and a failed enrichment", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-failed@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		failed := createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/wishlist/failed",
		)

		pending := createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/wishlist/pending",
		)

		db := collectiondbtest.New(testPool)

		require.NoError(
			t,
			db.SetTestSavedItemEnrichmentStateAt(
				context.Background(),
				collectiondbtest.SetTestSavedItemEnrichmentStateAtParams{
					EnrichmentStatus: "failed",
					CreatedAt: pgtype.Timestamptz{
						Time:  time.Now().Add(-time.Minute),
						Valid: true,
					},
					ID: failed,
				},
			),
		)

		require.NoError(
			t,
			db.SetTestSavedItemEnrichmentStateAt(
				context.Background(),
				collectiondbtest.SetTestSavedItemEnrichmentStateAtParams{
					EnrichmentStatus: "pending",
					CreatedAt: pgtype.Timestamptz{
						Time:  time.Now().Add(-2 * time.Minute),
						Valid: true,
					},
					ID: pending,
				},
			),
		)

		page := listCollectionSavedItemsOverHTTP(t, userID, wishlistID, "")

		require.Len(t, page.Data.SavedItems, 2)

		byID := map[uuid.UUID]listedSavedItem{}

		for _, item := range page.Data.SavedItems {
			byID[item.ID] = item
		}

		require.Contains(t, byID, failed)
		require.Contains(t, byID, pending)

		// A failed item keeps its row, reports its status, and reports that nothing
		// was ever refreshed.
		require.Equal(t, "failed", byID[failed].EnrichmentStatus)
		require.Nil(t, byID[failed].Title)
		require.Nil(t, byID[failed].Description)
		require.Nil(t, byID[failed].ImageURL)
		require.Nil(t, byID[failed].LastEnrichedAt)

		require.Equal(t, "pending", byID[pending].EnrichmentStatus)
		require.Nil(t, byID[pending].Title)
		require.Nil(t, byID[pending].LastEnrichedAt)
	})

	t.Run("returns only the requested collection's items", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-scoped@example.com")

		unsortedID := createUnsorted(t, userID)
		wishlistID := createTestUserCollection(t, userID, "Wishlist")
		otherID := createTestUserCollection(t, userID, "Recipes")

		wanted := createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/wishlist/1",
		)

		createSavedItemInCollection(
			t,
			userID,
			otherID,
			"https://example.com/recipes/1",
		)

		// Unsorted already exists from earlier in this test; an item in it proves the
		// listing is scoped to one collection rather than returning a user's items.
		inboxItem := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/inbox/1",
		)

		page := listCollectionSavedItemsOverHTTP(t, userID, wishlistID, "")

		require.Equal(t, []uuid.UUID{wanted}, page.ids())
		require.NotContains(t, page.ids(), inboxItem)
	})

	// Unsorted is an ordinary collection here. Nothing in this listing is
	// special-cased for it, so it needs no separate rule beyond the one every
	// collection follows.
	t.Run("lists unsorted like any other collection", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-unsorted@example.com")
		unsortedID := createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		inboxItem := createSavedItemInCollection(
			t,
			userID,
			unsortedID,
			"https://example.com/inbox/1",
		)

		createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/wishlist/1",
		)

		page := listCollectionSavedItemsOverHTTP(t, userID, unsortedID, "")

		require.Equal(t, []uuid.UUID{inboxItem}, page.ids())
		require.False(t, page.Meta.Cursor.HasMore)
		require.Nil(t, page.Meta.Cursor.NextCursor)
	})

	// Listing is a read. It must not queue enrichment work, so a caller polling a
	// collection does not fetch every item in it.
	t.Run("does not queue enrichment", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-noenqueue@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/wishlist/pending",
		)

		// The queue-facing assertion lives in the worker suite; here the item is left
		// pending and the listing is made, which is only meaningful because the
		// collection slice holds no enqueuer at all.
		page := listCollectionSavedItemsOverHTTP(t, userID, wishlistID, "")

		require.Len(t, page.Data.SavedItems, 1)
		require.Equal(t, "pending", page.Data.SavedItems[0].EnrichmentStatus)

		// Listing changed nothing: the item is still exactly as it was.
		state := savedItemState(t, page.Data.SavedItems[0].ID)

		require.Equal(t, wishlistID, state.CollectionID)
		require.Equal(t, "pending", state.EnrichmentStatus)
		require.False(t, state.LastEnrichedAt.Valid)
	})

	t.Run("orders newest first", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-order@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		oldest := createSavedItemAt(
			t, userID, wishlistID, "https://example.com/oldest", base,
		)
		newest := createSavedItemAt(
			t,
			userID,
			wishlistID,
			"https://example.com/newest",
			base.Add(2*time.Minute),
		)
		middle := createSavedItemAt(
			t,
			userID,
			wishlistID,
			"https://example.com/middle",
			base.Add(time.Minute),
		)

		page := listCollectionSavedItemsOverHTTP(t, userID, wishlistID, "")

		require.Equal(t, []uuid.UUID{newest, middle, oldest}, page.ids())
	})

	// created_at is constant within a transaction, so rows inserted together genuinely
	// tie. Without the id tie-break a page boundary can skip or repeat an item, and that
	// failure is silent.
	t.Run("keeps a stable order when timestamps are equal", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-ties@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		tiedAt := time.Now().Add(-time.Hour).Truncate(time.Second)

		for i := range 5 {
			createSavedItemAt(
				t,
				userID,
				wishlistID,
				"https://example.com/tied/"+string(rune('a'+i)),
				tiedAt,
			)
		}

		// limit=2 pages straight through the tied block, so the boundary lands inside
		// it repeatedly.
		seen, calls := walkCollectionSavedItems(t, userID, wishlistID, "limit=2")

		require.Greater(t, calls, 2)
		requireUniqueSavedItems(t, seen)
		require.Len(t, seen, 5)
	})

	t.Run("pages without skipping or repeating", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-walk@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		expected := make([]uuid.UUID, 0, 7)

		for i := range 7 {
			expected = append(
				expected,
				createSavedItemAt(
					t,
					userID,
					wishlistID,
					"https://example.com/item/"+string(rune('a'+i)),
					base.Add(time.Duration(i)*time.Minute),
				),
			)
		}

		seen, _ := walkCollectionSavedItems(t, userID, wishlistID, "limit=3")

		require.Len(t, seen, len(expected))
		requireUniqueSavedItems(t, seen)

		for _, id := range expected {
			require.Contains(t, seen, id)
		}
	})

	// A collection holding nothing is an ordinary state, and the response is an empty
	// array rather than null.
	t.Run("returns an empty array for an empty collection", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-empty@example.com")
		createUnsorted(t, userID)

		emptyID := createTestUserCollection(t, userID, "Nothing")

		req := httptest.NewRequest(
			http.MethodGet,
			"/collections/"+emptyID.String()+"/saved-items",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Contains(
			t,
			string(raw),
			`"saved_items":[]`,
			"an empty collection must serialize as [] rather than null",
		)

		var page listSavedItemsPage

		require.NoError(t, json.Unmarshal(raw, &page))
		require.Empty(t, page.Data.SavedItems)
		require.False(t, page.Meta.Cursor.HasMore)
		require.Nil(t, page.Meta.Cursor.NextCursor)
	})

	t.Run("serializes next_cursor as null on the final page", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-nullcursor@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/only",
		)

		req := httptest.NewRequest(
			http.MethodGet,
			"/collections/"+wishlistID.String()+"/saved-items",
			nil,
		)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Contains(
			t,
			string(raw),
			`"next_cursor":null`,
			"the final page must carry an explicit null next_cursor",
		)

		require.NotContains(
			t,
			string(raw),
			`"pagination"`,
			"this listing reports no offset fields",
		)
	})

	t.Run("applies the default limit", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-default@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		for i := range 25 {
			createSavedItemAt(
				t,
				userID,
				wishlistID,
				"https://example.com/bulk/"+string(rune('a'+i%26))+string(rune('a'+i/26)),
				base.Add(time.Duration(i)*time.Second),
			)
		}

		page := listCollectionSavedItemsOverHTTP(t, userID, wishlistID, "")

		require.Len(
			t,
			page.Data.SavedItems,
			collection.SavedItemListDefaultLimit,
		)
		require.True(t, page.Meta.Cursor.HasMore)
	})
}

func TestCollectionAPI_ListSavedItems_CursorBehaviour(t *testing.T) {
	// A cursor is a position, not a row reference. Nothing in the resumed query reads
	// the item the cursor came from, so deleting it must not stop the walk.
	t.Run("a cursor keeps working after its anchor item is deleted", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-anchor@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		first := createSavedItemAt(
			t, userID, wishlistID, "https://example.com/first", base,
		)
		anchor := createSavedItemAt(
			t,
			userID,
			wishlistID,
			"https://example.com/anchor",
			base.Add(time.Minute),
		)
		last := createSavedItemAt(
			t,
			userID,
			wishlistID,
			"https://example.com/last",
			base.Add(2*time.Minute),
		)

		// Newest first, so limit 1 walks last, anchor, first.
		pageOne := listCollectionSavedItemsOverHTTP(
			t, userID, wishlistID, "limit=1",
		)

		require.Equal(t, []uuid.UUID{last}, pageOne.ids())
		require.True(t, pageOne.Meta.Cursor.HasMore)

		pageTwo := listCollectionSavedItemsOverHTTP(
			t,
			userID,
			wishlistID,
			"limit=1&cursor="+*pageOne.Meta.Cursor.NextCursor,
		)

		require.Equal(t, []uuid.UUID{anchor}, pageTwo.ids())
		require.True(t, pageTwo.Meta.Cursor.HasMore)

		deleteSavedItemRow(t, anchor)

		pageThree := listCollectionSavedItemsOverHTTP(
			t,
			userID,
			wishlistID,
			"limit=1&cursor="+*pageTwo.Meta.Cursor.NextCursor,
		)

		// It returns what sorts after the recorded position, not an empty page.
		require.Equal(t, []uuid.UUID{first}, pageThree.ids())
		require.False(t, pageThree.Meta.Cursor.HasMore)
		require.Nil(t, pageThree.Meta.Cursor.NextCursor)
	})

	// Deleting a collection removes its items, so a resumed cursor then reports an
	// empty page rather than failing. A client has to refetch, which is the correct
	// outcome for a resource that is no longer there.
	t.Run("a cursor past a deleted collection returns an empty page", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-deleted@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		base := time.Now().Add(-time.Hour).Truncate(time.Second)

		createSavedItemAt(
			t, userID, wishlistID, "https://example.com/first", base,
		)
		createSavedItemAt(
			t,
			userID,
			wishlistID,
			"https://example.com/second",
			base.Add(time.Minute),
		)

		pageOne := listCollectionSavedItemsOverHTTP(
			t, userID, wishlistID, "limit=1",
		)

		require.True(t, pageOne.Meta.Cursor.HasMore)
		require.NotNil(t, pageOne.Meta.Cursor.NextCursor)

		// Delete through the real endpoint so the children go with it.
		svc := newRealCollectionService(t)

		require.NoError(
			t,
			svc.DeleteCollection(
				context.Background(),
				collection.DeleteCollectionParams{
					UserID:       userID,
					CollectionID: wishlistID,
					Action:       collection.SavedItemsActionDelete,
				},
			),
		)

		require.False(t, collectionExistsInDB(t, wishlistID))

		resp := listSavedItemsErrorResponse(
			t,
			userID,
			wishlistID,
			"cursor="+*pageOne.Meta.Cursor.NextCursor,
		)

		// The collection itself is gone, so this is a not found rather than an empty
		// listing: the resource being addressed no longer exists.
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestCollectionAPI_ListSavedItems_Errors(t *testing.T) {
	listSavedItemError := func(
		t *testing.T,
		userID uuid.UUID,
		collectionID uuid.UUID,
		query string,
		expectedStatus int,
		expectedCode string,
	) {
		t.Helper()

		path := "/collections/" + collectionID.String() + "/saved-items"
		if query != "" {
			path += "?" + query
		}

		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		requireErrorCode(t, resp, expectedStatus, expectedCode)
	}

	t.Run("cannot list another user's collection", func(t *testing.T) {
		truncateCollectionData(t)

		ownerID := createCollectionUser(t, "collection-items-owner@example.com")
		otherID := createCollectionUser(t, "collection-items-other@example.com")

		createUnsorted(t, ownerID)
		createUnsorted(t, otherID)

		ownerCollectionID := createTestUserCollection(t, ownerID, "Private")

		ownerItem := createSavedItemInCollection(
			t,
			ownerID,
			ownerCollectionID,
			"https://example.com/owners",
		)

		otherCollectionID := createTestUserCollection(t, otherID, "Theirs")

		// A collection with nothing in it, which would be an empty 200 listing if the
		// ownership check were absent.
		ownPage := listCollectionSavedItemsOverHTTP(
			t,
			otherID,
			otherCollectionID,
			"",
		)
		require.Empty(t, ownPage.Data.SavedItems)

		listSavedItemError(
			t,
			otherID,
			ownerCollectionID,
			"",
			http.StatusNotFound,
			collection.CodeCollectionNotFound,
		)

		// The owner's item is untouched.
		require.True(t, savedItemExists(t, ownerItem))
		require.Equal(
			t,
			ownerCollectionID,
			savedItemState(t, ownerItem).CollectionID,
		)
	})

	// An unknown id and a foreign one must be indistinguishable, or this endpoint
	// discloses whether a collection id exists.
	t.Run("not found responses are identical for unknown and foreign ids", func(t *testing.T) {
		truncateCollectionData(t)

		ownerID := createCollectionUser(t, "collection-items-nf-owner@example.com")
		otherID := createCollectionUser(t, "collection-items-nf-other@example.com")

		createUnsorted(t, ownerID)
		createUnsorted(t, otherID)

		ownerCollectionID := createTestUserCollection(t, ownerID, "Private")

		readBody := func(collectionID uuid.UUID) string {
			req := httptest.NewRequest(
				http.MethodGet,
				"/collections/"+collectionID.String()+"/saved-items",
				nil,
			)
			req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, otherID))

			resp, err := testApp.Test(req)
			require.NoError(t, err)
			require.Equal(t, http.StatusNotFound, resp.StatusCode)

			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			return string(raw)
		}

		require.Equal(
			t,
			readBody(uuid.New()),
			readBody(ownerCollectionID),
		)
	})

	t.Run("rejects an invalid limit", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-badlimit@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		listSavedItemError(
			t,
			userID,
			wishlistID,
			"limit=51",
			http.StatusBadRequest,
			"VALIDATION_ERROR",
		)
		listSavedItemError(
			t,
			userID,
			wishlistID,
			"limit=-1",
			http.StatusBadRequest,
			"VALIDATION_ERROR",
		)
		listSavedItemError(
			t,
			userID,
			wishlistID,
			"limit=abc",
			http.StatusBadRequest,
			"BAD_REQUEST",
		)
	})

	// A cursor that decodes cleanly and passes every structural check can still hold a
	// value a timestamptz comparison cannot use. It must be reported as the bad cursor
	// it is, not as a server fault.
	t.Run("rejects a decodable cursor holding an invalid timestamp", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-badts@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		createSavedItemInCollection(
			t,
			userID,
			wishlistID,
			"https://example.com/one",
		)

		for _, value := range []string{
			"not-a-timestamp",
			"2026-13-45T99:99:99Z",
			"1750000000",
			"z",
		} {
			listSavedItemError(
				t,
				userID,
				wishlistID,
				"cursor="+encodeSavedItemCursor(t, value),
				http.StatusBadRequest,
				collection.CodeInvalidCursor,
			)
		}
	})

	t.Run("rejects a malformed cursor", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-badcursor@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		listSavedItemError(
			t,
			userID,
			wishlistID,
			"cursor=not-a-cursor%21%21",
			http.StatusBadRequest,
			collection.CodeInvalidCursor,
		)
	})

	t.Run("rejects a cursor with no position", func(t *testing.T) {
		truncateCollectionData(t)

		userID := createCollectionUser(t, "collection-items-barecursor@example.com")
		createUnsorted(t, userID)

		wishlistID := createTestUserCollection(t, userID, "Wishlist")

		token, err := pagination.Encode(
			collection.ListSavedItemsInCollectionCursor{
				Version: pagination.CurrentVersion,
			},
		)
		require.NoError(t, err)

		listSavedItemError(
			t,
			userID,
			wishlistID,
			"cursor="+token,
			http.StatusBadRequest,
			collection.CodeInvalidCursor,
		)
	})

	t.Run("rejects a malformed collection id", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/collections/not-a-uuid/saved-items",
			nil,
		)
		req.Header.Set(
			"Authorization",
			"Bearer "+newTestAccessToken(
				t,
				createCollectionUser(t, "collection-items-badpath@example.com"),
			),
		)

		resp, err := testApp.Test(req)
		require.NoError(t, err)

		requireErrorCode(t, resp, http.StatusBadRequest, "BAD_REQUEST")
	})

	t.Run("rejects a request without an access token", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/collections/"+uuid.New().String()+"/saved-items",
			nil,
		)

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

// TestSavedItems_ListRouteIsGone pins the removal of GET /saved-items.
//
// Every saved item belongs to a collection, so the listing is now addressed through
// one. The route must not survive as a second way to ask for the same thing, because a
// client that used it would receive a representation without the enrichment columns
// the collection listing reports.
func TestSavedItems_ListRouteIsGone(t *testing.T) {
	truncateCollectionData(t)

	userID := createCollectionUser(t, "saved-items-list-removed@example.com")
	createUnsorted(t, userID)

	req := httptest.NewRequest(http.MethodGet, "/saved-items", nil)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	// No GET route matches /saved-items any more. POST does, so the router reports the
	// method as not allowed rather than the path as missing, which is itself the
	// evidence that the listing route specifically was removed while the resource
	// stayed.
	require.Equal(
		t,
		http.StatusMethodNotAllowed,
		resp.StatusCode,
		"GET /saved-items must no longer be registered",
	)

	// The saved-item resource itself is untouched by that removal.
	singleReq := httptest.NewRequest(
		http.MethodGet,
		"/saved-items/"+uuid.New().String(),
		nil,
	)
	singleReq.Header.Set(
		"Authorization",
		"Bearer "+newTestAccessToken(t, userID),
	)

	singleResp, err := testApp.Test(singleReq)
	require.NoError(t, err)

	require.Equal(
		t,
		http.StatusNotFound,
		singleResp.StatusCode,
		"GET /saved-items/:id must still exist and report a missing item",
	)
}

// encodeSavedItemCursor builds a structurally valid cursor carrying an arbitrary
// position value.
func encodeSavedItemCursor(t *testing.T, value string) string {
	t.Helper()

	id := uuid.New()

	token, err := pagination.Encode(
		collection.ListSavedItemsInCollectionCursor{
			Version: pagination.CurrentVersion,
			Value:   &value,
			ID:      &id,
		},
	)
	require.NoError(t, err)

	return token
}

func deleteSavedItemRow(t *testing.T, savedItemID uuid.UUID) {
	t.Helper()

	_, err := testPool.Exec(
		context.Background(),
		"DELETE FROM saved_items WHERE id = $1",
		savedItemID,
	)
	require.NoError(t, err)
}

func listSavedItemsErrorResponse(
	t *testing.T,
	userID uuid.UUID,
	collectionID uuid.UUID,
	query string,
) *http.Response {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodGet,
		"/collections/"+collectionID.String()+"/saved-items?"+query,
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)

	return resp
}
