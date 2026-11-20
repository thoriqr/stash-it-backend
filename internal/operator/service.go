package operator

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Service is what the operator page can do with a report.
type Service interface {
	// List returns one page of reports and the numbers the header shows.
	List(ctx context.Context, params ListParams) (ListResult, error)

	// SetStatus moves one report to another status.
	SetStatus(
		ctx context.Context,
		feedbackID uuid.UUID,
		status string,
	) (StatusChange, error)

	// Delete removes one report, reporting whether it was there.
	Delete(ctx context.Context, feedbackID uuid.UUID) (bool, error)

	// Summary returns the context shown above the list.
	Summary(ctx context.Context) (Summary, error)
}

type service struct {
	repository Repository
}

func NewService(repository Repository) *service {
	return &service{repository: repository}
}

// List returns one page of reports.
//
// The page number is derived from the offset and the limit everywhere it is reported,
// so the number the header shows is the one the rows actually came from. The limit is
// clamped rather than rejected: this is a page, and a browser cannot act on a
// rejection the way an API client could.
func (s *service) List(
	ctx context.Context,
	params ListParams,
) (ListResult, error) {
	if params.Limit <= 0 {
		params.Limit = DefaultLimit
	}

	if params.Limit > MaxLimit {
		params.Limit = MaxLimit
	}

	if params.Offset < 0 {
		params.Offset = 0
	}

	if params.Status != "" && !IsKnownStatus(params.Status) {
		params.Status = ""
	}

	result, err := s.repository.ListFeedback(ctx, params)
	if err != nil {
		return ListResult{}, err
	}

	// Derived here rather than read from the request, so a page reached by setting
	// the offset directly still reports the page it is actually showing.
	result.Page = params.Offset/params.Limit + 1

	if result.Total > 0 {
		result.TotalPages = int(
			(result.Total + int64(params.Limit) - 1) / int64(params.Limit),
		)
	}

	return result, nil
}

// SetStatus moves one report to another status.
//
// The status is checked here as well as by the schema's CHECK, because the form posts
// free text: an unknown value has to come back as a message the page can show rather
// than as a constraint violation.
func (s *service) SetStatus(
	ctx context.Context,
	feedbackID uuid.UUID,
	status string,
) (StatusChange, error) {
	if !IsKnownStatus(status) {
		return StatusChange{}, ErrUnknownStatus
	}

	return s.repository.SetStatus(ctx, feedbackID, status)
}

func (s *service) Delete(
	ctx context.Context,
	feedbackID uuid.UUID,
) (bool, error) {
	return s.repository.Delete(ctx, feedbackID)
}

// Summary returns the deployment context shown above the list.
//
// It is passed through unchanged: there is nothing to decide, and giving a count a policy
// would be inventing one.
func (s *service) Summary(ctx context.Context) (Summary, error) {
	return s.repository.Summary(ctx)
}

// IsKnownStatus reports whether status is one the schema allows.
func IsKnownStatus(status string) bool {
	for _, known := range Statuses {
		if known == status {
			return true
		}
	}

	return false
}

// ErrUnknownStatus reports a status the schema does not allow.
var ErrUnknownStatus = errors.New("unknown feedback status")
