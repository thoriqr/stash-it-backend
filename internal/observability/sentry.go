package observability

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/apperror"
)

// Config is the resolved Sentry configuration.
type Config struct {
	DSN              string
	Environment      string
	Release          string
	SampleRate       float64
	TracesSampleRate float64
	Debug            bool
}

// Reporter sends unexpected errors to Sentry when a DSN is configured.
//
// It is a value rather than a set of package-level calls so the HTTP error
// handler can be handed a reporter it can test with, and so a deployment without
// a DSN gets a reporter that does nothing rather than a nil check at every call
// site.
type Reporter struct {
	enabled bool
	log     *zap.Logger
}

// Init builds the reporter. An empty DSN is not an error: it means this
// deployment does not report errors, and the returned reporter is disabled.
func Init(cfg Config, log *zap.Logger) (*Reporter, error) {
	if log == nil {
		log = zap.NewNop()
	}

	if cfg.DSN == "" {
		return Disabled(log), nil
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.DSN,
		Environment:      cfg.Environment,
		Release:          cfg.Release,
		SampleRate:       cfg.SampleRate,
		EnableTracing:    cfg.TracesSampleRate > 0,
		TracesSampleRate: cfg.TracesSampleRate,
		Debug:            cfg.Debug,
		AttachStacktrace: true,
		SendDefaultPII:   false,
		DataCollection: &sentry.DataCollection{
			UserInfo: sentry.Set(false),
			Cookies: &sentry.KeyValueCollectionBehavior{
				Mode: sentry.CollectionOff,
			},
			HTTPHeaders: &sentry.HeaderCollectionConfig{
				Request: &sentry.KeyValueCollectionBehavior{
					Mode: sentry.CollectionOff,
				},
				Response: &sentry.KeyValueCollectionBehavior{
					Mode: sentry.CollectionOff,
				},
			},
			QueryParams: &sentry.KeyValueCollectionBehavior{
				Mode: sentry.CollectionOff,
			},
		},
		BeforeSend: func(
			event *sentry.Event,
			hint *sentry.EventHint,
		) *sentry.Event {
			if event == nil {
				return nil
			}

			// A client that went away is not a defect. Fiber handlers see this
			// through cancelled contexts and database drivers, and reporting it
			// would make normal disconnects look like failures.
			if hint != nil && hint.OriginalException != nil &&
				errors.Is(hint.OriginalException, context.Canceled) {
				return nil
			}

			// The reporter never attaches the Fiber request, and this is the
			// second guard: no body, query string, cookie or header can reach an
			// event even if a future call site sets one. A query string can carry
			// a continuation token and a body can carry a password or PIN.
			event.Request = nil
			event.User = sentry.User{}
			event.Breadcrumbs = nil

			return event
		},
	})
	if err != nil {
		return nil, err
	}

	return &Reporter{enabled: true, log: log}, nil
}

// Disabled returns a reporter that sends nothing.
func Disabled(log *zap.Logger) *Reporter {
	if log == nil {
		log = zap.NewNop()
	}

	return &Reporter{log: log}
}

// Enabled reports whether events are being sent.
func (r *Reporter) Enabled() bool {
	return r != nil && r.enabled
}

// CaptureHTTP sends one reportable HTTP error and returns its Sentry event id.
//
// The caller decides whether the error is reportable; this method only knows how
// to attach the context Sentry needs. The route template is used rather than the
// raw path, and the raw request is never attached, so identifiers and credentials
// in the URL do not travel with the event.
func (r *Reporter) CaptureHTTP(
	c fiber.Ctx,
	appErr *apperror.AppError,
) string {
	if !r.Enabled() {
		return ""
	}

	route := routePath(c)
	requestID := requestid.FromContext(c)

	hub := sentry.CurrentHub().Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelError)
		scope.SetTag("request_id", requestID)
		scope.SetTag("http.method", c.Method())
		scope.SetTag("http.route", route)
		scope.SetTag("http.status", strconv.Itoa(appErr.Status))
		scope.SetTag("error.code", appErr.Code)
		scope.SetFingerprint([]string{
			"http",
			appErr.Code,
			c.Method(),
			route,
		})
	})

	// Capture the AppError rather than the error the handler received. The
	// AppError carries the stack captured where Internal or InternalWith was
	// called; an outer wrapper would not, and Sentry would fall back to the
	// capture point.
	eventID := hub.CaptureException(appErr)
	if eventID == nil {
		return ""
	}

	return string(*eventID)
}

// CapturePanic logs a recovered panic and sends it to Sentry when enabled.
//
// The panic stack is logged here rather than left to Fiber's recoverer so a
// deployment without a DSN still records it in the same structured log as every
// other failure.
func (r *Reporter) CapturePanic(c fiber.Ctx, recovered any) {
	stack := debug.Stack()
	route := routePath(c)
	requestID := requestid.FromContext(c)

	if r != nil && r.log != nil {
		r.log.Error(
			"panic recovered",
			zap.String("request_id", requestID),
			zap.String("method", c.Method()),
			zap.String("route", route),
			zap.Any("panic", recovered),
			zap.ByteString("stack", stack),
		)
	}

	if !r.Enabled() {
		return
	}

	hub := sentry.CurrentHub().Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelFatal)
		scope.SetTag("request_id", requestID)
		scope.SetTag("http.method", c.Method())
		scope.SetTag("http.route", route)
		scope.SetFingerprint([]string{"panic", route})
		scope.SetContext("panic", sentry.Context{
			"value": fmt.Sprint(recovered),
			"stack": string(stack),
		})
	})

	hub.CaptureException(fmt.Errorf("panic: %v", recovered))
}

// Flush waits for queued events to leave the process. It is called on shutdown,
// where the alternative is losing the last events before the process exits.
func (r *Reporter) Flush(timeout time.Duration) {
	if !r.Enabled() {
		return
	}

	sentry.Flush(timeout)
}

// routePath returns the matched route template, never the raw URL.
//
// A UUID in a path is not a secret, but the route template is both more useful
// for grouping and immune to carrying any future credential placed in a path.
func routePath(c fiber.Ctx) string {
	if route := c.Route(); route != nil && route.Path != "" {
		return route.Path
	}

	return c.Path()
}
