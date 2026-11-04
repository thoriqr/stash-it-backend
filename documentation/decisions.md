# Decisions

Design decisions that must not change casually. Revisit one only with an explicit
decision of its own. Each entry says what was decided and why, so the reasoning
lives here and not only in the code. Where an entry names a function, type or
column, the code and its comments hold the detail.

---

## Saved items, enrichment, collections and rate limiting

1. **`saved_items.platform` is the content/source platform, not the client
   platform.** It represents services such as YouTube or Spotify, not web,
   Android, or iOS.

2. **`X-Platform` is session-only.** It populates `sessions.platform` and must
   never be used by Saved Item enrichment.

3. **`platform` is metadata-derived.** It must not be inferred from the URL
   hostname, client input, or an implicit domain-to-platform mapping.

4. **`domain` is derived and normalized server-side.** Only lowercase and one
   leading `www.` are removed. Do not add public-suffix or registrable-domain
   logic without an explicit decision.

5. **Duplicate URLs are allowed.** There is no unique constraint, deduplication,
   or `url_hash`.

6. **No Saved Item update/PATCH endpoint exists yet.** Do not add one unless
   explicitly requested.

7. **Delete is a hard delete.** There is no soft-delete or `deleted_at`.

8. **`POST /saved-items` enqueues enrichment, and the synchronous endpoint
   remains available.** A save commits its row first and then queues the item's
   id. `POST /saved-items/:id/enrich` still enriches one specific item on
   demand, and enrichment is repeatable from either path.

   Both paths produce the same row for the same page, and neither changes
   `collection_id` or `domain`.

9. **Enrichment has exactly three persisted states: `pending`, `completed`,
   `failed`.** There is deliberately no `processing` or `retrying` value. How
   many more attempts the queue will make is execution mechanics, not the state
   of the Saved Item, so it is never written. Adding a status value requires an
   explicit decision and a migration.

10. **The guarded HTTP client is the only outbound fetch path for user-supplied
    URLs.** Do not add another client, transport, or SSRF implementation. The
    worker constructs the same one and shares no other outbound path.

11. **Outbound ports are restricted to 80/443.** Widening this allowlist requires
    an explicit decision.

12. **`internal/enrichment` is reusable extraction logic, not persistence.**
    It has no Fiber, SQLC, Asynq, or database responsibility. It is shared by
    the synchronous endpoint and the worker, which is why neither path can
    diverge on what a page error means.

13. **Enrichment must not modify `collection_id`.** Metadata enrichment and
    automatic collection organization are separate concerns. Organization writes
    `collection_id` and nothing else.

13a. **Automatic organization is event-driven and never overrides a user.** A task
exists only because a successful enrichment produced a platform, and it may only
move an item that is still in `Unsorted`. Do not add a scan, a sweep, a periodic
query, or a persisted organization status to find items the event missed.

13b. **`collections.type = 'system'` describes who created a collection, not who
owns it.** A system collection belongs to exactly one user through
`collections.user_id`, and migration 000025's composite foreign key is what makes
an item's collection and its owner have to agree.

14. **The worker has a separate configuration boundary.** It uses
    `WorkerConfig` / `LoadWorker` and does not depend on API-only secrets. It also
    imports nothing from `internal/api`.

15. **`FailureKind` separates a page that cannot be used from an origin that is
    not answering.** Terminal client statuses, non-HTML content and unparseable
    documents are permanent; network failures, unreadable bodies, `429` and `5xx`
    are retryable within the bounded budget. Changing which group a failure falls
    into changes retry behavior, so treat it as a product decision.

16. **A save succeeds even when enqueueing fails.** A failed enqueue leaves the
    item `pending`, and there is no reconciliation pass for it either. This is a
    deliberate trade-off: the Saved Item is valid product data regardless of its
    metadata, and the user can always ask on demand.

17. **Canonical URLs are not automatically applied.** The extractor may report a
    canonical URL, but replacing the Saved Item's original URL is a separate
    product decision.

18. **`Metadata.Author` and `Metadata.SiteName` are currently extraction-only
    fields.** Adding persistence for them requires an explicit product/schema
    decision.

19. **Deleting a saved item never deletes its collection.** The delete reports the
    collection, whether it is now empty, and whether the user may delete it, and
    those are advisory values for the caller. Removing a collection is a separate
    endpoint the caller invokes deliberately. An empty collection is a valid state,
    so removing one as a side effect would take an action nobody asked for.

