package queue

import "time"

// Task and queue names.
//
// A task type and a queue are named constants rather than literals scattered
// across the producer and the consumer, because the two sides are separate
// packages in separate binaries. Asynq matches a consumed task by its type name
// as a string, so a typo on either side would compile, start, and then quietly
// never run anything.
const (
	// TaskTypeEnrichSavedItem is the Asynq task type for background metadata
	// enrichment of one saved item.
	TaskTypeEnrichSavedItem = "enrichment:saved_item"

	// QueueEnrichment is the queue enrichment tasks are processed from.
	//
	// It is a dedicated queue rather than Asynq's "default" because enrichment
	// is the slow, outbound-network work this project has. Keeping it on its own
	// queue is what lets the next job type be added without either starving
	// enrichment or being starved by it.
	QueueEnrichment = "enrichment"

	// TaskTypeOrganizeSavedItem is the Asynq task type for automatic filing of
	// one saved item into a collection chosen from its enriched platform.
	//
	// It follows the same "<area>:<thing>" shape as TaskTypeEnrichSavedItem, with
	// the two areas matching the queue names below. Organization is a distinct
	// area rather than a phase of enrichment because the two have different
	// reasons to fail and different retry budgets.
	TaskTypeOrganizeSavedItem = "organization:saved_item"

	// QueueOrganization is the queue organization tasks are processed from.
	//
	// A dedicated queue, on the same reasoning as QueueEnrichment. The work here
	// is fast database work with no outbound network, so sharing the enrichment
	// queue would mean a burst of saves delayed organization behind their own
	// page fetches.
	QueueOrganization = "organization"
)

// Task options.
//
// These bound a single enrichment attempt and the retries of it. They are set
// when the task is created, not when it is consumed, so a task that is already
// waiting in Redis carries its own retry policy with it and the policy does not
// depend on which worker version picks it up.
const (
	// EnrichmentTaskTimeout bounds one attempt.
	//
	// It has to clear security.OutboundFetchTimeout, which is 10s and covers the
	// whole outbound request including redirects and body reads, with room left
	// for the database writes either side of it. A smaller value would make Asynq
	// abandon attempts that were about to succeed, which Asynq then treats as a
	// failure and retries.
	EnrichmentTaskTimeout = 30 * time.Second

	// EnrichmentMaxRetry is how many times a retryable attempt is repeated after
	// the first one, so an item is fetched at most six times in total.
	//
	// The bound exists because the worker is not guaranteed to be running when the
	// work happens. Scale to zero means a task can wait hours before anything
	// touches it, and a queue that is only ever growing must not be able to grow
	// without limit either.
	EnrichmentMaxRetry = 5

	// EnrichmentRetryBaseDelay is the wait before the first retry.
	EnrichmentRetryBaseDelay = 30 * time.Second

	// EnrichmentRetryMaxDelay caps the wait between retries.
	//
	// Without a cap an exponential backoff reaches multi-hour waits after a
	// handful of attempts, at which point the queue is holding work nobody is
	// going to come back for.
	EnrichmentRetryMaxDelay = 30 * time.Minute
)

// EnrichmentRetryJitter is the fraction of a delay that jitter may add or
// remove.
//
// Jitter is not for hiding anything. A save burst, or one origin having a bad
// afternoon, fails many items at the same instant; without it every one of those
// retries would land on the same millisecond and the next burst would be exactly
// as synchronized as the first.
const EnrichmentRetryJitter = 0.2

// Organization task options.
//
// They are separate from the enrichment options rather than shared, because the
// work is different. Organizing one saved item is a locked row read, a possible
// collection insert, and one update: it never touches the network, so it needs
// neither enrichment's attempt timeout nor a backoff measured in minutes. Its
// failure is almost always a database contention problem that a short wait
// clears, which is what a short base delay buys.
const (
	// OrganizationTaskTimeout bounds one attempt.
	//
	// It is generous for the amount of work involved because the number that
	// matters is the one where Asynq abandons an attempt holding a row lock. A
	// generous ceiling keeps a slow but progressing attempt from being abandoned
	// and retried while it is still holding the lock it needs.
	OrganizationTaskTimeout = 10 * time.Second

	// OrganizationMaxRetry is how many times a retryable attempt is repeated
	// after the first one.
	//
	// A retryable organization failure is a transient database problem, so the
	// budget only needs to outlast contention rather than an origin being down.
	OrganizationMaxRetry = 3

	// OrganizationRetryBaseDelay is the wait before the first retry.
	OrganizationRetryBaseDelay = 5 * time.Second

	// OrganizationRetryMaxDelay caps the wait between retries.
	OrganizationRetryMaxDelay = time.Minute
)

// OrganizationRetryJitter is the fraction of an organization delay that jitter
// may add or remove, on the same reasoning as EnrichmentRetryJitter.
const OrganizationRetryJitter = 0.2
