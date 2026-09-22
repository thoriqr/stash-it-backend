package registration

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

	registrationID, err :=
		s.repository.GetCompletedRegistrationByEmail(
			ctx,
			email,
		)
	if err != nil {
		return RegisterManualResult{}, err
	}

	if registrationID != uuid.Nil {
		return RegisterManualResult{}, apperror.ConflictWith(
			CodeRegistrationAlreadyCompleted,
			"registration has already been completed",
			nil,
		)
	}

	now := time.Now()

	registrationExpiresAt := pgtype.Timestamptz{
		Time:  now.Add(registrationExpiresIn),
		Valid: true,
	}

	result, err := s.repository.CreateManualRegistration(
		ctx,
		CreateManualRegistrationParams{
			Email:                 email,
			RegistrationExpiresAt: registrationExpiresAt,
		},
	)
	if err != nil {
		return RegisterManualResult{}, err
	}

	return RegisterManualResult{
		VerificationID: result.VerificationRequest.ID,
		AlreadyPending: result.AlreadyPending,
	}, nil
}

type CreatePINResult struct {
	VerificationID uuid.UUID
}

func (s *Service) CreatePIN(
	ctx context.Context,
	verificationID uuid.UUID,
) (CreatePINResult, error) {
	verification, err := s.repository.GetVerification(
		ctx,
		verificationID,
	)
	if err != nil {
		return CreatePINResult{}, err
	}

	if err := validateVerificationPending(verification); err != nil {
		return CreatePINResult{}, err
	}

	code, err := s.verificationCodeHasher.Generate()
	if err != nil {
		return CreatePINResult{}, apperror.Internal(err)
	}

	codeHash := s.verificationCodeHasher.Hash(code)

	now := time.Now()

	codeExpiresAt := pgtype.Timestamptz{
		Time:  now.Add(verificationCodeExpiresIn),
		Valid: true,
	}

	_, err = s.repository.IssueVerificationCode(
		ctx,
		IssueVerificationCodeParams{
			VerificationID: verificationID,
			CodeHash:       codeHash,
			CodeExpiresAt:  codeExpiresAt,
		},
	)
	if err != nil {
		return CreatePINResult{}, err
	}

	// TODO: send verification PIN to user's email.
	fmt.Printf(
		"DEV verification PIN for create %s: %s\n",
		verificationID,
		code,
	)

	return CreatePINResult{
		VerificationID: verificationID,
	}, nil
}

type GetVerificationResult struct {
	VerificationID        uuid.UUID
	Status                VerificationRequestStatus
	RegistrationExpiresAt time.Time
	LastSentAt            *time.Time
	PinIssuedCount int32
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
		VerificationID:        verification.ID,
		Status:                VerificationRequestStatus(verification.Status),
		RegistrationExpiresAt: verification.RegistrationExpiresAt.Time,
		LastSentAt:            lastSentAt,
		PinIssuedCount: verification.PinIssuedCount,
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

	_, err = s.repository.IssueVerificationCode(
		ctx,
		IssueVerificationCodeParams{
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
	verification, err := s.repository.GetVerification(
		ctx,
		verificationID,
	)
	if err != nil {
		return VerifyRegistrationResult{}, err
	}

	if err := validateVerificationPending(verification); err != nil {
		return VerifyRegistrationResult{}, err
	}

	code, err := s.repository.GetActiveVerificationCode(
		ctx,
		verificationID,
		VerificationCodeMaxAttempts,
	)
	if err != nil {
		return VerifyRegistrationResult{}, err
	}

	if !s.verificationCodeHasher.Verify(pin, code.CodeHash) {
		attempts, err := s.repository.IncrementVerificationCodeAttempts(
			ctx,
			code.ID,
			VerificationCodeMaxAttempts,
		)
		if err != nil {
			return VerifyRegistrationResult{}, err
		}

		if attempts >= VerificationCodeMaxAttempts {
			return VerifyRegistrationResult{}, apperror.ConflictWith(
				CodeVerificationCodeAttemptsExceeded,
				"verification code attempt limit exceeded",
				nil,
			)
		}

		return VerifyRegistrationResult{}, apperror.ConflictWith(
			CodeInvalidVerificationCode,
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

	_, err = s.repository.CompleteVerification(
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

	continuation, err := s.repository.GetRegistrationContinuation(
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

	continuation, err := s.repository.GetRegistrationContinuation(
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
			CodeInvalidRegistrationType,
			"registration is not a manual registration",
			nil,
		)
	}

	passwordHash, err := s.passwordHasher.Hash(params.Password)
	if err != nil {
		return FinalizeManualRegistrationResult{}, apperror.Internal(err)
	}

	now := time.Now()

	user, err := s.repository.FinalizeManualRegistration(
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
