package client_release

// CheckRequest is the query for GET /client-releases/latest.
//
// Both fields are strings at the binding boundary because the service owns the
// rules and reports every failure under its own code. Binding build as an integer
// would surface a non-numeric value as a generic BAD_REQUEST instead of
// INVALID_CLIENT_BUILD.
type CheckRequest struct {
	Platform string `query:"platform" example:"android"`
	Build    string `query:"build" example:"4"`
}
