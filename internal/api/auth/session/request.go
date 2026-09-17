package session

type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required,max=512"`
}

type ListSessionsRequest struct {
	Page  int `query:"page" validate:"omitempty,min=1"`
	Limit int `query:"limit" validate:"omitempty,min=1,max=50"`
}