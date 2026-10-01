package session

import (
	"time"

	"github.com/google/uuid"
)

type RefreshTokenResponse struct {
	AccessToken  string `json:"access_token" example:"eyJhbGciOiJIUzI1NiJ9..."`
	RefreshToken string `json:"refresh_token" example:"v1.refresh-token-example"`
}

type ListSessionsResponse struct {
	Sessions []SessionResponse `json:"sessions"`
}

type SessionResponse struct {
    ID                uuid.UUID  `json:"id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
    Platform          string     `json:"platform" example:"web"`
    InstallationID    *uuid.UUID `json:"installation_id" example:"01a0f359-093b-737a-963a-80f7ca6768ed"`
    DeviceName        *string    `json:"device_name" example:"Chrome on Windows"`
    UserAgent         *string    `json:"user_agent" example:"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"`
    CreatedAt         time.Time  `json:"created_at" example:"2026-10-01T10:30:00Z"`
    LastActivityAt    time.Time  `json:"last_activity_at" example:"2026-10-02T00:15:00Z"`
    AbsoluteExpiresAt time.Time  `json:"absolute_expires_at" example:"2026-10-08T10:30:00Z"`
    IsCurrent         bool       `json:"is_current" example:"true"`
}

type RefreshTokenAPIResponse struct {
	Data    *RefreshTokenResponse `json:"data"`
	Message string                `json:"message" example:"token refreshed successfully"`
}

type LogoutAPIResponse struct {
    Data    *struct{} `json:"data"`
    Message string   `json:"message" example:"logout successful"`
}

type ListSessionsAPIResponse struct {
    Data    *ListSessionsResponse `json:"data"`
    Message string                `json:"message" example:"sessions retrieved successfully"`
    Meta    ListSessionsMeta      `json:"meta"`
}

type ListSessionsMeta struct {
    Pagination ListSessionsPagination `json:"pagination"`
}

type ListSessionsPagination struct {
    Page       int   `json:"page" example:"1"`
    Limit      int   `json:"limit" example:"20"`
    Total      int64 `json:"total" example:"42"`
    TotalPages int   `json:"total_pages" example:"3"`
}

type RevokeSessionAPIResponse struct {
    Data    *struct{} `json:"data"`
    Message string   `json:"message" example:"session revoked successfully"`
}