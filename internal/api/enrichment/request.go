package enrichment

import "github.com/thoriqr/stash-it-backend/internal/clientmetadata"

// EnrichSavedItemRequest is the optional body of POST /saved-items/:id/enrich.
//
// The endpoint existed without a body, and it keeps working without one: a host
// the server may fetch ignores everything here. The body is read only for a host
// in CLIENT_ENRICHMENT_HOSTS, where the client has already fetched and parsed the
// page and the server must not.
//
// Metadata is a pointer so "the client sent nothing" is distinguishable from "the
// client sent an empty object". Both are recorded as a failed enrichment for an
// allowlisted host, but the distinction matters for what a future client can
// express.
type EnrichSavedItemRequest struct {
	Metadata *clientmetadata.Metadata `json:"metadata"`
}
