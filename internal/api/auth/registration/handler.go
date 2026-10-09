package registration

import (
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
)

type Handler struct {
	service RegistrationService
}

func NewHandler(service RegistrationService) *Handler {
	return &Handler{
		service: service,
	}
}

// RegisterManual godoc
// @Summary Register with email
// @Description Start a manual registration flow using an email address.
// @Description Possible error codes:
// @Description - VALIDATION_ERROR
// @Description - REGISTRATION_ALREADY_COMPLETED
// @Description - REGISTRATION_ALREADY_EXISTS
// @Description - INTERNAL_SERVER_ERROR
// @Tags Registration
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "Registration request"
// @Success 201 {object} RegisterAPIResponse
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error"
// @Failure 409 {object} swagger.APIErrorResponse "Registration conflict"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/register/manual [post]
func (h *Handler) RegisterManual(c fiber.Ctx) error {
	var req RegisterRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.RegisterManual(
		c.Context(),
		req.Email,
	)
	if err != nil {
		return err
	}

	response := RegisterResponse{
		VerificationID: result.VerificationID,
	}

	return httpx.Created(
		c,
		"registration requested",
		&response,
	)
}

// GetVerification godoc
// @Summary Get registration verification
// @Description Get the current status of a registration verification request.
// @Description Possible error codes:
// @Description - BAD_REQUEST
// @Description - RESOURCE_NOT_FOUND
// @Description - CONFLICT
// @Description - INTERNAL_SERVER_ERROR
// @Tags Registration
// @Produce json
// @Param verification_id path string true "Verification ID"
// @Success 200 {object} GetVerificationResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid verification ID"
// @Failure 404 {object} swagger.APIErrorResponse "Verification not found"
// @Failure 409 {object} swagger.APIErrorResponse "Verification is no longer pending"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/register/verification/{verification_id} [get]
func (h *Handler) GetVerification(c fiber.Ctx) error {
	verificationID, err := uuid.Parse(c.Params("verification_id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid verification id",
			err,
		)
	}

	result, err := h.service.GetVerification(
		c.Context(),
		verificationID,
	)
	if err != nil {
		return err
	}

	response := mapGetVerificationResponse(result)

	return httpx.OK(
		c,
		"verification retrieved",
		&response,
	)
}

// CreatePIN godoc
// @Summary Create verification PIN
// @Description Generate and send a verification PIN for a pending registration.
// @Description Possible error codes:
// @Description - VERIFICATION_NOT_PENDING
// @Description - REGISTRATION_NOT_PENDING
// @Description - REGISTRATION_EXPIRED
// @Description - VERIFICATION_RESEND_COOLDOWN
// @Description - IP_RATE_LIMIT_EXCEEDED
// @Description - IP_RATE_LIMIT_UNAVAILABLE
// @Description - CLIENT_IP_UNAVAILABLE
// @Description - PIN_RATE_LIMIT_EXCEEDED
// @Description - PIN_RATE_LIMIT_UNAVAILABLE
// @Description - RESOURCE_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Registration
// @Produce json
// @Param verification_id path string true "Verification ID"
// @Success 201 {object} CreatePINAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid verification ID"
// @Failure 404 {object} swagger.APIErrorResponse "Verification not found"
// @Failure 409 {object} swagger.APIErrorResponse "Verification or registration conflict"
// @Failure 429 {object} swagger.APIErrorResponse "Too many requests from this client address or for this email address; Retry-After reports when to try again"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Failure 503 {object} swagger.APIErrorResponse "Rate limiting or client address resolution is unavailable, so no code was issued"
// @Router /auth/register/verification/{verification_id}/pin [post]
func (h *Handler) CreatePIN(c fiber.Ctx) error {
	verificationID, err := uuid.Parse(c.Params("verification_id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid verification id",
			err,
		)
	}

	result, err := h.service.CreatePIN(
		c.Context(),
		verificationID,
	)
	if err != nil {
		return err
	}

	response := CreatePINResponse{
		VerificationID: result.VerificationID.String(),
	}

	return httpx.Created(
		c,
		"verification code created",
		&response,
	)
}

