package login

type LoginManualRequest struct {
	Email    string `json:"email" validate:"required,email,max=254" example:"user@example.com"`
	Password string `json:"password" validate:"required,min=8,max=128" example:"password123"`
}

type LoginGoogleRequest struct {
	IDToken string `json:"id_token" validate:"required" example:"eyJhbGciOiJSUzI1NiIsImtpZCI6ImV4YW1wbGUifQ..."`
}

// ConfirmAccountLinkRequest proves that the caller still controls the Google
// identity the pending confirmation was created for.
//
// The token is required, and required to match. The confirmation id on its own
// authorizes nothing: it names a flow, it is carried in a URL path, and a path
// is written to access logs, browser history and Referer headers. Requiring the
// identity that started the flow means a leaked id cannot be completed by anyone
// who does not also hold that credential.
//
// The field is the same id_token LoginGoogleRequest already takes, deliberately:
// the client authenticates with one credential throughout the flow and this is
// the same credential, not a new kind of secret.
type ConfirmAccountLinkRequest struct {
	IDToken string `json:"id_token" validate:"required" example:"eyJhbGciOiJSUzI1NiIsImtpZCI6ImV4YW1wbGUifQ..."`
}