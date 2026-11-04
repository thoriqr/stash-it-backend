package login

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/idtoken"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

const (
	testWebClientID     = "web-client"
	testDesktopClientID = "desktop-client"
)

// audienceRecorder stands in for idtoken.Validate.
//
// It records which configured audiences the verifier tried and accepts only the
// values in accepted. The real signature, issuer and expiry checks stay inside
// idtoken.Validate; these tests exercise the audience selection around it without
// contacting Google.
type audienceRecorder struct {
	accepted map[string]bool
	calls    []string
}

func (r *audienceRecorder) validate(
	_ context.Context,
	_ string,
	audience string,
) (*idtoken.Payload, error) {
	r.calls = append(r.calls, audience)

	if r.accepted[audience] {
		return validGooglePayload(), nil
	}

	return nil, errors.New("invalid token")
}

func validGooglePayload() *idtoken.Payload {
	return &idtoken.Payload{
		Subject: "google-subject",
		Claims: map[string]any{
			"email":          "user@example.com",
			"email_verified": true,
			"name":           "Google User",
		},
	}
}

func verifierWithRecorder(
	t *testing.T,
	webClientID string,
	desktopClientID string,
	accepted ...string,
) (*googleTokenVerifier, *audienceRecorder) {
	t.Helper()

	recorder := &audienceRecorder{accepted: map[string]bool{}}
	for _, audience := range accepted {
		recorder.accepted[audience] = true
	}

	verifier := NewGoogleTokenVerifier(webClientID, desktopClientID)
	concrete, ok := verifier.(*googleTokenVerifier)
	require.True(t, ok)
	concrete.validate = recorder.validate

	return concrete, recorder
}

func TestGoogleTokenVerifier_AcceptsTheWebClientID(t *testing.T) {
	verifier, recorder := verifierWithRecorder(
		t,
		testWebClientID,
		testDesktopClientID,
		testWebClientID,
	)

	identity, err := verifier.Verify(context.Background(), "id-token")

	require.NoError(t, err)
	require.Equal(t, "google-subject", identity.Subject)
	require.Equal(t, "user@example.com", identity.Email)
	require.True(t, identity.EmailVerified)
	require.Equal(t, "Google User", identity.DisplayName)
	require.Equal(t, []string{testWebClientID}, recorder.calls)
}

func TestGoogleTokenVerifier_AcceptsTheDesktopClientID(t *testing.T) {
	verifier, recorder := verifierWithRecorder(
		t,
		testWebClientID,
		testDesktopClientID,
		testDesktopClientID,
	)

	identity, err := verifier.Verify(context.Background(), "id-token")

	require.NoError(t, err)
	require.Equal(t, "google-subject", identity.Subject)
	require.Equal(
		t,
		[]string{testWebClientID, testDesktopClientID},
		recorder.calls,
		"the desktop audience is tried after the web audience",
	)
}

func TestGoogleTokenVerifier_RejectsAnUnrelatedAudience(t *testing.T) {
	verifier, recorder := verifierWithRecorder(
		t,
		testWebClientID,
		testDesktopClientID,
		"unrelated-client",
	)

	_, err := verifier.Verify(context.Background(), "id-token")

	require.Error(t, err)
	appErr := apperror.FromError(err)
	require.Equal(t, CodeInvalidGoogleToken, appErr.Code)
	require.Equal(t, http.StatusUnauthorized, appErr.Status)
	require.Equal(
		t,
		[]string{testWebClientID, testDesktopClientID},
		recorder.calls,
	)
}

// Without a desktop client the verifier keeps the previous single-audience
// behaviour: the web client is still tried, and no empty audience is invented.
func TestGoogleTokenVerifier_OptionalDesktopClientIDPreservesExistingBehaviour(t *testing.T) {
	verifier, recorder := verifierWithRecorder(
		t,
		testWebClientID,
		"",
		testWebClientID,
	)

	require.Equal(t, []string{testWebClientID}, verifier.audiences)

	identity, err := verifier.Verify(context.Background(), "id-token")

	require.NoError(t, err)
	require.Equal(t, "google-subject", identity.Subject)
	require.Equal(t, []string{testWebClientID}, recorder.calls)
}

// Audience selection must not bypass the identity claim checks that already
// apply after validation succeeds.
func TestGoogleTokenVerifier_PreservesIdentityClaimValidation(t *testing.T) {
	verifier, _ := verifierWithRecorder(
		t,
		testWebClientID,
		testDesktopClientID,
		testWebClientID,
	)

	verifier.validate = func(
		context.Context,
		string,
		string,
	) (*idtoken.Payload, error) {
		return &idtoken.Payload{
			Subject: "google-subject",
			Claims: map[string]any{
				"email":          "user@example.com",
				"email_verified": false,
			},
		}, nil
	}

	_, err := verifier.Verify(context.Background(), "id-token")

	require.Error(t, err)
	require.Equal(
		t,
		CodeInvalidGoogleToken,
		apperror.FromError(err).Code,
	)
}
