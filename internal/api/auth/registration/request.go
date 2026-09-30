package registration

type RegisterRequest struct {
	Email string `json:"email" validate:"required,email,max=255" example:"user@example.com"`
}

type VerifyRegistrationRequest struct {
	PIN string `json:"pin" validate:"required,len=6,numeric" example:"123456"`
}

type FinalizeManualRegistrationRequest struct {
	DisplayName string `json:"display_name" validate:"required,min=2,max=100" example:"John Doe"`
	Password    string `json:"password" validate:"required,min=8" example:"password123"`
}

type FinalizeSocialRegistrationRequest struct {
	DisplayName string `json:"display_name" validate:"required,min=2,max=100" example:"John Doe"`
}