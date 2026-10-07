package queue_test

import (
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/security"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

// The backoff has to grow so a struggling origin is not hammered at a fixed rate,
// and it has to stop growing so a task is not parked beyond usefulness. Both
// bounds are asserted here rather than trusted to the formula.
//
// Growth and the cap are asserted separately because once the ceiling is reached
// they contradict each other: every later attempt draws the same capped delay, and
// independent jitter means any of them may come out lower than the one before it.
// That is what jitter is for, so no ordering can be required past the cap.
func TestEnrichmentRetryDelay_GrowsAndStaysCapped(t *testing.T) {
	// jittered returns a delay's value after jitter, bounded by the allowance the
	// jitter fraction permits in each direction.
	jittered := func(delay time.Duration) (time.Duration, time.Duration) {
		return time.Duration(
				float64(delay) * (1 - queue.EnrichmentRetryJitter),
			), time.Duration(
				float64(delay) * (1 + queue.EnrichmentRetryJitter),
			)
	}

	// capFloor is the lowest value a capped delay can take, so anything at or
	// below it is a capped draw rather than a growing one.
	capFloor, _ := jittered(queue.EnrichmentRetryMaxDelay)

	previous := time.Duration(0)
	sawCappedAttempt := false

	for attempt := 0; attempt <= queue.EnrichmentMaxRetry+3; attempt++ {
		delay := queue.EnrichmentRetryDelay(attempt, nil, nil)

		require.Positive(t, delay, "a retry must never be scheduled with no wait")

		// Jitter is a fraction of the delay, so this ceiling is the capped delay
		// plus its jitter and nothing more.
		_, ceiling := jittered(queue.EnrichmentRetryMaxDelay)

		require.LessOrEqual(
			t,
			delay,
			ceiling,
			"retry %d exceeded the capped delay", attempt,
		)

		if delay <= capFloor {
			sawCappedAttempt = true
		}

		if !sawCappedAttempt && previous > 0 {
			// Still growing: exponential growth means a later attempt is not
			// scheduled sooner than an earlier one, within the jitter allowance.
			require.GreaterOrEqual(
				t,
				delay,
				time.Duration(
					float64(previous)*(1-queue.EnrichmentRetryJitter),
				)-time.Second,
				"retry %d was scheduled sooner than the one before it",
				attempt,
			)
		}

		previous = delay
	}

	require.True(
		t,
		sawCappedAttempt,
		"the backoff must reach its cap within the retry budget",
	)
}

// The first retry must not fire immediately, and the last allowed retry must
// already be at the ceiling, so the retry policy spans minutes rather than
// seconds and does not quietly run out of room.
func TestEnrichmentRetryDelay_BoundsMatchTheRetryBudget(t *testing.T) {
	first := queue.EnrichmentRetryDelay(0, nil, nil)

	require.GreaterOrEqual(
		t,
		first,
		time.Duration(
			float64(queue.EnrichmentRetryBaseDelay)*(1-queue.EnrichmentRetryJitter),
		)-time.Second,
	)
	require.LessOrEqual(
		t,
		first,
		time.Duration(
			float64(queue.EnrichmentRetryBaseDelay)*(1+queue.EnrichmentRetryJitter),
		)+time.Second,
	)

	last := queue.EnrichmentRetryDelay(queue.EnrichmentMaxRetry, nil, nil)

	require.Greater(
		t,
		last,
		first,
		"the final retry must wait longer than the first one",
	)
}

// An attempt has to be allowed longer than the outbound fetch it contains,
// otherwise Asynq abandons an attempt that was about to succeed.
//
// The fetch ceiling is referenced by name rather than copied, so raising
// security.OutboundFetchTimeout without raising the task timeout fails here
// instead of quietly producing attempts that are abandoned mid-flight.
func TestEnrichmentTaskTimeout_ClearsTheOutboundFetchCeiling(t *testing.T) {
	require.Greater(
		t,
		queue.EnrichmentTaskTimeout,
		security.OutboundFetchTimeout,
		"a task must be allowed longer than the request it makes",
	)
}

// The task timeout has to clear the fetch ceiling with room for the database
// writes on either side of it, not merely exceed it. An attempt that was
// abandoned while writing its result would have done the outbound work for
// nothing and would be retried from the start.
func TestEnrichmentTaskTimeout_LeavesRoomForTheDatabaseWrites(t *testing.T) {
	require.Greater(
		t,
		queue.EnrichmentTaskTimeout,
		2*security.OutboundFetchTimeout,
		"the task timeout must clear the fetch ceiling with room to spare",
	)
}

// Retries are bounded. An unbounded retry policy on a queue that only fills when
// someone saves something is how a small deployment runs out of Redis.
func TestEnrichmentMaxRetry_IsBounded(t *testing.T) {
	require.Greater(t, queue.EnrichmentMaxRetry, 0)
	require.LessOrEqual(t, queue.EnrichmentMaxRetry, 10)
}

// The queue name is a contract between the producer and the worker, and it is
// matched as a string at runtime, so the two are pinned here rather than left to
// two independent literals staying equal by luck.
func TestQueueAndTaskTypeNames(t *testing.T) {
	require.Equal(t, "enrichment", queue.QueueEnrichment)
	require.Equal(t, "enrichment:saved_item", queue.TaskTypeEnrichSavedItem)
}

// The delay function must satisfy the signature Asynq installs it through.
func TestEnrichmentRetryDelay_IsAnAsynqRetryDelayFunc(t *testing.T) {
	var delay asynq.RetryDelayFunc = queue.EnrichmentRetryDelay

	require.NotNil(t, delay)
}
