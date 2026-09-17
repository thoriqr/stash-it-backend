package session

import (
	"time"

	"github.com/google/uuid"
)

type RefreshTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type ListSessionsResponse struct {
	Sessions []SessionResponse `json:"sessions"`
}

type SessionResponse struct {
	ID                uuid.UUID  `json:"id"`
	Platform          string     `json:"platform"`
	InstallationID    *uuid.UUID `json:"installation_id"`
	DeviceName        *string    `json:"device_name"`
	UserAgent         *string    `json:"user_agent"`
	CreatedAt         time.Time  `json:"created_at"`
	LastActivityAt    time.Time  `json:"last_activity_at"`
	AbsoluteExpiresAt time.Time  `json:"absolute_expires_at"`
	IsCurrent         bool       `json:"is_current"`
}