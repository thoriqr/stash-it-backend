package registration

type RegisterAPIResponse struct {
	Data    *RegisterResponse `json:"data"`
	Message string            `json:"message" example:"verification code sent"`
}

type GetVerificationAPIResponse struct {
	Data    *GetVerificationResponse `json:"data"`
	Message string                   `json:"message" example:"verification retrieved"`
}

type CreatePINAPIResponse struct {
	Data    *CreatePINResponse `json:"data"`
	Message string             `json:"message" example:"verification PIN created"`
}

type ResendVerificationAPIResponse struct {
	Data    *ResendVerificationResponse `json:"data"`
	Message string                      `json:"message" example:"verification code resent"`
}

type VerifyRegistrationAPIResponse struct {
	Data    *VerifyRegistrationResponse `json:"data"`
	Message string                      `json:"message" example:"registration verified"`
}

type GetRegistrationContinuationAPIResponse struct {
	Data    *GetRegistrationContinuationResponse `json:"data"`
	Message string                               `json:"message" example:"registration continuation is valid"`
}

type FinalizeManualRegistrationAPIResponse struct {
	Data    *FinalizeManualRegistrationResponse `json:"data"`
	Message string                              `json:"message" example:"registration completed successfully"`
}

type FinalizeSocialRegistrationAPIResponse struct {
	Data    *FinalizeSocialRegistrationResponse `json:"data"`
	Message string                              `json:"message" example:"registration completed successfully"`
}