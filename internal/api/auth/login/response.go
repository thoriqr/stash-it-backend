package login

type LoginUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type LoginManualResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	User         LoginUser `json:"user"`
}

type LoginGoogleResponse struct {
	Outcome LoginOutcome `json:"outcome"`

	AccessToken  string     `json:"access_token,omitempty"`
	RefreshToken string     `json:"refresh_token,omitempty"`
	User         *LoginUser `json:"user,omitempty"`

	Provider       string `json:"provider,omitempty"`
	ConfirmationID string `json:"confirmation_id,omitempty"`

	VerificationID string `json:"verification_id,omitempty"`
}