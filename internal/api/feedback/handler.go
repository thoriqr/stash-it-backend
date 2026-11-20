package feedback

import (
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// platformHeader is the client platform the app sends on every request.
//
// It is read here rather than taken from the session, and that is the point: a
// session's platform is recorded at sign-in, so it describes the build that signed in,
// not the one that hit the bug. This header is attached to the request that carries the
// report, so it always names the build that produced it.
//
// It is also why this feature has no dependency on the session module. The header name
// is duplicated from that module's unexported constant on purpose: the two features do
// not import one another, and the client contract is the thing they must agree on rather
// than the constant.
const platformHeader = "X-Platform"

type Handler struct {
	service Service
}

func NewHandler(service Service) *Handler {
	return &Handler{
		service: service,
	}
}

// Create godoc
// @Summary File feedback
// @Description Record one report from the authenticated user. A report is a bug they hit, an idea they have, or anything else worth saying about the product.
// @Description The type must be one of bug, idea, or other, and the message is required, is trimmed, and may be at most 5000 characters. A message that is only whitespace is rejected.
// @Description The optional app_version and app_build name the installed build that filed the report. Both may be omitted, so a client that does not send them still reports.
// @Description The platform is taken from the X-Platform header, which the app already sends on every request, and is not accepted in the body.
// @Description The response carries the report's id and when it was recorded. The report's status is not reported: it belongs to the operator and no user action depends on it.
// @Description Possible error codes:
// @Description - VALIDATION_ERROR
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - USER_RATE_LIMIT_EXCEEDED
// @Description - USER_RATE_LIMIT_UNAVAILABLE
// @Description - INTERNAL_SERVER_ERROR
// @Tags Feedback
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param X-Platform header string false "Client platform reported by the app, for example android or windows" maxLength(32) example(android)
// @Param request body CreateFeedbackRequest true "Feedback request"
// @Success 201 {object} CreateFeedbackAPIResponse "The report's id and the moment it was recorded"
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 429 {object} swagger.APIErrorResponse "Rate limit exceeded for this user"
// @Failure 503 {object} swagger.APIErrorResponse "Rate limiting is unavailable, so the report is refused"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /feedback [post]
func (h *Handler) Create(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	var req CreateFeedbackRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.Create(
		c.Context(),
		userID,
		NewFeedbackParams{
			Type:    req.Type,
			Message: req.Message,
			Context: Context{
				Platform:   optionalString(c.Get(platformHeader)),
				AppVersion: optionalString(req.AppVersion),
				AppBuild:   optionalInt32(req.AppBuild),
			},
		},
	)
	if err != nil {
		return err
	}

	response := mapCreatedFeedbackResponse(result)

	return httpx.Created(
		c,
		"feedback received successfully",
		&response,
	)
}

// optionalString reports a non-blank value and treats everything else as absent.
//
// A header or body field that is present but empty says nothing, and storing "" would
// make "the client said nothing" indistinguishable from "the client said something
// empty".
func optionalString(value string) *string {
	if value == "" {
		return nil
	}

	return &value
}

// optionalInt32 reports a positive value and treats zero as absent, which is what the
// validator's omitempty means for the number it accepted.
func optionalInt32(value int32) *int32 {
	if value < 1 {
		return nil
	}

	return &value
}
