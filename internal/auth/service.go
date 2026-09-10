package auth

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
	registrationRepository RegistrationRepository
	passwordHasher         *security.PasswordHasher
	verificationCodeHasher *security.VerificationCodeHasher
}

func NewService(
	registrationRepository RegistrationRepository,
	passwordHasher *security.PasswordHasher,
	verificationCodeHasher *security.VerificationCodeHasher,
) *Service {
	return &Service{
		registrationRepository: registrationRepository,
		passwordHasher:         passwordHasher,
		verificationCodeHasher: verificationCodeHasher,
	}
}

type RegisterManualResult struct {
    VerificationID uuid.UUID
    AlreadyPending bool
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *Service) RegisterManual(
	ctx context.Context,
	email string,
) (RegisterManualResult, error) {
	email = normalizeEmail(email)

	pendingRegistration, found, err :=
		s.registrationRepository.GetPendingRegistrationByEmail(
			ctx,
			email,
		)
	if err != nil {
		return RegisterManualResult{}, err
	}

	if found {
		return RegisterManualResult{
			VerificationID: pendingRegistration.VerificationID,
			AlreadyPending: true,
		}, nil
	}

	code, err := s.verificationCodeHasher.Generate()
	if err != nil {
		return RegisterManualResult{}, apperror.Internal(err)
	}

	codeHash := s.verificationCodeHasher.Hash(code)

	now := time.Now()

	registrationExpiresAt := pgtype.Timestamptz{
		Time:  now.Add(registrationExpiresIn),
		Valid: true,
	}

	codeExpiresAt := pgtype.Timestamptz{
		Time:  now.Add(verificationCodeExpiresIn),
		Valid: true,
	}

	result, err := s.registrationRepository.CreateManualRegistration(
		ctx,
		CreateManualRegistrationParams{
			Email:                 email,
			RegistrationExpiresAt: registrationExpiresAt,
			CodeHash:              codeHash,
			CodeExpiresAt:         codeExpiresAt,
		},
	)
	if err != nil {
		return RegisterManualResult{}, err
	}

	// TODO: send verification PIN to user's email.
	fmt.Printf("DEV verification PIN for %s: %s\n", email, code)

	return RegisterManualResult{
		VerificationID: result.ID,
		AlreadyPending: false,
	}, nil
}

type GetVerificationResult struct {
	VerificationID        uuid.UUID
	Status                VerificationRequestStatus
	RegistrationExpiresAt time.Time
	LastSentAt             *time.Time
}

func (s *Service) GetVerification(
	ctx context.Context,
	verificationID uuid.UUID,
) (GetVerificationResult, error) {
	verification, err := s.registrationRepository.GetVerification(
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
		VerificationID:       verification.ID,
		Status:               VerificationRequestStatus(verification.Status),
		RegistrationExpiresAt: verification.RegistrationExpiresAt.Time,
		LastSentAt:            lastSentAt,
	}, nil
}

type ResendVerificationResult struct {
	VerificationID uuid.UUID
}