// ResendVerification godoc
// @Summary Resend verification PIN
// @Description Generate and send a new verification PIN for a pending registration.
// @Description Possible error codes:
// @Description - VERIFICATION_NOT_PENDING
// @Description - REGISTRATION_NOT_PENDING
// @Description - REGISTRATION_EXPIRED
// @Description - VERIFICATION_RESEND_COOLDOWN
// @Description - IP_RATE_LIMIT_EXCEEDED
// @Description - IP_RATE_LIMIT_UNAVAILABLE
// @Description - CLIENT_IP_UNAVAILABLE
// @Description - PIN_RATE_LIMIT_EXCEEDED
// @Description - PIN_RATE_LIMIT_UNAVAILABLE
// @Description - RESOURCE_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Registration
// @Produce json
// @Param verification_id path string true "Verification ID"
// @Success 200 {object} ResendVerificationAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid verification ID"
// @Failure 404 {object} swagger.APIErrorResponse "Verification not found"
// @Failure 409 {object} swagger.APIErrorResponse "Verification or registration conflict"
// @Failure 429 {object} swagger.APIErrorResponse "Too many requests from this client address or for this email address; Retry-After reports when to try again"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Failure 503 {object} swagger.APIErrorResponse "Rate limiting or client address resolution is unavailable, so no code was issued"
// @Router /auth/register/verification/{verification_id}/resend [post]
func (h *Handler) ResendVerification(c fiber.Ctx) error {
	verificationID, err := uuid.Parse(c.Params("verification_id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid verification id",
			err,
		)
	}

	result, err := h.service.ResendVerification(
		c.Context(),
		verificationID,
	)
	if err != nil {
		return err
	}

	response := ResendVerificationResponse{
		VerificationID: result.VerificationID.String(),
	}

	return httpx.OK(
		c,
		"verification code resent",
		&response,
	)
}

// VerifyRegistration godoc
// @Summary Verify registration
// @Description Verify a registration using the verification PIN.
// @Description Possible error codes:
// @Description - VERIFICATION_NOT_PENDING
// @Description - REGISTRATION_NOT_PENDING
// @Description - REGISTRATION_EXPIRED
// @Description - VERIFICATION_CODE_ATTEMPTS_EXCEEDED
// @Description - INVALID_VERIFICATION_CODE
// @Description - RESOURCE_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Registration
// @Accept json
// @Produce json
// @Param verification_id path string true "Verification ID"
// @Param request body VerifyRegistrationRequest true "Verification request"
// @Success 200 {object} VerifyRegistrationAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid request"
// @Failure 404 {object} swagger.APIErrorResponse "Verification not found"
// @Failure 409 {object} swagger.APIErrorResponse "Verification conflict"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/register/verification/{verification_id}/verify [post]
func (h *Handler) VerifyRegistration(c fiber.Ctx) error {
	verificationID, err := uuid.Parse(c.Params("verification_id"))
	if err != nil {
		return apperror.BadRequestWith(
			"",
			"invalid verification id",
			err,
		)
	}

	var request VerifyRegistrationRequest

	if err := httpx.BindBody(c, &request); err != nil {
		return err
	}

	result, err := h.service.VerifyRegistration(
		c.Context(),
		verificationID,
		request.PIN,
	)
	if err != nil {
		return err
	}

	response := VerifyRegistrationResponse{
		RegistrationContinuationToken: result.RegistrationContinuationToken,
	}

	return httpx.OK(
		c,
		"registration verified",
		&response,
	)
}

// GetRegistrationContinuation godoc
// @Summary Get registration continuation
// @Description Validate and retrieve the registration continuation associated with the continuation token.
// @Description Possible error codes:
// @Description - REGISTRATION_CONTINUATION_REQUIRED
// @Description - REGISTRATION_CONTINUATION_CONSUMED
// @Description - REGISTRATION_CONTINUATION_EXPIRED
// @Description - REGISTRATION_NOT_PENDING
// @Description - REGISTRATION_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags Registration
// @Produce json
// @Param X-Registration-Continuation header string true "Registration continuation token"
// @Success 200 {object} GetRegistrationContinuationAPIResponse
// @Failure 401 {object} swagger.APIErrorResponse "Registration continuation token is required"
// @Failure 409 {object} swagger.APIErrorResponse "Registration continuation is invalid or expired"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/register/continuation [get]
func (h *Handler) GetRegistrationContinuation(c fiber.Ctx) error {
	token, err := extractRegistrationContinuationToken(c)
	if err != nil {
		return err
	}

	result, err := h.service.GetRegistrationContinuation(
		c.Context(),
		token,
	)
	if err != nil {
		return err
	}

	response := GetRegistrationContinuationResponse{
		Email:            result.Email,
		RegistrationType: string(result.RegistrationType),
		RequiresPassword: result.RequiresPassword,
	}

	return httpx.OK(
		c,
		"registration continuation is valid",
		&response,
	)
}

