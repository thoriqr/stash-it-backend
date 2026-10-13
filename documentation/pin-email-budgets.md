# PIN email budgets

Purpose of this document: record what the PIN email budgets are, why they are
shaped the way they are, and what a change to them would have to reconsider. It
is the reference for the policy; `PROGRESS.md` records that it exists and what
changed when.

## What they bound

Every message this application sends today is a verification PIN, and every one
of them is produced by exactly two features: **registration** and **password
reset**. Each has two issuance routes — an initial `/pin` and a `/resend` — and
all four draw on the same two budgets.

| Budget     | Subject                    | Limit             | Namespace         | Enforced by           |
| ---------- | -------------------------- | ----------------- | ----------------- | --------------------- |
| per-address| normalized email address   | 10 per 24 hours   | `pin-email`       | service, after load   |
| global     | the deployment             | 250 per 24 hours  | `pin-email-global`| service, before issue |

Both are Redis-backed **rolling (sliding) windows**, not fixed windows.

These are configuration, not constants: `PIN_EMAIL_BUDGET_PER_EMAIL_MAX`,
`PIN_EMAIL_BUDGET_GLOBAL_MAX` and `PIN_EMAIL_BUDGET_WINDOW_SECONDS`, loaded and
range-checked in `internal/config/email_budget.go`. The global figure exists to
keep the deployment under a delivery provider's plan quota, and that quota is a
fact about an account someone chose — it changes when the plan does, and must not
change by requiring a release.

Defaults are **10 per address**, **250 deployment-wide**, over a **86 400 second**
window.

**These thresholds are initial engineering estimates, not measured production
values.** No deployment has been selected and no traffic has been observed. The
250 exists to sit below a candidate provider plan's daily quota rather than at
it, and that plan has not been chosen. Do not treat any of the three as
validated, and do not move one to match another without saying which
shared-address false positive the change trades away — every one of these budgets
is charged to everyone behind one address, so a tighter number is a tighter
number for a household or a CGNAT too.

## Why a rolling window and not the existing fixed-window limiter

The existing `ratelimit.Limiter` counts requests inside a window that begins at
the first occurrence. That is the right shape for a request *rate*: refusing is
not self-sustaining there, because the counter keeps rising and the refusal is
what stops a client sitting inside one window indefinitely.

It is the wrong shape for a *quantity over a period*. A fixed window has an edge,
and a budget spent either side of that edge is twice the budget inside one
continuous 24-hour stretch. A rolling window has no edge: it asks how many
entries are still inside the period, so any 24 hours holds at most the maximum,
whichever moment those 24 hours begin at.

So a second mechanism exists — `ratelimit.SlidingLimiter` in
`internal/ratelimit/sliding.go` — with its own script, its own result type and
its own key prefix. **`Limiter.Allow` is untouched.** The thirteen fixed-window
namespaces already in production keep exactly the behaviour they have.

The prefix is `rlw`, not `rl`, and that is load-bearing: the rolling window
reuses the `pin-email` namespace name the fixed-window limiter already owns, and
the two store different types under the same name. A rolling window written over
a counter an earlier build left behind would fail with `WRONGTYPE` on every call,
which under a fail-closed budget reads as an outage rather than as a rollout.

## Why the per-address budget is shared

This was decided by reading the schema rather than on first principles:

- `pending_registrations_email_idx` is UNIQUE over addresses whose registration is
  pending or completed.
- `RegisterManual` answers 409 for an address that already has a completed
  registration, and **reuses** an existing pending registration rather than
  opening a second one.
- A pending registration belongs to somebody with no account for a password reset
  to find.

So for any given address exactly one of the two flows can issue anything at all,
and two budgets would be two budgets where one is never reachable.

**That is a dependency, not an accident.** If the product ever allows a second
account per address, re-registration after expiry, or a change of address, the
flows stop being mutually exclusive and this has to be revisited: a shared budget
would then let registration traffic spend a registered user's recovery allowance,
which is precisely the cost sharing was chosen to avoid.

## Ordering within a request

```
IP middleware
  -> parse verification id
  -> load the verification record
  -> validate pending/usable state
  -> 60-second resend cooldown
  -> per-address budget
  -> global budget
  -> generate and persist the PIN (existing transaction)
  -> send
```

The order is load-bearing in every direction:

- **The cooldown is before both budgets.** A caller already being told to wait is
  not also charged for asking. This matters more than it used to: the allowance is
  ten a day, so a slot spent on a request that was never going to send is one the
  user cannot get back until tomorrow.
- **The per-address budget is before the global one.** A caller refused for its
  own address must not also spend a slot of the whole deployment's budget. The
  mirror case — a global refusal costing a per-address slot — is the cheaper
  error, because reaching the global limit already means 250 messages went out.