19a. **Deleting a collection is an explicit destructive operation whose caller
states the disposition of its saved items.** `saved_items_action` is `delete` or
`move` with no default, because the two have opposite consequences and one
destroys content. Nothing is inferred on the caller's behalf, and a missing
target is an error rather than a substitution.

19b. **Unsorted is identified by `system_key` and is the only protected
collection.** Collection type is not the criterion: `type = 'system'` says who
created a collection, not what it is, so an automatically created collection is
deletable like one the user named. Unsorted as a _move target_ is ordinary and is
named by its id; there is no `move_to_unsorted` mode and no fallback to it.

19c. **`saved_items_collection_id_fkey` remains the final guard on collection
deletion.** The children are disposed of first because `collection_id` is
`NOT NULL`, and the FK decides emptiness at the final DELETE rather than an
application pre-check. A violation rolls the transaction back and is reported as
`COLLECTION_NOT_EMPTY`. Never change it to `ON DELETE CASCADE`.

19d. **The move-all-items behavior exists only as part of collection deletion.**
`MoveSavedItemsToCollection` has no endpoint of its own. A general batch-move
feature is a separate product decision and is deliberately not implemented here.

19e. **A cursor is a position, not a row.** It records where a page ended, and the
next query resumes from there without ever reading the row it came from. So a
cursor keeps working after that row is deleted or renamed, and a cursor pointing
past the end returns an empty page rather than an error. This is the property that
separates this from offset pagination, where a deleted row shifts everything after
it and the client silently receives a duplicate.

19f. **`GET /collections` uses cursor pagination; the existing offset list endpoints
are unchanged.** `GET /collections/:id/saved-items` is cursor-paginated too.
`GET /auth/sessions` keeps `page`/`limit` and `meta.pagination`. `httpx.Meta`
carries both, with `cursor` omitted entirely where unset, so no existing response
changes. Converting the session endpoint is a separate piece of work and needs its
own decision.

19g. **The Inbox is a cross-collection recent history, served by
`GET /saved-items`. Every saved item belongs to exactly one collection, so each
item reports that collection and the history is never confused with any single
collection's contents.** This supersedes the earlier decision that there is no
standalone saved-item listing. The reasoning it was based on still stands and is
why the endpoint looks the way it does: it reports the full thirteen-field item
including `enrichment_status`, `last_enriched_at`, `description` and
`image_url`, so a caller can tell a page with no title from one that had not
been read, and it carries a nested `collection` of `id` and `name` per item
because a listing that spans collections cannot name one collection for the
whole response. What changed is the product's need: the Flutter Inbox is a
chronological record of what was saved, so an item has to stay in it after it
has been filed into a collection of the user's own.

`GET /collections/:id/saved-items` is unchanged and still the way to see one
collection's contents. `Unsorted` is **not** the Inbox: it is one collection
among the ones an item can be in, it is pinned first in `GET /collections`
because every save lands there, and it is never used as a substitute for the
cross-collection history. Do not "simplify" one of these three into another.

Nothing is filtered out of the history. A `pending` item, an item whose page
could not be read, and an item automatic organization filed are all ordinary
states of a saved item and all appear; a history that hid the two it had not
finished processing would be a record of only what went well. The listing also
never writes and never triggers enrichment or organization.

19j. **A collection listing reports a bounded saved item count, and the bound is
one number with a separate boolean rather than a value a client has to
interpret.** `saved_items_count` is capped at
`CollectionSavedItemsCountCap` (99) and `saved_items_count_overflow` says
whether the real count was higher, so a client renders an exact badge up to 99
and `99+` above it without ever comparing against a threshold of its own. The
count is of the **whole collection**, on **every page** — a count that described
the page would be a different number on each one, and a badge that changed as
the user scrolled would not be a count of anything.

The bound is passed to every listing statement as a query parameter rather than
written into the SQL, so `CollectionSavedItemsCountCap` is the only place it is
stated; a literal in each of the nine statements would be nine places that could
disagree. This is about the size of a collection, not about the size of a page,
and does not reintroduce a pagination `total`: `has_more` still comes from one
extra fetched row.

The bound is on the **rows read**, not only on the number reported. The inner
`SELECT` asks for at most `CollectionSavedItemsCountCap + 1` matching rows and
stops there, so one collection's count costs the same whether it holds 5 items or
50,000. Clipping `COUNT(*)` alone would have bounded only the answer and left the
aggregate walking every row. The `+ 1` is what makes the threshold decidable:
exactly `cap` rows is still an exact count, so without it a collection holding
exactly 99 would be indistinguishable from one holding more.

