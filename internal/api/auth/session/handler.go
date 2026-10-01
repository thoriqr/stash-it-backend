package session

import (
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/httpx"
	"github.com/thoriqr/stash-it-backend/internal/middleware"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type Handler struct {
	service SessionService
}

func NewHandler(service SessionService) *Handler {
	return &Handler{
		service: service,
	}
}

// RefreshToken godoc
// @Summary Refresh access token
// @Description Refresh an access token using a valid refresh token.
// @Description Possible error codes:
// @Description - REFRESH_TOKEN_INVALID
// @Description - SESSION_REVOKED
// @Description - SESSION_EXPIRED
// @Description - INTERNAL_SERVER_ERROR
// @Tags Session
// @Accept json
// @Produce json
// @Param request body RefreshTokenRequest true "Refresh token request"
// @Success 200 {object} RefreshTokenAPIResponse
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error"
// @Failure 401 {object} swagger.APIErrorResponse "Refresh token or session is invalid"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/refresh [post]
func (h *Handler) RefreshToken(c fiber.Ctx) error {
	var req RefreshTokenRequest

	if err := httpx.BindBody(c, &req); err != nil {
		return err
	}

	result, err := h.service.RefreshToken(
		c.Context(),
		req.RefreshToken,
	)
	if err != nil {
		return err
	}

	response := RefreshTokenResponse{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
	}

	return httpx.OK(
		c,
		"token refreshed successfully",
		&response,
	)
}

// Logout godoc
// @Summary Logout
// @Description Log out the current session using a valid access token.
// @Description Possible error codes:
// @Description - INVALID_AUTHORIZATION_HEADER
// @Description - INVALID_ACCESS_TOKEN
// @Description - ACCESS_TOKEN_EXPIRED
// @Tags Session
// @Produce json
// @Security BearerAuth
// @Success 200 {object} LogoutAPIResponse
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/logout [post]
func (h *Handler) Logout(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	if err := h.service.Logout(
		c.Context(),
		claims.SessionID,
	); err != nil {
		return err
	}

	return httpx.OKMessage(
		c,
		"logout successful",
	)
}

// ListSessions godoc
// @Summary List sessions
// @Description Get the current user's sessions.
// @Description Possible error codes:
// @Description - INTERNAL_SERVER_ERROR
// @Tags Session
// @Produce json
// @Security BearerAuth
// @Param page query int false "Page number" minimum(1) default(1) example(1)
// @Param limit query int false "Number of sessions per page" minimum(1) maximum(50) default(20) example(20)
// @Success 200 {object} ListSessionsAPIResponse
// @Failure 400 {object} swagger.ValidationErrorResponse "Validation error"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/sessions [get]
func (h *Handler) ListSessions(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	var req ListSessionsRequest
	if err := httpx.BindQuery(c, &req); err != nil {
		return err
	}

	result, err := h.service.ListSessions(
		c.Context(),
		userID,
		req.Page,
		req.Limit,
	)
	if err != nil {
		return err
	}

	sessions := make([]SessionResponse, 0, len(result.Sessions))

	for _, session := range result.Sessions {
		var installationID *uuid.UUID
		if session.InstallationID.Valid {
			id := uuid.UUID(session.InstallationID.Bytes)
			installationID = &id
		}

		var deviceName *string
		if session.DeviceName.Valid {
			deviceName = &session.DeviceName.String
		}

		var userAgent *string
		if session.UserAgent.Valid {
			userAgent = &session.UserAgent.String
		}

		sessions = append(sessions, SessionResponse{
			ID:                session.ID,
			Platform:          session.Platform,
			InstallationID:    installationID,
			DeviceName:        deviceName,
			UserAgent:         userAgent,
			CreatedAt:         session.CreatedAt.Time,
			LastActivityAt:    session.LastActivityAt.Time,
			AbsoluteExpiresAt: session.AbsoluteExpiresAt.Time,
			IsCurrent:         session.ID == claims.SessionID,
		})
	}

	response := ListSessionsResponse{
		Sessions: sessions,
	}

	meta := httpx.Meta{
		Pagination: &httpx.Pagination{
			Page:       result.Page,
			Limit:      result.Limit,
			Total:      result.Total,
			TotalPages: result.TotalPages,
		},
	}

	return httpx.OKWithMeta(
		c,
		"sessions retrieved successfully",
		&response,
		meta,
	)
}

// RevokeSession godoc
// @Summary Revoke session
// @Description Revoke a session belonging to the current user.
// @Description Possible error codes:
// @Description - RESOURCE_NOT_FOUND
// @Description - INTERNAL_SERVER_ERROR
// @Tags Session
// @Produce json
// @Security BearerAuth
// @Param session_id path string true "Session ID" example(01a0f359-093b-737a-963a-80f7ca6768ed)
// @Success 200 {object} RevokeSessionAPIResponse
// @Failure 400 {object} swagger.APIErrorResponse "Invalid session ID"
// @Failure 401 {object} swagger.APIErrorResponse "Invalid or expired access token"
// @Failure 404 {object} swagger.APIErrorResponse "Session not found"
// @Failure 500 {object} swagger.APIErrorResponse "Internal server error"
// @Router /auth/sessions/{session_id} [delete]
func (h *Handler) RevokeSession(c fiber.Ctx) error {
	claims := c.Locals(middleware.AuthClaimsKey).(security.AccessTokenClaims)

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return apperror.Internal(err)
	}

	sessionID, err := uuid.Parse(c.Params("session_id"))
	if err != nil {
			return apperror.BadRequestWith(
			"",
			"invalid session id",
			err,
		)
	}

	if err := h.service.RevokeSessionForUser(
		c.Context(),
		userID,
		sessionID,
	); err != nil {
		return err
	}

	return httpx.OKMessage(
		c,
		"session revoked successfully",
	)
}