- **Both are before anything is issued.** A refusal costs no row, no generated
  code and no message. `pin_issued_count` is not a delivery counter: it is only
  ever written inside the issuance transaction, so a refusal never moves it and a
  delivery failure does.
- **The IP budget is middleware, in front of the handler**, so requests that name
  no UUID or a verification that does not exist are still counted. The cheapest
  request an attacker can make is one that cannot succeed. Its policy and its
  trusted-proxy behaviour are unchanged.

**IP and per-address counters are never refunded.** Once a request has been
counted, nothing gives it back — not a cooldown, not a budget refusal, not a
failed send. The global one is different, and only in one direction; see below.

## Reservations and refunds

Only the **global** budget is reserved and refunded. The per-address budget is a
plain charge.

The rule is one line: **a global slot is given back only when the request failed
strictly before the email provider could have been contacted.**

| Failure                                     | Slot |
| ------------------------------------------- | ---- |
| Per-address budget refused                  | not spent — global is never reached |
| Global budget refused                       | not spent — there was no admission |
| Code generation failed                      | **refunded** |
| Issuance transaction failed                 | **refunded** |
| Sender explicitly reports no attempt        | **refunded** |
| Sender was reached and then failed          | kept    |
| Send succeeded                              | kept    |

"Reached and then failed" covers timeouts, connection resets and generic provider
errors. None of them prove the provider declined to process the message, so
handing the capacity back on that evidence would let a message be delivered
against a slot already given to somebody else.

A refund that cannot be completed is logged, not reported to the caller. The work
it was protecting has already failed, and turning that into a different error
would tell a caller their account is broken when what happened is that a counter
is one too high until its window passes.

## `Retry-After`

- **Computed from the oldest entry still inside the window**, inside the same
  atomic script execution that decided the call. Never from the Redis key TTL:
  that TTL is garbage collection, re-armed to a whole window on every call, so a
  caller refused on a budget whose key was written five minutes ago would
  otherwise be told to wait nineteen hours and fifty-five minutes for a slot that
  frees in seconds.
- **Rounded up to whole seconds and never negative**, because the header's
  contract is "not before".
- **A 503 from an unevaluated budget carries none.** There is no window to name.
- **A 503 from a delivery failure carries the full cooldown**, because the
  transaction that persisted the code is what restamped `last_sent_at` and it
  committed moments before the sender was called. Reporting the whole cooldown is
  the safe direction: a client that waits slightly too long is never turned away
  for asking too early.

## Privacy

The subject is HMAC'd with the same secret that keys verification codes, so a
Redis key never carries a readable email address — a key is visible to anything
holding the connection string and to a key scan.

Inside a window's sorted set the member is an **opaque random UUID**, never
derived from the subject or the clock. It has to be unique within the set (two
calls in the same millisecond would collide on a timestamp) and it has to
identify exactly one admission so a refund can name that one. A derived value
would risk being either guessable or reconstructing, and a hash of the subject
would put the very thing the key hides back inside the value.

Logs on a budget refusal record the namespace, the count, the maximum and the
wait. They never record the address.

## Error codes

| Situation                   | Status | Code                        | `Retry-After` |
| --------------------------- | ------ | --------------------------- | ------------- |
| per-address budget spent    | 429    | `PIN_RATE_LIMIT_EXCEEDED`   | yes           |
| global budget spent         | 429    | `PIN_EMAIL_BUDGET_EXCEEDED` | yes           |
| client budget spent         | 429    | `IP_RATE_LIMIT_EXCEEDED`    | yes           |
| per-address budget unusable | 503    | `PIN_RATE_LIMIT_UNAVAILABLE`| **no**        |
| global budget unusable      | 503    | `PIN_EMAIL_BUDGET_UNAVAILABLE` | **no**    |
| code issued, message not sent | 503  | `PIN_EMAIL_DELIVERY_FAILED` | yes           |

`PIN_RATE_LIMIT_EXCEEDED` keeps the string it has always had. Only the size and
the window behind it changed, so clients already handling it keep working.

The global budget has its own codes rather than reusing the per-address ones
because the two refusals are different problems. One says *this address may not be
mailed any more*; the other says *this deployment may not mail anybody*, which is
an operational condition a client can do nothing clever about.

**An unusable budget is a refusal.** Allowing on failure would hand anyone the
ability to switch the ceiling off by causing the failure that produces it. It is
a visible, recoverable outage rather than a silently removed ceiling.

## The sender contract

`email.Sender` is unchanged: one method, `Send(ctx, Message) error`.

