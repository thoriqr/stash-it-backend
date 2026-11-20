# Operator Page

Where reports filed through `POST /feedback` are read, triaged and removed.

The API contract for filing a report is in `docs/swagger.yaml` and is not repeated here.
This document covers the other half of the feature: the page that reads what users send.

## Why it is not an API route

The page is **not** part of the public API, and it has no entry in Swagger.

That is a decision about the surface, not about convenience. A route on the API's Fiber
app travels through Caddy and answers on `api.stashit.thoriqr.my.id` — so it would be
reachable by anyone on the internet, and the only thing between them and every user's
email address would be one shared secret. A maintenance route guarded by
`X-Release-Secret` is fine for publishing a release, because the secret protects an
action nobody else wants; reading and deleting feedback is different.

Instead the page is a **second Fiber app on its own listener**, wired in
`cmd/api/main.go` and built in `internal/operator`. It is bound to `127.0.0.1` on the
VPS and reached over an SSH tunnel.

Three properties hold the surface closed, and removing any one of them breaks it:

1. **No route on the public app.** Nothing for Caddy to forward.
2. **Bound to loopback.** Not reachable from the network, so no password is needed to
   reach it — SSH already proved who the caller is.
3. **`Origin` checked on every write.** A browser on the same machine can be made to
   POST to it by any site the operator visits; `Origin` is what stops that, and it is a
   forbidden header name for JavaScript, so a hostile page cannot forge it.

The optional secret (`OPERATOR_PAGE_SECRET`) is a fourth, for the case where the page is
bound somewhere reachable by mistake.

## Configuration

```env
# Empty disables the page entirely. There is no default: a deployment that has not asked
# for a page should not have a listener for it.
OPERATOR_PAGE_ADDR=127.0.0.1:8081

# Optional. Empty skips the check, which is the ordinary case: on the VPS the page is
# reached over an SSH tunnel and SSH is the authentication.
OPERATOR_PAGE_SECRET=
```

Two addresses are values you will actually use:

| Environment | `OPERATOR_PAGE_ADDR` | How to reach it |
| --- | --- | --- |
| VPS | `127.0.0.1:8081` | `ssh -L 8081:127.0.0.1:8081 <user>@<vps>` then `http://127.0.0.1:8081` |
| Local | `127.0.0.1:8081` | `http://127.0.0.1:8081` in a browser |

The tunnel command opens no port on the VPS: sshd accepts the connection on port 22,
which is already open, and then connects to `8081` **from the machine itself**. The
firewall never sees 8081, and there is nothing to open or close afterwards.

Binding to `0.0.0.0` is not a supported shape. A listener on all interfaces is reachable
by anything that can route to the machine, and pointing Caddy at it would publish it.

## What the page does

| Action | Route | Effect |
| --- | --- | --- |
| Read | `GET /?page=N&limit=N&status=<status>` | one page of reports, newest first |
| Triage | `POST /feedback/{id}/status` | sets `status` |
| Remove | `POST /feedback/{id}/delete` | deletes the row |

The writes are HTML form `POST`s that redirect back to the list with `303 See Other`.
There is no `PUT` or `DELETE`: an HTML form supports neither, and a method override would
add a layer without adding anything. `303` is what stops a browser re-posting when the
operator refreshes.

Every form carries `page` and `status_filter` as hidden fields, so triaging a report on
page 3 returns to page 3 of the same filter rather than to the top of the list.

## The summary strip

Above the list, and deliberately outside it.

**Registered users** is one card: rows in `users`, not active sessions. "How many people
have signed up" is a question this table answers; "how many are online" is not, and
activity lives in `sessions`, which this read does not join.

**One card per published platform.** Each card states two facts, separately:

```
android
Latest published   1.3.1 (build 7)   19 Nov 2026
                   android-v1.3.1-7   46.0MB
                   https://downloads…/stash-it-1.3.1-7.apk
────
Min supported      1.3.0 (build 5)
```

- **Latest published** is what a client is being offered, read from the channel's
  `latest_release_id`. Its git tag, artifact size and download URL sit with it, because
  together they are how an operator confirms that what is published is what they meant to
  publish.
- **Min supported** is the oldest build still accepted, read from
  `min_supported_release_id`. It has no artifact beside it: it is a floor rather than an
  offer, so there is nothing to download.

They are shown as two facts rather than combined into a verdict. An earlier version of
this strip tried to summarise them — comparing the two builds, then counting builds below
the minimum — and both were wrong in different ways: the comparison read as reassuring
exactly when the floor had been moved onto the newest build, and the count answered a
question nobody was asking. What an operator checks is "which build is offered" and "what
is the floor", one after the other.

When the floor *is* the newest build, the card says `every older build is refused`. That is
the only statement made about the pair, because it is the one consequence of the two
values that is easy to miss: publishing can move the floor onto itself, and a card showing
two identical versions does not read as "everyone must update".

