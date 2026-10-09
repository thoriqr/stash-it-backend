package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	saveditemdbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/saved_item/generated"
	// The collection slice's test helper already sets the full enrichment state on a
	// saved item. Reused rather than duplicated: the columns and the valid values
	// are the same, and a second copy in the saved_item helpers could disagree with
	// this one about what a valid enrichment state looks like.
	collectiondbtest "github.com/thoriqr/stash-it-backend/internal/testutil/db/collection/generated"
)

// rawKeys returns the keys of a decoded JSON object, so a test can assert on the
// exact set rather than on a list of fields it happens to remember to check.
func rawKeys(t *testing.T, raw map[string]any) []string {
	t.Helper()

	keys := make([]string, 0, len(raw))

	for key := range raw {
		keys = append(keys, key)
	}

	return keys
}

// decodeObject decodes a response body as a plain object.
func decodeObject(t *testing.T, body io.Reader) map[string]any {
	t.Helper()

	var raw map[string]any

	require.NoError(t, json.NewDecoder(body).Decode(&raw))

	return raw
}

// postSavedItem saves a URL and returns the response and the raw body, so a test can
// assert on both the decoded shape and the exact key set.
func postSavedItem(
	t *testing.T,
	userID uuid.UUID,
	rawURL string,
) (*http.Response, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/saved-items",
		strings.NewReader(`{"url": "`+rawURL+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, userID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	return resp, decodeObject(t, resp.Body)
}

// getSavedItem reads one saved item and returns the response and the raw body.
func getSavedItem(
	t *testing.T,
	requesterID uuid.UUID,
	savedItemID uuid.UUID,
) (*http.Response, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodGet,
		"/saved-items/"+savedItemID.String(),
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, requesterID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	return resp, decodeObject(t, resp.Body)
}

// dataObject returns the response's data object.
func dataObject(t *testing.T, raw map[string]any) map[string]any {
	t.Helper()

	data, ok := raw["data"].(map[string]any)
	require.True(t, ok, "response has no data object")

	return data
}

// TestSavedItem_CreateContract pins the minimal create representation.
//
// The response is deliberately short, and the point of the key-set assertion is that
// it stays short: a decoder would leave an undeclared field nil either way, so
// checking that domain is nil proves nothing about whether the endpoint sent it.
func TestSavedItem_CreateContract(t *testing.T) {
	ctx := context.Background()
	db := saveditemdbtest.New(testPool)

	require.NoError(t, db.TruncateSavedItemData(ctx))

	userID, err := db.CreateSavedItemUser(
		ctx,
		saveditemdbtest.CreateSavedItemUserParams{
			Email:       "saved-item-create-contract@example.com",
			DisplayName: "Saved Item Create Contract User",
		},
	)
	require.NoError(t, err)

	unsortedID := createUnsortedCollection(t, ctx, db, userID)

	_, raw := postSavedItem(t, userID, "https://example.com/articles/contract")

	require.Equal(t, "saved item created successfully", raw["message"])

	data := dataObject(t, raw)

	// Only the item, and no collection object: the collection's identity is not what
	// a caller asks about when confirming a save.
	require.ElementsMatch(t, []string{"saved_item"}, rawKeys(t, data))

	savedItem, ok := data["saved_item"].(map[string]any)
	require.True(t, ok, "response has no saved_item object")

	// Exactly the four agreed fields. Nothing about the item's metadata is reported,
	// because at save time none of it has been read.
	require.ElementsMatch(
		t,
		[]string{"id", "url", "collection_id", "enrichment_status"},
		rawKeys(t, savedItem),
	)

	require.Equal(t, "https://example.com/articles/contract", savedItem["url"])
	require.NotEmpty(t, savedItem["id"])

	// collection_id is the user's own Unsorted collection, read back from the stored
	// row rather than restated by the mapper.
	require.Equal(t, unsortedID.String(), savedItem["collection_id"])

	// enrichment_status is the stored value. On a fresh save that is always the
	// column's default, and reporting it costs no additional query because it comes
	// back from the same INSERT.
	require.Equal(t, "pending", savedItem["enrichment_status"])

	// The values above are the row's own, so the response cannot be describing
	// something the database did not accept.
	storedID := mustParseUUID(t, savedItem["id"].(string))

	state, err := db.GetSavedItemState(ctx, storedID)
	require.NoError(t, err)

	require.Equal(t, unsortedID, state.CollectionID)
	require.Equal(t, "example.com", state.Domain.String)
	require.True(t, state.Domain.Valid)
}

// The save itself is unchanged by the response change: the same INSERT runs, and
// background enrichment is still queued exactly once naming the committed row. That
// is asserted by TestSavedItem_Create_QueuesBackgroundEnrichment in
// worker_enqueue_api_test.go, which decodes the create response through the same
// helper every other save test uses. It is deliberately not repeated here: a second
// app against the shared queue namespace would add no signal, and the contract test
// below is about the response.
//
// TestSavedItem_DetailContract pins the complete detail representation and its
// nested collection object.
func TestSavedItem_DetailContract(t *testing.T) {
	ctx := context.Background()
	db := saveditemdbtest.New(testPool)

	require.NoError(t, db.TruncateSavedItemData(ctx))

	userID, err := db.CreateSavedItemUser(
		ctx,
		saveditemdbtest.CreateSavedItemUserParams{
			Email:       "saved-item-detail-contract@example.com",
			DisplayName: "Saved Item Detail Contract User",
		},
	)
	require.NoError(t, err)

	// A user collection, not Unsorted, so the reported collection proves the item's
	// own collection is used rather than the one every save lands in.
	createUnsortedCollection(t, ctx, db, userID)
	wishlistID := createUserCollection(t, ctx, db, userID, "YouTube")

	// An enriched item, so every nullable column has a value and the assertions
	// below distinguish "reported" from "reported as null".
	enrichedID := seedSavedItemInCollection(
		t, ctx, db, userID, wishlistID, "https://example.com/watch?v=abc",
	)

	enrichedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)

	require.NoError(
		t,
		collectiondbtest.New(testPool).SetTestSavedItemEnrichmentStateAt(
			ctx,
			collectiondbtest.SetTestSavedItemEnrichmentStateAtParams{
				EnrichmentStatus: "completed",
				LastEnrichedAt: pgtype.Timestamptz{
					Time:  enrichedAt,
					Valid: true,
				},
				Title:       testText("A video"),
				Description: testText("A description"),
				ImageUrl:    testText("https://example.com/og.png"),
				CreatedAt:   pgtype.Timestamptz{Time: enrichedAt, Valid: true},
				ID:          enrichedID,
			},
		),
	)

	// The data object holds the item and the collection, and nothing else.
	_, raw := getSavedItem(t, userID, enrichedID)

	require.Equal(t, "saved item retrieved successfully", raw["message"])

	data := dataObject(t, raw)

	require.ElementsMatch(
		t,
		[]string{"saved_item", "collection"},
		rawKeys(t, data),
	)

	// The complete saved item: every column a client needs, matching the shape the
	// collection listing reports.
	savedItem, ok := data["saved_item"].(map[string]any)
	require.True(t, ok, "response has no saved_item object")

	require.ElementsMatch(
		t,
		[]string{
			"id", "url", "domain", "platform", "title",
			"description", "image_url", "collection_id",
			"enrichment_status", "last_enriched_at",
			"created_at", "updated_at",
		},
		rawKeys(t, savedItem),
	)
	require.NotContains(t, rawKeys(t, savedItem), "user_id")

	// The collection carries id and name only. type and system_key describe how a
	// collection came to be, which plays no part in showing one item.
	collection, ok := data["collection"].(map[string]any)
	require.True(t, ok, "response has no collection object")

	require.ElementsMatch(t, []string{"id", "name"}, rawKeys(t, collection))
	require.Equal(t, wishlistID.String(), collection["id"])
	require.Equal(t, "YouTube", collection["name"])

	// collection_id stays on the item itself and agrees with the collection object.
	// The redundancy is deliberate: it makes the item self-describing once it is
	// carried out of this response.
	require.Equal(t, collection["id"], savedItem["collection_id"])

	require.Equal(t, enrichedID.String(), savedItem["id"])
	require.Equal(t, "https://example.com/watch?v=abc", savedItem["url"])
	require.Equal(t, "example.com", savedItem["domain"])
	require.Equal(t, "A video", savedItem["title"])
	require.Equal(t, "A description", savedItem["description"])
	require.Equal(t, "https://example.com/og.png", saved_item_image_url(t, savedItem))
	require.Equal(t, "completed", savedItem["enrichment_status"])

	enrichedAtRaw, err := time.Parse(
		time.RFC3339Nano,
		savedItem["last_enriched_at"].(string),
	)
	require.NoError(t, err)
	require.True(t, enrichedAt.Equal(enrichedAtRaw))
}

