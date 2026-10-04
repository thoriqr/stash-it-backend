package saved_item

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// SavedItem is the saved item projection this feature works with, carrying the
// columns the saved items queries select.
//
// It replaces the generated saveditemdb.SavedItem table model, which cannot
// serve this feature: migration 000022 gave saved_items more columns than the
// queries project, so sqlc emits a distinct row type per query rather than
// reusing the model. Owning the projection here keeps the repository interface,
// the service and the mapper on one type without depending on a generated struct
// that sqlc does not consider used.
//
// The field shape matches the generated model exactly, including Url rather than
// URL, so switching between the two is a pure symbol swap with no field churn.
//
// collection_id and the enrichment columns are absent because the queries do not
// project them. The collection feature has its own projection, which does carry
// collection_id; the two are deliberately separate types.
type SavedItem struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Url       string
	Domain    pgtype.Text
	Platform  pgtype.Text
	Title     pgtype.Text
	CreatedAt pgtype.Timestamptz
	UpdatedAt pgtype.Timestamptz
}