19h. **A collection listing names its collection.** `GET /collections/:id/saved-items`
reports `data.collection` with `id` and `name`, reused from the ownership lookup
that call already had to make — no extra query. It is reported on every response
including an empty page, because "nothing in it" and "no such collection" are
different things and the name is what tells them apart. `type` and `system_key` are
left out: they answer questions for `GET /collections`, and a client that reached
this endpoint has already named the collection it wants. Unsorted goes through the
same type as everything else.

19i. **A response schema that omits `meta` is a documentation bug, and it is guarded.**
Both cursor endpoints described `meta.cursor` in prose while their generated 200
schema showed a two-key envelope, because `httpx.OKWithMeta` builds `meta` and the
API response structs did not declare it. Nothing at runtime could notice, and the
result is still valid OpenAPI, so no linter did either. Both now declare a
documentation-only mirror sharing one `CursorPageMeta`, and
`TestCollectionAPI_ListSavedItems_DocumentedShape` walks `docs/swagger.json` and a
real body side by side at every level, failing if either gains or loses a key.

19k. **A collection name is a destination, not a reserved namespace.** Filing a
saved item resolves the name against every collection the user owns, and collection
type does not restrict the result. A name already held by a system collection such
as `Unsorted` or `YouTube` resolves to that collection; it is not a name to reject.
`collections_user_name_unique` is per-user uniqueness across both types, not a
reserved-name rule, and `COLLECTION_NAME_RESERVED` is not part of the collection API
contract. The automatic organization worker has its own, narrower reserved-name rule
for platform names and is a separate writer with separate behavior.

20. **A forward header is believed only behind a verified allowlist, and the
    declaration of where a client's address comes from is one setting rather than
    two.** `CLIENT_IP_SOURCE` is `peer` or `proxy`, and everything else — the
    framework's proxy trust, its allowlist, which header is read, and whether the
    forwarded chain is parsed and validated — is derived from it. There is no
    combination in which a header is read without a verified allowlist beside it,
    which is what makes "the header was present" and "the header was believed"
    the same thing. Do not read `X-Forwarded-For`, `X-Real-IP` or any similar
    header outside `middleware.IPRateLimit`'s use of `c.IP()`, and never as an
    identity, a rate-limit subject, or a logging key on its own.

21. **`EnableIPValidation` is mandatory whenever a header is read.** Fiber v3.5.0's
    `extractIPFromHeader` returns the header's raw bytes when it is off, so a
    chain such as `203.0.113.7, 198.51.100.4` would become the client's identity
    — a separate counter for every chain a client chose to send. Turning it off
    next to proxy trust is a vulnerability, not a performance setting.

22. **A limiter that cannot answer refuses; it never allows.** A Redis outage, an
    unusable policy, or a client address that cannot be resolved all produce 503
    rather than a pass. Allowing on failure would let anyone remove the ceiling by
    causing one. Do not add a fallback that admits the request when the counter
    is unreachable.

23. **The two PIN budgets are separate dimensions and neither replaces the other.**
    The per-email budget stops one address being mailed repeatedly, which an
    attacker with many addresses defeats; the per-IP budget stops one address
    reaching the endpoints repeatedly, which an attacker with many proxies
    defeats. Merging them would leave a way in. `pin_issued_count` remains
    issuance history and is not a counter for either.

24. **The IP budget runs in front of the handler, not in the service.** A service
    never sees a request that failed to parse its UUID or named a verification
    that does not exist, so a limit enforced there would count only well-formed
    requests and would be free to burn with the cheapest kind of request there
    is. Moving it into the service would silently weaken it.

25. **`Retry-After` is the counter's remaining window, never the configured one.**
    It is read by the same atomic script that increments the counter, rounded up,
    and never negative. A refused request must not extend the window, and a 503
    must carry no `Retry-After` at all — there is no known recovery time and a
    fabricated one would be a guess presented as a fact.

26. **`verify-ip` and `finalize-ip` are budgets on cost, and each is separate from
    every other flow's.** Verification sends nothing and is bounded because a
    wrong attempt still costs lookups and a write; finalization is the only route
    in the flow that performs an Argon2id derivation, and the shared
    `PasswordWorkLimiter` bounds concurrency rather than rate, so it cannot stop
    one caller from occupying every slot indefinitely. Neither reuses `pin-ip`,
    and neither shares a counter with the other: adjacent steps of one
    registration are two acts, and a user who mistyped a PIN must still be able
    to finalize. Do not merge these namespaces, and do not add a per-email budget
    to either — the email is not known before the expensive work, so the key
    would be attacker-chosen and would let one caller lock a legitimate
    registrant out of their own registration.

