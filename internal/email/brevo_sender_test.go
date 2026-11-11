package email_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/email"
)

type capturedBrevoRequest struct {
	method    string
	path      string
	apiKey    string
	body      map[string]any
	decodeErr error
}

func newTestBrevoSender(
	t *testing.T,
	baseURL string,
	timeout time.Duration,
) *email.BrevoSender {
	t.Helper()

	return email.NewBrevoSender(
		zap.NewNop(),
		"test-key",
		baseURL,
		"no-reply@example.com",
		"Stash It",
		timeout,
	)
}

func testMessage() email.Message {
	return email.Message{
		To:      email.Recipient{Email: "alice@example.com", Name: "Alice"},
		Subject: "Your code",
		Text:    "Your code is 123456",
	}
}

// A successful send carries the identity and body Brevo requires, and returns no
// error. The message id is logged rather than returned, because it is diagnostic
// information and not part of the sender contract.
func TestBrevoSender_SendsTheMessage(t *testing.T) {
	requests := make(chan capturedBrevoRequest, 1)

	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		captured := capturedBrevoRequest{
			method: r.Method,
			path:   r.URL.Path,
			apiKey: r.Header.Get("api-key"),
		}
		captured.decodeErr = json.NewDecoder(r.Body).Decode(&captured.body)
		requests <- captured

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"messageId":"<201@relay>"}`))
	}))
	defer server.Close()

	sender := newTestBrevoSender(t, server.URL, time.Second)

	err := sender.Send(t.Context(), testMessage())
	require.NoError(t, err)

	captured := <-requests
	require.Equal(t, http.MethodPost, captured.method)
	require.Equal(t, "/smtp/email", captured.path)
	require.Equal(t, "test-key", captured.apiKey)
	require.NoError(t, captured.decodeErr)

	require.Equal(t, map[string]any{
		"email": "no-reply@example.com",
		"name":  "Stash It",
	}, captured.body["sender"])
	require.Equal(t, []any{map[string]any{
		"email": "alice@example.com",
		"name":  "Alice",
	}}, captured.body["to"])
	require.Equal(t, "Your code", captured.body["subject"])
	require.Equal(t, "Your code is 123456", captured.body["textContent"])
}

// Brevo declining the request proves it did not accept the message, so the
// deployment-wide budget slot is safe to give back. That is the difference
// between a failed delivery and a delivery that might still happen.
func TestBrevoSender_ClassifiesProviderRejectionsAsNotAttempted(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusUnprocessableEntity,
		http.StatusTooManyRequests,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(
					`{"code":"invalid_parameter","message":"rejected"}`,
				))
			}))
			defer server.Close()

			err := newTestBrevoSender(t, server.URL, time.Second).
				Send(t.Context(), testMessage())

			require.Error(t, err)
			require.False(
				t,
				email.Attempted(err),
				"a provider rejection must not consume the global budget slot",
			)

			// The error is what reaches the logs. It must not carry the PIN or
			// the recipient, both of which the message body contains.
			require.NotContains(t, err.Error(), "123456")
			require.NotContains(t, err.Error(), "alice@example.com")
		})
	}
}

// A server error does not prove the message was declined, so the slot stays
// spent. This is the safe direction: handing capacity back on ambiguous evidence
// could let a message be delivered against a slot already given to somebody else.
func TestBrevoSender_TreatsServerFailuresAsAttempted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"internal_error"}`))
	}))
	defer server.Close()

	err := newTestBrevoSender(t, server.URL, time.Second).
		Send(t.Context(), testMessage())

	require.Error(t, err)
	require.True(t, email.Attempted(err))
}

// A connection that could not be established never carried a request, so nothing
// could have been accepted.
func TestBrevoSender_TreatsAnUnreachableHostAsNotAttempted(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	err = newTestBrevoSender(t, "http://"+address, time.Second).
		Send(t.Context(), testMessage())

	require.Error(t, err)
	require.False(t, email.Attempted(err))
}

// Once the request has been written, a timeout leaves the outcome unknown. It is
// treated as attempted, which keeps the slot.
func TestBrevoSender_TreatsATimeoutAfterSendingAsAttempted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	err := newTestBrevoSender(t, server.URL, 20*time.Millisecond).
		Send(t.Context(), testMessage())

	require.Error(t, err)
	require.True(t, email.Attempted(err))
}

// The startup check accepts a credential Brevo authorizes.
func TestBrevoSender_CheckAcceptsAnAuthorizedCredential(t *testing.T) {
	requests := make(chan capturedBrevoRequest, 1)

	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		requests <- capturedBrevoRequest{
			method: r.Method,
			path:   r.URL.Path,
			apiKey: r.Header.Get("api-key"),
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"email":"no-reply@example.com"}`))
	}))
	defer server.Close()

	require.NoError(t, newTestBrevoSender(t, server.URL, time.Second).
		Check(t.Context()))

	captured := <-requests
	require.Equal(t, http.MethodGet, captured.method)
	require.Equal(t, "/account", captured.path)
	require.Equal(t, "test-key", captured.apiKey)
}

// A wrong key and an unauthorized source address are the two failures the startup
// check exists to catch. Both are reported with the sentinel the composition root
// uses to refuse to start.
func TestBrevoSender_CheckReportsAnUnauthorizedCredential(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(
					`{"code":"unauthorized","message":"IP not authorized"}`,
				))
			}))
			defer server.Close()

			err := newTestBrevoSender(t, server.URL, time.Second).
				Check(t.Context())

			require.ErrorIs(t, err, email.ErrBrevoUnauthorized)
			require.ErrorContains(t, err, "IP not authorized")
		})
	}
}

// A provider outage is not a credential problem, so the startup check reports it
// as an ordinary error and the process may still start.
func TestBrevoSender_CheckLeavesOrdinaryFailuresUnclassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := newTestBrevoSender(t, server.URL, time.Second).
		Check(t.Context())

	require.Error(t, err)
	require.NotErrorIs(t, err, email.ErrBrevoUnauthorized)
}