// saved_item_image_url reads image_url and fails the test if the key is missing, so
// a rename shows up as a clear message rather than a nil comparison.
func saved_item_image_url(
	t *testing.T,
	savedItem map[string]any,
) string {
	t.Helper()

	value, ok := savedItem["image_url"]
	require.True(t, ok, "saved_item has no image_url")

	url, ok := value.(string)
	require.True(t, ok, "saved_item.image_url is %T, not a string", value)

	return url
}

// Unsorted is not special-cased: it arrives through the same collection object as
// any other collection, with the same two keys.
func TestSavedItem_DetailContract_Unsorted(t *testing.T) {
	ctx := context.Background()
	db := saveditemdbtest.New(testPool)

	require.NoError(t, db.TruncateSavedItemData(ctx))

	userID, err := db.CreateSavedItemUser(
		ctx,
		saveditemdbtest.CreateSavedItemUserParams{
			Email:       "saved-item-detail-unsorted@example.com",
			DisplayName: "Saved Item Detail Unsorted User",
		},
	)
	require.NoError(t, err)

	unsortedID := createUnsortedCollection(t, ctx, db, userID)

	itemID := seedSavedItemInCollection(
		t, ctx, db, userID, unsortedID, "https://example.com/inbox/1",
	)

	_, raw := getSavedItem(t, userID, itemID)

	data := dataObject(t, raw)

	collection, ok := data["collection"].(map[string]any)
	require.True(t, ok, "response has no collection object")

	require.ElementsMatch(t, []string{"id", "name"}, rawKeys(t, collection))
	require.Equal(t, unsortedID.String(), collection["id"])
	require.NotEmpty(t, collection["name"])

	savedItem, ok := data["saved_item"].(map[string]any)
	require.True(t, ok)

	require.Equal(t, unsortedID.String(), savedItem["collection_id"])
}

