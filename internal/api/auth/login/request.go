package login

type LoginManualRequest struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

type LoginGoogleRequest struct {
	IDToken string `json:"id_token" validate:"required"`
}