27. **`GetVerification` and `GetRegistrationContinuation` stay unlimited.** Both
    are pure reads, so a limit there would cost a polling client its budget
    without bounding any expensive work. `GetVerification` is specifically what a
    client calls to learn when it may resend; refusing it would break the flow
    rather than protect it. Adding a budget to either needs an explicit decision.

    For the continuation route specifically: each request costs a token hash and
    one indexed lookup on `registration_continuations.token_hash`, served by the
    unique index migration 000027 added. It opens no transaction, writes nothing,
    sends no message, makes no external call and performs no Argon2id work. The
    token is high-entropy and is issued only after a PIN has been verified, so
    whoever holds one has already proved control of the mailbox the response
    describes; the response is that caller's own registration metadata, and
    repeating a non-mutating read neither widens nor changes it. The client needs
    to read it while it works out which fields finalization requires.

    `TestRegistrationIPRateLimit_UnlimitedReadRoutesAreNotCounted` pins this by
    asserting the route consults no budget at all. Mounting a limiter here would
    fail that test, which is the point: the decision is recorded, not merely
    unmade.

28. **The single-use transitions are guarded redundantly, and the tests assert the
    outcome rather than a particular guard.** `CompleteVerification` checks both
    `consumed_at IS NULL` and `status = 'pending'` with `rowsAffected == 1`, and
    the continuation is additionally protected by a unique index on
    `pending_registration_id`; finalization is protected by the users insert losing
    `users_email_unique`. Removing any one guard leaves the observable outcome
    unchanged, because a losing request is usually refused at the service's first
    read before the transaction opens. That redundancy is deliberate. Do not
    remove a guard on the grounds that another remains — and do not expect the
    concurrency tests to fail if one is removed, since they assert exactly one
    continuation and exactly one user rather than which guard produced them.

28a. **`POST /auth/register/finalize/social` carries no IP budget, and that is a
    decision rather than an omission.** This route does real work — it creates
    the user, the Unsorted collection and the auth identity, consumes the
    continuation, completes the registration and mints a session — so it must not
    be described as a cheap read. What bounds it is the lifecycle around it.

    It performs no Argon2id derivation, sends no message and does not re-verify
    the Google token, so the `PasswordWorkLimiter` and the reasoning behind
    `finalize-ip` do not reach it. `finalize-ip` exists because manual
    finalization is the one route in the flow that can occupy every password-work
    slot indefinitely; this route never calls the hasher, so that cost is not one
    it can impose.

    The continuation is single-use. `ConsumeRegistrationContinuation` matches only
    while `consumed_at IS NULL AND expires_at > NOW()` and its result is checked,
    with `users_email_unique` and `auth_identities_provider_subject_unique` behind
    it, so one token produces at most one account and one session however many
    times it is presented.
    `TestFinalizeSocialRegistration_ConcurrentSameTokenCreatesExactlyOneAccount`
    drives concurrent requests with a single token and asserts exactly that.

    A continuation only exists after the Google authentication, PIN issuance and
    PIN verification steps, and each of those already spends its own budget
    (`google-auth-ip`, `pin-ip`, `verify-ip`). What this route can be asked to do
    is therefore bounded by those upstream controls rather than by anything here.

    A fail-closed limiter would put a Redis dependency on the last step of account
    creation. While Redis is unavailable a user holding a valid continuation would
    be refused, and the continuation's 15-minute life means a prolonged outage
    costs them the registration outright rather than delaying it. That is a real
    cost, weighed against an incremental saving nothing has yet demonstrated.

    Revisit this if the single-use invariant changes, if the upstream budgets are
    removed or loosened, or if registration volume makes the route measurably
    expensive. Until then the absence of a budget here is intended. Do not close
    the gap by copying `finalize-ip`'s threshold or naming a new namespace: those
    numbers are sized for password work this route does not perform.

---

## Authentication and accounts

29. **Passwords are hashed with Argon2id, never bcrypt, and the parameters travel
    with the hash.** `internal/security/password.go` stores the parameters, salt
    and hash together as a PHC string, so verification reconstructs the parameters
    a hash was made with and existing hashes stay verifiable after the parameters
    are strengthened. Do not introduce bcrypt. Verification codes are HMAC'd with a
    configured secret, and plaintext secrets, tokens, passwords and codes are never
    stored or logged.

