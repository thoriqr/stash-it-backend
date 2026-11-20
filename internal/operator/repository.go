package operator

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	operatordb "github.com/thoriqr/stash-it-backend/internal/operator/generated"
)

// Repository is the persistence the operator page needs.
//
// It reads and writes the same feedback table the API writes, through its own
// queries. That is deliberate rather than duplication for its own sake: the page has
// no request types, no validator and no auth middleware, so sharing the feature's
// repository would drag all three into a surface that has no use for them.
//
// None of these methods are scoped by owner. The only caller is this page, which is
// reachable only from the machine it runs on.
type Repository interface {
	ListFeedback(
		ctx context.Context,
		params ListParams,
	) (ListResult, error)

	SetStatus(
		ctx context.Context,
		feedbackID uuid.UUID,
		status string,
	) (StatusChange, error)

	Delete(
		ctx context.Context,
		feedbackID uuid.UUID,
	) (bool, error)

	// Summary reads the context the page shows above the list, which is deliberately not
	// paginated and not filtered: how many accounts exist and what is currently published
	// are facts about the deployment, not about the page of reports being read.
	Summary(ctx context.Context) (Summary, error)
}

type repository struct {
	queries *operatordb.Queries
}

func NewRepository(queries *operatordb.Queries) Repository {
	return &repository{queries: queries}
}

// ListParams is one validated request to read a page of reports.
type ListParams struct {
	// Limit is the page size, already clamped by the service.
	Limit int

	// Offset is how many reports to skip, derived from the page number.
	Offset int

	// Status filters the page to one status when set, and includes every report when
	// empty.
	Status string
}

// ListResult is one page of reports plus the numbers the page header shows.
type ListResult struct {
	// Reports is made with a length, so an empty page is an empty slice rather than
	// nil. The template ranges over it either way, but a nil would be one more state
	// to reason about for no gain.
	Reports []Report

	Page       int
	Limit      int
	Total      int64
	TotalPages int

	// Status is the filter that produced this page, echoed back so the page can keep
	// it selected.
	Status string

	// CountsByStatus is every status's total, so the filter chips can show how much
	// work is behind each one without three more requests.
	CountsByStatus map[string]int64
}

// Report is one row as this page renders it.
type Report struct {
	ID      uuid.UUID
	Type    string
	Message string
	Status  string

	// Platform, AppVersion and AppBuild describe the client that filed the report.
	// All three are nullable: a build that sent none is an ordinary report.
	Platform   pgtype.Text
	AppVersion pgtype.Text
	AppBuild   pgtype.Int4

	CreatedAt pgtype.Timestamptz
	UpdatedAt pgtype.Timestamptz

	// DisplayName and Email belong to the account that filed the report. Both are NOT
	// NULL on users, so neither is optional here.
	DisplayName string
	Email       string
}

// StatusChange reports what a status update stored.
type StatusChange struct {
	ID        uuid.UUID
	Status    string
	UpdatedAt pgtype.Timestamptz
}

// Summary is the context the page shows above the list.
//
// It is a separate read from the listing on purpose. The page is paginated and filtered;
// these three numbers are not, because they answer questions about the deployment rather
// than about the reports on screen. Folding them into the listing would have meant either
// recounting them per page or reporting a total that changed as the reader scrolled.
type Summary struct {
	// Users is how many accounts exist.
	Users int64

	// Releases is one entry per platform that has a published channel. A platform nothing
	// has been published for is absent rather than reported as empty, so the page can say
	// "nothing published" instead of showing a version it invented.
	Releases []ReleaseSummary
}

// ReleaseSummary is one platform's published policy.
//
// Latest and MinSupported are reported as two separate facts rather than as a comparison.
// They answer different questions — what a client is being offered, and what it is allowed
// to be — and neither is more important than the other, so neither is summarised in terms of
// the other.
type ReleaseSummary struct {
	Platform string

	LatestVersion string
	LatestBuild   int32
	PublishedAt   pgtype.Timestamptz

	// GitTag names the commit the latest build came from, and DownloadURL identifies the
	// file. They belong beside the latest version because they are how an operator confirms
	// that what is published is what they meant to publish.
	GitTag        string
	DownloadURL   string
	FileSizeBytes int64

	// MinVersion and MinBuild are the oldest build still accepted. They carry nothing else:
	// this is a floor rather than an offer, so there is no artifact to point at.
	MinVersion string
	MinBuild   int32
}

