package login

type LoginUser struct {
	ID          string `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Email       string `json:"email" example:"user@example.com"`
	DisplayName string `json:"display_name" example:"John Doe"`
}

type LoginManualResponse struct {
	AccessToken  string    `json:"access_token" example:"eyJhbGciOiJIUzI1NiJ9.example-access-token"`
	RefreshToken string    `json:"refresh_token" example:"eyJhbGciOiJIUzI1NiJ9.example-refresh-token"`
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

type GetAccountLinkConfirmationResponse struct {
	ID                  string `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Provider            string `json:"provider" example:"google"`
	EmailSnapshot       string `json:"email_snapshot" example:"user@gmail.com"`
	DisplayNameSnapshot string `json:"display_name_snapshot" example:"John Doe"`
	UserEmail           string `json:"user_email" example:"user@example.com"`
	UserDisplayName     string `json:"user_display_name" example:"John Doe"`
}

type LoginManualAPIResponse struct {
	Data    *LoginManualResponse `json:"data"`
	Message string               `json:"message" example:"login successful"`
}

type LoginGoogleSuccessAPIResponse struct {
	Data    LoginGoogleSuccessData `json:"data"`
	Message string                 `json:"message" example:"login successful"`
}

type LoginGoogleSuccessData struct {
	Outcome      string    `json:"outcome" example:"authenticated"`
	AccessToken  string    `json:"access_token" example:"eyJhbGciOiJIUzI1NiJ9..."`
	RefreshToken string    `json:"refresh_token" example:"v1.refresh-token-example"`
	User         LoginUser `json:"user"`
}

type GetAccountLinkConfirmationAPIResponse struct {
	Data    *GetAccountLinkConfirmationResponse `json:"data"`
	Message string                              `json:"message" example:"account link confirmation retrieved successfully"`
}