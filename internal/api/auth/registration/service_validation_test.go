package registration

import (
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	registrationdb "github.com/thoriqr/stash-it-backend/internal/api/auth/registration/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

func TestValidateVerificationPending(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name         string
		verification registrationdb.GetVerificationRow
		wantCode     string
		wantStatus   int
	}{
		{
			name: "valid",
			verification: registrationdb.GetVerificationRow{
				Status:             string(VerificationRequestPending),
				RegistrationStatus: string(PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(time.Hour),
					Valid: true,
				},
			},
		},
		{
			name: "verification not pending",
			verification: registrationdb.GetVerificationRow{
				Status:             "verified",
				RegistrationStatus: string(PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(time.Hour),
					Valid: true,
				},
			},
			wantCode:   CodeVerificationNotPending,
			wantStatus: http.StatusConflict,
		},
		{
			name: "registration not pending",
			verification: registrationdb.GetVerificationRow{
				Status:             string(VerificationRequestPending),
				RegistrationStatus: string(PendingRegistrationCompleted),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(time.Hour),
					Valid: true,
				},
			},
			wantCode:   CodeRegistrationNotPending,
			wantStatus: http.StatusConflict,
		},
		{
			name: "registration expiration missing",
			verification: registrationdb.GetVerificationRow{
				Status:             string(VerificationRequestPending),
				RegistrationStatus: string(PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Valid: false,
				},
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "registration expired",
			verification: registrationdb.GetVerificationRow{
				Status:             string(VerificationRequestPending),
				RegistrationStatus: string(PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(-time.Hour),
					Valid: true,
				},
			},
			wantCode:   CodeRegistrationExpired,
			wantStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVerificationPending(tt.verification)

			if tt.wantCode == "" && tt.wantStatus == 0 {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected error, got nil")
			}

			appErr := apperror.FromError(err)

			if appErr.Status != tt.wantStatus {
				t.Errorf(
					"expected status %d, got %d",
					tt.wantStatus,
					appErr.Status,
				)
			}

			if tt.wantCode != "" && appErr.Code != tt.wantCode {
				t.Errorf(
					"expected error code %q, got %q",
					tt.wantCode,
					appErr.Code,
				)
			}
		})
	}
}

func TestValidateRegistrationContinuation(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name         string
		continuation registrationdb.GetRegistrationContinuationRow
		wantCode     string
		wantStatus   int
	}{
		{
			name: "valid",
			continuation: registrationdb.GetRegistrationContinuationRow{
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(time.Minute),
					Valid: true,
				},
				RegistrationStatus: string(PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
		},
		{
			name: "continuation already consumed",
			continuation: registrationdb.GetRegistrationContinuationRow{
				ConsumedAt: pgtype.Timestamptz{
					Time:  now.Add(-time.Minute),
					Valid: true,
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(time.Minute),
					Valid: true,
				},
				RegistrationStatus: string(PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			wantCode:   CodeRegistrationContinuationConsumed,
			wantStatus: http.StatusConflict,
		},
		{
			name: "continuation expiration missing",
			continuation: registrationdb.GetRegistrationContinuationRow{
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
				ExpiresAt: pgtype.Timestamptz{
					Valid: false,
				},
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "continuation expired",
			continuation: registrationdb.GetRegistrationContinuationRow{
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(-time.Minute),
					Valid: true,
				},
				RegistrationStatus: string(PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			wantCode:   CodeRegistrationContinuationExpired,
			wantStatus: http.StatusConflict,
		},
		{
			name: "registration not pending",
			continuation: registrationdb.GetRegistrationContinuationRow{
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(time.Minute),
					Valid: true,
				},
				RegistrationStatus: string(PendingRegistrationCompleted),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(7 * 24 * time.Hour),
					Valid: true,
				},
			},
			wantCode:   CodeRegistrationNotPending,
			wantStatus: http.StatusConflict,
		},
		{
			name: "registration expiration missing",
			continuation: registrationdb.GetRegistrationContinuationRow{
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(time.Minute),
					Valid: true,
				},
				RegistrationStatus: string(PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Valid: false,
				},
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "registration expired",
			continuation: registrationdb.GetRegistrationContinuationRow{
				ConsumedAt: pgtype.Timestamptz{
					Valid: false,
				},
				ExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(time.Minute),
					Valid: true,
				},
				RegistrationStatus: string(PendingRegistrationPending),
				RegistrationExpiresAt: pgtype.Timestamptz{
					Time:  now.Add(-time.Hour),
					Valid: true,
				},
			},
			wantCode:   CodeRegistrationExpired,
			wantStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRegistrationContinuation(tt.continuation)

			if tt.wantCode == "" && tt.wantStatus == 0 {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected error, got nil")
			}

			appErr := apperror.FromError(err)

			if appErr.Status != tt.wantStatus {
				t.Errorf(
					"expected status %d, got %d",
					tt.wantStatus,
					appErr.Status,
				)
			}

			if tt.wantCode != "" && appErr.Code != tt.wantCode {
				t.Errorf(
					"expected error code %q, got %q",
					tt.wantCode,
					appErr.Code,
				)
			}
		})
	}
}