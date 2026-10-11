package password_reset_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	passwordreset "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset"
	passwordresetdb "github.com/thoriqr/stash-it-backend/internal/api/auth/password_reset/generated"
	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// issueEntryPoint is one of the two public routes into PIN issuance.
//
// They are separate operations with separate response contracts, and the reason
// they share a policy is that they are the same act. Each case below therefore
// runs against both, because a policy asserted once against one of them is
// exactly the assertion that let /pin skip the cooldown in the first place.
type issueEntryPoint struct {
	name   string
	invoke func(
		test *testService,
		ctx context.Context,
		verificationID uuid.UUID,
	) (uuid.UUID, error)
}

func createPINEntryPoint() issueEntryPoint {
	return issueEntryPoint{
		name: "CreatePIN",
		invoke: func(
			test *testService,
			ctx context.Context,
			verificationID uuid.UUID,
		) (uuid.UUID, error) {
			result, err := test.passwordResetService.CreatePIN(
				ctx,
				verificationID,
			)

			return result.VerificationID, err
		},
	}
}

func resendEntryPoint() issueEntryPoint {
	return issueEntryPoint{
		name: "ResendVerification",
		invoke: func(
			test *testService,
			ctx context.Context,
			verificationID uuid.UUID,
		) (uuid.UUID, error) {
			result, err := test.passwordResetService.ResendVerification(
				ctx,
				verificationID,
			)

			return result.VerificationID, err
		},
	}
}

// TestService_IssueVerificationCode_CooldownAppliesToBothEndpoints pins the
// invariant that neither route is the way around the other.
//
// A refused issuance must also be refused *before* anything happens: no code is
// generated, no code is invalidated, last_sent_at and pin_issued_count are not
// advanced, and no message is sent. The repository expectation for
// IssueVerificationCode is deliberately absent on the refused cases — gomock
// treats an unexpected call as a failure, so that is the assertion, rather than
// a count asserted after the fact.
func TestService_IssueVerificationCode_CooldownAppliesToBothEndpoints(t *testing.T) {
	ctx := context.Background()

	entryPoints := []issueEntryPoint{
		createPINEntryPoint(),
		resendEntryPoint(),
	}

	cases := []struct {
		name       string
		lastSentAt pgtype.Timestamptz
		allowed    bool
	}{
		{
			name:       "never sent a code",
			lastSentAt: pgtype.Timestamptz{},
			allowed:    true,
		},
		{
			name: "cooldown already elapsed",
			lastSentAt: pgtype.Timestamptz{
				Time:  time.Now().Add(-2 * time.Minute),
				Valid: true,
			},
			allowed: true,
		},
		{
			name: "sent inside the cooldown window",
			lastSentAt: pgtype.Timestamptz{
				Time:  time.Now().Add(-30 * time.Second),
				Valid: true,
			},
			allowed: false,
		},
	}

	for _, entryPoint := range entryPoints {
		for _, testCase := range cases {
			t.Run(
				entryPoint.name+"/"+testCase.name,
				func(t *testing.T) {
					test := newTestService(t)

					verificationID := uuid.New()
					testEmail := "cooldown@example.com"

					test.repository.
						EXPECT().
						GetVerification(ctx, verificationID).
						Return(
							passwordresetdb.GetVerificationRow{
								ID:                  verificationID,
								Email:               testEmail,
								Status:              string(passwordreset.VerificationRequestPending),
								PasswordResetStatus: string(passwordreset.PendingPasswordResetPending),
								PasswordResetExpiresAt: pgtype.Timestamptz{
									Time:  time.Now().Add(7 * 24 * time.Hour),
									Valid: true,
								},
								LastSentAt: testCase.lastSentAt,
							},
							nil,
						)

					if testCase.allowed {
						test.repository.
							EXPECT().
							IssueVerificationCode(
								ctx,
								gomock.Any(),
							).
							Return(
								passwordresetdb.VerificationRequest{
									ID: verificationID,
								},
								nil,
							)
					}

					issuedID, err := entryPoint.invoke(
						test,
						ctx,
						verificationID,
					)

					if testCase.allowed {
						if err != nil {
							t.Fatalf(
								"expected no error, got %v",
								err,
							)
						}

						if issuedID != verificationID {
							t.Errorf(
								"expected verification ID %s, got %s",
								verificationID,
								issuedID,
							)
						}

						if len(test.emailSender.Messages) != 1 {
							t.Fatalf(
								"expected 1 email message, got %d",
								len(test.emailSender.Messages),
							)
						}

						if got := test.emailSender.Messages[0].To.Email; got != testEmail {
							t.Errorf(
								"expected recipient %q, got %q",
								testEmail,
								got,
							)
						}

						return
					}

					if err == nil {
						t.Fatal("expected error, got nil")
					}

					appErr := apperror.FromError(err)

					if appErr.Code != passwordreset.CodeVerificationResendCooldown {
						t.Errorf(
							"expected error code %q, got %q",
							passwordreset.CodeVerificationResendCooldown,
							appErr.Code,
						)
					}

					if appErr.Status != http.StatusConflict {
						t.Errorf(
							"expected status %d, got %d",
							http.StatusConflict,
							appErr.Status,
						)
					}

					if issuedID != uuid.Nil {
						t.Errorf(
							"expected no issued verification ID, got %s",
							issuedID,
						)
					}

					// Nothing is generated before the cooldown is checked, so a
					// refusal cannot invalidate the code the user is holding or
					// move the window they are waiting out.
					if len(test.emailSender.Messages) != 0 {
						t.Errorf(
							"expected no email message, got %d",
							len(test.emailSender.Messages),
						)
					}
				},
			)
		}
	}
}