func (r *repository) ListFeedback(
	ctx context.Context,
	params ListParams,
) (ListResult, error) {
	rows, err := r.queries.ListFeedback(
		ctx,
		operatordb.ListFeedbackParams{
			Status:     optionalText(params.Status),
			PageOffset: int32(params.Offset),
			PageLimit:  int32(params.Limit),
		},
	)
	if err != nil {
		return ListResult{}, err
	}

	total, err := r.queries.CountFeedback(ctx, optionalText(params.Status))
	if err != nil {
		return ListResult{}, err
	}

	counts, err := r.queries.CountFeedbackByStatus(ctx)
	if err != nil {
		return ListResult{}, err
	}

	countsByStatus := make(map[string]int64, len(counts))
	for _, row := range counts {
		countsByStatus[row.Status] = row.Count
	}

	return ListResult{
		Reports:        newReports(rows),
		Limit:          params.Limit,
		Total:          total,
		Status:         params.Status,
		CountsByStatus: countsByStatus,
	}, nil
}

// Summary reads the accounts count and every published release channel.
//
// Two statements rather than one, because they are unrelated and a join between them would
// multiply rows for no reason. No transaction: both are reads, and a publish landing
// between them would show a user count from one moment and a release from another, which is
// a fresh observation rather than a promise — the same thing every number on this page is.
func (r *repository) Summary(ctx context.Context) (Summary, error) {
	users, err := r.queries.CountUsers(ctx)
	if err != nil {
		return Summary{}, err
	}

	releases, err := r.queries.ListReleaseSummary(ctx)
	if err != nil {
		return Summary{}, err
	}

	summaries := make([]ReleaseSummary, 0, len(releases))

	for _, row := range releases {
		summaries = append(summaries, ReleaseSummary{
			Platform:      row.Platform,
			LatestVersion: row.LatestVersion,
			LatestBuild:   row.LatestBuild,
			PublishedAt:   row.PublishedAt,
			GitTag:        row.GitTag,
			DownloadURL:   row.DownloadUrl,
			FileSizeBytes: row.FileSizeBytes,
			MinVersion:    row.MinVersion,
			MinBuild:      row.MinBuild,
		})
	}

	return Summary{Users: users, Releases: summaries}, nil
}

func (r *repository) SetStatus(
	ctx context.Context,
	feedbackID uuid.UUID,
	status string,
) (StatusChange, error) {
	row, err := r.queries.SetFeedbackStatus(
		ctx,
		operatordb.SetFeedbackStatusParams{
			ID:     feedbackID,
			Status: status,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StatusChange{}, ErrNotFound
		}

		return StatusChange{}, err
	}

	return StatusChange{
		ID:        row.ID,
		Status:    row.Status,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func (r *repository) Delete(
	ctx context.Context,
	feedbackID uuid.UUID,
) (bool, error) {
	deleted, err := r.queries.DeleteFeedback(ctx, feedbackID)
	if err != nil {
		return false, err
	}

	return deleted > 0, nil
}

// ErrNotFound reports that a report the page acted on is not there.
//
// It is a package error rather than an apperror because nothing here crosses an API
// boundary: the handler turns it into a message on the page.
var ErrNotFound = errors.New("feedback not found")

func newReports(rows []operatordb.ListFeedbackRow) []Report {
	reports := make([]Report, 0, len(rows))

	for _, row := range rows {
		reports = append(reports, Report{
			ID:          row.ID,
			Type:        row.Type,
			Message:     row.Message,
			Status:      row.Status,
			Platform:    row.Platform,
			AppVersion:  row.AppVersion,
			AppBuild:    row.AppBuild,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
			DisplayName: row.DisplayName,
			Email:       row.Email,
		})
	}

	return reports
}

func optionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}

	return pgtype.Text{String: value, Valid: true}
}
