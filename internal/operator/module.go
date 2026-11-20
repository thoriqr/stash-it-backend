package operator

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	operatordb "github.com/thoriqr/stash-it-backend/internal/operator/generated"
)

// NewApp builds the operator page as a Fiber app over the given pool.
//
// It is separate from Start so the page can be served by a test without binding a port:
// everything the page does — the guard, the queries, the template — is reachable through
// this value, and the only thing Start adds is a listener.
func NewApp(pool *pgxpool.Pool, secret string, log *zap.Logger) *fiber.App {
	queries := operatordb.New(pool)

	service := NewService(NewRepository(queries))

	handler := NewHandler(service, secret)

	app := fiber.New(fiber.Config{
		// The page is small and its responses are short; a body limit keeps a stray large
		// POST from being read into memory at all.
		BodyLimit: 1 << 20,
		// A failure is rendered by the handlers themselves, so this is only reached by
		// something outside them.
		ErrorHandler: newErrorHandler(log),
	})

	Routes(app, handler)

	return app
}

// newErrorHandler answers a request no handler produced a response for.
//
// It is separate from NewApp so its two rules can be tested without a database: which
// status reaches the client, and what is worth putting in the log.
func newErrorHandler(log *zap.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		status := fiber.StatusInternalServerError

		// The status the framework chose is kept. An unknown path is a 404 and a wrong
		// method is a 405; answering 500 for either would tell the browser the server
		// broke when it had simply been asked for something that is not here.
		var fiberErr *fiber.Error
		if errors.As(err, &fiberErr) {
			status = fiberErr.Code
		}

		// Only a fault is logged.
		//
		// A 4xx on this listener is a browser asking for something that was never here.
		// Chrome requests /.well-known/appspecific/com.chrome.devtools.json on every
		// origin it loads, and a stale bookmark asks for a path that no longer exists.
		// Neither is a problem, and logging each one buries the failures that are: the
		// page has no links that can 404, so anything here is noise rather than a broken
		// link of ours.
		//
		// This is the opposite of the API's rule, and deliberately. There, a 4xx is an
		// outcome the endpoint was built to produce and the log is how a client's mistake
		// becomes visible. Here the only reader is the person looking at the page.
		if status >= fiber.StatusInternalServerError {
			log.Error(
				"operator page request failed",
				zap.String("method", c.Method()),
				zap.String("path", c.Path()),
				zap.Int("status", status),
				zap.Error(err),
			)
		}

		return c.Status(status).SendString(http.StatusText(status))
	}
}

// The page's shutdown is bounded, short, and not reported as a failure.
//
// It used to log "context deadline exceeded" on every Ctrl+C, and that was the code being
// wrong rather than the log being noisy. Fiber's ShutdownWithContext waits for open
// connections to go idle and, as its own documentation says, does not close keepalive
// connections — so a browser tab left open on the page held one and every shutdown spent
// the full timeout waiting for something that was never going to finish. The API at :8080
// never showed it because nothing keeps a browser connection to it.
//
// There is nothing to drain here. A page that renders a list has no in-flight work worth
// waiting for, and the operator closes the tab or the SSH session when they are done. So
// the wait is short and its expiry is expected rather than exceptional: reporting it would
// train the reader to ignore the one line that would matter if this were the API.
const operatorShutdownTimeout = time.Second

// Start wires the operator page onto its own listener and serves it in the background.
//
// It is a second Fiber app rather than routes on the API's app, and that is the whole
// design: routes on the API's app would travel through Caddy and be reachable from the
// internet at the public hostname. A separate app on a separate listener has no shared
// middleware, no shared error handler and no shared port, so nothing about the API can
// expose it and nothing about it can affect the API.
//
// addr is the address to bind, and an empty value means this deployment has no operator
// page: nothing is started and no error is returned, because not having one is an
// ordinary state rather than a misconfiguration.
//
// The returned function stops the page.
func Start(
	ctx context.Context,
	pool *pgxpool.Pool,
	addr string,
	secret string,
	log *zap.Logger,
) func() {
	if addr == "" {
		return func() {}
	}

	app := NewApp(pool, secret, log)

	serveErr := make(chan error, 1)

	go func() {
		serveErr <- app.Listen(addr)
	}()

	// The listener reports its own failure rather than taking the process down. An
	// address already in use is worth a line in the log, not a reason to stop serving
	// the API: this surface is optional, and the rest of the application does not
	// depend on it.
	go func() {
		select {
		case err := <-serveErr:
			if err != nil {
				log.Warn(
					"operator page stopped",
					zap.String("addr", addr),
					zap.Error(err),
				)
			}
		case <-ctx.Done():
		}
	}()

	log.Info(
		"operator page listening",
		zap.String("addr", addr),
		// Whether a secret is required, never the secret. An empty value is the
		// ordinary case and means access is decided by the address alone.
		zap.Bool("secret_required", secret != ""),
	)

	return func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			operatorShutdownTimeout,
		)
		defer cancel()

		// Deliberately not logged. Exceeding this timeout is the normal outcome when a
		// browser tab is open, and Fiber force-closes the connections when it expires, so
		// there is neither a decision to make nor a fault to report. The API's shutdown
		// error is logged for the opposite reason: cutting off a request there is an
		// outcome an operator needs to see.
		_ = app.ShutdownWithContext(shutdownCtx)
	}
}