// TestService_IssueVerificationCode_CooldownIsUnaffectedByEntryPoint states the
// same invariant from the other direction: which route issued the previous code
// does not change what the next one is allowed to do.
//
// The two routes write the same last_sent_at, so a code issued by /pin is
// observed by /resend and the other way round. That is the property that was
// previously false in one direction only, and it is worth asserting directly
// rather than leaving it implied by the cases above.
func TestService_IssueVerificationCode_CooldownIsUnaffectedByEntryPoint(t *testing.T) {
	ctx := context.Background()

	entryPoints := map[string]issueEntryPoint{
		"CreatePIN":          createPINEntryPoint(),
		"ResendVerification": resendEntryPoint(),
	}

	for issuerName, issuer := range entryPoints {
		for callerName, caller := range entryPoints {
			t.Run(
				issuerName+" issued, "+callerName+" called",
				func(t *testing.T) {
					test := newTestService(t)

					verificationID := uuid.New()
					issuedAt := time.Now()
					expiresAt := issuedAt.Add(7 * 24 * time.Hour)

					// The issuer reads a verification that has never sent a
					// code, so it issues. The caller then reads the row that
					// issuer left behind.
					test.repository.
						EXPECT().
						GetVerification(ctx, verificationID).
						Return(
							issuedVerification(
								verificationID,
								expiresAt,
								pgtype.Timestamptz{},
							),
							nil,
						)

					test.repository.
						EXPECT().
						IssueVerificationCode(ctx, gomock.Any()).
						Return(
							passwordresetdb.VerificationRequest{
								ID: verificationID,
							},
							nil,
						)

					if _, err := issuer.invoke(
						test,
						ctx,
						verificationID,
					); err != nil {
						t.Fatalf("issuing failed: %v", err)
					}

					if len(test.emailSender.Messages) != 1 {
						t.Fatalf(
							"expected 1 email message from the issuer, got %d",
							len(test.emailSender.Messages),
						)
					}

					// No further IssueVerificationCode expectation: the caller
					// must be refused before the repository is reached.
					test.repository.
						EXPECT().
						GetVerification(ctx, verificationID).
						Return(
							issuedVerification(
								verificationID,
								expiresAt,
								pgtype.Timestamptz{
									Time:  issuedAt,
									Valid: true,
								},
							),
							nil,
						)

					_, err := caller.invoke(test, ctx, verificationID)

					if err == nil {
						t.Fatal("expected error, got nil")
					}

					if got := apperror.FromError(err).Code; got !=
						passwordreset.CodeVerificationResendCooldown {
						t.Fatalf(
							"expected error code %q, got %q",
							passwordreset.CodeVerificationResendCooldown,
							got,
						)
					}

					if len(test.emailSender.Messages) != 1 {
						t.Errorf(
							"expected the refused call to send no message, got %d in total",
							len(test.emailSender.Messages),
						)
					}
				},
			)
		}
	}
}

// issuedVerification is a pending password reset verification whose only
// variable is when it last sent a code.
func issuedVerification(
	verificationID uuid.UUID,
	expiresAt time.Time,
	lastSentAt pgtype.Timestamptz,
) passwordresetdb.GetVerificationRow {
	return passwordresetdb.GetVerificationRow{
		ID:                  verificationID,
		Email:               "entry-points@example.com",
		Status:              string(passwordreset.VerificationRequestPending),
		PasswordResetStatus: string(passwordreset.PendingPasswordResetPending),
		PasswordResetExpiresAt: pgtype.Timestamptz{
			Time:  expiresAt,
			Valid: true,
		},
		LastSentAt: lastSentAt,
	}
}
