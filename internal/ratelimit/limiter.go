// Package ratelimit provides a Redis-backed fixed-window request limiter.
//
// It is deliberately generic: it counts occurrences of a subject within a window
// and answers whether the caller is still within its budget. What those subjects
// are, and what a budget is protecting, belongs to the feature that asks. The
// package knows nothing about registration, email or HTTP, so an endpoint can
// reuse it without this package learning what that endpoint is for.
//
// The window starts at the first counted occurrence rather than at a wall-clock
// boundary. That is the behaviour a caller wants when the budget protects a real
// cost: a request that arrives just before a boundary does not get a fresh
// allowance by waiting a moment for the clock to tick over.
package ratelimit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyPrefix namespaces every key this package writes.
//
// Redis is shared with the Asynq queue, and both sides may iterate or clear keys.
// A prefix that no other component uses is what keeps one from ever colliding
// with the other.
const keyPrefix = "rl"

// incrementWithTTL counts one occurrence, gives the counter an expiration the
// first time it is created, and reports the window's remaining life.
//
// Everything happens inside one script, and that is the whole point. INCR on its
// own is atomic, but a separate EXPIRE afterwards is not: a process that died
// between the two would leave a counter with no TTL, and that key would then
// accumulate forever and rate-limit the subject permanently. Redis runs a script
// to completion without interleaving another client, so the counter can never be
// incremented without either already having a TTL or receiving one in the same
// atomic step.
//
// The count and the remaining TTL are read in that same script rather than by a
// second round trip. Reading them separately would let the window expire between
// the two: a caller could be told "over budget" from a count that was real and a
// TTL that no longer was, and the only honest way to keep the two in step is to
// never let them drift apart.
//
// The TTL is set only when the counter is created (n == 1). Setting it on every
// call would slide the window forward with each request, so a subject could never
// fall out of its budget no matter how slowly it spent it. A refused request is
// therefore counted but never extends the window: it spends budget the window
// already had.
var incrementWithTTL = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then
	redis.call('EXPIRE', KEYS[1], ARGV[1])
end

local ttl = redis.call('PTTL', KEYS[1])

-- A counter with no expiry is the exact failure this script exists to prevent,
-- and one can still exist if an older build wrote it before the increment and
-- the expiry shared a script, or if something else is using the prefix.
-- Repairing it here costs one command and converts a permanent block into a
-- window. It only runs when there was no expiry to lengthen, so it can never
-- extend a window that already exists.
if ttl < 0 then
	redis.call('EXPIRE', KEYS[1], ARGV[1])
	ttl = tonumber(ARGV[1]) * 1000
end

return {n, ttl}
`)

// releaseOne gives one counted occurrence back to the subject's budget.
//
// It exists for the one case where an occurrence was counted before the work it
// guards could be known to have failed: a caller spends budget first so the
// expensive work is bounded, then has to undo that spend once the work turns out
// to have succeeded. Counting the attempt and then removing it states the same
// fact as never having counted it, without a second round trip or a window in
// which the budget could be observed in the wrong state.
//
// The guards are what make it safe to call at all:
//
//   - A missing key is left missing. A bare DECR would create it at -1, and a
//     negative counter would read as far below any budget and block the subject
//     for a window it never spent.
//   - The count never goes below zero, so a duplicate release cannot hand out
//     budget that was never taken.
//   - DECR does not touch the expiry, so releasing never extends the window. A
//     released slot belongs to the window it was taken from, and letting the
//     expiry slide forward would let a successful call postpone the reset.
var releaseOne = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
	return 0
end

local remaining = redis.call('DECR', KEYS[1])

if remaining < 0 then
	redis.call('SET', KEYS[1], 0)
	return 0
end

return remaining
`)

// Policy is one limiter's budget: how many occurrences are allowed, and over
// what period.
type Policy struct {
	// Max is the number of occurrences allowed within Window.
	Max int64

	// Window is how long the budget lasts. It is measured from the first counted
	// occurrence, so it is not aligned to any clock.
	Window time.Duration
}

