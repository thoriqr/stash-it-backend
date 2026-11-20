package feedback

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// Service is the reports feature's behavior.
//
// It has one operation because filing is the only thing this API does with feedback.
// Triage lives in internal/operator, which reads the same table directly: an operator
// page has no business with a request type, a validator, or the Auth middleware this
// feature's routes are built around.
type Service interface {
	// Create validates one report, records it and reports what was stored.
	Create(
		ctx context.Context,
		userID uuid.UUID,
		params NewFeedbackParams,
	) (Feedback, error)
}

type service struct {
	repository Repository
}

func NewService(repository Repository) *service {
	return &service{
		repository: repository,
	}
}

// Create validates and records one report.
//
// The message is trimmed and rejected when nothing is left. That rule lives here
// rather than in the schema because a blank message is a malformed request, not a state
// a row can legitimately be in: the database stores what the service accepted, and a
// CHECK duplicating this would turn a service bug into a constraint violation. The same
// reasoning is why saved_items.note carries no constraint.
//
// The context is normalized rather than validated. A platform this deployment does not
// recognize is recorded as sent: refusing the report over the description of the device
// that sent it would lose the report and tell the user nothing actionable.
func (s *service) Create(
	ctx context.Context,
	userID uuid.UUID,
	params NewFeedbackParams,
) (Feedback, error) {
	message := strings.TrimSpace(params.Message)
	if message == "" || utf8.RuneCountInString(message) > MessageMaxLength {
		return Feedback{}, invalidMessage()
	}

	context, err := NormalizeContext(params.Context)
	if err != nil {
		return Feedback{}, err
	}

	return s.repository.CreateFeedback(
		ctx,
		newCreateFeedbackParams(userID, params.Type, message, context),
	)
}

func invalidMessage() error {
	return apperror.Validation(
		[]apperror.ErrorField{{
			Field:   "message",
			Message: "is required",
		}},
		nil,
	)
}
