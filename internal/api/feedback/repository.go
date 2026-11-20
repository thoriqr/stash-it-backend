package feedback

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	feedbackdb "github.com/thoriqr/stash-it-backend/internal/api/feedback/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// Repository is the persistence this feature needs.
//
// It has exactly one method, because filing a report is the only thing this API does
// with feedback. Triage — listing, status changes and deletion — reads and writes the
// same table through its own queries in internal/operator, so a status change and a
// report write are not two definitions of the same row: each surface states only what
// it needs.
type Repository interface {
	// CreateFeedback writes one report and returns it as it was stored.
	CreateFeedback(
		ctx context.Context,
		params feedbackdb.CreateFeedbackParams,
	) (Feedback, error)
}

type repository struct {
	queries *feedbackdb.Queries
}

func NewRepository(
	queries *feedbackdb.Queries,
) Repository {
	return &repository{
		queries: queries,
	}
}

func (r *repository) CreateFeedback(
	ctx context.Context,
	params feedbackdb.CreateFeedbackParams,
) (Feedback, error) {
	row, err := r.queries.CreateFeedback(ctx, params)
	if err != nil {
		return Feedback{}, apperror.Internal(err)
	}

	return newFeedback(
		row.ID,
		row.UserID,
		row.Type,
		row.Message,
		row.Status,
		row.Platform,
		row.AppVersion,
		row.AppBuild,
		row.CreatedAt,
		row.UpdatedAt,
	), nil
}

// newFeedback builds the projection from the columns the insert returns.
//
// Every field is taken from the row rather than derived, including status: a create
// response reporting it is reporting what the column default actually wrote.
func newFeedback(
	id uuid.UUID,
	userID uuid.UUID,
	reportType string,
	message string,
	status string,
	platform pgtype.Text,
	appVersion pgtype.Text,
	appBuild pgtype.Int4,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) Feedback {
	return Feedback{
		ID:         id,
		UserID:     userID,
		Type:       reportType,
		Message:    message,
		Status:     status,
		Platform:   platform,
		AppVersion: appVersion,
		AppBuild:   appBuild,
		CreatedAt:  createdAt,
		UpdatedAt:  updatedAt,
	}
}

func optionalText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}

	return pgtype.Text{
		String: *value,
		Valid:  true,
	}
}

func optionalInt4(value *int32) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}

	return pgtype.Int4{
		Int32: *value,
		Valid: true,
	}
}
