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