30. **Password work is bounded by one shared capacity limit, not by a rate limit.**
    `security.PasswordWorkLimiter` caps how many derivations run at once across the
    whole process, and it lives **inside** the shared `PasswordHasher`, so `Hash`,
    `Verify` and `DummyVerify` are all bounded and a new call site cannot reach
    Argon2id without passing through the bound. One limiter and one hasher are
    built in `internal/api/auth/module.go` and shared; a limiter per feature would
    make the total the sum of the parts. `DummyVerify` is bounded too, and has to
    be: it costs the same derivation, so an exemption would be the cheapest way to
    exceed the limit.

    This is not `ratelimit.Limiter`. A rate limit bounds how often a subject
    submits, which composes across subjects and bounds nothing about what is
    running now. `Acquire` hands back a per-acquisition release closure rather than
    the limiter exposing an unowned `Release()`, because a release that ran twice
    would free a slot a live operation was relying on. Exhaustion is `503` with a
    bounded `Retry-After`: not invalid credentials, because the password was never
    checked, and not `429`, because no budget was spent.

31. **Email is normalized once, and every read path compares against the
    normalized form.** `registration.NormalizeEmail` trims, then lowercases, and
    registration stores that form. PostgreSQL's `=` on `TEXT` is case sensitive, so
    a path that compares the raw input would miss rows it should find.

32. **Email flows are non-enumerating, and the verification endpoints are
    unauthenticated on purpose.** Registration and password-reset verification
    carry no bearer token: the existing `verification_id` plus the emailed PIN is
    the credential.

33. **A `confirmation_id` is a flow identifier, never a bearer credential, and
    account linking is decided by provider identity.** The id travels in a URL
    path, and a path reaches access logs, browser history and `Referer` headers.
    `ConfirmAccountLink` must verify the Google ID token against
    `login.GoogleTokenVerifier` — Google verification stays behind that interface
    so tests can fake it — and match **both** `provider` and `provider_subject`
    against the confirmation row. Never match on an email address: it is not an
    identity, and doing so would accept a different account that happens to share
    one. Do not "simplify" the comparison to an email check or to a subject-only
    check; the provider column is data, not code.

34. **Linking is gated on mailbox control, not on the existing account's
    credential.** This matches password reset, which is unauthenticated and proves
    the same thing. Requiring the manual password would be stricter than the app's
    own recovery path, would break users who cannot recall it, and would create a
    phishing-shaped prompt. Do not add it as a "security improvement"; changing
    this is a deliberate change to the trust model.

35. **The confirmation's own transaction is the single-use authority for account
    linking.** Provider and subject are immutable (only `confirmed_at` is ever
    written), so the service may compare them outside the transaction, but the
    `FOR UPDATE` read and the `rowsAffected != 1` guard inside it are what make
    concurrent attempts produce exactly one link. Do not move, weaken or "optimize
    away" either. Registration's `CompleteVerification` is guarded redundantly in
    the same way.

36. **Manual login and Google authentication have separate budgets, and neither
    flow's may be reused by the other.** Manual login has `login-ip` (route
    middleware) and `login-email` (service); all three Google routes share
    `google-auth-ip` (route middleware). They are two doors into the same
    application: neither flow's traffic may lock the other out, and because a
    limiter that cannot answer refuses rather than allows, one namespace counting
    both would let a single exhausted counter take both doors down. Do not merge the namespaces.

37. **The Google budget is a ceiling on cost, not on credential guessing, and it
    carries no per-email budget.** The Google routes have no secret to try, so
    nothing there is about brute force. What the budget bounds is token
    verification and the session, confirmation and pending-row writes that a
    caller holding one valid token could otherwise repeat indefinitely. The email
    would come from a Google-verified claim the caller does not choose, so it could
    only ever be spent by the legitimate owner, which makes it a lockout rather
    than a protection. `verify-ip` and `finalize-ip` are budgets on cost for the same
    reason.

38. **Limiter thresholds are unmeasured engineering estimates.** That includes the
    values in `internal/api/auth/login/constants.go`. Do not treat them as
    validated, and do not lower one to match another without saying which
    shared-address false positive that trades away.

## Enrichment and the worker