// Validate reports whether the policy can be enforced as written.
//
// A zero or negative Max would disable limiting entirely, and a sub-second Window
// would be truncated to nothing by Redis, which takes whole seconds. Both are
// rejected rather than silently corrected, because a budget nobody asked for is
// the same as no budget at all.
func (p Policy) Validate() error {
	if p.Max <= 0 {
		return fmt.Errorf("rate limit max must be positive, got %d", p.Max)
	}

	if p.Window < time.Second {
		return fmt.Errorf(
			"rate limit window must be at least one second, got %s",
			p.Window,
		)
	}

	return nil
}

// Limiter counts subjects in Redis.
//
// It is safe for concurrent use and for use from many processes at once: every
// decision is one atomic script execution against the single Redis the whole
// deployment shares, so instances agree on the count without coordinating with
// each other. Nothing is kept in memory, which also means a Cloud Run instance
// that scales to zero loses no state.
type Limiter struct {
	client redis.UniversalClient
	secret []byte
}

// New builds a Limiter over an existing Redis client.
//
// The client is taken as an argument rather than built here so that the process
// owns exactly one connection pool and closes it in one place, which is how the
// API binary already treats Redis for the queue.
//
// The secret keys the subject hash. It must not be empty: an empty key would
// still produce a hash, but it would be one that anyone holding the database
// could recompute, which defeats the point of hashing at all.
func New(client redis.UniversalClient, secret []byte) *Limiter {
	return &Limiter{
		client: client,
		secret: secret,
	}
}

// Result is one limiter's answer to one occurrence.
type Result struct {
	// Count is how many occurrences have been recorded in the current window,
	// including this one.
	Count int64

	// Allowed reports whether that count is still within the policy. It is true
	// while Count is at or below the policy's Max, so the Max'th call in a window
	// is allowed and the next one is not.
	Allowed bool

	// RetryAfter is how long until the window resets and the budget is whole
	// again. It is read from the same atomic step that produced Count, rounded up
	// to a whole number of seconds, and is never negative.
	//
	// It is only meaningful when Allowed is false. On an allowed request the
	// window may be anywhere in its life, and reporting the remainder of a
	// window the caller is not blocked by would invite it to wait for no reason.
	RetryAfter time.Duration
}

// Allow counts one occurrence of subject within namespace and reports where that
// leaves the caller relative to policy.
//
// A refused occurrence is still counted: an attempt that was turned away is still
// an attempt, and counting it is what stops a caller from spending forever inside
// one window. Counting it does not extend the window, so a caller cannot punish
// everyone behind it by being refused repeatedly.
//
// An error means the count could not be established at all. Callers that guard
// something costly must treat that as a refusal rather than as permission: the
// limiter has not said the caller is within budget, it has said nothing.
func (l *Limiter) Allow(
	ctx context.Context,
	namespace string,
	subject string,
	policy Policy,
) (Result, error) {
	if err := policy.Validate(); err != nil {
		return Result{}, err
	}

	values, err := incrementWithTTL.Run(
		ctx,
		l.client,
		[]string{l.key(namespace, subject)},
		int64(policy.Window.Seconds()),
	).Slice()
	if err != nil {
		// The error names the namespace and never the key, so a log line drawn
		// from this stays free of anything derived from the subject.
		return Result{}, fmt.Errorf(
			"incrementing rate limit counter for namespace %q: %w",
			namespace,
			err,
		)
	}

	if len(values) != 2 {
		return Result{}, fmt.Errorf(
			"rate limit script returned %d values for namespace %q, want 2",
			len(values),
			namespace,
		)
	}

	count, err := toInt64(values[0])
	if err != nil {
		return Result{}, fmt.Errorf(
			"reading rate limit count for namespace %q: %w",
			namespace,
			err,
		)
	}

	ttlMillis, err := toInt64(values[1])
	if err != nil {
		return Result{}, fmt.Errorf(
			"reading rate limit window for namespace %q: %w",
			namespace,
			err,
		)
	}

	return Result{
		Count:      count,
		Allowed:    count <= policy.Max,
		RetryAfter: retryAfter(ttlMillis),
	}, nil
}

