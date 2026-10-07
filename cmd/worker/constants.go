package main

import "time"

// shutdownTimeout bounds how long a signalled worker waits for in-flight
// enrichment before giving up on it.
//
// Each attempt already has its own ceiling in queue.EnrichmentTaskTimeout, so
// this is not a second limit on any one task; it is the total time the process is
// willing to stay up past the signal. Without it a worker being asked to stop
// would wait on whatever the network is doing, and the platform would eventually
// kill it, turning a graceful stop into an abrupt one.
//
// Anything still queued or mid-attempt when this expires is not lost. It is left
// in Redis for the next worker, which is the same place it would have been had
// this one never started.
const shutdownTimeout = 15 * time.Second
