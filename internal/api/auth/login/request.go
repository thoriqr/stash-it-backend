package login

type LoginManualRequest struct {
	Email    string `json:"email" validate:"required,email,max=254" example:"user@example.com"`
	Password string `json:"password" validate:"required,min=8,max=128" example:"password123"`
}

type LoginGoogleRequest struct {
	IDToken string `json:"id_token" validate:"required" example:"eyJhbGciOiJSUzI1NiIsImtpZCI6ImV4YW1wbGUifQ..."`
}