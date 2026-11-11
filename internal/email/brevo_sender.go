package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// DefaultBrevoAPIURL is the production endpoint. It is a constant rather than an
// environment variable because the API key travels with every request: a
// configurable base URL would let a deployment send its credential to a host
// nobody intended. Tests pass their own URL to the constructor.
const DefaultBrevoAPIURL = "https://api.brevo.com/v3"

const (
	brevoSendPath      = "/smtp/email"
	brevoAccountPath   = "/account"
	brevoBodyReadLimit = 64 << 10
)

// ErrBrevoUnauthorized reports that Brevo refused the credential itself: either
// the API key is wrong or the caller's IP address is not authorized.
//
// It is a sentinel because the startup check treats it differently from every
// other failure. A wrong key or a blocked IP is a deployment mistake that will
// not repair itself, so the API refuses to start rather than serving a process
// whose registration flow can never send a PIN.
var ErrBrevoUnauthorized = errors.New(
	"brevo rejected the api key or the caller's ip address",
)

// BrevoSender delivers messages through Brevo's transactional email API.
//
// It keeps the provider's failure modes to itself. The budget only asks whether
// the provider could have accepted the message, and that question is answered
// here from the actual response: a 4xx proves it did not, while a 5xx or a
// connection failure after the request was written does not.
type BrevoSender struct {
	apiKey  string
	baseURL string
	from    Recipient
	client  *http.Client
	logger  *zap.Logger
}

// NewBrevoSender builds a sender for one verified sender identity.
//
// baseURL is a parameter rather than a constant read here so an httptest server
// can stand in for Brevo. The binary always passes DefaultBrevoAPIURL.
func NewBrevoSender(
	logger *zap.Logger,
	apiKey string,
	baseURL string,
	fromAddress string,
	fromName string,
	timeout time.Duration,
) *BrevoSender {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &BrevoSender{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		from: Recipient{
			Email: fromAddress,
			Name:  fromName,
		},
		client: &http.Client{
			Timeout: timeout,
			// A cross-host redirect would carry the api-key header to a host
			// nobody configured. Same-host redirects are ordinary; anything else
			// returns the redirect response, which this sender treats as a failed
			// send rather than following it.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 0 && req.URL.Host != via[0].URL.Host {
					return http.ErrUseLastResponse
				}

				return nil
			},
		},
		logger: logger,
	}
}

type brevoContact struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type brevoSendRequest struct {
	Sender      brevoContact   `json:"sender"`
	To          []brevoContact `json:"to"`
	Subject     string         `json:"subject"`
	TextContent string         `json:"textContent,omitempty"`
	HTMLContent string         `json:"htmlContent,omitempty"`
}

type brevoSendResponse struct {
	MessageID  string   `json:"messageId"`
	MessageIDs []string `json:"messageIds"`
}

type brevoErrorResponse struct {
	Code string `json:"code"`

	// Message is part of the response shape but is deliberately never logged or
	// returned. Brevo can echo the recipient in it, and a production log must not
	// carry delivery addresses.
	Message string `json:"message"`
}