// Release gives one counted occurrence back to the subject's budget.
//
// It is the counterpart to Allow for a caller that must spend budget *before* it
// knows whether the guarded work will fail. Charging first is what bounds that
// work: with only a read-only check, a burst of concurrent requests would all pass
// the check and all go on to do the expensive thing, so the limit would bound how
// many requests are ultimately refused while bounding none of the work that
// matters. Charging first and releasing on success bounds the work and still
// leaves only genuine failures counted.
//
// It never removes the window. A released slot returns to the window it came from,
// so releasing cannot postpone a reset, and a released budget is not a fresh one:
// the remaining occurrences and the remaining time are unchanged.
//
// Releasing something that was never counted is a no-op rather than an error. The
// guards in the script are what make that safe to rely on — the count is never
// pushed below zero and a missing key is never created — so a caller that releases
// twice, or releases without having charged, cannot hand out budget that did not
// exist. That is the reason this is a method on the limiter rather than a raw
// delete a caller could point at any key it liked.
//
// An error means the release did not happen. For a caller releasing after the
// work it guarded has already succeeded, that is a stricter count rather than a
// missing protection: the budget was enforced when it was charged, and an
// unreleased slot expires with its window. It is still reported, so a Redis outage
// is visible rather than silently absorbed.
func (l *Limiter) Release(
	ctx context.Context,
	namespace string,
	subject string,
) (Result, error) {
	values, err := releaseOne.Run(
		ctx,
		l.client,
		[]string{l.key(namespace, subject)},
	).Slice()
	if err != nil {
		return Result{}, fmt.Errorf(
			"releasing rate limit counter for namespace %q: %w",
			namespace,
			err,
		)
	}

	if len(values) != 1 {
		return Result{}, fmt.Errorf(
			"rate limit release script returned %d values for namespace %q, want 1",
			len(values),
			namespace,
		)
	}

	count, err := toInt64(values[0])
	if err != nil {
		return Result{}, fmt.Errorf(
			"reading released rate limit count for namespace %q: %w",
			namespace,
			err,
		)
	}

	return Result{Count: count, Allowed: true}, nil
}

// retryAfter converts a millisecond window remaining into the wait a client
// should be told to observe.
//
// It rounds up, because rounding down would tell a client to retry while the
// window is still open and earn itself another refusal for no gain. It is never
// negative: Redis reports a negative PTTL for a key with no expiry and for a key
// that has gone, and neither of those is a length of time to hand to a client.
func retryAfter(ttlMillis int64) time.Duration {
	if ttlMillis <= 0 {
		return 0
	}

	seconds := (ttlMillis + 999) / 1000

	return time.Duration(seconds) * time.Second
}

// toInt64 reads one value out of a Lua reply.
//
// A Lua number arrives as int64, but a script returning a number that has passed
// through a string conversion can arrive as a string, and a reply shape that
// changes under a client should be an error rather than a panic.
func toInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case float64:
		return int64(typed), nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected reply type %T", value)
	}
}

// key builds the Redis key for a subject.
//
// The subject is HMAC'd rather than used directly, so a key never carries an email
// address or anything else a caller would not want readable by anything holding
// the connection string, visible to a key scan, or written into a slow log. HMAC
// is used instead of a plain digest because the same secret that protects
// verification codes is the one available here, and a keyed digest keeps a
// subject from being confirmed by anyone who can guess it from a key listing.
//
// The error from a limiter names the namespace and never this value, so the key
// does not need to be recoverable from anything this package returns.
func (l *Limiter) key(namespace string, subject string) string {
	mac := hmac.New(sha256.New, l.secret)
	mac.Write([]byte(subject))

	return fmt.Sprintf(
		"%s:%s:%s",
		keyPrefix,
		namespace,
		hex.EncodeToString(mac.Sum(nil)),
	)
}

// ErrNoClient reports that a Limiter was built without a usable Redis client.
//
// It exists so the failure surfaces as a refusal rather than a panic. A nil client
// is a wiring mistake, and a feature guarding a costly action must not treat a
// wiring mistake as permission to proceed.
var ErrNoClient = errors.New("rate limiter has no redis client")

// Check reports whether the limiter is usable at all.
//
// It is meant for startup, where finding out is far better than finding out on the
// first request that matters. It performs a round trip rather than inspecting the
// field, because a client that is present but cannot reach Redis is just as
// unusable as one that is absent.
func (l *Limiter) Check(ctx context.Context) error {
	if l == nil || l.client == nil {
		return ErrNoClient
	}

	if err := l.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("pinging redis for rate limiting: %w", err)
	}

	return nil
}
