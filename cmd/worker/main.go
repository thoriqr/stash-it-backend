package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/thoriqr/stash-it-backend/internal/config"
	"github.com/thoriqr/stash-it-backend/internal/logger"
)

// This binary is the background worker. It is deliberately separate from
// cmd/api: the worker has no HTTP surface, imports nothing from internal/api,
// and does not pull in Fiber. It shares only infrastructure with the API, which
// here means the config loader boundary, the logger, and the Redis client.
//
// This is currently a startup and connectivity proof only. There is no queue,
// no job payload, no consumption loop and no enrichment logic yet. The process
// verifies it can reach Redis, logs that it started, and then stays alive until
// it is signalled.
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
	options, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		fmt.Println("Worker redis url error:", err)
		return
	}

	redisClient := redis.NewClient(options)
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

	log.Info(
		"worker started",
		zap.String("app_env", cfg.AppEnv),
		zap.String("redis_url", cfg.RedisURL),
	)

	// No work to do yet, so just stay alive until signalled. This is replaced
	// by the job consumption loop in a later change.
	<-ctx.Done()

	log.Info("worker shutting down")
}