package password_reset

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/security"
)


type Service struct {
	repository             Repository
	verificationCodeHasher *security.VerificationCodeHasher
}

type RequestPasswordResetResult struct {
	VerificationID uuid.UUID
	AlreadyPending bool
}

func NewService(
	repository Repository,
	verificationCodeHasher *security.VerificationCodeHasher,
) *Service {
	return &Service{
		repository:             repository,
		verificationCodeHasher: verificationCodeHasher,
	}
}


func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *Service) RequestPasswordReset(
	ctx context.Context,
	email string,
) (RequestPasswordResetResult, error) {
	email = normalizeEmail(email)

	_, err := s.repository.GetUserByEmail(ctx, email)
	if err != nil {
		return RequestPasswordResetResult{}, err
	}

	reset, found, err := s.repository.GetActivePasswordResetByEmail(ctx, email)
	if err != nil {
		return RequestPasswordResetResult{}, err
	}

	if found {
		return RequestPasswordResetResult{
			VerificationID: reset.VerificationID,
			AlreadyPending: true,
		}, nil
	}

	code, err := s.verificationCodeHasher.Generate()
	if err != nil {
		return RequestPasswordResetResult{}, apperror.Internal(err)
	}

	codeHash := s.verificationCodeHasher.Hash(code)

	now := time.Now()

	verificationRequest, err := s.repository.CreatePasswordReset(
		ctx,
		CreatePasswordResetParams{
			Email:          email,
			ResetExpiresAt: pgtype.Timestamptz{Time: now.Add(passwordResetExpiresIn), Valid: true},
			CodeHash:       codeHash,
			CodeExpiresAt:  pgtype.Timestamptz{Time: now.Add(verificationCodeExpiresIn), Valid: true},
		},
	)
	if err != nil {
		return RequestPasswordResetResult{}, err
	}

	// TODO: Send verification code through email provider.
	// Keep the code available for development until email delivery is implemented.
	fmt.Printf("DEV password reset PIN for %s: %s\n", email, code)

	return RequestPasswordResetResult{
		VerificationID: verificationRequest.ID,
		AlreadyPending: false,
	}, nil
}
