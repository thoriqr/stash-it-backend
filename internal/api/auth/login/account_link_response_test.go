package login_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/thoriqr/stash-it-backend/internal/api/auth/login"
	logindb "github.com/thoriqr/stash-it-backend/internal/api/auth/login/generated"
)

// maskEmail is unexported, so it is exercised the way the only caller reaches
// it: through GetAccountLinkConfirmation. Each case here is a row the repository
// could return and the exact string the service would put on the wire.
//
// The property every case asserts is the one that matters: the output is never
// the input. A helper that fell back to the original address would pass every
// ordinary case and still leak, so "not the input" is checked on each one
// separately rather than only where a mask obviously applies.
func TestService_GetAccountLinkConfirmation_MasksTheAccountEmail(t *testing.T) {
	cases := []struct {
		name     string
		userName string
		expected string
	}{
		{
			name:     "ordinary address",
			userName: "alice@example.com",
			expected: "a***@example.com",
		},
		{
			name:     "single character local part",
			userName: "a@example.com",
			expected: "a***@example.com",
		},
		{
			name:     "long local part keeps one character",
			userName: "a.very.long.local.part@sub.example.co.uk",
			expected: "a***@sub.example.co.uk",
		},
		{
			name:     "plus addressing keeps the domain whole",
			userName: "alice+stashit@gmail.com",
			expected: "a***@gmail.com",
		},
		{
			name:     "digits are a valid local part",
			userName: "1234567890@example.com",
			expected: "1***@example.com",
		},
		{
			// A quoted local part may contain an "@". Splitting at the first
			// would put part of the local part into what is presented as the
			// domain, which is the one thing this must never do.
			name:     "separator is the last at sign",
			userName: "\"a@b\"@example.com",
			expected: "\"***@example.com",
		},
		{
			name:     "non-ascii local part stays valid utf-8",
			userName: "álice@example.com",
			expected: "á***@example.com",
		},
		{
			// Nothing usable to show. Reporting the address would defeat the
			// function, so these report nothing at all.
			name:     "no separator reports nothing",
			userName: "alice",
			expected: "",
		},
		{
			name:     "empty local part reports nothing",
			userName: "@example.com",
			expected: "",
		},
		{
			name:     "empty domain reports nothing",
			userName: "alice@",
			expected: "",
		},
		{
			name:     "separator only reports nothing",
			userName: "@",
			expected: "",
		},
		{
			name:     "empty address reports nothing",
			userName: "",
			expected: "",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, repository, _, _, _, _ := newTestService(t)

			ctx := context.Background()
			confirmationID := uuid.New()

			// UserEmail is the account's own address and is the only source the
			// response may use. The row no longer carries a snapshot to confuse it
			// with, so this asserts what the field is rather than that it beat a
			// rival.
			repository.EXPECT().
				GetActiveAccountLinkConfirmation(ctx, confirmationID).
				Return(
					logindb.GetActiveAccountLinkConfirmationRow{
						ID:                  confirmationID,
						UserID:              uuid.New(),
						Provider:            "google",
						ProviderSubject:     "google-subject-123",
						DisplayNameSnapshot: pgtype.Text{String: "Alice", Valid: true},
						UserEmail:           testCase.userName,
					},
					nil,
				)

			result, err := service.GetAccountLinkConfirmation(ctx, confirmationID)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			if result.MaskedUserEmail != testCase.expected {
				t.Fatalf(
					"expected %q, got %q",
					testCase.expected,
					result.MaskedUserEmail,
				)
			}

			if testCase.userName != "" &&
				result.MaskedUserEmail == testCase.userName {
				t.Fatalf(
					"the full address %q was returned unmasked",
					testCase.userName,
				)
			}

			// The account address must never survive into the response in any
			// form. This is the assertion the others cannot make: a mask could
			// keep most of the address and still pass every case above.
			if testCase.userName != "" &&
				strings.Contains(result.MaskedUserEmail, testCase.userName) {
				t.Fatalf(
					"the response contains the full address %q: %q",
					testCase.userName,
					result.MaskedUserEmail,
				)
			}

			// The Google profile name is reported unchanged. It is a display
			// name, not a credential, and it is the one hint that identifies
			// which account is being linked.
			if result.DisplayNameSnapshot != "Alice" {
				t.Fatalf(
					"expected the display name snapshot unchanged, got %q",
					result.DisplayNameSnapshot,
				)
			}
		})
	}
}

// The response type is what reaches the wire, so the guarantee is asserted on it
// rather than only on the service result. A field could be dropped from the
// service result and still be marshalled from somewhere else; this cannot.
func TestGetAccountLinkConfirmationResponse_NeverCarriesTheAccountAddress(t *testing.T) {
	marshalled, err := json.Marshal(login.GetAccountLinkConfirmationResponse{
		ID:                  "01a0f359-093b-737a-963a-80f7ca6768ed",
		Provider:            "google",
		DisplayNameSnapshot: "Alice",
		MaskedUserEmail:     "a***@example.com",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	body := string(marshalled)

	// The fields that must exist for the frontend to render the screen.
	for _, key := range []string{
		"masked_user_email",
		"display_name_snapshot",
		"provider",
	} {
		if !strings.Contains(body, `"`+key+`":`) {
			t.Fatalf("expected %q in the response, got %s", key, body)
		}
	}

	// The fields that must not, because they carried the address in full or
	// repeated it. These are matched as whole JSON keys: `user_email` is a
	// substring of `masked_user_email`, so a bare substring test would report the
	// redaction as the very field it replaced.
	for _, key := range []string{"email_snapshot", "user_email", "user_display_name"} {
		if strings.Contains(body, `"`+key+`":`) {
			t.Fatalf("expected %q to be absent from the response, got %s", key, body)
		}
	}

	if strings.Contains(body, "alice@example.com") {
		t.Fatalf("the response carries a full address: %s", body)
	}
}
