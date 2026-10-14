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
// @Description Returns the profile of the authenticated user, together with the authentication providers linked to it and whether the account holds a password credential.
// @Description The user carries id, email, display_name, has_password_credential, created_at and identities. Nothing else about the account is reported: no password hash, no provider subject, no session identifier.
// @Description Each identity carries provider, linked_at, email_snapshot and display_name_snapshot. provider_subject is never reported.
// @Description The snapshots are what the provider asserted about the account at the moment the link was made, kept as history. They are not copies of the account's own email and display_name, they are not refreshed, and they may legitimately disagree with both — comparing them is the client's call. Nothing is fetched from the provider to update them.
// @Description Both snapshots preserve the stored nullability. A provider that asserted no value reports JSON null, which is different from a provider that asserted an empty value and reports "". Neither is ever filled in from the account's current email or display name.
// @Description provider is the value stored at link time and is not a value this API maintains a list of, so a client must render a provider it has not seen before rather than treating it as an error. The array is ordered oldest link first and is always present, as an empty array when nothing is linked.
// @Description has_password_credential decides which form the password settings screen should offer. It is read from the database on every request. It is a rendering hint and never a decision: PUT /users/me/password determines the actual case from the same database and applies the rule that follows from it.
// @Description Only the authenticated user's own profile is returned. There is no route that accepts a user id, so one user's profile cannot be read by naming another's.
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
// @Description Renames the authenticated user. display_name is the only field this endpoint accepts; a body carrying any other field changes nothing.
// @Description The name must be between 2 and 100 characters, which are the same bounds registration applies to the same column. It is not trimmed and not case-folded, so a value submitted with surrounding whitespace is stored with it — deliberately matching how registration stores a display name.
// @Description Only the authenticated user's row is written. No other user is affected and no unique rule applies to display names, so this endpoint never answers with a conflict.
// @Description The response is the complete profile, in the same shape GET /users/me returns, and is the row this request wrote rather than a re-read. A client therefore never has to merge two representations of the profile it is showing.
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
// @Description Sets the authenticated user's password. Which of the two things it does is decided by the database, not by the request: an account holding no credential stores a first password, and one that already holds one has its password replaced. has_password_credential from GET /users/me tells a client which form to render, and is never consulted to decide what happens here.
// @Description Send new_password alone when the account holds no credential, and both new_password and current_password when it does. Sending current_password for an account that has no credential is refused with UNEXPECTED_CURRENT_PASSWORD rather than ignored, because it would otherwise report success for a password that was never checked.
// @Description An existing credential is only replaced once the submitted current_password has been verified against it. A wrong password answers 401 CURRENT_PASSWORD_INVALID and costs one Argon2id derivation: the new password is not derived at all when the current one fails.
// @Description Creating a first password revokes nothing. Changing an existing one revokes every other active session of the account and preserves the session making the request. **That revocation does not take effect immediately for a session already holding an access token.** Access tokens in this application are stateless and are never checked against the session row, so a token issued before this request keeps working until its own expiry — up to about 15 minutes. Signing a device out here therefore means its refresh no longer works, not that its existing token stops working at once.
// @Description Two concurrent requests that both create a password produce exactly one success; the other is refused with 409 PASSWORD_CREDENTIAL_ALREADY_EXISTS and its password is not stored over the winner's. Two concurrent requests that both change a password produce exactly one success; the other is refused with 409 PASSWORD_ALREADY_CHANGED rather than silently overwriting a password it never verified.
// @Description If no Argon2id capacity is available the answer is 503 PASSWORD_WORK_UNAVAILABLE with a Retry-After, and nothing at all is written: no credential is stored and no session is revoked.
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
// @Failure 503 {object} swagger.APIErrorResponse "No password capacity is available; nothing was written"
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