39. **All outbound policy lives in `internal/security`, and the guarded client
    validates the resolved address.** `security.NewGuardedHTTPClient(policy)` is the
    only sanctioned way to fetch a user-supplied URL. Its
    `net.Dialer.ControlContext` validates the **resolved** address at dial time.
    Allowed schemes are `http` and `https`, and ports are restricted to 80 and 443. The
    blocked address ranges live in `internal/security`; do not duplicate or extend
    them, and do not add an address allow or deny list elsewhere. `Proxy` is nil on
    the guarded transport deliberately.

40. **`internal/enrichment` is handed its HTTP client and never builds one.** It
    receives its `*http.Client` by constructor injection and must never construct
    one, use `http.DefaultClient`, or call `http.Get`. That is what keeps every
    fetch on `security.NewGuardedHTTPClient`.

41. **Enrichment is scoped to the owner, except where a background task names the
    row.** Enrichment operates on one Saved Item at a time, scoped by
    `id AND user_id`, and a missing item and a foreign item must have the same
    not-found behavior. Background tasks are the documented exception: a task names
    the row it acts on, so the worker packages read by `id` alone and verify the
    owner against the payload rather than by scoping the read.

42. **A successful enrichment means the process ran, not that every field was
    found.** A page that exposes no metadata is a successful enrichment with an
    empty result. A failed enrichment is represented by `enrichment_status`, not by
    an HTTP 5xx, and a failed attempt preserves existing metadata and does not
    update `last_enriched_at`. Enrichment is repeatable and must not be gated on
    the current `enrichment_status`.

43. **Metadata URLs are resolved, never fetched.** A URL found in page metadata may
    be resolved to absolute form but is never fetched merely because it appears
    there.

44. **The queue package is the whole contract between the two binaries.**
    `internal/worker/queue` holds the task type, the queue name, the payload type,
    the task timeout and retry budget, and the producer the API enqueues through.
    It stays free of Fiber, `internal/api` and persistence, so the two sides cannot
    drift. `internal/api/saved_item` depends on its own one-method enqueuer
    interface rather than on the queue package, and the enqueuer may be nil.

45. **A task payload carries the saved item id only, and retry classification is
    read, not restated.** The worker reads the URL from the row, so a queued copy
    can never disagree with what was stored. Retry classification comes from
    `enrichment.Kind`: the worker maps that classification onto a queue decision
    and must not restate what a page error means. Retries are bounded by the
    queue's own budget and capped backoff. Permanent failures (terminal client
    statuses, non-HTML content, unparseable documents) are not retried; network
    failures, unreadable bodies, `429` and `5xx` are. Moving a failure between the
    two groups changes retry behavior and is a product decision.

46. **A Redis URL is never logged in full.** It may carry credentials in its
    userinfo section; log the parsed address and database instead.

47. **Automatic organization decides at execution time, against a locked row.**
    The payload says which item to look at, never where it goes;
    `enrichment_status`, `platform` and the current collection are all re-read.
    Only an item still in `Unsorted` may be moved, and being in `Unsorted` when
    enrichment finished grants nothing. That is what keeps organization from
    overriding a user's own filing, and it must not be relaxed to "organize unless
    the target differs".

48. **Organization reuses a matching collection, never renames one, and writes no
    enrichment state.** Whether the user named a collection or an earlier item
    created it, a match is a target. `system_key` is the platform's own normalized
    identity, derived from the platform value rather than from a curated mapping, a
    hostname or a domain table, which would reintroduce hostname-based inference of
    `platform`. There is no
    `organization_status` column, `MoveSavedItemToCollectionForOrganization`
    touches `collection_id` alone, and a failed organization leaves the item
    `completed`.

49. **Organization no-ops are successes, and an ownership mismatch is the one
    reported condition.** A missing item, an incomplete enrichment, a NULL
    platform and an item the user already filed are all finished tasks. An
    ownership mismatch is reported and archived, because it cannot happen through
    any real path and would otherwise hide a bug.

## Collections, ownership and listings

50. **Ownership is established by `id AND user_id`, never by a foreign key
    alone.** `collections.user_id` owns a collection and `saved_items.user_id` owns
    a Saved Item. The composite foreign key over `(collection_id, user_id)`
    (migration 000025) makes an item reference only a collection its own owner owns;
    do not weaken or work around it. Scope reads and writes by `id AND user_id`
    wherever a user is in scope: a `NOT NULL` foreign key on `collection_id` is not
    an ownership check. Among background work only automatic organization writes
    `collection_id`, and only for an item still in `Unsorted`; user-initiated moves
    go through the collection feature.