// A pending or failed enrichment is an ordinary state of a saved item. Both are
// returned in full, with every metadata column reported as null rather than omitted.
//
// This is the case the detail response was widened for: before, an item whose
// enrichment had not run could not be distinguished from one whose page exposed
// nothing, because there was no enrichment_status to say which.
func TestSavedItem_DetailContract_EnrichmentStates(t *testing.T) {
	ctx := context.Background()
	db := saveditemdbtest.New(testPool)

	require.NoError(t, db.TruncateSavedItemData(ctx))

	userID, err := db.CreateSavedItemUser(
		ctx,
		saveditemdbtest.CreateSavedItemUserParams{
			Email:       "saved-item-detail-states@example.com",
			DisplayName: "Saved Item Detail States User",
		},
	)
	require.NoError(t, err)

	unsortedID := createUnsortedCollection(t, ctx, db, userID)

	for _, status := range []string{"pending", "failed"} {
		itemID := seedSavedItemInCollection(
			t,
			ctx,
			db,
			userID,
			unsortedID,
			"https://example.com/state/"+status,
		)

		require.NoError(
			t,
			collectiondbtest.New(testPool).SetTestSavedItemEnrichmentStateAt(
				ctx,
				collectiondbtest.SetTestSavedItemEnrichmentStateAtParams{
					EnrichmentStatus: status,
					// Every metadata column deliberately left NULL, which is what
					// enrichment has not written in either state.
					CreatedAt: pgtype.Timestamptz{
						Time:  time.Now().Add(-time.Hour),
						Valid: true,
					},
					ID: itemID,
				},
			),
		)

		_, raw := getSavedItem(t, userID, itemID)

		data := dataObject(t, raw)
		savedItem, ok := data["saved_item"].(map[string]any)
		require.True(t, ok)

		require.Equal(t, status, savedItem["enrichment_status"], status)

		// Null, not absent. The keys are all present with a JSON null value, so a
		// client decoding into pointers gets nil rather than a missing field.
		for _, key := range []string{
			"title", "description", "image_url",
			"platform", "last_enriched_at",
		} {
			value, present := savedItem[key]

			require.True(
				t,
				present,
				"%s: saved_item has no %s key", status, key,
			)
			require.Nil(
				t,
				value,
				"%s: saved_item.%s must be null", status, key,
			)
		}

		// The collection is reported even though nothing about the item's metadata
		// is known yet.
		collection, ok := data["collection"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, unsortedID.String(), collection["id"])
		require.Equal(t, itemID.String(), savedItem["id"])
	}
}

