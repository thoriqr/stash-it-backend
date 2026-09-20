package password_reset

type RequestPasswordResetRequest struct {
	Email string `json:"email" validate:"required,email,max=255"`
}

type VerifyPasswordResetRequest struct {
	PIN string `json:"pin" validate:"required,len=6,numeric"`
}

type FinalizePasswordResetRequest struct {
	Password string `json:"password" validate:"required,min=8"`
}