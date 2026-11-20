package feedback

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Feedback is one report as this feature works with it.
//
// It is the projection the create statement returns. The operator's page has its own
// shapes for listing and triage, because those carry the owner's name and email and
// this one does not: a create response says "recorded", and nothing about the report's
// handling belongs in it.
//
// The context fields are pgtype values rather than plain ones because absence is
// meaningful: a client that sent no platform reported a genuine fact about itself, and
// it must stay distinguishable from a client that sent an empty string.
type Feedback struct {
	ID      uuid.UUID
	UserID  uuid.UUID
	Type    string
	Message string

	// Status is the operator's record of what has been done about this report. A create
	// never sets it; the column default does, and this type carries whatever the
	// database wrote.
	Status string

	Platform   pgtype.Text
	AppVersion pgtype.Text
	AppBuild   pgtype.Int4

	CreatedAt pgtype.Timestamptz
	UpdatedAt pgtype.Timestamptz
}

// Context is the client-supplied description of the device that filed a report.
//
// Every field is optional. A report is about the product, and the context exists to
// make it reproducible; it is never a condition of accepting one. That is also why
// nothing here is validated against a list of allowed values: a platform this
// deployment has not heard of is a reason to record it, not to refuse the report.
type Context struct {
	// Platform is the client platform as the app reports it, lowercased and trimmed by
	// NormalizeContext. It comes from the X-Platform header, which the app already sends
	// on every request, so it describes the build that actually filed the report rather
	// than a value remembered at sign-in.
	Platform *string

	// AppVersion and AppBuild come from the installed package, so they name the exact
	// build that produced the report. They are separate for the same reason client
	// releases keep them apart: build is the integer compared against a minimum, version
	// is the string a person reads.
	AppVersion *string
	AppBuild   *int32
}

// NewFeedbackParams is one validated report to write.
type NewFeedbackParams struct {
	UserID  uuid.UUID
	Type    string
	Message string

	// Context is the device description, never a condition of writing. An empty one is
	// ordinary: a build that sends nothing still files a usable report.
	Context Context
}
