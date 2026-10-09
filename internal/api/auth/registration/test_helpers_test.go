package registration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration/mocks"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
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
	pinRateLimiter         *FakePinRateLimiter
}

func newTestService(t *testing.T) testService {
	t.Helper()

	return newTestServiceWithLimiter(t, NewFakePinRateLimiter())
}

// newTestServiceWithLimiter builds the service over a caller-supplied limiter, so
// a test can model an exhausted budget or a limiter that cannot answer.
func newTestServiceWithLimiter(
	t *testing.T,
	pinRateLimiter *FakePinRateLimiter,
) testService {
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
		pinRateLimiter,
	)

	return testService{
		registrationService:    service,
		socialService:          service,
		repository:             repository,
		sessionCreator:         sessionCreator,
		verificationCodeHasher: verificationCodeHasher,
		emailSender:            emailSender,
		pinRateLimiter:         pinRateLimiter,
	}
}

// FakePinRateLimiter stands in for the Redis-backed limiter.
//
// It records what it was asked so a test can assert which namespace and subject a
// service spent, which is how the two endpoints are shown to share one budget.
type FakePinRateLimiter struct {
	mutex sync.Mutex

	// Allowed is what Allow reports. False models an exhausted budget.
	Allowed bool

	// Err, when set, is returned instead of Allowed.
	Err error

	// RetryAfter is the remaining window reported on a refusal.
	RetryAfter time.Duration

	// Subjects records every subject asked about, in order.
	Subjects []string

	// Namespaces records every namespace asked about, in order.
	Namespaces []string

	// Policies records every policy given, in order.
	Policies []ratelimit.Policy

	counts map[string]int64
}

func NewFakePinRateLimiter() *FakePinRateLimiter {
	return &FakePinRateLimiter{Allowed: true}
}

func (l *FakePinRateLimiter) Allow(
	_ context.Context,
	namespace string,
	subject string,
	policy ratelimit.Policy,
) (ratelimit.Result, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Namespaces = append(l.Namespaces, namespace)
	l.Subjects = append(l.Subjects, subject)
	l.Policies = append(l.Policies, policy)

	if l.counts == nil {
		l.counts = map[string]int64{}
	}

	l.counts[namespace]++

	if l.Err != nil {
		return ratelimit.Result{}, l.Err
	}

	return ratelimit.Result{
		Count:      l.counts[namespace],
		Allowed:    l.Allowed,
		RetryAfter: l.RetryAfter,
	}, nil
}

// CallsIn returns how many times the given namespace was consulted.
func (l *FakePinRateLimiter) CallsIn(namespace string) int {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return int(l.counts[namespace])
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