51. **The collection feature registers two route groups, and they stay separate.**
    Its saved-item-scoped endpoint mounts under the `/saved-items` prefix
    (`PUT /saved-items/:id/collection`) and its own resource mounts at
    `/collections` (`DELETE /collections/:id`). Fiber groups are additive, so
    neither registration touches the routes the other already registers.
    `DELETE /saved-items/:id` deletes a saved item and `DELETE /collections/:id`
    deletes a collection, and sharing one group would let one shadow the other.

52. **The saved-item delete reads its advisory values after the delete, with no
    transaction around them.** `collection_deletable` is "empty and not Unsorted":
    a convenience for the caller, not an authorization decision, because the
    collection delete re-reads the database and validates again. The count behind
    `collection_empty` is read after the delete and scoped by
    `collection_id AND user_id`. There is deliberately no transaction around the
    delete and its two reads: under READ COMMITTED each statement takes its own
    snapshot either way, and there is no state to keep consistent because nothing
    is being acted on. The values are advisory because the saved-item delete never
    deletes the collection itself.

53. **Collection deletion is one repository transaction and takes nothing
    `FOR UPDATE`.** It is owner-scoped by `id AND user_id`, and an unknown and a
    foreign collection produce the same not-found. The repository loads the
    source, refuses Unsorted, resolves the target when moving, deletes or moves the
    children, then deletes the collection. The move endpoint locks a saved item and
    then touches a collection, so locking the collection first here would invert
    that order and risk deadlock; emptiness is instead decided by the foreign key
    at the final `DELETE`, which is both race-free and cheaper. The request body
    keeps its two-field shape (`saved_items_action` and `target_collection_id`): both
    fields always exist and never
    change meaning, and an absent or contradictory action or target is a 400.

54. **`GET /collections` pins Unsorted by rank and excludes it NULL-safely.**
    Unsorted is pinned first under every sort by a rank over `system_key` in the
    `ORDER BY`: not filtered out and not special-cased per sort, so every ordering
    inherits the rule once, and it must never reappear on a later page. The pinned
    row is excluded with `system_key IS DISTINCT FROM 'unsorted'`, which is
    NULL-safe as well as selective: `collections_system_key_check` gives every
    `type = 'user'` collection a NULL key, so a plain `<>` evaluates to NULL for all
    of them and a resumed page would return system collections only.

55. **Every ordering needs a unique tie-breaker.** `created_at` defaults to
    `NOW()`, which is constant within a transaction, and migration 000022 inserts
    every user's Unsorted collection in one statement, so equal timestamps are a
    real state. `id` is `uuidv7()` with no overriding trigger, so it is unique and
    monotonic in creation order. The tie-break is not optional in any of the three
    listings.

56. **`name` orders by `lower(btrim(name))`, and every name comparison uses that
    same semantics.** It is the expression `collections_user_name_unique` is built
    on, so the ordering and the uniqueness constraint cannot disagree. A cursor's
    name value must be that same normalized form, never the raw display name.
    Names are compared and stored the way `lower(btrim(name))` and a trimmed
    display name already define; do not write a second normalization, because a
    collection whose stored name disagrees with the name the unique index matched
    cannot be found again.

57. **The collection count is computed in the page's own statement.** Both
    `saved_items_count` and `saved_items_count_overflow` come from a
    `LEFT JOIN LATERAL` correlated on the collection's own id and owner: never a
    second statement, never a pre-aggregated derived table over all of
    `saved_items`, and never one query per collection. An empty collection is
    reported rather than dropped, with `0` rather than null. `LEFT` and `COALESCE`
    are defensive rather than load-bearing: an ungrouped aggregate always returns
    exactly one row, so replacing either with its inner or raw form was measured
    and every test still passed. They are kept so a future grouping key cannot
    silently produce a null count or drop an empty collection. The observable
    contract is what the tests hold in place.

58. **The three cursor listings share one envelope and one set of mechanics.**
    `internal/pagination` owns the envelope only: JSON over
    `base64.RawURLEncoding`, plus a payload version. Cursor payload structs stay
    with their feature, because what a page must record differs per listing and
    one shared struct with optional fields would be a second source of truth.
    Payload fields that can be absent must be pointers: a plain `int` group decodes
    an omitted field to 0, which is itself a real group value, so a truncated
    cursor would read as valid and silently skip the pinned collection.
    `httpx.CursorPage.NextCursor` carries no `omitempty`: a nil pointer tagged that
    way is dropped from the JSON, and a client must be able to read `next_cursor`
    and find `null` rather than a missing key. `Meta.Cursor` is a pointer with
    `omitempty`, so endpoints that do not paginate by cursor leave the key out and
    their responses are unchanged.

