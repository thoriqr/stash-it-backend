package operator

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// freeAddr reserves a port and releases it, so the test can start the page on an address
// nothing else is holding. A small race is possible between the close and the listen, and
// it is accepted here: the alternative is threading a listener through Start for the
// benefit of one test.
func freeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	return addr
}

// waitForListen polls the address until it accepts a connection.
//
// It dials rather than issuing a request: this test is about the listener, and a request
// would run a handler that needs the database. A successful dial is exactly what "the page
// is up" means here.
func waitForListen(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()

			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("operator page never listened on %s", addr)
}

// TestStart_StopsPromptlyWithAnOpenKeepaliveConnection is the shutdown behaviour a browser
// produces.
//
// Fiber's ShutdownWithContext waits for open connections to go idle and does not close
// keepalive ones, so a tab left open on the page used to spend the whole timeout and log
// "context deadline exceeded" on every Ctrl+C. This holds a keepalive connection open and
// asserts two things: the stop returns quickly, and nothing is logged as a failure.
func TestStart_StopsPromptlyWithAnOpenKeepaliveConnection(t *testing.T) {
	addr := freeAddr(t)

	core, logs := observer.New(zapcore.WarnLevel)
	log := zap.New(core)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A nil pool is deliberate: this test never reaches the database, and the routes that
	// would are the ones it does not call. Starting a container to prove a shutdown
	// returns promptly would make the test about Postgres rather than about the listener.
	stop := Start(ctx, nil, addr, "", log)

	waitForListen(t, addr)

	// One connection left open with nothing on it, which is what a browser tab holds
	// between requests. This is the connection the old shutdown waited on forever.
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)

	defer func() {
		_ = conn.Close()
	}()

	started := time.Now()
	stop()
	elapsed := time.Since(started)

	// The bound is the configured timeout plus room for the machine, and it is well below
	// the five seconds the old code waited. A regression that restored the long wait would
	// fail here rather than showing up as a warning line nobody reads.
	require.Less(
		t,
		elapsed,
		3*time.Second,
		"stop waited %s; an open keepalive connection must not hold the page",
	)

	require.Empty(
		t,
		logs.All(),
		"stopping the page is not a failure and must not be logged as one",
	)
}

// TestStart_EmptyAddrStartsNothing proves an unconfigured deployment has no page at all,
// rather than a page that listens and refuses.
func TestStart_EmptyAddrStartsNothing(t *testing.T) {
	stop := Start(context.Background(), nil, "", "", zap.NewNop())

	// Calling it must be harmless, because main defers it unconditionally.
	require.NotPanics(t, stop)
}
