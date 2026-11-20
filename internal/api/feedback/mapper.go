package feedback

// mapCreatedFeedbackResponse builds the minimal answer a report gets.
//
// Only the id and the moment it was recorded are mapped. Status is deliberately not
// among them: it belongs to the operator, and a user has nothing to do with it.
func mapCreatedFeedbackResponse(feedback Feedback) CreatedFeedbackResponse {
	return CreatedFeedbackResponse{
		ID:        feedback.ID,
		CreatedAt: feedback.CreatedAt.Time,
	}
}