59. **A page fetches `limit + 1` rows, and keyset predicates are cast explicitly.**
    The cursor is built from the last **returned** row, never the lookahead row:
    the lookahead row was never sent, so a cursor built from it resumes past a
    collection the client never received. Keyset predicates need explicit casts on
    row-comparison parameters, because without them sqlc types every parameter from
    the row's leftmost element, which would give a `timestamptz` type to an id
    parameter.

60. **The service validates and parses a cursor's position before any query
    runs.** A position arrives as a string, so the time-based sorts must check it
    really is a timestamp and record the parsed `pgtype.Timestamptz` on the
    cursor. The repository then passes the value on without reinterpreting it, so
    a caller-supplied token can never turn into a server fault: an unusable one is
    `INVALID_CURSOR`, and only a real driver error is `INTERNAL_SERVER_ERROR`. A
    derived field must be `json:"-"`, so it cannot become a second copy of the
    position that disagrees with the wire value.

61. **The two item listings carry no `Sort` and no `Group` in their cursors, and
    use two queries.** `ListSavedItemsInCollectionCursor` and
    `ListSavedItemsCursor` omit both. They exist on the collection payload only
    because that listing pins Unsorted and offers three orders, and carrying fields
    that can never vary would mean writing validation branches that cannot be
    reached. One ordering and no pinned row means two queries, a first-page
    statement and an after-position statement, not six. A test asserts that the
    Inbox payload's JSON keys do not contain them.

62. **The item listings have one fixed order, prove ownership first, and never
    write.** `GET /collections/:id/saved-items` and `GET /saved-items` are both
    ordered `created_at DESC, id DESC` with no sort option. A collection is
    something a person curated, so offering orders would answer a question that
    resource does not pose (`GET /collections` is where ordering is a choice), and a
    history is read newest first or not at all. The per-collection listing proves
    ownership before any item is read, by resolving the collection with
    `id AND user_id`; without that it would be scoped by collection id alone. The
    Inbox is scoped to `si.user_id` taken from the token, never from the request.
    A null title and a failed enrichment are ordinary states in both: they are
    listed, `enrichment_status` is never null, and the metadata columns are
    pointers in the response. Neither listing triggers enrichment or organization
    or writes anything, and the Inbox service holds no enqueuer.

63. **The Inbox has no rate limiter, and its page limits are its own.** It is one
    indexed read that holds nothing beyond the request, like `GET /saved-items/:id`
    and `DELETE /saved-items/:id`. The two saved-item routes that are limited stay
    limited, because a save arranges unbounded downstream work and
    `POST /saved-items/:id/enrich` performs an outbound fetch in the API process.
    `SavedItemListDefaultLimit` and its `MaxLimit` counterpart are declared in
    `saved_item` rather than borrowed from `internal/api/collection`, which has its
    own identical pair for a page of one collection's items. The features do not
    import one another; that the values match today is the intended consistency,
    not a shared constant.

64. **A Saved Item's create and detail responses are deliberately different
    shapes.** Do not unify them into one DTO. `POST /saved-items` reports `id`,
    `url`, `collection_id` and `enrichment_status` only. A save commits before
    enrichment has run, so `domain` is the only metadata column carrying a value,
    and it was derived locally from the submitted URL rather than read off the
    page; reporting the rest would be unlooked-up nulls presented as metadata,
    which reads as "this page has nothing" rather than "this page has not been read
    yet". `enrichment_status` is included because the `INSERT` returns it, which
    costs no additional query, and reporting the stored value beats reporting a
    constant this code assumes.

    `GET /saved-items/:id` reports the complete saved item, the same fields as
    `GET /collections/:id/saved-items`, plus a `collection` object carrying `id` and
    `name`. Without `enrichment_status` a null title is ambiguous between "no
    title" and "not read yet". `type` and `system_key` stay out; they belong to
    `GET /collections`. `GetSavedItemByIDForUser` joins `collections` in the same
    statement, owner-scoped on both sides, so naming the collection costs no extra
    query; the join is `INNER` because `collection_id` is `NOT NULL` and references
    an existing row. `saved_item.collection_id` always equals `collection.id`, and
    the redundancy makes the item self-describing once it leaves the response. The
    Inbox joins the same way, and no collection field is added to it without a
    demonstrated need.
