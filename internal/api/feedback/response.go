package feedback

import (
	"time"

	"github.com/google/uuid"
)

// CreatedFeedbackResponse is what filing a report answers.
//
// It is deliberately this small. A report has no detail view a user can open, no state
// they can change and no reply they can read, so the only things worth answering are
// the id and the moment it was recorded. Status is not reported: it is the operator's
// own record, and a user who saw it would read meaning into a value nobody acts on
// their behalf with.
type CreatedFeedbackResponse struct {
	ID        uuid.UUID `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	CreatedAt time.Time `json:"created_at" example:"2026-10-02T10:30:00Z"`
}

type CreateFeedbackAPIResponse struct {
	Data    *CreatedFeedbackResponse `json:"data"`
	Message string                   `json:"message" example:"feedback received successfully"`
}
