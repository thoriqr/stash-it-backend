package password_reset

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/security"
)

type Service struct {
	repository             Repository
	passwordHasher         *security.PasswordHasher
	verificationCodeHasher *security.VerificationCodeHasher
	emailSender            email.Sender
}

type RequestPasswordResetResult struct {
	VerificationID uuid.UUID

	// UnknownAccount reports that no account holds the requested address, and
	// that the returned VerificationID is therefore a placeholder.
	//
	// It exists for tests and for anything that needs to know internally, not
	// for the response. The handler ignores it and returns the same body either
	// way, which is the entire point: a caller who could tell the two apart would
	// learn whether an address is registered here.
	UnknownAccount bool
}

func NewService(
	repository Repository,
	passwordHasher *security.PasswordHasher,
	verificationCodeHasher *security.VerificationCodeHasher,
	emailSender email.Sender,
) *Service {
	return &Service{
		repository:             repository,
		passwordHasher:         passwordHasher,
		verificationCodeHasher: verificationCodeHasher,
		emailSender:            emailSender,
	}
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// dummyVerificationID returns a placeholder verification id for an address that
// no account holds.
//
// It is random and unpersisted, which is what makes it safe. Nothing is written
// for it, so every endpoint that takes a verification id looks it up, finds no
// row, and refuses it through the same path any unknown id takes. A caller that
// treats the id as a credential finds out it was never one.
//
// It is a v7 UUID because the real ids are: a placeholder with a different
// version nibble or a different shape would be distinguishable from a real one
// by inspection alone, which would move the signal out of the status code and
// into the body rather than removing it.
//
// A failure to obtain randomness is a fault in the random source rather than
// something this can answer, so it is reported as one.
func dummyVerificationID() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, apperror.Internal(err)
	}

	return id, nil
}

func (s *Service) RequestPasswordReset(
	ctx context.Context,
	email string,
) (RequestPasswordResetResult, error) {
	email = normalizeEmail(email)

	// Whether an account holds this address is asked rather than assumed. The
	// two branches below are made indistinguishable only by both ending in the
	// same response, so this decision has to happen before anything else does.
	exists, err := s.repository.UserExists(ctx, email)
	if err != nil {
		return RequestPasswordResetResult{}, err
	}

	if !exists {
		// Nothing is created for an address nobody holds: no pending reset, no
		// verification request, no PIN, no message. The placeholder therefore
		// cannot become a delivery channel, and asking about an arbitrary
		// address costs one indexed lookup and nothing else.
		//
		// A real row would not be harmless even with PIN issuance suppressed —
		// the verification id is the credential the rest of the flow is written
		// against, and a row would make it one.
		dummyID, err := dummyVerificationID()
		if err != nil {
			return RequestPasswordResetResult{}, err
		}

		return RequestPasswordResetResult{
			VerificationID: dummyID,
			UnknownAccount: true,
		}, nil
	}

	reset, found, err := s.repository.GetActivePasswordResetByEmail(ctx, email)
	if err != nil {
		return RequestPasswordResetResult{}, err
	}

	if found {
		// Reusing the open reset rather than opening a second one is what keeps a
		// repeat request from resetting the window a user is waiting out. The
		// response carries the same id either way, so nothing here is visible.
		return RequestPasswordResetResult{
			VerificationID: reset.VerificationID,
		}, nil
	}

	now := time.Now()

	verificationRequest, err := s.repository.CreatePasswordReset(
		ctx,
		CreatePasswordResetParams{
			Email:          email,
			ResetExpiresAt: pgtype.Timestamptz{Time: now.Add(passwordResetExpiresIn), Valid: true},
		},
	)
	if err != nil {
		return RequestPasswordResetResult{}, err
	}

	return RequestPasswordResetResult{
		VerificationID: verificationRequest.ID,
	}, nil
}

type GetVerificationResult struct {
	VerificationID        uuid.UUID
	Status                VerificationRequestStatus
	PasswordResetExpiresAt time.Time
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
        VerificationID: verification.ID,
        Status:         VerificationRequestStatus(verification.Status),
        PasswordResetExpiresAt: verification.PasswordResetExpiresAt.Time,
        LastSentAt:      lastSentAt,
				PinIssuedCount: verification.PinIssuedCount,
    }, nil
}

type CreatePINResult struct {
	VerificationID uuid.UUID
}

