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

// GetAccountLinkConfirmationResponse describes the account a pending link would
// attach to.
//
// The address is redacted and the field is named for what it carries. It used to
// be `user_email` and it used to hold the address in full, on a route reachable
// with nothing but the confirmation id in the path — which is to say, by anyone
// who had seen that id in a log, a history entry or a Referer header.
//
// email_snapshot is gone from this response and, since migration 000026, from
// the table. It held the Google address the confirmation was created from, and
// the account it matched is by definition the account holding that same address,
// so it was a second copy of the value below. Nothing reads it and nothing needs
// it: the address a link actually writes onto auth_identities is read from the
// account at confirmation time.
//
// display_name_snapshot is a different field and is kept deliberately. It is the
// Google profile name as it read when the confirmation was created, which may
// differ from the profile name later and is not the same thing as the existing
// account's own display_name.
type GetAccountLinkConfirmationResponse struct {
	ID                  string `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
	Provider            string `json:"provider" example:"google"`
	DisplayNameSnapshot string `json:"display_name_snapshot" example:"John Doe"`
	MaskedUserEmail     string `json:"masked_user_email" example:"j***@example.com"`
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