A platform nothing has been published for has no channel row, so no card appears for it.
The strip says `Nothing published yet` rather than showing a version it invented.

## What a row shows

- the message, verbatim, with line breaks preserved
- `type` and `status`
- the owner's display name and email
- the client context: `android · 1.3.1 (7)`
- a relative age, with the exact timestamp on hover

The email is read by joining `users`, so it is the account as it is now rather than a
copy taken when the report was written.

The full address is shown, not a masked one. This page exists so the operator can check
whether a bug is reproducible for the person who reported it.

### Filtering and paging

`page`/`limit` rather than a cursor. The page is read by one person who wants numbered
pages, and the total it needs is already being counted for the header and the chips, so
keyset pagination would add a mechanism without removing any work.

The chips show the count behind each status, read in one statement so the three numbers
cannot disagree with each other. Filtering narrows the list but not the counts: the other
statuses stay visible, which is the point of a chip row.

`closed` is not hidden by default. The chip row makes it one click away, and a list that
silently omitted rows would be a worse answer to "what did I receive" than a long one.

## The lifecycle

```
new ──────→ planned ──────→ closed
 └──────────────────────────┘
     (closed directly when there is nothing to do)
       closed ──→ new  (reopen, for a mistake)
```

| Status | Meaning | Buttons offered |
| --- | --- | --- |
| `new` | recorded, not yet looked at | Accept as planned · Close · Delete |
| `planned` | accepted as worth doing | Close · Delete |
| `closed` | nothing further will be done | Reopen · Delete |

There is no `shipped` and no `rejected`. Both end in `closed`, because nothing the
operator does afterwards differs between them. A decision worth remembering — such as why
a host is not scraped — belongs in `decisions.md`, not in a status.

`planned` is the backlog:

```sql
SELECT id, type, message, created_at
FROM feedback
WHERE status = 'planned'
ORDER BY created_at DESC;
```

There is no separate todo table. A second table would mean two rows claiming the same
fact, and every operation would have to update both or let them disagree.

## Closing versus deleting

**Closing is what happens to a report that was handled. Deleting is for what was never a
report.**

- a bug that was fixed: `closed`
- a feature decided against: `closed`
- `asdfasdf`, a test message, an accident: delete

Deleting drops the row permanently. On the order of dozens of reports a year, keeping
handled ones costs nothing and is the only record of who asked for what.

## It reads the database directly

The page queries the table through its own `sqlc` target (`internal/operator/queries.sql`)
rather than calling the API over HTTP.

That is not a shortcut around the layers. It is the same repository → service → handler
structure; only the handler renders HTML instead of JSON. The reason is availability: a
page that called `POST /feedback` or any other route over HTTP would need a token for an
outbound call, would sit behind the Redis-backed rate limiter, and would stop working
exactly when the API is what needs diagnosing.

The feature and the page are separate packages that do not import one another, which is
the same rule the rest of the project follows. They each state what they need from the
`feedback` table.

## Replying to a user

There is no reply field and no reply mechanism. A reply needs somewhere to be read, and
the client has no surface for one; the form does not promise one either.

When a reply is genuinely needed, the address is the one already on the account:
`feedback.user_id` joins to `users`, which carries a verified `email`, and
`internal/email` can send to it. What is not recorded is *that* a reply was sent, so
adding replies later needs a `replied_at` column rather than a message thread.

## Verifying it

The page is covered end to end in `internal/integration/operator_page_test.go`, which
drives its own Fiber app without binding a port:

- it reads reports the API wrote, with owner, context and status
- filtering narrows the list while the counts still describe the whole queue
- a status change and a delete land, and a delete of something gone is reported
- an unknown status is refused and leaves the row alone
- a write from another origin, or with no `Origin`, is refused
- a write from the page's own origin **with a port** is accepted, which is the case a
  browser actually produces and the one an implementation comparing hostnames gets wrong
- a configured secret is required for every request
- a message containing markup is escaped
- the summary reports users and releases, and does not move when the list is paged or
  filtered
- a forced publish is reported as refusing the older builds, not as "nothing forced"
- the public API does not serve these paths

`internal/operator/template_test.go` covers the rendering itself, including the lifecycle
each status offers, the refusal count and its singular/plural wording, and the fact that a
report with no client context renders no context line.

`internal/operator/error_handler_test.go` covers what an unhandled request does: a 404 stays
a 404 and is not logged, while a 5xx is logged at error level.

`internal/operator/shutdown_test.go` covers stopping the listener with a browser's
keepalive connection still open.

## Schema

`migrations/000033_create_feedback.{up,down}.sql`. The columns and their reasoning are in
the migration's comments; the short version is that `type` and `status` are closed
vocabularies the system owns and carry `CHECK` constraints, while the message and the
client context are input that the request validator and the service check rather than the
schema.

`user_id` has no index. The page joins from `feedback` to `users.id`, which is already a
primary key, so an index on the foreign key would not be used. It would only matter for
"all reports by this user", which is not a view that exists.