What was added is a marker. A sender that cannot reach a provider at all returns
an error built with `email.NotAttempted`, and `email.Attempted(err)` reports
whether the provider may have been reached. **Anything not explicitly marked is
treated as attempted**, which is the safe direction.

Reporting this from the sender rather than inferring it keeps provider-specific
knowledge — which failures prove non-delivery — inside whichever sender is
eventually written, where it can be decided against real responses instead of
guessed at in the budget.

`UnconfiguredSender` reports `NotAttempted`, and this is the smallest change that
fixes a real problem: every non-development build is wired with it, so every PIN
email it is asked to deliver fails. If that failure looked like a failed delivery,
each attempt would consume a slot of the deployment-wide budget while delivering
nothing, and a staging deployment would exhaust the budget meant to protect a
real provider account before anyone had chosen a provider. It makes no network
call, so the marker is an accurate description rather than a convenience.

`DevSender` deliberately does **not** report `NotAttempted`. It stands in for a
provider that *was* reached, so its failures are attempted failures. The two
senders together are what let a test prove the budget refunds one and not the
other.

## What is deliberately not here

- **No provider.** Brevo is a candidate, not a decision. Selecting and integrating
  one would change `email.Sender`'s implementation and nothing here: the budget,
  the namespaces, the ordering and the refund rule would all be unchanged.
- **No migration, no outbox, no queue, and no send-before-persist.** The database
  issuance ordering and the one-active-PIN constraint are unchanged. The code is
  persisted before the sender is called, which is exactly what makes the global
  reservation refundable on a database failure and not on a send failure.
- **No development-mode bypass.** `DevSender` spends real counters against a real
  Redis whenever Redis is present. That is deliberate: a mode where the budget
  does not apply is a mode where the budget is untested, and a permissive fake is
  available in tests instead.

## Known limitation

Because the code is persisted first, a delivery failure leaves the user with no
usable code — the previous one was invalidated by the same transaction — and with
the cooldown restarted. The cost is bounded and small: both flows return the same
verification id when re-requested, so recovery is one cooldown and one more call
rather than a new flow.

Repairing the persisted state would mean changing when a code becomes active,
which is a schema and security change of its own. It is recorded rather than fixed.

## Where the code is

| Concern                | Location                                    |
| ---------------------- | ------------------------------------------- |
| Rolling-window limiter | `internal/ratelimit/sliding.go`             |
| Fixed-window limiter   | `internal/ratelimit/limiter.go` (unchanged) |
| Budget, namespaces     | `internal/email/budget.go`                  |
| Sender marker          | `internal/email/budget.go`, `sender.go`     |
| Configuration          | `internal/config/email_budget.go`           |
| Wiring                 | `cmd/api/main.go`, `internal/api/auth/module.go` |
| Feature interfaces     | `internal/api/auth/registration/types.go`, `.../password_reset/types.go` |
| Fakes                  | `internal/testutil/pin_email_budget.go`     |

Both features declare their **own** `PINEmailBudget` interface rather than
importing one. That duplication is the point: a consumer names what it consumes,
so a change to either declaration cannot silently change what the other depends
on. The method sets must stay identical or the wiring stops satisfying both —
which `internal/testutil/pin_email_budget.go` asserts at compile time.

## Testing

- **Redis, real limiter** (`internal/integration/sliding_ratelimit_redis_test.go`):
  admission, refusals not being recorded, entries ageing out on their own clock,
  `Retry-After` vs the key TTL, keys carrying no subject, no `WRONGTYPE`
  collision with the fixed-window limiter, concurrent admissions never exceeding
  the maximum, two instances sharing one budget, refunds removing exactly one
  admission and being safe to call twice, unreachable Redis.
- **Redis, real budget, through HTTP**
  (`internal/integration/pin_email_budget_test.go`): initial issuance and resend
  sharing one address budget, per-address isolation, a per-address refusal not
  spending global, cooldown before both budgets, `Retry-After`, **both features
  sharing one global budget**, both features keying one namespace, unreachable
  Redis, `UnconfiguredSender` not draining the budget, an attempted failure
  keeping its slot, and a concurrent burst never exceeding the limit.
- **Service level, per feature**: ordering, refusals, refunds and the typed 503,
  driven through both entry points.
- **Configuration** (`internal/config/email_budget_test.go`): defaults, the
  accepted range, every unusable value, and the rule that one address may not be
  allowed more than the deployment is.

The fakes in `internal/testutil` answer *which component spent what, in what
order*. They cannot answer *are these two features really drawing on one
budget* — two fakes handed to two features are two budgets by construction — nor
can they show that concurrent admissions stay inside the limit. Those are the
real-Redis tests' job, and splitting the two is deliberate rather than
duplicative.