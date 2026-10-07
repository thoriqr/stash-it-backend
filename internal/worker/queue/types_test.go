package queue_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

// The payload must round trip the id exactly, since that id is the only thing
// the worker has to go on.
func TestEnrichSavedItemPayload_RoundTripsTheID(t *testing.T) {
	savedItemID := uuid.New()

	encoded, err := json.Marshal(queue.NewEnrichSavedItemPayload(savedItemID))
	require.NoError(t, err)

	var decoded queue.EnrichSavedItemPayload

	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, savedItemID, decoded.SavedItemID)
}

// The payload carries an id and nothing else. A URL or a metadata blob here
// would be a second copy of data the row already owns, and could disagree with
// it after an edit.
func TestEnrichSavedItemPayload_CarriesNoOtherData(t *testing.T) {
	encoded, err := json.Marshal(
		queue.NewEnrichSavedItemPayload(uuid.New()),
	)
	require.NoError(t, err)

	var decoded map[string]any

	require.NoError(t, json.Unmarshal(encoded, &decoded))

	require.Len(
		t,
		decoded,
		1,
		"the payload must hold the saved item id and nothing else",
	)
}
