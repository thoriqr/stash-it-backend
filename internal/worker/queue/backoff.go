package queue

import (
	"math/rand/v2"
	"time"

	"github.com/hibiken/asynq"
)

// EnrichmentRetryDelay is the Asynq retry delay for a failed enrichment task.
//
// Asynq owns the retry loop, so this only decides how long to wait between two
// attempts. It is installed on the server rather than on the task because that is
// where Asynq takes it from, which is why it is exported: the worker process is
// a different package from the one that owns the policy.
//
// Only n and the ceiling matter here. The error is not inspected, because
// whether a failure deserves another attempt is decided where the failure
// happens — the task handler has classified it by then, and a permanent failure
// never reaches this function at all.
func EnrichmentRetryDelay(n int, _ error, _ *asynq.Task) time.Duration {
	delay := EnrichmentRetryBaseDelay

	// Doubling stops as soon as the ceiling is reached rather than continuing to
	// n, so an unexpectedly large retry count cannot turn into an overflowing
	// duration or a long loop.
	for range n {
		if delay >= EnrichmentRetryMaxDelay {
			return jitteredRetryDelay(EnrichmentRetryMaxDelay)
		}

		delay *= 2
	}

	if delay > EnrichmentRetryMaxDelay {
		delay = EnrichmentRetryMaxDelay
	}

	return jitteredRetryDelay(delay)
}

// jitteredRetryDelay spreads one delay by up to EnrichmentRetryJitter in either
// direction.
func jitteredRetryDelay(delay time.Duration) time.Duration {
	spread := float64(delay) * EnrichmentRetryJitter

	offset := (rand.Float64()*2 - 1) * spread

	return time.Duration(float64(delay) + offset)
}
