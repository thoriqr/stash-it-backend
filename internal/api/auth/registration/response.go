package registration

import "github.com/google/uuid"

type RegisterResponse struct {
    VerificationID uuid.UUID `json:"verification_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
}

type CreatePINResponse struct {
    VerificationID string `json:"verification_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
}

type GetVerificationResponse struct {
    VerificationID string `json:"verification_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
    Status         string `json:"status" example:"pending"`
    PINIssued      bool   `json:"pin_issued" example:"true"`
    ResendInSeconds int   `json:"resend_in_seconds" example:"42"`
}

type ResendVerificationResponse struct {
    VerificationID string `json:"verification_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
}

type VerifyRegistrationResponse struct {
    RegistrationContinuationToken string `json:"registration_continuation_token" example:"eyJhbGciOiJIUzI1NiJ9..."`
}

type GetRegistrationContinuationResponse struct {
    Email            string `json:"email" example:"user@example.com"`
    RegistrationType string `json:"registration_type" example:"manual"`
    RequiresPassword bool   `json:"requires_password" example:"true"`
}

type FinalizeManualRegistrationResponse struct {
    Email       string `json:"email" example:"user@example.com"`
    DisplayName string `json:"display_name" example:"John Doe"`
}

type FinalizeSocialRegistrationUser struct {
    ID          string `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
    Email       string `json:"email" example:"user@example.com"`
    DisplayName string `json:"display_name" example:"John Doe"`
}

type FinalizeSocialRegistrationResponse struct {
    AccessToken  string                          `json:"access_token" example:"eyJhbGciOiJIUzI1NiJ9..."`
    RefreshToken string                          `json:"refresh_token" example:"v1.refresh-token-example"`
    User         FinalizeSocialRegistrationUser `json:"user"`
}


type RegisterAPIResponse struct {
	Data    *RegisterResponse `json:"data"`
	Message string            `json:"message" example:"registration requested"`
}

type GetVerificationAPIResponse struct {
	Data    *GetVerificationResponse `json:"data"`
	Message string                   `json:"message" example:"verification retrieved"`
}

type CreatePINAPIResponse struct {
	Data    *CreatePINResponse `json:"data"`
	Message string             `json:"message" example:"verification code created"`
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