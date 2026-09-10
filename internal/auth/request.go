package auth

type RegisterRequest struct {
	Email string `json:"email" validate:"required,email,max=255"`
}

type VerifyRegistrationRequest struct {
	PIN string `json:"pin" validate:"required,len=6,numeric"`
}

type FinalizeManualRegistrationRequest struct {
	DisplayName string `json:"display_name" validate:"required,min=2,max=100"`
	Password    string `json:"password" validate:"required,min=8"`
}