// Ownership isolation is unchanged, and the new collection object does not become a
// side channel: a caller with no claim to an item gets no collection name at all.
func TestSavedItem_DetailContract_ForeignItemLeaksNothing(t *testing.T) {
	ctx := context.Background()
	db := saveditemdbtest.New(testPool)

	require.NoError(t, db.TruncateSavedItemData(ctx))

	ownerID, err := db.CreateSavedItemUser(
		ctx,
		saveditemdbtest.CreateSavedItemUserParams{
			Email:       "saved-item-detail-owner@example.com",
			DisplayName: "Saved Item Detail Owner User",
		},
	)
	require.NoError(t, err)

	otherID, err := db.CreateSavedItemUser(
		ctx,
		saveditemdbtest.CreateSavedItemUserParams{
			Email:       "saved-item-detail-other@example.com",
			DisplayName: "Saved Item Detail Other User",
		},
	)
	require.NoError(t, err)

	createUnsortedCollection(t, ctx, db, ownerID)
	createUnsortedCollection(t, ctx, db, otherID)

	// A named collection, so a leak would be visible as a distinctive string.
	privateID := createUserCollection(t, ctx, db, ownerID, "Private Reading")

	itemID := seedSavedItemInCollection(
		t, ctx, db, ownerID, privateID, "https://example.com/private",
	)

	req := httptest.NewRequest(
		http.MethodGet,
		"/saved-items/"+itemID.String(),
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+newTestAccessToken(t, otherID))

	resp, err := testApp.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.NotContains(
		t,
		string(body),
		"Private Reading",
		"a 404 must not disclose the collection's name",
	)

	raw := decodeObject(t, strings.NewReader(string(body)))

	require.NotContains(t, rawKeys(t, raw), "data")
	require.Contains(t, raw, "error")

	// The item is untouched by the refused read.
	state, err := db.GetSavedItemState(ctx, itemID)
	require.NoError(t, err)
	require.Equal(t, privateID, state.CollectionID)
}

// Reading an item must not change it, and must not start enrichment. A detail read
// that quietly refreshed metadata would make the two indistinguishable to a caller.
func TestSavedItem_DetailContract_ReadDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	db := saveditemdbtest.New(testPool)

	require.NoError(t, db.TruncateSavedItemData(ctx))

	userID, err := db.CreateSavedItemUser(
		ctx,
		saveditemdbtest.CreateSavedItemUserParams{
			Email:       "saved-item-detail-readonly@example.com",
			DisplayName: "Saved Item Detail Read Only User",
		},
	)
	require.NoError(t, err)

	unsortedID := createUnsortedCollection(t, ctx, db, userID)
	wishlistID := createUserCollection(t, ctx, db, userID, "Reading")

	itemID := seedSavedItemInCollection(
		t, ctx, db, userID, wishlistID, "https://example.com/reading/1",
	)

	require.NoError(
		t,
		collectiondbtest.New(testPool).SetTestSavedItemEnrichmentStateAt(
			ctx,
			collectiondbtest.SetTestSavedItemEnrichmentStateAtParams{
				EnrichmentStatus: "failed",
				CreatedAt: pgtype.Timestamptz{
					Time:  time.Now().Add(-time.Hour),
					Valid: true,
				},
				ID: itemID,
			},
		),
	)

	before, err := db.GetSavedItemState(ctx, itemID)
	require.NoError(t, err)

	_, raw := getSavedItem(t, userID, itemID)

	data := dataObject(t, raw)

	savedItem, ok := data["saved_item"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "failed", savedItem["enrichment_status"])

	after, err := db.GetSavedItemState(ctx, itemID)
	require.NoError(t, err)

	// Nothing about the row moved. In particular the item was not filed into
	// Unsorted, which is where a create would have put it.
	require.Equal(t, before.CollectionID, after.CollectionID)
	require.Equal(t, wishlistID, after.CollectionID)
	require.NotEqual(t, unsortedID, after.CollectionID)
	require.Equal(t, before.UpdatedAt.Time, after.UpdatedAt.Time)
}