func (s *Service) CreatePIN(
	ctx context.Context,
	verificationID uuid.UUID,
) (CreatePINResult, error) {
	issuedID, err := s.issueVerificationCode(ctx, verificationID)
	if err != nil {
		return CreatePINResult{}, err
	}

	return CreatePINResult{
		VerificationID: issuedID,
	}, nil
}

// issueVerificationCode is the whole of PIN issuance, and both entry points are
// the same act expressed differently.
//
// CreatePIN is named for the first code and ResendVerification for a
// replacement, but the server does not tell them apart. The endpoint is
// unauthenticated and verification_id plus the emailed PIN are the credential,
// so anyone holding one may call either, repeatedly, for as long as the rules
// below allow. A caller that has never had a code has no cooldown to observe,
// which means /resend issues a first code and /pin issues a replacement on
// exactly the same terms.
//
// Keeping the workflow in one place is therefore not tidiness. Two copies of
// this sequence are two rules to keep in step, and these two were already out of
// step: /pin performed every step below without observing the cooldown /resend
// enforced, which made the choice of route the way around it. The order inside
// the sequence is load-bearing for the same reason — the cooldown is checked
// before the code is generated and before anything is written, so a refusal
// costs no row, no generated code and no message.
func (s *Service) issueVerificationCode(
	ctx context.Context,
	verificationID uuid.UUID,
) (uuid.UUID, error) {
	verification, err := s.repository.GetVerification(
		ctx,
		verificationID,
	)
	if err != nil {
		return uuid.Nil, err
	}

	if err := validateVerificationPending(verification); err != nil {
		return uuid.Nil, err
	}

	if err := ensureVerificationCooldownElapsed(verification); err != nil {
		return uuid.Nil, err
	}

	code, err := s.verificationCodeHasher.Generate()
	if err != nil {
		return uuid.Nil, apperror.Internal(err)
	}

	codeHash := s.verificationCodeHasher.Hash(code)

	// Read the clock once, after the code exists and before the row is written,
	// so the stored expiry is the lifetime the code was actually issued with.
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
		return uuid.Nil, err
	}

	message := newVerificationPINEmail(
		verification.Email,
		code,
	)

	if err := s.emailSender.Send(ctx, message); err != nil {
		return uuid.Nil, apperror.Internal(err)
	}

	return verificationID, nil
}

type ResendVerificationResult struct {
	VerificationID uuid.UUID
}

func (s *Service) ResendVerification(
	ctx context.Context,
	verificationID uuid.UUID,
) (ResendVerificationResult, error) {
	issuedID, err := s.issueVerificationCode(ctx, verificationID)
	if err != nil {
		return ResendVerificationResult{}, err
	}

	return ResendVerificationResult{
		VerificationID: issuedID,
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

	// The context is passed because the shared hasher takes one: it spends the
	// process-wide password-work capacity, and acquiring that capacity is what
	// needs the caller's deadline. Nothing else about this flow changes.
	passwordHash, err := s.passwordHasher.Hash(ctx, params.Password)
	if err != nil {
		if errors.Is(err, security.ErrPasswordWorkCapacityTimeout) {
			// The password was never hashed, so nothing about this reset has been
			// decided: the continuation is untouched, the pending reset is still
			// pending, no credential was written and no session was revoked. The
			// refusal happens before the finalization transaction opens, which is
			// what makes those all true rather than merely likely.
			//
			// It has to reach the caller as its own answer rather than as a fault.
			// Nothing is broken — the server is momentarily full — and nothing was
			// wrong with the password either, so telling the caller theirs is
			// unacceptable would send them off to choose a different one.
			//
			// Only the capacity timeout becomes this. A cancelled context or an
			// expired deadline stays a fault, because the two ask the caller to do
			// different things: this one means come back shortly, while a
			// cancellation means the caller went away and the work should stop.
			// Folding them together would report a request nobody is waiting for
			// as merely busy.
			//
			// The wait comes from the hasher rather than from a constant here,
			// because it is the deployment's configured value and the header it
			// produces has to be the same number the caller was actually kept
			// waiting for.
			return apperror.ServiceUnavailableWithRetryAfter(
				CodePasswordWorkUnavailable,
				"the server is busy, please try again shortly",
				s.passwordHasher.Wait(),
				err,
			)
		}

		// Anything else is a fault: a failure to generate the salt, or a context
		// that ended before the derivation could start.
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
