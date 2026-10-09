package main

import "time"

// listenAddr is the address the API serves on.
//
// It is a constant rather than a literal at the call site so the startup log line
// and the server cannot disagree about where the process is listening, which would
// make a shutdown log that named the wrong address actively misleading.
const listenAddr = ":8080"

// shutdownTimeout bounds how long a signalled API waits for in-flight HTTP
// requests before giving up on them.
//
// It is a ceiling on the drain rather than on any one request: a request already
// being served is allowed to finish, but a client that is merely holding a
// keepalive connection must not be able to hold the process up indefinitely. Fiber
// does not close keepalive connections during shutdown, so without a bound a
// graceful stop could wait forever and the platform would eventually kill the
// process, turning a graceful stop back into an abrupt one.
//
// When this expires Fiber returns the context error rather than nil. Requests still
// open at that point were cut off, which is why the caller logs that error instead
// of discarding it, and it is why the value is comfortably under a Cloud Run grace
// period: draining for longer than the platform allows turns a bounded, reported
// shutdown back into an abrupt one.
//
// This is the same figure cmd/worker uses, chosen for consistency rather than
// because the two drains are alike. The worker waits on outbound fetches with a
// ten second ceiling each; this one waits on requests that are mostly database
// round trips.
const shutdownTimeout = 15 * time.Second