func (s *Service) ResendVerification(
	ctx context.Context,
	verificationID uuid.UUID,
) (ResendVerificationResult, error) {
	verification, err := s.registrationRepository.GetVerification(
		ctx,
		verificationID,
	)
	if err != nil {
		return ResendVerificationResult{}, err
	}

	if err := validateVerificationPending(verification); err != nil {
		return ResendVerificationResult{}, err
	}

	// Resend cooldown must have elapsed.
	if verification.LastSentAt.Valid {
		resendAt := verification.LastSentAt.Time.Add(
			verificationResendCooldown,
		)

		if time.Now().Before(resendAt) {
			return ResendVerificationResult{}, apperror.ConflictWith(
				"VERIFICATION_RESEND_COOLDOWN",
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
		Time:  time.Now().Add(verificationCodeExpiresIn),
		Valid: true,
	}

	_, err = s.registrationRepository.ResendVerification(
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
	// email + code

	fmt.Printf(
		"DEV verification PIN for resend %s: %s\n",
		verificationID,
		code,
	)

	return ResendVerificationResult{
		VerificationID: verificationID,
	}, nil
}

type VerifyRegistrationResult struct {
	RegistrationContinuationToken string
}

func (s *Service) VerifyRegistration(
	ctx context.Context,
	verificationID uuid.UUID,
	pin string,
) (VerifyRegistrationResult, error) {
	verification, err := s.registrationRepository.GetVerification(
		ctx,
		verificationID,
	)
	if err != nil {
		return VerifyRegistrationResult{}, err
	}

	if err := validateVerificationPending(verification); err != nil {
		return VerifyRegistrationResult{}, err
	}

	code, err := s.registrationRepository.GetActiveVerificationCode(
		ctx,
		verificationID,
		verificationCodeMaxAttempts,
	)
	if err != nil {
		return VerifyRegistrationResult{}, err
	}

	if !s.verificationCodeHasher.Verify(pin, code.CodeHash) {
		if err := s.registrationRepository.IncrementVerificationCodeAttempts(
			ctx,
			code.ID,
			verificationCodeMaxAttempts,
		); err != nil {
			return VerifyRegistrationResult{}, err
		}

		return VerifyRegistrationResult{}, apperror.ConflictWith(
			"INVALID_VERIFICATION_CODE",
			"verification code is invalid",
			nil,
		)
	}

	token, err := security.GenerateToken()
	if err != nil {
		return VerifyRegistrationResult{}, apperror.Internal(err)
	}

	tokenHash := security.HashToken(token)

	continuationExpiresAt := pgtype.Timestamptz{
		Time:  time.Now().Add(registrationContinuationExpiresIn),
		Valid: true,
	}

	_, err = s.registrationRepository.CompleteVerification(
		ctx,
		CompleteVerificationParams{
			VerificationCodeID:    code.ID,
			VerificationRequestID: verificationID,
			PendingRegistrationID: verification.SubjectID,
			TokenHash:             tokenHash,
			ExpiresAt:             continuationExpiresAt,
		},
	)
	if err != nil {
		return VerifyRegistrationResult{}, err
	}

	fmt.Printf("DEV registration continuation token: %s\n", token)

	return VerifyRegistrationResult{
		RegistrationContinuationToken: token,
	}, nil
}

type GetRegistrationContinuationResult struct {
	Email            string
	RegistrationType RegistrationType
	RequiresPassword bool
	ExpiresAt        time.Time
}

func (s *Service) GetRegistrationContinuation(
	ctx context.Context,
	token string,
) (GetRegistrationContinuationResult, error) {
	tokenHash := security.HashToken(token)

	continuation, err := s.registrationRepository.GetRegistrationContinuation(
		ctx,
		tokenHash,
	)
	if err != nil {
		return GetRegistrationContinuationResult{}, err
	}

	if err := validateRegistrationContinuation(continuation); err != nil {
		return GetRegistrationContinuationResult{}, err
	}

	return GetRegistrationContinuationResult{
		Email:            continuation.Email,
		RegistrationType: RegistrationType(continuation.RegistrationType),
		RequiresPassword: continuation.RegistrationType == string(RegistrationTypeManual),
		ExpiresAt:        continuation.ExpiresAt.Time,
	}, nil
}

type FinalizeManualRegistrationInput struct {
	ContinuationToken string
	DisplayName       string
	Password          string
}

type FinalizeManualRegistrationResult struct {
	UserID      uuid.UUID
	Email       string
	DisplayName string
}

func (s *Service) FinalizeManualRegistration(
	ctx context.Context,
	params FinalizeManualRegistrationInput,
) (FinalizeManualRegistrationResult, error) {
	tokenHash := security.HashToken(params.ContinuationToken)

	continuation, err := s.registrationRepository.GetRegistrationContinuation(
		ctx,
		tokenHash,
	)
	if err != nil {
		return FinalizeManualRegistrationResult{}, err
	}

	if err := validateRegistrationContinuation(continuation); err != nil {
		return FinalizeManualRegistrationResult{}, err
	}

	if continuation.RegistrationType != string(RegistrationTypeManual) {
		return FinalizeManualRegistrationResult{}, apperror.ConflictWith(
			"INVALID_REGISTRATION_TYPE",
			"registration is not a manual registration",
			nil,
		)
	}

	passwordHash, err := s.passwordHasher.Hash(params.Password)
	if err != nil {
		return FinalizeManualRegistrationResult{}, apperror.Internal(err)
	}

	now := time.Now()

	user, err := s.registrationRepository.FinalizeManualRegistration(
		ctx,
		FinalizeManualRegistrationParams{
			PendingRegistrationID: continuation.PendingRegistrationID,
			ContinuationID:        continuation.ID,
			Email:                 continuation.Email,
			DisplayName:           params.DisplayName,
			EmailVerifiedAt: pgtype.Timestamptz{
				Time:  now,
				Valid: true,
			},
			PasswordHash: passwordHash,
		},
	)
	if err != nil {
		return FinalizeManualRegistrationResult{}, err
	}

	return FinalizeManualRegistrationResult{
		UserID:      user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
	}, nil
}