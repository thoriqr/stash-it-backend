package enrichment

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/thoriqr/stash-it-backend/internal/enrichment"
)

// SavedItem is what background enrichment needs from a saved item: which item
// it is, whose it is, and the URL to fetch.
//
// It is a narrower projection than the API enrichment feature's because the two
// have different jobs. The endpoint returns the whole row to a client, while this
// one reads a URL and then writes. The user_id is read so a log line can name the
// owner, and nothing else here is projected because nothing else is used.
//
// Following the rule that a feature owns the projection its own queries produce,
// this is a separate type from internal/api/enrichment.SavedItem rather than a
// shared one. The two packages are deliberately independent applications of the
// same table.
type SavedItem struct {
	ID     uuid.UUID
	UserID uuid.UUID
	Url    string
}

// SavedItemOrganizer schedules the automatic organization that follows a
// successful enrichment.
//
// It is declared here, in the consuming package, for the same reason every other
// narrow dependency in this codebase is: the consumer states the interface it
// uses and mocks are generated from that. The concrete implementation is the
// queue package's producer, and this package never imports the package that
// defines it.
//
// The method takes the saved item's id and its owner because that is what the
// organization task needs: it names one item, and it asserts the item belongs to
// the user it is filing it for. The owner comes from the enrichment write's own
// committed result rather than from the row loaded at the start of the attempt,
// which a completed write has since overtaken.
//
// The interface holds no Asynq and no queue types, which is what keeps the
// scheduling decision here instead of leaking into the thing that does the
// enriching, and what lets the service be tested without a queue.
type SavedItemOrganizer interface {
	// EnqueueSavedItemOrganization schedules automatic organization for one saved
	// item.
	//
	// The caller decides what to do about an error: it treats one as recoverable,
	// because the enrichment that scheduled this has already committed and the
	// saved item is valid product data regardless of whether anything happens to
	// it next.
	EnqueueSavedItemOrganization(
		ctx context.Context,
		savedItemID uuid.UUID,
		userID uuid.UUID,
	) error
}

// MetadataEnricher fetches a URL and extracts metadata from the page at it.
//
// It is declared here, in the consuming package, for the same reason every other
// narrow dependency in this codebase is: the consumer states the interface it
// uses and mocks are generated from that. The production implementation is
// enrichment.Enricher, which is the very same capability the synchronous
// endpoint uses — both are constructed around security.NewGuardedHTTPClient, so
// neither path can reach a URL the other could not.
//
// The interface holds no Asynq and no queue types, which is what keeps the retry
// policy in the task handler instead of leaking into the thing that does the
// enriching.
type MetadataEnricher interface {
	// Enrich returns the metadata found at rawURL, or a classified failure.
	//
	// A returned error is a normal outcome, not an exceptional one. The service
	// records it on the item and reports the classification upward.
	Enrich(
		ctx context.Context,
		rawURL string,
	) (enrichment.Metadata, error)
}

// toPersistenceText converts one extracted value into its stored form.
//
// A nil pointer is SQL NULL. Absence is the point: a field the page did not
// provide must be stored as NULL rather than as an empty string, and the nil the
// extractor returns is the only honest representation of "the page did not say".
func toPersistenceText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}

	return pgtype.Text{
		String: *value,
		Valid:  true,
	}
}
