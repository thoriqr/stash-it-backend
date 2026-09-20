package password_reset

import (
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestValidateVerificationPending(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name         string
		verification passwordresetdb.GetVerificationRow
		wantCode     string
		wantStatus   int
	}{
		{"valid", passwordresetdb.GetVerificationRow{Status: string(VerificationRequestPending), PasswordResetStatus: string(PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}}, "", 0},
		{"verification not pending", passwordresetdb.GetVerificationRow{Status: "verified", PasswordResetStatus: string(PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}}, CodeVerificationNotPending, http.StatusConflict},
		{"password reset not pending", passwordresetdb.GetVerificationRow{Status: string(VerificationRequestPending), PasswordResetStatus: string(PendingPasswordResetCompleted), PasswordResetExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}}, CodePasswordResetNotPending, http.StatusConflict},
		{"password reset expiration missing", passwordresetdb.GetVerificationRow{Status: string(VerificationRequestPending), PasswordResetStatus: string(PendingPasswordResetPending)}, "", http.StatusInternalServerError},
		{"password reset expired", passwordresetdb.GetVerificationRow{Status: string(VerificationRequestPending), PasswordResetStatus: string(PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}}, CodePasswordResetExpired, http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVerificationPending(tt.verification)
			if tt.wantCode == "" && tt.wantStatus == 0 {
				if err != nil { t.Fatalf("expected no error, got %v", err) }
				return
			}
			if err == nil { t.Fatal("expected error, got nil") }
			appErr := apperror.FromError(err)
			if appErr.Status != tt.wantStatus { t.Errorf("expected status %d, got %d", tt.wantStatus, appErr.Status) }
			if tt.wantCode != "" && appErr.Code != tt.wantCode { t.Errorf("expected error code %q, got %q", tt.wantCode, appErr.Code) }
		})
	}
}

func TestValidatePasswordResetContinuation(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name         string
		continuation passwordresetdb.GetPasswordResetContinuationRow
		wantCode     string
		wantStatus   int
	}{
		{"valid", passwordresetdb.GetPasswordResetContinuationRow{ExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}, PasswordResetStatus: string(PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}}, "", 0},
		{"continuation already consumed", passwordresetdb.GetPasswordResetContinuationRow{ConsumedAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}, ExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}, PasswordResetStatus: string(PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}}, CodePasswordResetContinuationConsumed, http.StatusConflict},
		{"continuation expiration missing", passwordresetdb.GetPasswordResetContinuationRow{}, "", http.StatusInternalServerError},
		{"continuation expired", passwordresetdb.GetPasswordResetContinuationRow{ExpiresAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}, PasswordResetStatus: string(PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}}, CodePasswordResetContinuationExpired, http.StatusConflict},
		{"password reset not pending", passwordresetdb.GetPasswordResetContinuationRow{ExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}, PasswordResetStatus: string(PendingPasswordResetCompleted), PasswordResetExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}}, CodePasswordResetNotPending, http.StatusConflict},
		{"password reset expiration missing", passwordresetdb.GetPasswordResetContinuationRow{ExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}, PasswordResetStatus: string(PendingPasswordResetPending)}, "", http.StatusInternalServerError},
		{"password reset expired", passwordresetdb.GetPasswordResetContinuationRow{ExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}, PasswordResetStatus: string(PendingPasswordResetPending), PasswordResetExpiresAt: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}}, CodePasswordResetExpired, http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePasswordResetContinuation(tt.continuation)
			if tt.wantCode == "" && tt.wantStatus == 0 {
				if err != nil { t.Fatalf("expected no error, got %v", err) }
				return
			}
			if err == nil { t.Fatal("expected error, got nil") }
			appErr := apperror.FromError(err)
			if appErr.Status != tt.wantStatus { t.Errorf("expected status %d, got %d", tt.wantStatus, appErr.Status) }
			if tt.wantCode != "" && appErr.Code != tt.wantCode { t.Errorf("expected error code %q, got %q", tt.wantCode, appErr.Code) }
		})
	}
}
