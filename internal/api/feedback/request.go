package feedback

// CreateFeedbackRequest is what a client files.
//
// The type and message are required; the context is not. A build that sends no platform
// or version still files a usable report, which is what lets older installs keep
// reporting after this endpoint ships.
//
// Platform is deliberately absent from this type. It arrives on X-Platform, which the
// app already sends on every request, so a body field would be a second way to say the
// same thing that could disagree with the header.
type CreateFeedbackRequest struct {
	Type string `json:"type" validate:"required,oneof=bug idea other" example:"bug"`

	// Message is the report itself. max=5000 is measured in runes by the validator, so a
	// multilingual report is not rejected for being long in bytes. The service applies
	// the same bound again for callers that reach it without this type, and also rejects
	// a value that is only whitespace: required does not cover that, because a string of
	// spaces is not empty as far as the validator is concerned.
	Message string `json:"message" validate:"required,max=5000" example:"Saving an Instagram reel leaves the title empty."`

	// AppVersion and AppBuild come from the installed package, so they name the exact
	// build that produced the report. Both are optional for the same reason the platform
	// is: an older client that does not send them must still be able to report.
	AppVersion string `json:"app_version" validate:"omitempty,max=32" example:"1.3.1"`
	AppBuild   int32  `json:"app_build" validate:"omitempty,min=1" example:"7"`
}
