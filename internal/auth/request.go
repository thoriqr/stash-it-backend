package auth

type CreateUserRequest struct {
	Email string `json:"email" validate:"required,email,max=254"`
}