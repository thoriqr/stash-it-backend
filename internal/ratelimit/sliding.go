package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Sliding-window accounting, alongside the fixed-window Limiter in limiter.go.
//
// The two answer different questions and are kept apart deliberately. Limiter is
// a *request rate* limiter: it counts how often something happened inside a
// window that begins at the first occurrence, which is what a caller wants when
// the thing being bounded is how fast one client may hammer a route.
//
// A SlidingLimiter bounds a *quantity over a period of time* — how many messages
// the whole deployment may send in twenty-four hours — and for that a fixed
// window is the wrong shape. A fixed window has an edge, and a budget spent
// either side of that edge is twice the budget inside one continuous period. A
// rolling window has no edge: it asks how many entries are still inside the
// period, so any twenty-four-hour stretch holds at most the maximum, whichever
// moment the stretch happens to begin at.
//
// Nothing here changes Limiter.Allow, its namespaces, its counters, or its
// callers. This is a second component with its own prefix, its own script and its
// own result type, so the thirteen namespaces already in production keep the
// exact behaviour they have today.
//
// The prefix namespaces every key this file writes.
//
// It is deliberately not `rl`. The rolling windows reuse namespace names the
// fixed-window limiter already owns — the per-address PIN budget is `pin-email`
// either way — and the two store different types under the same name. A rolling
// window written over a counter an earlier build left behind would fail with
// WRONGTYPE on every call, which under a fail-closed limiter reads as an outage
// rather than as a rollout. A distinct prefix makes the two families incapable
// of colliding, and a deployment can move to them without clearing anything.
const slidingKeyPrefix = "rlw"

// admitToSlidingWindow decides one admission and does it atomically.
//
// Everything happens inside one script, and that is the whole point. Pruning,
// counting and recording are three separate Redis commands, and running them as
// three would let a concurrent request observe a count that another request was
// about to change. Redis runs a script to completion without interleaving another
// client, so the count this script reads is the count that was true when it
// started and the entry this script writes is visible to the next one.
//
// Entries are pruned by score rather than by key expiry. Key expiry is only
// garbage collection — it says nothing about which entries are still in the
// window — so it is re-armed on every call without any risk of sliding the
// window forward. That is the opposite of the fixed-window limiter, where the
// TTL *is* the window and re-arming it would extend the budget.
//
// ARGV: now (unix milliseconds), window (milliseconds), max, member.
//
// Returns count, admitted (0 or 1), retry-after (milliseconds, 0 when admitted).
var admitToSlidingWindow = redis.NewScript(`
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local max = tonumber(ARGV[3])
local member = ARGV[4]

redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - window)

local count = tonumber(redis.call('ZCARD', KEYS[1]))

redis.call('PEXPIRE', KEYS[1], window)

if count >= max then
	local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')

	local retry = 0
	if #oldest == 2 then
		retry = tonumber(oldest[2]) + window - now
		if retry < 0 then
			retry = 0
		end
	end

	return {count, 0, retry}
end

redis.call('ZADD', KEYS[1], now, member)

return {count + 1, 1, 0}
`)

// refundSlidingWindow gives one admission back.
//
// It removes the member rather than decrementing a count, which is what makes a
// refund safe to call twice: the second call names a member that is no longer in
// the set and removes nothing. A count-based refund cannot make that promise,
// because nothing ties the second decrement to the admission it is undoing and a
// caller that released twice would hand out a slot that belongs to somebody else.
//
// A missing key is left missing, so a refund can never bring a budget into
// existence.
//
// Returns 1 when a member was removed, 0 when there was nothing to remove.
var refundSlidingWindow = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
	return 0
end

