package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/database"
	enrichmentcore "github.com/thoriqr/stash-it-backend/internal/enrichment"
	"github.com/thoriqr/stash-it-backend/internal/logger"
	"github.com/thoriqr/stash-it-backend/internal/security"
	workerenrichment "github.com/thoriqr/stash-it-backend/internal/worker/enrichment"
	workerenrichmentdb "github.com/thoriqr/stash-it-backend/internal/worker/enrichment/generated"
	workerorganization "github.com/thoriqr/stash-it-backend/internal/worker/organization"
	workerorganizationdb "github.com/thoriqr/stash-it-backend/internal/worker/organization/generated"
	"github.com/thoriqr/stash-it-backend/internal/worker/queue"
)

// This binary is the background worker. It is deliberately separate from
// cmd/api: the worker has no HTTP surface, imports nothing from internal/api,
// and does not pull in Fiber. It shares only infrastructure with the API, which
// here means the config loader boundary, the logger, the Redis connection, and
// the two capability packages both sides use for the same reasons.
//
// What the worker does is two jobs, and they are deliberately separate task types
// on separate queues rather than phases of one task. Enrichment takes a saved
// item id off the enrichment queue, fetches the page, and records what it found.
// When that produced a platform it queues one organization task, and organization
// takes that off its own queue, re-reads the item, and files it into a collection
// named by the platform if the item is still in Unsorted. Splitting them means a
// failure to organize cannot disturb what enrichment recorded, and a retry of one
// never re-fetches the page for the other.
//
// There is no scheduler and no sweep. Work appears because a save put it there,
// and organization appears because an enrichment produced a platform; nothing
// periodically scans saved_items for items that look unorganized.
func main() {
	// Created before anything else so a SIGTERM during startup still unwinds
	// cleanly instead of being handled by the default disposition.
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	cfg, err := config.LoadWorker()
	if err != nil {
		fmt.Println("Worker config error:", err)
		return
	}

	log, err := logger.New(cfg.AppEnv)
	if err != nil {
		fmt.Println("Worker logger error:", err)
		return
	}
	defer func() {
		_ = log.Sync()
	}()

	// ParseURL keeps the whole Redis configuration in one value and accepts
	// redis:// as well as rediss://, so switching to a TLS endpoint later is a
	// configuration change rather than a code change.
	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		fmt.Println("Worker redis url error:", err)
		return
	}

	redisClient := redis.NewClient(redisOptions)
	defer func() {
		_ = redisClient.Close()
	}()

	// Ping is the startup connectivity check. Failing here is deliberate: a
	// worker that cannot reach Redis cannot do its job, and exiting makes the
	// misconfiguration obvious instead of leaving a process that looks healthy
	// and silently does nothing.
	if err := redisClient.Ping(ctx).Err(); err != nil {
		fmt.Println("Worker redis connection error:", err)
		return
	}

	pool, err := database.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Println("Worker database error:", err)
		return
	}
	defer pool.Close()

	// The worker is the second consumer of the guarded outbound HTTP client, and
	// the second user of the extraction capability. Both are constructed here from
	// the same security boundary the synchronous endpoint uses, so a URL the API
	// refuses to fetch is a URL the worker also refuses to fetch. The client is
	// built from the policy rather than fetched from anywhere, so there is no
	// second SSRF implementation to keep in step with the first.
	outboundFetchPolicy := security.DefaultOutboundFetchPolicy()

	metadataEnricher := enrichmentcore.NewEnricher(
		security.NewGuardedHTTPClient(outboundFetchPolicy),
		outboundFetchPolicy,
	)

	enrichmentQueries := workerenrichmentdb.New(pool)

	enrichmentRepository := workerenrichment.NewRepository(
		enrichmentQueries,
	)

	organizationQueries := workerorganizationdb.New(pool)

	organizationRepository := workerorganization.NewRepository(
		pool,
		organizationQueries,
	)

	// The enrichment service schedules organization through this producer, so the
	// handoff is wired here rather than inside either package: the enrichment
	// package knows only the narrow interface it needs, and the organization
	// package knows nothing about enrichment at all.
	workProducer := queue.NewProducer(redisClient)

	enrichmentService := workerenrichment.NewService(
		enrichmentRepository,
		metadataEnricher,
		workProducer,
		log,
	)

	enrichmentHandler := workerenrichment.NewHandler(
		enrichmentService,
		log,
	)

	organizationService := workerorganization.NewService(
		organizationRepository,
		log,
	)

	organizationHandler := workerorganization.NewHandler(
		organizationService,
		log,
	)

	// Asynq is built over the Redis connection this process already owns and has
	// already pinged, so the queue needs no separate configuration and no second
	// startup check. NewServerFromRedisClient does not take ownership: the deferred
	// Close above is still the one that closes the pool.
	server := asynq.NewServerFromRedisClient(
		redisClient,
		asynq.Config{
			// Concurrency is bounded because enrichment is network-bound: each
			// in-flight task is a request to somebody else's server with a ten
			// second ceiling on it. A modest number keeps the worker from opening a
			// large number of connections it has no use for, while still overlapping
			// the waits that make up most of the work.
			Concurrency: cfg.WorkerConcurrency,

			// Queues is declared explicitly rather than left to the "default"
			// queue, because each queue name is also the name a producer writes to.
			// A worker listening to the wrong queue would look healthy and idle.
			//
			// Both are listed so neither kind of work starves the other: enrichment
			// is outbound-network bound and organization is not.
			Queues: map[string]int{
				queue.QueueEnrichment:   1,
				queue.QueueOrganization: 1,
			},

			// RetryDelayFunc is the queue package's policy rather than a literal
			// here, so the backoff lives next to the constants that describe it.
			RetryDelayFunc: queue.EnrichmentRetryDelay,

			// Asynq logs through an untyped variadic interface while this project
			// logs structured fields with zap, so the two are bridged instead of
			// letting Asynq write unstructured lines to stderr alongside them.
			Logger: logger.Asynq(log),

			// ShutdownTimeout bounds how long a signalled worker waits for in-flight
			// enrichment. Each attempt has its own 30 second ceiling, so this is the
			// time to let current work finish rather than to start anything new.
			ShutdownTimeout: shutdownTimeout,
		},
	)

	mux := asynq.NewServeMux()
	mux.Handle(queue.TaskTypeEnrichSavedItem, enrichmentHandler)
	mux.Handle(queue.TaskTypeOrganizeSavedItem, organizationHandler)

	log.Info(
		"worker started",
		zap.String("app_env", cfg.AppEnv),
		// Only the parsed connection endpoint, never cfg.RedisURL itself. A Redis
		// URL may carry a password in its userinfo section, and logging it would
		// write a live credential into the log stream. The address and the database
		// are what make a startup line diagnosable; ownership of the connection is
		// unaffected.
		zap.String("redis_addr", redisOptions.Addr),
		zap.Int("redis_db", redisOptions.DB),
		zap.Int("concurrency", cfg.WorkerConcurrency),
		zap.Strings(
			"queues",
			[]string{queue.QueueEnrichment, queue.QueueOrganization},
		),
		zap.Strings(
			"task_types",
			[]string{
				queue.TaskTypeEnrichSavedItem,
				queue.TaskTypeOrganizeSavedItem,
			},
		),
	)

	if err := server.Start(mux); err != nil {
		fmt.Println("Worker server start error:", err)
		return
	}

	// Nothing here decides when work appears. There is no scheduler and no
	// polling: tasks are already in Redis because a save enqueued them, and this
	// loop simply stays out of the way until the process is asked to stop.
	<-ctx.Done()

	log.Info("worker shutting down")

	// Shutdown stops the server from picking up new tasks and waits for the ones
	// already running. Anything still queued stays in Redis for whichever worker
	// runs next, which is what makes this safe to deploy alongside the API: a
	// scale-to-zero event or a redeploy loses no work, it only defers it.
	server.Shutdown()

	log.Info("worker stopped")
}
