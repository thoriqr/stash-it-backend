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
	passwordHasher         *security.PasswordHasher
	verificationCodeHasher *security.VerificationCodeHasher
}

type RequestPasswordResetResult struct {
	VerificationID uuid.UUID
	AlreadyPending bool
}

func NewService(
	repository Repository,
	passwordHasher *security.PasswordHasher,
	verificationCodeHasher *security.VerificationCodeHasher,
) *Service {
	return &Service{
		repository:             repository,
		passwordHasher:         passwordHasher,
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

type GetVerificationResult struct {
	VerificationID        uuid.UUID
	Status                VerificationRequestStatus
	PasswordResetExpiresAt time.Time
	LastSentAt            *time.Time
}

func (s *Service) GetVerification(
    ctx context.Context,
    verificationID uuid.UUID,
) (GetVerificationResult, error) {
    verification, err := s.repository.GetVerification(
        ctx,
        verificationID,
    )
    if err != nil {
        return GetVerificationResult{}, err
    }

    if err := validateVerificationPending(verification); err != nil {
        return GetVerificationResult{}, err
    }

    var lastSentAt *time.Time

    if verification.LastSentAt.Valid {
        lastSentAt = &verification.LastSentAt.Time
    }

    return GetVerificationResult{
        VerificationID: verification.ID,
        Status:         VerificationRequestStatus(verification.Status),
        PasswordResetExpiresAt: verification.PasswordResetExpiresAt.Time,
        LastSentAt:      lastSentAt,
    }, nil
}

type ResendVerificationResult struct {
	VerificationID uuid.UUID
}

func (s *Service) ResendVerification(
	ctx context.Context,
	verificationID uuid.UUID,
) (ResendVerificationResult, error) {
	verification, err := s.repository.GetVerification(
		ctx,
		verificationID,
	)
	if err != nil {
		return ResendVerificationResult{}, err
	}

	if err := validateVerificationPending(verification); err != nil {
		return ResendVerificationResult{}, err
	}

	now := time.Now()

	// Resend cooldown must have elapsed.
	if verification.LastSentAt.Valid {
		resendAt := verification.LastSentAt.Time.Add(
			verificationResendCooldown,
		)

		if now.Before(resendAt) {
			return ResendVerificationResult{}, apperror.ConflictWith(
				CodeVerificationResendCooldown,
				"verification code was sent too recently",
				nil,
			)
		}
	}

	code, err := s.verificationCodeHasher.Generate()
	if err != nil {
		return ResendVerificationResult{}, apperror.Internal(err)
	}

	codeHash := s.verificationCodeHasher.Hash(code)

	codeExpiresAt := pgtype.Timestamptz{
		Time:  now.Add(verificationCodeExpiresIn),
		Valid: true,
	}

	_, err = s.repository.ResendVerification(
		ctx,
		ResendVerificationParams{
			VerificationID: verificationID,
			CodeHash:       codeHash,
			CodeExpiresAt:  codeExpiresAt,
		},
	)
	if err != nil {
		return ResendVerificationResult{}, err
	}

	// TODO: send verification PIN to user's email.
	fmt.Printf(
		"DEV password reset PIN for resend %s: %s\n",
		verificationID,
		code,
	)

	return ResendVerificationResult{
		VerificationID: verificationID,
	}, nil
}

type VerifyPasswordResetResult struct {
	PasswordResetContinuationToken string
}

func (s *Service) VerifyPasswordReset(
	ctx context.Context,
	verificationID uuid.UUID,
	pin string,
) (VerifyPasswordResetResult, error) {
	verification, err := s.repository.GetVerification(
		ctx,
		verificationID,
	)
	if err != nil {
		return VerifyPasswordResetResult{}, err
	}

	if err := validateVerificationPending(verification); err != nil {
		return VerifyPasswordResetResult{}, err
	}

	code, err := s.repository.GetActiveVerificationCode(
		ctx,
		verificationID,
		VerificationCodeMaxAttempts,
	)
	if err != nil {
		return VerifyPasswordResetResult{}, err
	}

	if !s.verificationCodeHasher.Verify(pin, code.CodeHash) {
		attempts, err := s.repository.IncrementVerificationCodeAttempts(
			ctx,
			code.ID,
			VerificationCodeMaxAttempts,
		)
		if err != nil {
			return VerifyPasswordResetResult{}, err
		}

		if attempts >= VerificationCodeMaxAttempts {
			return VerifyPasswordResetResult{}, apperror.ConflictWith(
				CodeVerificationCodeAttemptsExceeded,
				"verification code attempt limit exceeded",
				nil,
			)
		}

		return VerifyPasswordResetResult{}, apperror.ConflictWith(
			CodeInvalidVerificationCode,
			"verification code is invalid",
			nil,
		)
	}

	token, err := security.GenerateToken()
	if err != nil {
		return VerifyPasswordResetResult{}, apperror.Internal(err)
	}

	tokenHash := security.HashToken(token)

	continuationExpiresAt := pgtype.Timestamptz{
		Time:  time.Now().Add(passwordResetContinuationExpiresIn),
		Valid: true,
	}

	_, err = s.repository.CompleteVerification(
		ctx,
		CompleteVerificationParams{
			VerificationCodeID:   code.ID,
			VerificationRequestID: verificationID,
			PendingPasswordResetID: verification.SubjectID,
			TokenHash:             tokenHash,
			ExpiresAt:             continuationExpiresAt,
		},
	)
	if err != nil {
		return VerifyPasswordResetResult{}, err
	}

	fmt.Printf(
		"DEV password reset continuation token: %s\n",
		token,
	)

	return VerifyPasswordResetResult{
		PasswordResetContinuationToken: token,
	}, nil
}

type GetPasswordResetContinuationResult struct {
	Email                 string
	HasPasswordCredential bool
	ExpiresAt             time.Time
}

func (s *Service) GetPasswordResetContinuation(
	ctx context.Context,
	token string,
) (GetPasswordResetContinuationResult, error) {
	tokenHash := security.HashToken(token)

	continuation, err := s.repository.GetPasswordResetContinuation(
		ctx,
		tokenHash,
	)
	if err != nil {
		return GetPasswordResetContinuationResult{}, err
	}

	if err := validatePasswordResetContinuation(continuation); err != nil {
		return GetPasswordResetContinuationResult{}, err
	}

	return GetPasswordResetContinuationResult{
		Email:                 continuation.Email,
		HasPasswordCredential: continuation.HasPasswordCredential,
		ExpiresAt:             continuation.ExpiresAt.Time,
	}, nil
}

type FinalizePasswordResetInput struct {
	ContinuationToken string
	Password          string
}

func (s *Service) FinalizePasswordReset(
	ctx context.Context,
	params FinalizePasswordResetInput,
) error {
	tokenHash := security.HashToken(params.ContinuationToken)

	continuation, err := s.repository.GetPasswordResetContinuation(
		ctx,
		tokenHash,
	)
	if err != nil {
		return err
	}

	if err := validatePasswordResetContinuation(continuation); err != nil {
		return err
	}

	user, err := s.repository.GetUserByEmail(
		ctx,
		continuation.Email,
	)
	if err != nil {
		return err
	}

	passwordHash, err := s.passwordHasher.Hash(params.Password)
	if err != nil {
		return apperror.Internal(err)
	}

	err = s.repository.UpsertPasswordCredentialAndCompleteReset(
		ctx,
		UpsertPasswordCredentialAndCompleteResetParams{
			UserID:                      user.ID,
			PasswordHash:                passwordHash,
			PasswordResetContinuationID: continuation.ID,
			PendingPasswordResetID:      continuation.PendingPasswordResetID,
		},
	)
	if err != nil {
		return err
	}

	return nil
}