package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// searchSavedItemFixture seeds one enriched item in a named collection and returns the
// ids a contract assertion needs.
//
// The item is enriched on purpose: every nullable column then carries a value, which is
// the case where a field the endpoint never emits would be least visible.
func searchSavedItemFixture(
	t *testing.T,
	email string,
) (userID uuid.UUID, itemID uuid.UUID, collectionID uuid.UUID) {
	t.Helper()

	truncateSearchData(t)

	userID, _ = createSearchUserWithUnsorted(t, email)

	collectionID = createSearchCollection(t, userID, "Camera Gear", "user", "")

	itemID = createSearchSavedItem(
		t,
		userID,
		collectionID,
		"https://example.org/notes/camera-buying-guide",
		"example.org",
		"Camera Buying Guide",
	)

	setSearchSavedItemEnrichmentState(
		t,
		itemID,
		"completed",
		"Camera Buying Guide",
		"https://example.org/og.png",
	)

	return userID, itemID, collectionID
}

// searchResults performs the search and returns the saved item carrying itemID together
// with the collections array.
//
// The body is decoded as plain JSON rather than into a typed struct because the
// assertions below are about exact key sets. A typed decode cannot report a field that
// was never sent: an undeclared key is simply absent from the struct.
func searchResults(
	t *testing.T,
	userID uuid.UUID,
	query string,
	itemID uuid.UUID,
) (savedItem map[string]any, collections []any) {
	t.Helper()

	resp := runSearch(t, userID, query, true)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Data struct {
			Collections []map[string]any `json:"collections"`
			SavedItems  []map[string]any `json:"saved_items"`
		} `json:"data"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	for _, candidate := range body.Data.SavedItems {
		if candidate["id"] == itemID.String() {
			savedItem = candidate
		}
	}

	require.NotNil(t, savedItem, "search returned no result for %s", itemID)

	for _, candidate := range body.Data.Collections {
		if candidate["id"] == collectionIDOf(savedItem) {
			collections = append(collections, candidate)
		}
	}

	return savedItem, collections
}

// collectionIDOf reads the nested collection's id out of a decoded result.
func collectionIDOf(savedItem map[string]any) string {
	nested, ok := savedItem["collection"].(map[string]any)
	if !ok {
		return ""
	}

	id, _ := nested["id"].(string)

	return id
}

// TestSearchAPI_ResponseContract pins the successful response to exactly the fields a
// client is meant to receive.
//
// The assertions are on key sets rather than on individual values, because a decoder
// would leave an undeclared field unset either way: checking that a field is empty
// proves nothing about whether the endpoint sent it. Pinning the set is what stops one
// being added or dropped without anyone deciding to.
func TestSearchAPI_ResponseContract(t *testing.T) {
	userID, itemID, collectionID := searchSavedItemFixture(
		t,
		"search-contract-fields@example.com",
	)

	savedItem, collections := searchResults(t, userID, "camera", itemID)

	// A saved item result: the agreed fields and nothing else. In particular there is
	// no collection_id beside the collection object, and no platform, description,
	// last_enriched_at, updated_at or score.
	require.ElementsMatch(
		t,
		[]string{
			"id", "title", "url", "domain", "image_url",
			"enrichment_status", "collection", "created_at",
		},
		rawKeys(t, savedItem),
	)

	require.Equal(t, itemID.String(), savedItem["id"])
	require.Equal(t, "Camera Buying Guide", savedItem["title"])
	require.Equal(t, "https://example.org/notes/camera-buying-guide", savedItem["url"])
	require.Equal(t, "example.org", savedItem["domain"])
	require.Equal(t, "https://example.org/og.png", savedItem["image_url"])
	require.Equal(t, "completed", savedItem["enrichment_status"])
	require.NotEmpty(t, savedItem["created_at"])

	// The collection is reported as an object carrying the item's own collection, so a
	// client can show and link to it without another request.
	nested, ok := savedItem["collection"].(map[string]any)
	require.True(t, ok, "saved item has no collection object")

	require.ElementsMatch(t, []string{"id", "name"}, rawKeys(t, nested))
	require.Equal(t, collectionID.String(), nested["id"])
	require.Equal(t, "Camera Gear", nested["name"])

	// A collection result is a destination: an id to navigate with and a name to
	// render. type and system_key are not reported here, and the id is what the
	// client passes to GET /collections/{id}/saved-items.
	require.Len(t, collections, 1)
	require.ElementsMatch(t, []string{"id", "name"}, rawKeys(t, collections[0].(map[string]any)))
	require.Equal(t, collectionID.String(), collections[0].(map[string]any)["id"])
	require.Equal(t, "Camera Gear", collections[0].(map[string]any)["name"])
}

// Unsorted is not special-cased: an item in the inbox arrives through the same nested
// object with the same two keys, so a client can treat it as any other collection.
func TestSearchAPI_ResponseContract_Unsorted(t *testing.T) {
	truncateSearchData(t)

	userID, unsortedID := createSearchUserWithUnsorted(
		t,
		"search-contract-unsorted@example.com",
	)

	itemID := createSearchSavedItem(
		t,
		userID,
		unsortedID,
		"https://example.org/notes/camera-buying-guide",
		"",
		"",
	)

	savedItem, _ := searchResults(t, userID, "camera", itemID)

	nested, ok := savedItem["collection"].(map[string]any)
	require.True(t, ok, "saved item has no collection object")

	require.ElementsMatch(t, []string{"id", "name"}, rawKeys(t, nested))
	require.Equal(t, unsortedID.String(), nested["id"])
	require.Equal(t, "Unsorted", nested["name"])

	// An item nothing has read for reports nulls rather than a value assembled from
	// its URL, and enrichment_status is what says which of the two cases it is.
	require.Nil(t, savedItem["title"])
	require.Nil(t, savedItem["domain"])
	require.Nil(t, savedItem["image_url"])
	require.Equal(t, "pending", savedItem["enrichment_status"])
}

// The envelope is data and message. There is no meta: search is capped rather than
// paged, so it has no cursor to report and must not grow a meta object to resemble the
// paginated listings.
func TestSearchAPI_ResponseContract_Envelope(t *testing.T) {
	userID, _, _ := searchSavedItemFixture(t, "search-contract-envelope@example.com")

	resp := runSearch(t, userID, "camera", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := decodeObject(t, resp.Body)

	require.ElementsMatch(t, []string{"data", "message"}, rawKeys(t, body))
	require.Equal(t, "search completed successfully", body["message"])

	data := dataObject(t, body)
	require.ElementsMatch(t, []string{"saved_items", "collections"}, rawKeys(t, data))
}

// TestSearchAPI_ResponseContract_DocumentedShape pins the generated documentation to
// the real response.
//
// The drift this catches is invisible at runtime: the body is built by hand rather than
// from the schema, so a documented field the endpoint omits and an undocumented field it
// sends both pass every other test in the suite. This walks the documented tree and the
// actual body side by side at every level.
func TestSearchAPI_ResponseContract_DocumentedShape(t *testing.T) {
	userID, itemID, _ := searchSavedItemFixture(
		t,
		"search-contract-docshape@example.com",
	)

	doc := loadSwaggerDoc(t)

	resp := runSearch(t, userID, "camera", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := decodeObject(t, resp.Body)

	api := doc.definition(t, "search.SearchAPIResponse")

	// The envelope itself: data and message.
	requireSameKeys(t, "$", propertyKeys(api), rawKeys(t, body))

	data := doc.property(t, api, "data", "$")
	actualData := dataObject(t, body)

	requireSameKeys(t, "data", propertyKeys(data), rawKeys(t, actualData))

	// A saved item. Resolving the array's element schema rather than the array itself
	// is the point: the element schema is what describes one result's fields.
	documentedItem := doc.arrayItem(
		t,
		doc.property(t, data, "saved_items", "data"),
		"data.saved_items",
	)

	actualItems, ok := actualData["saved_items"].([]any)
	require.True(t, ok, "response has no saved_items array")

	var actualItem map[string]any

	for _, element := range actualItems {
		candidate, isObject := element.(map[string]any)
		require.True(t, isObject)

		if candidate["id"] == itemID.String() {
			actualItem = candidate
		}
	}

	require.NotNil(t, actualItem)

	requireSameKeys(
		t,
		"data.saved_items[]",
		propertyKeys(documentedItem),
		rawKeys(t, actualItem),
	)

	// The nested collection on a saved item.
	documentedCollection := doc.property(
		t,
		documentedItem,
		"collection",
		"data.saved_items[]",
	)

	actualCollection, ok := actualItem["collection"].(map[string]any)
	require.True(t, ok, "saved item has no collection object")

	requireSameKeys(
		t,
		"data.saved_items[].collection",
		propertyKeys(documentedCollection),
		rawKeys(t, actualCollection),
	)

	// A collection result.
	documentedCollections := doc.arrayItem(
		t,
		doc.property(t, data, "collections", "data"),
		"data.collections",
	)

	actualCollections, ok := actualData["collections"].([]any)
	require.True(t, ok, "response has no collections array")
	require.NotEmpty(t, actualCollections)

	for _, element := range actualCollections {
		candidate, isObject := element.(map[string]any)
		require.True(t, isObject)

		requireSameKeys(
			t,
			"data.collections[]",
			propertyKeys(documentedCollections),
			rawKeys(t, candidate),
		)
	}
}

// The search response documentation names the endpoint's own result shape, so the
// description and the schema beside it have to agree about the fields. This asserts the
// description mentions what the endpoint now returns, because a description promising a
// field the schema omits is valid OpenAPI and so passes every linter.
func TestSearchAPI_ResponseContract_DocumentedDescription(t *testing.T) {
	doc := loadSwaggerDoc(t)

	paths, ok := doc["paths"].(map[string]any)
	require.True(t, ok, "swagger.json has no paths object")

	searchPath, ok := paths["/search"].(map[string]any)
	require.True(t, ok, "swagger.json documents no /search path")

	get, ok := searchPath["get"].(map[string]any)
	require.True(t, ok, "swagger.json documents no GET /search")

	description, ok := get["description"].(string)
	require.True(t, ok, "GET /search has no description")

	for _, promised := range []string{
		"enrichment_status",
		"GET /collections/{id}/saved-items",
	} {
		require.Contains(t, description, promised)
	}
}

// A search that matched nothing is a successful request with empty arrays. The contract
// above is about a populated result, so this is where the empty case is pinned.
func TestSearchAPI_ResponseContract_Empty(t *testing.T) {
	userID, _, _ := searchSavedItemFixture(t, "search-contract-empty@example.com")

	resp := runSearch(t, userID, "zzzqqq", true)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	raw := readResponseBody(t, resp)

	// Both keys present, as arrays, never null and never absent.
	require.Contains(t, raw, `"saved_items":[]`)
	require.Contains(t, raw, `"collections":[]`)

	body := decodeObject(t, strings.NewReader(raw))

	data := dataObject(t, body)
	require.ElementsMatch(t, []string{"saved_items", "collections"}, rawKeys(t, data))
}
