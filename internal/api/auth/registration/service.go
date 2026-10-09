package registration

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/session"
	sessiondb "github.com/thoriqr/stash-it-backend/internal/api/auth/session/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type SocialRegistrationService interface {
	CreateSocialRegistration(
		ctx context.Context,
		params CreateSocialRegistrationInput,
	) (uuid.UUID, error)
}

type RegistrationService interface {
    RegisterManual(
        ctx context.Context,
        email string,
    ) (RegisterManualResult, error)

    CreatePIN(
        ctx context.Context,
        verificationID uuid.UUID,
    ) (CreatePINResult, error)

    GetVerification(
        ctx context.Context,
        verificationID uuid.UUID,
    ) (GetVerificationResult, error)

    ResendVerification(
        ctx context.Context,
        verificationID uuid.UUID,
    ) (ResendVerificationResult, error)

    VerifyRegistration(
        ctx context.Context,
        verificationID uuid.UUID,
        pin string,
    ) (VerifyRegistrationResult, error)

    GetRegistrationContinuation(
        ctx context.Context,
        token string,
    ) (GetRegistrationContinuationResult, error)

    FinalizeManualRegistration(
        ctx context.Context,
        params FinalizeManualRegistrationInput,
    ) (FinalizeManualRegistrationResult, error)

		FinalizeSocialRegistration(
			ctx context.Context,
			params FinalizeSocialRegistrationInput,
			metadata session.SessionMetadata,
		) (FinalizeSocialRegistrationResult, error)
}

type service struct {
    repository             Repository
    sessionService         session.SessionCreator
    accessTokenGenerator   *security.AccessTokenGenerator
    passwordHasher         *security.PasswordHasher
    verificationCodeHasher *security.VerificationCodeHasher
    emailSender            email.Sender
}

func NewService(
    repository Repository,
    sessionService session.SessionCreator,
    accessTokenGenerator *security.AccessTokenGenerator,
    passwordHasher *security.PasswordHasher,
    verificationCodeHasher *security.VerificationCodeHasher,
    emailSender email.Sender,
) *service {
    return &service{
        repository:             repository,
        sessionService:         sessionService,
        accessTokenGenerator:   accessTokenGenerator,
        passwordHasher:         passwordHasher,
        verificationCodeHasher: verificationCodeHasher,
        emailSender:            emailSender,
    }
}

type RegisterManualResult struct {
	VerificationID uuid.UUID
	AlreadyPending bool
}