return redis.call('ZREM', KEYS[1], ARGV[1])
`)

// SlidingPolicy is one sliding window's budget: how many entries may be held at
// once, and over what period.
//
// The name is the period rather than the window because that is what it is. A
// subject that spends its budget slowly still holds every entry it took, and the
// next one only frees when the oldest of them leaves the period.
type SlidingPolicy struct {
	// Max is the number of entries that may exist within Window at any moment.
	Max int64

	// Window is how far back entries count. It is measured against the moment of
	// each call rather than against any clock boundary.
	Window time.Duration
}

// Validate reports whether the policy can be enforced as written.
func (p SlidingPolicy) Validate() error {
	if p.Max <= 0 {
		return fmt.Errorf("sliding window max must be positive, got %d", p.Max)
	}

	if p.Window < time.Second {
		return fmt.Errorf(
			"sliding window window must be at least one second, got %s",
			p.Window,
		)
	}

	return nil
}

// SlidingResult is one admission decision.
type SlidingResult struct {
	// Count is how many entries the subject held inside the window after this
	// call, whether or not this one was admitted.
	Count int64

	// Allowed reports whether the entry was recorded. It is false when the
	// subject was already at the maximum.
	Allowed bool

	// RetryAfter is how long until the oldest entry leaves the window and one
	// slot frees. It is read from that entry's own timestamp inside the same
	// atomic step that decided the call, rounded up to whole seconds, and is
	// never negative.
	//
	// It is deliberately *not* the key's TTL. The TTL here is garbage collection
	// and is re-armed to a whole window on every call, so a caller refused on a
	// budget whose key was written five minutes ago would be told to wait
	// nineteen hours and fifty-five minutes for a slot that frees in seconds.
	//
	// It is only meaningful when Allowed is false.
	RetryAfter time.Duration

	// Admission is the opaque identifier recorded for this call, and is empty
	// when the call was refused.
	//
	// It is what a later Release names. Holding it is what lets a reservation be
	// given back exactly, rather than decrementing a counter that cannot say
	// which of its entries it is undoing.
	Admission string
}

// SlidingLimiter counts subjects in Redis with a rolling window.
//
// It is safe for concurrent use and for use from many processes at once: every
// decision is one atomic script execution against the single Redis the whole
// deployment shares, so instances agree on the count without coordinating. No
// state is kept in memory, which also means an instance that scales to zero
// loses nothing.
type SlidingLimiter struct {
	client redis.UniversalClient
	secret []byte
}

// NewSlidingLimiter builds a SlidingLimiter over an existing Redis client.
//
// The client is taken as an argument for the same reason it is for Limiter: the
// process owns one connection pool and closes it in one place.
//
// The secret keys the subject hash and must not be empty, for the same reason
// Limiter's must not be. A deployment sharing one Redis with another holds a
// different secret and therefore a different key, so the two do not share
// budgets.
func NewSlidingLimiter(client redis.UniversalClient, secret []byte) *SlidingLimiter {
	return &SlidingLimiter{
		client: client,
		secret: secret,
	}
}

// Allow records one occurrence of subject within namespace when the subject is
// still inside its budget, and reports where that leaves it.
//
// **A refused occurrence is not recorded.** This is the deliberate difference
// between this and Limiter.Allow, which counts refusals so a caller cannot spend
// forever inside one window, and it is worth saying why the reasoning does not
// transfer.
//
// There, refusing is not self-sustaining: the caller's counter keeps rising, and
// the refusal is what stops a client sitting inside a window indefinitely. Here,
// a refusal is already self-sustaining, because the window is defined by the
// entries inside it rather than by a counter: a subject at its maximum stays at
// its maximum until its oldest entry leaves the period, however many refusals
// arrive in between. Recording them would add nothing to that and would cost
// something concrete — a client hammering a closed budget would grow the set
// without bound, on a key that is meant to hold at most Max entries, and a
// budget of ten would become a set of ten thousand. It would also make Count
// meaningless as a utilisation figure, since Count would grow while the number of
// genuine admissions did not.
//
// An error means the budget could not be evaluated at all. Callers must treat
// that as a refusal: the limiter has not said the subject is within budget, it
// has said nothing, and a limiter that cannot answer is not permission.
func (l *SlidingLimiter) Allow(
	ctx context.Context,
	namespace string,
	subject string,
	policy SlidingPolicy,
) (SlidingResult, error) {
	if err := policy.Validate(); err != nil {
		return SlidingResult{}, err
	}

	// The member is opaque and random rather than derived from the subject or
	// from the clock. It has to be unique within the set — two calls in the same
	// millisecond would collide on a timestamp — and it has to identify exactly
	// one admission so a refund can name that one. A derived value risks being
	// either guessable or reconstructing, and a hash of the subject would put the
	// very thing the key is written to hide back inside the value.
	memberID, err := uuid.NewRandom()
	if err != nil {
		return SlidingResult{}, fmt.Errorf(
			"generating a sliding window member for namespace %q: %w",
			namespace,
			err,
		)
	}

	member := memberID.String()

	values, err := admitToSlidingWindow.Run(
		ctx,
		l.client,
		[]string{l.key(namespace, subject)},
		time.Now().UnixMilli(),
		policy.Window.Milliseconds(),
		policy.Max,
		member,
	).Slice()
	if err != nil {
		// The error names the namespace and never the key, so a log line drawn
		// from this stays free of anything derived from the subject.
		return SlidingResult{}, fmt.Errorf(
			"admitting to sliding window %q: %w",
			namespace,
			err,
		)
	}

	if len(values) != 3 {
		return SlidingResult{}, fmt.Errorf(
			"sliding window script returned %d values for namespace %q, want 3",
			len(values),
			namespace,
		)
	}

	count, err := toInt64(values[0])
	if err != nil {
		return SlidingResult{}, fmt.Errorf(
			"reading sliding window count for namespace %q: %w",
			namespace,
			err,
		)
	}

	admitted, err := toInt64(values[1])
	if err != nil {
		return SlidingResult{}, fmt.Errorf(
			"reading sliding window decision for namespace %q: %w",
			namespace,
			err,
		)
	}

	retryMillis, err := toInt64(values[2])
	if err != nil {
		return SlidingResult{}, fmt.Errorf(
			"reading sliding window wait for namespace %q: %w",
			namespace,
			err,
		)
	}

	result := SlidingResult{
		Count:      count,
		Allowed:    admitted == 1,
		RetryAfter: retryAfter(retryMillis),
	}

	if result.Allowed {
		result.Admission = member
	}

	return result, nil
}

// Release gives one admission back, identified by the Admission that Allow
// returned for it.
//
// It is the counterpart to Allow for a caller that must spend budget *before* it
// knows whether the work it guards will happen: a message budget is charged, and
// if the work turns out never to reach the provider the charge is undone. Doing
// it this way rather than with a read-only check is what bounds the work — a
// burst of concurrent requests would otherwise all pass a check made before any
// of them had reserved anything.
//
// Refunding is for failures that occur *strictly before* the guarded party was
// contacted. Once something might have been accepted elsewhere, the slot is
// spent whatever the call returns.
//
// Releasing something that was never recorded is a no-op rather than an error,
// because the member names exactly one entry: a second release removes nothing.
func (l *SlidingLimiter) Release(
	ctx context.Context,
	namespace string,
	subject string,
	admission string,
) error {
	if admission == "" {
		return nil
	}

	if _, err := refundSlidingWindow.Run(
		ctx,
		l.client,
		[]string{l.key(namespace, subject)},
		admission,
	).Result(); err != nil {
		return fmt.Errorf(
			"refunding sliding window %q: %w",
			namespace,
			err,
		)
	}

	return nil
}

// key builds the Redis key for a subject under this limiter's own prefix.
//
// The derivation is the same HMAC the fixed-window limiter uses and for the same
// reason: a key is visible to anything holding the connection string and to a
// key scan, and neither should be able to read an email address back out.
func (l *SlidingLimiter) key(namespace string, subject string) string {
	return deriveKey(slidingKeyPrefix, namespace, l.secret, subject)
}

// Check reports whether the limiter is usable at all, for startup.
func (l *SlidingLimiter) Check(ctx context.Context) error {
	if l == nil || l.client == nil {
		return ErrNoClient
	}

	if err := l.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("pinging redis for sliding window limiting: %w", err)
	}

	return nil
}
