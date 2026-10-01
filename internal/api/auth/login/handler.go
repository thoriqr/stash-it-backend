package login

import (
	"github.com/google/uuid"

	"github.com/gofiber/fiber/v3"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

// LoginManual godoc
// @Summary Login with email and password
// @Description Authenticate a user using email and password and create a new session.
// @Description Possible error codes:
// @Description - VALIDATION_ERROR
// @Description - INVALID_CREDENTIALS
// @Description - INTERNAL_SERVER_ERROR
// @Tags Login
// @Accept json
// @Produce json
// @Param request body LoginManualRequest true "Login request"
// @Param X-Platform header string false "Client platform"
// @Param X-Installation-ID header string false "Client installation ID (UUID)"
// @Param X-Device-Name header string false "Client device name"
// @Param User-Agent header string false "Client user agent"
// @Success 200 {object} LoginManualAPIResponse
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid email or password"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/login [post]
func (h *Handler) LoginManual(c fiber.Ctx) error {
	var req LoginManualRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	metadata, err := session.ExtractMetadata(c)
	if err != nil {
		return err
	}

	result, err := h.service.LoginManual(
		c.Context(),
		req.Email,
		req.Password,
		metadata,
	)
	if err != nil {
		return err
	}

	response := mapLoginManualResponse(result)

	return httpx.OK(
		c,
		"login successful",
		&response,
	)
}


// LoginGoogle godoc
// @Summary Login with Google
// @Description Authenticate a user using a Google ID token.
// @Description The response contains one of three outcomes:
// @Description - authenticated: the user is authenticated and access/refresh tokens are returned.
// @Description - account_link_required: the user must confirm linking the Google account.
// @Description - registration_required: the user must continue the registration flow.
// @Description Possible error codes:
// @Description - VALIDATION_ERROR
// @Description - INVALID_GOOGLE_TOKEN
// @Description - INTERNAL_SERVER_ERROR
// @Tags Login
// @Accept json
// @Produce json
// @Param request body LoginGoogleRequest true "Google login request"
// @Param X-Platform header string false "Client platform"
// @Param X-Installation-ID header string false "Client installation ID (UUID)"
// @Param X-Device-Name header string false "Client device name"
// @Param User-Agent header string false "Client user agent"
// @Success 200 {object} LoginGoogleSuccessAPIResponse
// @Failure 400 {object} swagger.ValidationError "Validation error"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid Google ID token"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/login/google [post]
func (h *Handler) LoginGoogle(c fiber.Ctx) error {
	var req LoginGoogleRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	metadata, err := session.ExtractMetadata(c)
	if err != nil {
		return err
	}

	result, err := h.service.LoginGoogle(
		c.Context(),
		req.IDToken,
		metadata,
	)
	if err != nil {
		return err
	}

	response := mapLoginGoogleResponse(result)

	message := "login successful"

	switch result.Outcome {
	case LoginOutcomeAccountLinkRequired:
		message = "account link confirmation required"
	case LoginOutcomeRegistrationRequired:
		message = "registration required"
	}

	return httpx.OK(
		c,
		message,
		&response,
	)
}

// GetAccountLinkConfirmation godoc
// @Summary Get account link confirmation
// @Description Retrieve an active Google account link confirmation.
// @Description Possible error codes:
// @Description - BAD_REQUEST
// @Description - ACCOUNT_LINK_CONFIRMATION_INVALID
// @Description - INTERNAL_SERVER_ERROR
// @Tags Login
// @Produce json
// @Param confirmation_id path string true "Account link confirmation ID"
// @Success 200 {object} GetAccountLinkConfirmationAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid confirmation ID"
// @Failure 409 {object} swagger.APIErrorResponse "Account link confirmation is invalid"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/login/google/account-link/{confirmation_id} [get]
func (h *Handler) GetAccountLinkConfirmation(c fiber.Ctx) error {
	confirmationID, err := uuid.Parse(c.Params("confirmation_id"))
	if err != nil {
		return apperror.BadRequestWith(
		"",
		"invalid confirmation id",
		err,
		)
	}

	result, err := h.service.GetAccountLinkConfirmation(
		c.Context(),
		confirmationID,
	)
	if err != nil {
		return err
	}

	response := GetAccountLinkConfirmationResponse{
		ID:                  result.ID.String(),
		Provider:            result.Provider,
		EmailSnapshot:       result.EmailSnapshot,
		DisplayNameSnapshot: result.DisplayNameSnapshot,
		UserEmail:           result.UserEmail,
		UserDisplayName:     result.UserDisplayName,
	}

	return httpx.OK(
		c,
		"account link confirmation retrieved successfully",
		&response,
	)
}

// ConfirmAccountLink godoc
// @Summary Confirm Google account link
// @Description Confirm a Google account link and create an authenticated session.
// @Description Possible error codes:
// @Description - BAD_REQUEST
// @Description - ACCOUNT_LINK_CONFIRMATION_INVALID
// @Description - AUTH_IDENTITY_ALREADY_EXISTS
// @Description - INTERNAL_SERVER_ERROR
// @Tags Login
// @Produce json
// @Param confirmation_id path string true "Account link confirmation ID"
// @Param X-Platform header string false "Client platform"
// @Param X-Installation-ID header string false "Client installation ID (UUID)"
// @Param X-Device-Name header string false "Client device name"
// @Param User-Agent header string false "Client user agent"
// @Success 200 {object} LoginGoogleSuccessAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid confirmation ID or session metadata"
// @Failure 409 {object} swagger.APIErrorResponse "Account link confirmation is invalid or auth identity already exists"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/login/google/account-link/{confirmation_id}/confirm [post]
func (h *Handler) ConfirmAccountLink(c fiber.Ctx) error {
	confirmationID, err := uuid.Parse(c.Params("confirmation_id"))
	if err != nil {
		return apperror.BadRequestWith(
		"",
		"invalid confirmation id",
		err,
		)
	}

	metadata, err := session.ExtractMetadata(c)
	if err != nil {
		return err
	}

	result, err := h.service.ConfirmAccountLink(
		c.Context(),
		confirmationID,
		metadata,
	)
	if err != nil {
		return err
	}

	response := mapLoginGoogleResponse(result)

	return httpx.OK(
		c,
		"account link confirmed successfully",
		&response,
	)
}