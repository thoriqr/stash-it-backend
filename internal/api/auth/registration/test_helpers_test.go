package registration_test

import (
	"context"
	"testing"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration/mocks"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"go.uber.org/mock/gomock"
)

type testService struct {
    registrationService    registration.RegistrationService
    socialService          registration.SocialRegistrationService
    repository             *mocks.MockRepository
    sessionCreator         *sessionmocks.MockSessionCreator
    verificationCodeHasher *security.VerificationCodeHasher
    emailSender            *FakeEmailSender
}

func newTestService(t *testing.T) testService {
    t.Helper()

    ctrl := gomock.NewController(t)

    repository := mocks.NewMockRepository(ctrl)
    sessionCreator := sessionmocks.NewMockSessionCreator(ctrl)
    emailSender := &FakeEmailSender{}

    verificationCodeHasher := security.NewVerificationCodeHasher(
        []byte("test-secret"),
    )

    accessTokenGenerator := security.NewAccessTokenGenerator(
        []byte("test-secret"),
    )

    service := registration.NewService(
        repository,
        sessionCreator,
        accessTokenGenerator,
        security.NewPasswordHasher(),
        verificationCodeHasher,
        emailSender,
    )

    return testService{
        registrationService:    service,
        socialService:          service,
        repository:             repository,
        sessionCreator:         sessionCreator,
        verificationCodeHasher: verificationCodeHasher,
        emailSender:            emailSender,
    }
}

type FakeEmailSender struct {
    Messages []email.Message
    Err      error
}

func (s *FakeEmailSender) Send(
    ctx context.Context,
    message email.Message,
) error {
    if s.Err != nil {
        return s.Err
    }

    s.Messages = append(s.Messages, message)
    return nil
}