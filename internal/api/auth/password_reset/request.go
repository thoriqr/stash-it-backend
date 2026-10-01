package password_reset

type RequestPasswordResetRequest struct {
	Email string `json:"email" validate:"required,email,max=255" example:"user@example.com"`
}

type VerifyPasswordResetRequest struct {
	PIN string `json:"pin" validate:"required,len=6,numeric" example:"123456"`
}

type FinalizePasswordResetRequest struct {
	Password string `json:"password" validate:"required,min=8" example:"password123"`
}