package registration_test

import (
	"testing"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration/mocks"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"go.uber.org/mock/gomock"
)

type testService struct {
    registrationService    registration.RegistrationService
    socialService          registration.SocialRegistrationService
    repository             *mocks.MockRepository
    sessionCreator         *sessionmocks.MockSessionCreator
    verificationCodeHasher *security.VerificationCodeHasher
}

func newTestService(t *testing.T) testService {
    t.Helper()

    ctrl := gomock.NewController(t)

    repository := mocks.NewMockRepository(ctrl)
    sessionCreator := sessionmocks.NewMockSessionCreator(ctrl)

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
    )

    return testService{
        registrationService:    service,
        socialService:          service,
        repository:             repository,
        sessionCreator:         sessionCreator,
        verificationCodeHasher: verificationCodeHasher,
    }
}