// NormalizeEmail is the one definition of what an email address is, for every
// feature that reads or writes one.
//
// Registration stores the normalized form, and the lookup side of login has to
// agree with it, because the comparison is exact: PostgreSQL treats = on TEXT as
// case sensitive, so an address stored as alice@example.com is not found by
// Alice@Example.com. Normalizing on write without normalizing on read turns a
// mixed-case login into an invalid-credentials response that the caller cannot
// tell apart from a wrong password.
//
// It is exported because login depends on it. Sharing one definition is what
// keeps the two features from drifting apart, which is the failure this rule
// exists to prevent.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *service) RegisterManual(
	ctx context.Context,
	email string,
) (RegisterManualResult, error) {
	email = NormalizeEmail(email)

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

type CreateSocialRegistrationInput struct {
	Email               string
	Provider            string
	ProviderSubject     string
	EmailSnapshot       pgtype.Text
	DisplayNameSnapshot pgtype.Text
}

func (s *service) CreateSocialRegistration(
	ctx context.Context,
	params CreateSocialRegistrationInput,
) (uuid.UUID, error) {
	email := NormalizeEmail(params.Email)

	now := time.Now()

	registrationExpiresAt := pgtype.Timestamptz{
		Time:  now.Add(registrationExpiresIn),
		Valid: true,
	}

	result, err := s.repository.CreateSocialRegistration(
		ctx,
		CreateSocialRegistrationParams{
			Email:                 email,
			Provider:              params.Provider,
			ProviderSubject:       params.ProviderSubject,
			EmailSnapshot:         params.EmailSnapshot,
			DisplayNameSnapshot:   params.DisplayNameSnapshot,
			RegistrationExpiresAt: registrationExpiresAt,
		},
	)
	if err != nil {
		return uuid.Nil, err
	}

	return result.VerificationRequest.ID, nil
}

type CreatePINResult struct {
	VerificationID uuid.UUID
}

func (s *service) CreatePIN(
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

	// This is the first issuance of a code only by accident of the caller: the
	// endpoint is unauthenticated, and verification_id plus the emailed PIN are
	// the credential, so anyone holding one can call it repeatedly. The cooldown
	// is what stops that from becoming a way to mail an address as fast as the
	// endpoint can be called, and to reset the per-code attempt limit by having a
	// fresh code issued.
	if err := ensureVerificationCooldownElapsed(verification); err != nil {
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

	message := newVerificationPINEmail(verification.Email, code)

	if err := s.emailSender.Send(ctx, message); err != nil {
		return CreatePINResult{}, apperror.Internal(err)
	}

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

func (s *service) GetVerification(
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

func (s *service) ResendVerification(
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

	// The same check CreatePIN makes, called from the same helper: two issuance
	// paths that each spelled this rule out would be two rules to keep in step.
	if err := ensureVerificationCooldownElapsed(verification); err != nil {
		return ResendVerificationResult{}, err
	}

	now := time.Now()

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

	message := newVerificationPINEmail(
		verification.Email,
		code,
	)

	if err := s.emailSender.Send(ctx, message); err != nil {
		return ResendVerificationResult{}, apperror.Internal(err)
	}

	return ResendVerificationResult{
		VerificationID: verificationID,
	}, nil
}

type VerifyRegistrationResult struct {
	RegistrationContinuationToken string
}

func (s *service) VerifyRegistration(
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

func (s *service) GetRegistrationContinuation(
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

func (s *service) FinalizeManualRegistration(
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


type FinalizeSocialRegistrationInput struct {
	ContinuationToken string
	DisplayName       string
}

type FinalizeSocialRegistrationResult struct {
    UserID       uuid.UUID
    Email        string
    DisplayName  string
    Session      sessiondb.Session
    RefreshToken string
    AccessToken  string
}

func (s *service) FinalizeSocialRegistration(
	ctx context.Context,
	params FinalizeSocialRegistrationInput,
	metadata session.SessionMetadata,
) (FinalizeSocialRegistrationResult, error) {
	tokenHash := security.HashToken(params.ContinuationToken)

	continuation, err := s.repository.GetRegistrationContinuation(
		ctx,
		tokenHash,
	)
	if err != nil {
		return FinalizeSocialRegistrationResult{}, err
	}

	if err := validateRegistrationContinuation(continuation); err != nil {
		return FinalizeSocialRegistrationResult{}, err
	}

	if continuation.RegistrationType != string(RegistrationTypeSocial) {
		return FinalizeSocialRegistrationResult{}, apperror.ConflictWith(
			CodeInvalidRegistrationType,
			"registration is not a social registration",
			nil,
		)
	}

	now := time.Now()

	user, err := s.repository.FinalizeSocialRegistration(
		ctx,
		FinalizeSocialRegistrationParams{
			PendingRegistrationID: continuation.PendingRegistrationID,
			ContinuationID:        continuation.ID,
			DisplayName:           params.DisplayName,
			EmailVerifiedAt: pgtype.Timestamptz{
				Time:  now,
				Valid: true,
			},
		},
	)
	if err != nil {
		return FinalizeSocialRegistrationResult{}, err
	}

	sessionResult, err := s.sessionService.CreateSession(
    ctx,
    user.ID,
    metadata,
	)
	if err != nil {
    return FinalizeSocialRegistrationResult{}, err
	}

	accessToken, err := s.accessTokenGenerator.Generate(
    user.ID,
    sessionResult.Session.ID,
    session.AccessTokenLifetime,
)
	if err != nil {
    return FinalizeSocialRegistrationResult{}, apperror.Internal(err)
	}

	return FinalizeSocialRegistrationResult{
    UserID:       user.ID,
    Email:        user.Email,
    DisplayName:  user.DisplayName,
    Session:      sessionResult.Session,
    RefreshToken: sessionResult.RefreshToken,
    AccessToken:  accessToken,
	}, nil
}