// FinalizeManualRegistration godoc
// @Summary Finalize manual registration
// @Description Complete a manual registration using a valid registration continuation token.
// @Description Possible error codes:
// @Description - REGISTRATION_CONTINUATION_CONSUMED
// @Description - REGISTRATION_CONTINUATION_EXPIRED
// @Description - REGISTRATION_NOT_PENDING
// @Description - REGISTRATION_EXPIRED
// @Description - INVALID_REGISTRATION_TYPE
// @Description - USER_ALREADY_EXISTS
// @Description - INTERNAL_SERVER_ERROR
// @Tags Registration
// @Accept json
// @Produce json
// @Param X-Registration-Continuation header string true "Registration continuation token"
// @Param request body FinalizeManualRegistrationRequest true "Finalize registration request"
// @Success 201 {object} FinalizeManualRegistrationAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Validation error"
// @Failure 401 {object} swagger.APIErrorResponse "Registration continuation token is required"
// @Failure 409 {object} swagger.APIErrorResponse "Registration cannot be finalized"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/register/finalize/manual [post]
func (h *Handler) FinalizeManualRegistration(c fiber.Ctx) error {
	var req FinalizeManualRegistrationRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	continuationToken, err := extractRegistrationContinuationToken(c)
	if err != nil {
		return err
	}

	result, err := h.service.FinalizeManualRegistration(
		c.Context(),
		FinalizeManualRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       req.DisplayName,
			Password:          req.Password,
		},
	)
	if err != nil {
		return err
	}

	response := FinalizeManualRegistrationResponse{
		Email:       result.Email,
		DisplayName: result.DisplayName,
	}

	return httpx.Created(
		c,
		"registration completed successfully",
		&response,
	)
}

// FinalizeSocialRegistration godoc
// @Summary Finalize social registration
// @Description Complete a social registration using a valid registration continuation token and create an authenticated session.
// @Description Possible error codes:
// @Description - REGISTRATION_CONTINUATION_CONSUMED
// @Description - REGISTRATION_CONTINUATION_EXPIRED
// @Description - REGISTRATION_NOT_PENDING
// @Description - REGISTRATION_EXPIRED
// @Description - INVALID_REGISTRATION_TYPE
// @Description - USER_ALREADY_EXISTS
// @Description - AUTH_IDENTITY_ALREADY_EXISTS
// @Description - INTERNAL_SERVER_ERROR
// @Tags Registration
// @Accept json
// @Produce json
// @Param X-Registration-Continuation header string true "Registration continuation token"
// @Param X-Platform header string false "Client platform"
// @Param X-Installation-ID header string false "Client installation ID (UUID)"
// @Param X-Device-Name header string false "Client device name"
// @Param User-Agent header string false "Client user agent"
// @Param request body FinalizeSocialRegistrationRequest true "Finalize social registration request"
// @Success 201 {object} FinalizeSocialRegistrationAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid request"
// @Failure 401 {object} swagger.APIErrorResponse "Registration continuation token is required"
// @Failure 409 {object} swagger.APIErrorResponse "Registration cannot be finalized"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/register/finalize/social [post]
func (h *Handler) FinalizeSocialRegistration(c fiber.Ctx) error {
	var req FinalizeSocialRegistrationRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	continuationToken, err := extractRegistrationContinuationToken(c)
	if err != nil {
		return err
	}

	metadata, err := session.ExtractMetadata(c)
	if err != nil {
		return err
	}

	result, err := h.service.FinalizeSocialRegistration(
		c.Context(),
		FinalizeSocialRegistrationInput{
			ContinuationToken: continuationToken,
			DisplayName:       req.DisplayName,
		},
		metadata,
	)
	if err != nil {
		return err
	}

	response := mapFinalizeSocialRegistrationResponse(result)

	return httpx.Created(
		c,
		"registration completed successfully",
		&response,
	)
}
