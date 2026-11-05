package user

import (
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

// errClaimsMissing is what a request reaching these handlers without verified
// claims is reported as.
//
// It is a fault rather than a refusal, and the reason is that it cannot be
// reached through any correctly wired route: middleware.Auth runs first, refuses
// any request without a valid token, and only then puts claims on the request.
// Reaching this state means the limiter or the handler is mounted somewhere
// Auth is not, which is a wiring mistake.
//
// It matters more here than it does elsewhere. Every handler in this feature
// takes its user id from the token and nothing else, so inventing one would merge
// every such request into a single account and hand the caller a choice of which
// profile to read.
var errClaimsMissing = errors.New("authenticated route reached without verified claims")

// authenticatedUser is the identity every handler in this feature acts on.
//
// Both values come from the token middleware.Auth already verified, and neither
// is ever read from a path parameter, a query string or a request body. There is
// no route here that accepts a user id at all, so "read somebody else's profile"
// is not a request this API can express — which is why no handler below has to
// scope anything by a second predicate, and why a caller-supplied id cannot reach
// the database even by accident.
//
// The two-value type assertion is deliberate. Most handlers in this repository use
// the unchecked form and panic on a wiring mistake; user_ratelimit.go uses this
// one, and it costs nothing here because there is no path that produces a
// different value.
func authenticatedUser(c fiber.Ctx) (uuid.UUID, uuid.UUID, error) {
	claims, ok := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)
	if !ok || claims.Subject == "" || claims.SessionID == uuid.Nil {
		return uuid.Nil, uuid.Nil, apperror.Internal(errClaimsMissing)
	}

	// sub is a string in the claim and a UUID everywhere else, so it is parsed
	// here. The verifier has already refused an empty one; an unparseable value
	// would mean a token this process signed, so it is a fault rather than a
	// request error.
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, uuid.Nil, apperror.Internal(err)
	}

	return userID, claims.SessionID, nil
}

type Handler struct {
	service Service
}

func NewHandler(service Service) *Handler {
	return &Handler{
		service: service,
	}
}

// GetUser godoc
// @Summary Get the current user's profile
// @Description Retrieve the authenticated user's profile, including linked authentication providers and whether the account has a password credential.
// @Description identities contains the authentication providers linked to the account. The array is always present and may be empty. Clients should support provider values they do not recognize.
// @Description email_snapshot and display_name_snapshot represent provider information captured when the identity was linked. They are historical values and may differ from the user's current email and display name. Both fields may be null.
// @Description has_password_credential indicates whether the account has a password credential and can be used to determine which password settings options to display. The password update endpoint remains authoritative when applying changes.
// @Description Possible error codes:
// @Description - USER_NOT_FOUND
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags User
// @Produce json
// @Security BearerAuth
// @Success 200 {object} GetUserAPIResponse
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "The account this token names no longer exists"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /users/me [get]
func (h *Handler) GetUser(c fiber.Ctx) error {
	userID, _, err := authenticatedUser(c)
	if err != nil {
		return err
	}

	profile, err := h.service.GetUser(c.Context(), userID)
	if err != nil {
		return err
	}

	return httpx.OK(c, "user retrieved successfully", &UserData{
		User: mapUserResponse(profile, profile.Identities),
	})
}

// UpdateUser godoc
// @Summary Update the current user's display name
// @Description Update the authenticated user's display name. display_name is the only field accepted by this endpoint.
// @Description The display name must contain between 2 and 100 characters. Leading and trailing whitespace are preserved.
// @Description The response contains the updated user profile, including linked authentication providers.
// @Description Possible error codes:
// @Description - VALIDATION_ERROR
// @Description - BAD_REQUEST
// @Description - USER_NOT_FOUND
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags User
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body UpdateUserRequest true "Profile update"
// @Success 200 {object} UpdateUserAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Malformed body, or display_name absent, shorter than 2 or longer than 100 characters"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "The account this token names no longer exists"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /users/me [patch]
func (h *Handler) UpdateUser(c fiber.Ctx) error {
	userID, _, err := authenticatedUser(c)
	if err != nil {
		return err
	}

	var req UpdateUserRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	profile, err := h.service.UpdateUser(c.Context(), userID, req.DisplayName)
	if err != nil {
		return err
	}

	return httpx.OK(c, "user updated successfully", &UserData{
		User: mapUserResponse(profile, profile.Identities),
	})
}

// SetPassword godoc
// @Summary Create or change the current user's password
// @Description Create a password for an account that does not have a password credential, or change the existing password.
// @Description Send new_password alone when creating a password. When changing an existing password, both new_password and current_password are required.
// @Description The operation is determined by the account's current password credential state. has_password_credential from GET /users/me is a UI hint; this endpoint remains authoritative.
// @Description Creating a password does not revoke existing sessions. Changing an existing password revokes all other active sessions while preserving the current session. Existing access tokens on revoked sessions may remain usable until they expire.
// @Description The response message distinguishes between "password created successfully" and "password changed successfully".
// @Description Possible error codes:
// @Description - VALIDATION_ERROR
// @Description - BAD_REQUEST
// @Description - CURRENT_PASSWORD_REQUIRED
// @Description - UNEXPECTED_CURRENT_PASSWORD
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Description - CURRENT_PASSWORD_INVALID
// @Description - PASSWORD_CREDENTIAL_NOT_FOUND
// @Description - PASSWORD_CREDENTIAL_ALREADY_EXISTS
// @Description - PASSWORD_ALREADY_CHANGED
// @Description - USER_RATE_LIMIT_EXCEEDED
// @Description - USER_RATE_LIMIT_UNAVAILABLE
// @Description - PASSWORD_WORK_UNAVAILABLE
// @Description - INTERNAL_SERVER_ERROR
// @Tags User
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body SetPasswordRequest true "Password to set"
// @Success 200 {object} SetPasswordAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Malformed body, new_password absent, shorter than 8 or longer than 128 characters, or current_password sent for an account with no password"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token, or the submitted current_password does not match"
// @Failure 409 {object} swagger.APIErrorResponse "A concurrent request created or changed this password first"
// @Failure 429 {object} swagger.APIErrorResponse "Too many password requests from this account"
// @Failure 503 {object} swagger.APIErrorResponse "No password capacity is available, or the request could not be rate limited; nothing was written"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /users/me/password [put]
func (h *Handler) SetPassword(c fiber.Ctx) error {
	userID, currentSessionID, err := authenticatedUser(c)
	if err != nil {
		return err
	}

	var req SetPasswordRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.SetPassword(c.Context(), SetPasswordParams{
		UserID:           userID,
		CurrentSessionID: currentSessionID,
		CurrentPassword:  req.CurrentPassword,
		NewPassword:      req.NewPassword,
	})
	if err != nil {
		return err
	}

	return httpx.OKMessage(c, setPasswordMessage(result))
}

// setPasswordMessage names which of the two operations ran.
//
// It is not decoration. The two branches differ in something the client cannot
// otherwise observe — one evicted the account's other sessions and one did not —
// and saying "password updated successfully" after creating a password that
// changed nothing would read as though every other device had been signed out.
func setPasswordMessage(result SetPasswordResult) string {
	if result == SetPasswordChanged {
		return "password changed successfully"
	}

	return "password created successfully"
}
