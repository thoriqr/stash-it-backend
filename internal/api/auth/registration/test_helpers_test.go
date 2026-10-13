package registration_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration"
	"github.com/thoriqr/stash-it-backend/internal/api/auth/registration/mocks"
	sessionmocks "github.com/thoriqr/stash-it-backend/internal/api/auth/session/mocks"
	"github.com/thoriqr/stash-it-backend/internal/email"
	"github.com/thoriqr/stash-it-backend/internal/ratelimit"
	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/testutil"
	"go.uber.org/mock/gomock"
)

type testService struct {
	registrationService    registration.RegistrationService
	socialService          registration.SocialRegistrationService
	repository             *mocks.MockRepository
	sessionCreator         *sessionmocks.MockSessionCreator
	verificationCodeHasher *security.VerificationCodeHasher
	emailSender            *FakeEmailSender
	pinEmailBudget         *FakePINEmailBudget
}

func newTestService(t *testing.T) testService {
	t.Helper()

	return newTestServiceWithBudget(t, NewFakePINEmailBudget())
}

// newTestServiceWithBudget builds the service over a caller-supplied budget, so a
// test can model an exhausted budget, one that cannot answer, or one whose
// deployment-wide reservation is refused while the per-address one is not.
func newTestServiceWithBudget(
	t *testing.T,
	pinEmailBudget *FakePINEmailBudget,
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
		testutil.NewPasswordHasher(),
		verificationCodeHasher,
		emailSender,
		pinEmailBudget,
	)

	return testService{
		registrationService:    service,
		socialService:          service,
		repository:             repository,
		sessionCreator:         sessionCreator,
		verificationCodeHasher: verificationCodeHasher,
		emailSender:            emailSender,
		pinEmailBudget:         pinEmailBudget,
	}
}

// FakePINEmailBudget stands in for the shared sliding-window budget.
//
// It records what it was asked so a test can assert which address a service spent
// the per-address budget against and how many deployment-wide reservations it
// took — which is how both issuance endpoints and both features are shown to draw
// on one budget.
//
// The two reservations have independent outcomes on purpose. The whole point of
// the ordering rules is that one can refuse while the other admits, and a fake
// that could not express that could not test them.
type FakePINEmailBudget struct {
	mutex sync.Mutex

	// PerEmailAllowed is what the per-address reservation reports. False models an
	// address that has spent its budget.
	PerEmailAllowed bool

	// PerEmailErr, when set, is returned instead of a decision.
	PerEmailErr error

	// PerEmailRetryAfter is the wait reported on a per-address refusal.
	PerEmailRetryAfter time.Duration

	// GlobalAllowed is what the deployment-wide reservation reports.
	GlobalAllowed bool

	// GlobalErr, when set, is returned instead of a decision.
	GlobalErr error

	// GlobalRetryAfter is the wait reported on a deployment-wide refusal.
	GlobalRetryAfter time.Duration

	// DeliverErr, when set, is what Deliver reports without calling the sender.
	// It stands in for a sender that was reached and failed.
	DeliverErr error

	// PerEmailSubjects records every address a per-address reservation was asked
	// about, in order.
	PerEmailSubjects []string

	// PerEmailCalls counts per-address reservations.
	PerEmailCalls int

	// GlobalCalls counts deployment-wide reservations.
	GlobalCalls int

	// Refunds records every admission handed back, in order.
	Refunds []string

	admissionSeq int
}

func NewFakePINEmailBudget() *FakePINEmailBudget {
	return &FakePINEmailBudget{
		PerEmailAllowed: true,
		GlobalAllowed:   true,
	}
}

func (l *FakePINEmailBudget) ReservePerEmail(
	_ context.Context,
	normalizedEmail string,
) (ratelimit.SlidingResult, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.PerEmailCalls++
	l.PerEmailSubjects = append(l.PerEmailSubjects, normalizedEmail)

	if l.PerEmailErr != nil {
		return ratelimit.SlidingResult{}, l.PerEmailErr
	}

	if !l.PerEmailAllowed {
		return ratelimit.SlidingResult{
			Count:      int64(l.PerEmailCalls),
			Allowed:    false,
			RetryAfter: l.PerEmailRetryAfter,
		}, nil
	}

	return ratelimit.SlidingResult{
		Count:     int64(l.PerEmailCalls),
		Allowed:   true,
		Admission: "per-email-" + normalizedEmail,
	}, nil
}

func (l *FakePINEmailBudget) ReserveGlobal(
	_ context.Context,
) (ratelimit.SlidingResult, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.GlobalCalls++

	if l.GlobalErr != nil {
		return ratelimit.SlidingResult{}, l.GlobalErr
	}

	if !l.GlobalAllowed {
		return ratelimit.SlidingResult{
			Count:      int64(l.GlobalCalls),
			Allowed:    false,
			RetryAfter: l.GlobalRetryAfter,
		}, nil
	}

	return ratelimit.SlidingResult{
		Count:     int64(l.GlobalCalls),
		Allowed:   true,
		Admission: l.nextAdmission(),
	}, nil
}

func (l *FakePINEmailBudget) RefundGlobal(
	_ context.Context,
	admission string,
) error {
	// An empty admission is not a refund, and recording it would let a test count
	// one that the production component would never have performed.
	if admission == "" {
		return nil
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.Refunds = append(l.Refunds, admission)

	return nil
}

// Deliver applies the real refund rule rather than a simplified one, so a test
// that injects this fake is not blind to the one behaviour it is not meant to be
// permissive about: whether a failed send gives the deployment-wide slot back.
func (l *FakePINEmailBudget) Deliver(
	ctx context.Context,
	global ratelimit.SlidingResult,
	sender email.Sender,
	message email.Message,
) error {
	var err error

	if l.DeliverErr != nil {
		err = l.DeliverErr
	} else {
		err = sender.Send(ctx, message)
	}

	if err != nil && !email.Attempted(err) {
		_ = l.RefundGlobal(ctx, global.Admission)
	}

	return err
}

func (l *FakePINEmailBudget) nextAdmission() string {
	l.admissionSeq++

	return "global-" + strconv.Itoa(l.admissionSeq)
}

// RefundCount returns how many reservations were handed back.
func (l *FakePINEmailBudget) RefundCount() int {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return len(l.Refunds)
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