// Send hands one message to Brevo.
//
// A 2xx means Brevo accepted the message for processing, not that it reached the
// mailbox. That distinction is Brevo's to report through its event webhooks and
// is deliberately not guessed at here.
func (s *BrevoSender) Send(
	ctx context.Context,
	message Message,
) error {
	if s.apiKey == "" {
		return NotAttempted(errors.New("brevo: api key is not configured"))
	}

	// A context that has already ended never produced a request, so nothing
	// could have been accepted. Checking before the call is what keeps that case
	// out of the ambiguous "the request may have been written" bucket.
	if err := ctx.Err(); err != nil {
		return NotAttempted(fmt.Errorf(
			"brevo: request context ended before sending: %w",
			err,
		))
	}

	payload := brevoSendRequest{
		Sender: brevoContact{
			Email: s.from.Email,
			Name:  s.from.Name,
		},
		To: []brevoContact{{
			Email: message.To.Email,
			Name:  message.To.Name,
		}},
		Subject:     message.Subject,
		TextContent: message.Text,
		HTMLContent: message.HTML,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return NotAttempted(fmt.Errorf("brevo: encoding the message: %w", err))
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		s.baseURL+brevoSendPath,
		bytes.NewReader(body),
	)
	if err != nil {
		return NotAttempted(fmt.Errorf("brevo: building the request: %w", err))
	}

	req.Header.Set("api-key", s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	response, err := s.client.Do(req)
	if err != nil {
		return classifyBrevoTransportError(err)
	}
	defer response.Body.Close()

	responseBody, readErr := io.ReadAll(
		io.LimitReader(response.Body, brevoBodyReadLimit),
	)

	if response.StatusCode >= http.StatusOK &&
		response.StatusCode < http.StatusMultipleChoices {
		var accepted brevoSendResponse
		if readErr == nil && len(responseBody) > 0 {
			_ = json.Unmarshal(responseBody, &accepted)
		}

		fields := []zap.Field{zap.Int("status", response.StatusCode)}
		if accepted.MessageID != "" {
			fields = append(fields, zap.String("message_id", accepted.MessageID))
		}

		s.logger.Info("brevo email accepted", fields...)

		return nil
	}

	providerCode, _ := parseBrevoError(responseBody, readErr)

	fields := []zap.Field{
		zap.Int("status", response.StatusCode),
		zap.String("provider_code", providerCode),
	}

	s.logger.Warn("brevo email rejected", fields...)

	failure := fmt.Errorf(
		"brevo: status %d, provider code %q",
		response.StatusCode,
		providerCode,
	)

	// A 4xx is Brevo saying it did not accept the message. That is exactly the
	// evidence the budget needs to give the global slot back; treating it as an
	// attempted failure would spend capacity on mail that was never sent.
	if response.StatusCode >= http.StatusBadRequest &&
		response.StatusCode < http.StatusInternalServerError {
		return NotAttempted(failure)
	}

	return failure
}

// Check asks Brevo whether this credential and source address may call the API.
//
// It exists for startup, not for delivery. A wrong key or an unauthorized IP is
// a permanent deployment mistake, and finding it here is the difference between
// a refused start and a registration flow that fails for every user.
func (s *BrevoSender) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		s.baseURL+brevoAccountPath,
		nil,
	)
	if err != nil {
		return fmt.Errorf("brevo: building the account check: %w", err)
	}

	req.Header.Set("api-key", s.apiKey)
	req.Header.Set("Accept", "application/json")

	response, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("brevo: checking the api key: %w", err)
	}
	defer response.Body.Close()

	responseBody, readErr := io.ReadAll(
		io.LimitReader(response.Body, brevoBodyReadLimit),
	)

	switch {
	case response.StatusCode >= http.StatusOK &&
		response.StatusCode < http.StatusMultipleChoices:
		return nil

	case response.StatusCode == http.StatusUnauthorized ||
		response.StatusCode == http.StatusForbidden:
		providerCode, providerMessage := parseBrevoError(
			responseBody,
			readErr,
		)

		// The provider's own message is included here, and only here. The
		// account check carries no recipient, so the message cannot leak one,
		// and "unauthorized" alone does not distinguish a wrong key from an
		// unauthorized IP or a missing permission.
		return fmt.Errorf(
			"%w: status %d, provider code %q, provider message %q",
			ErrBrevoUnauthorized,
			response.StatusCode,
			providerCode,
			providerMessage,
		)

	default:
		return fmt.Errorf(
			"brevo: account check returned status %d",
			response.StatusCode,
		)
	}
}

// classifyBrevoTransportError separates "no request was written" from "the
// request may have been processed".
//
// Only the former is safe to mark NotAttempted. DNS resolution and dialing
// happen before a byte of the request is sent, and a TLS handshake failure does
// too. A timeout while waiting for the response, a reset, or a write failure
// could all follow a request Brevo already accepted, so those stay attempted.
func classifyBrevoTransportError(err error) error {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return NotAttempted(fmt.Errorf(
			"brevo: resolving the api host: %w",
			err,
		))
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return NotAttempted(fmt.Errorf(
			"brevo: connecting to the api: %w",
			err,
		))
	}

	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return NotAttempted(fmt.Errorf(
			"brevo: tls handshake failed: %w",
			err,
		))
	}

	return fmt.Errorf("brevo: sending the request: %w", err)
}

func parseBrevoError(
	body []byte,
	readErr error,
) (code string, message string) {
	if readErr != nil || len(body) == 0 {
		return "", ""
	}

	var parsed brevoErrorResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", ""
	}

	return parsed.Code, parsed.Message
}
