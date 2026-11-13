# Client Releases

How Stash-it tells an installed Windows or Android client that a newer build
exists. The database is the source of truth: publishing a client release does not
require a backend rebuild or restart.

## Status

Implemented:

- `migrations/000032_create_client_releases.{up,down}.sql` (proposal; **not applied
  automatically**)
- `GET /client-releases/latest`
- the maintenance routes under `/internal/maintenance/client-releases`, guarded by
  `X-Release-Secret`
- unit tests in `internal/api/client_release`
- integration tests in `internal/integration/client_release_api_test.go` and
  `client_release_admin_api_test.go`
- Swagger annotations and generated `docs/`

Not implemented yet:

- the `release` CLI
- R2 upload tooling

The maintenance routes are the supported publish path. The SQL in this document
is a manual fallback.

## Endpoint

```http
GET /client-releases/latest?platform=android&build=4
```

Public, no authentication, no version prefix. The handler sets
`Cache-Control: no-store`; the decision can change with a single policy update.

| Parameter | Type | Required | Rules |
| --- | --- | --- | --- |
| `platform` | string | yes | `android` or `windows`; matches Flutter's `Platform.operatingSystem` |
| `build` | integer | yes | positive; already parsed from `PackageInfo.buildNumber` |

### Response

```json
{
  "data": {
    "platform": "android",
    "status": "optional",
    "current_build": 4,
    "min_supported": {
      "version": "1.3.0",
      "build": 3
    },
    "latest": {
      "version": "1.5.0",
      "build": 6,
      "download_url": "https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk",
      "sha256": "9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",
      "file_size_bytes": 48231001,
      "release_notes": "- Perbaikan sinkronisasi koleksi",
      "published_at": "2026-11-01T10:00:00Z"
    }
  },
  "message": "client release check completed successfully"
}
```

`status` is authoritative and computed by the server:

| Condition | `status` | `latest` | `min_supported` |
| --- | --- | --- | --- |
| No channel for the platform | `none` | `null` | `null` |
| `current < min_supported.build` | `required` | present | present |
| `min_supported.build <= current < latest.build` | `optional` | present | present |
| `current >= latest.build` | `none` | present | present |

`latest` stays present when `status` is `none` and a channel exists, including
when the client is ahead of the published release. That is informational, not a
downgrade instruction.

There is no `meta` key. `httpx.OK` emits `{data, message}`; `meta` is only used
by paginated endpoints through `OKWithMeta`.

### Errors

```json
{
  "error": {
    "code": "INVALID_PLATFORM",
    "message": "platform must be android or windows",
    "fields": []
  }
}
```

| HTTP | Code | When |
| --- | --- | --- |
| 400 | `INVALID_PLATFORM` | `platform` is absent, blank or not supported |
| 400 | `INVALID_CLIENT_BUILD` | `build` is absent, not an integer or not positive |
| 500 | `INTERNAL_SERVER_ERROR` | database failure or an internal fault |

A valid platform with no channel is `200` with `status: none`, never `404`. A
deployment that has not published anything yet is an ordinary state.

## Maintenance API

The maintenance routes are operator routes, not user routes. They require:

```http
X-Release-Secret: <CLIENT_RELEASE_SECRET>
```

and are mounted under `/internal/maintenance`, the same prefix as session
cleanup. `CLIENT_RELEASE_SECRET` is optional: when it is empty the routes stay
registered and refuse every request with `401 INVALID_RELEASE_SECRET`. There is no
rate limiter on them deliberately; the existing limiter fails closed when Redis is
unreachable, and this is exactly the surface an operator needs while something
else is broken.

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/internal/maintenance/client-releases` | Publish a release and move `latest`; `force` also moves `min_supported` |
| `POST` | `/internal/maintenance/client-releases/force` | Set `min_supported` to an existing build |
| `POST` | `/internal/maintenance/client-releases/rollback` | Move `latest` back to an existing build |
| `GET` | `/internal/maintenance/client-releases?platform=android` | List releases newest first with the current policy |

Publish rules:

- the build must be above the current latest; a publish never moves backwards;
- the first release for a platform becomes both latest and minimum;
- `force: false` (the default) leaves `min_supported` unchanged;
- `force: true` makes the new release the minimum as well;
- a duplicate `(platform, build)`, `(platform, version)` or `download_url` is
  `409 CLIENT_RELEASE_ALREADY_EXISTS`;
- a policy conflict is `409 INVALID_RELEASE_POLICY`.

Force and rollback address a release by `platform` + `build`. Force refuses a
build above the current latest. Rollback refuses a target above the current latest
and lowers the minimum only when it would otherwise sit above the new latest.

Example publish:

```sh
curl -X POST https://api.stashit.thoriqr.my.id/internal/maintenance/client-releases \
  -H "X-Release-Secret: $CLIENT_RELEASE_SECRET" \
  -H "Content-Type: application/json" \
  -d '{
    "platform": "android",
    "version": "1.5.0",
    "build": 6,
    "download_url": "https://downloads.stashit.thoriqr.my.id/android/stash-it-1.5.0-6.apk",
    "sha256": "9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",
    "file_size_bytes": 48231001,
    "release_notes": "- Perbaikan sinkronisasi koleksi",
    "git_tag": "client-v1.5.0-6",
    "force": false
  }'
```

Force and rollback:

```sh
curl -X POST https://api.stashit.thoriqr.my.id/internal/maintenance/client-releases/force \
  -H "X-Release-Secret: $CLIENT_RELEASE_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"platform":"android","build":6}'

curl -X POST https://api.stashit.thoriqr.my.id/internal/maintenance/client-releases/rollback \
  -H "X-Release-Secret: $CLIENT_RELEASE_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"platform":"android","build":5}'
```

Admin error codes:

| HTTP | Code | When |
| --- | --- | --- |
| 401 | `INVALID_RELEASE_SECRET` | missing or wrong secret, or no secret configured |
| 400 | `INVALID_PLATFORM` | unsupported platform |
| 400 | `INVALID_RELEASE_METADATA` | invalid version, build, URL, digest or size |
| 404 | `RELEASE_NOT_FOUND` | force/rollback target or channel not found |
| 409 | `CLIENT_RELEASE_ALREADY_EXISTS` | duplicate release or URL |
| 409 | `INVALID_RELEASE_POLICY` | publish/force/rollback would break the bounds |
| 500 | `INTERNAL_SERVER_ERROR` | database or internal fault |

## Tables

`client_releases` is one immutable artifact per platform and build:

- `platform`, `version` (semver, without `+build`), `build` (the comparison key)
- `download_url`, `sha256`, `file_size_bytes`, `release_notes`, `git_tag`
- `published_at`, `updated_at`

`client_release_channels` is the mutable policy per platform:

- `latest_release_id` is the release currently offered.
- `min_supported_release_id` is the oldest build still allowed. It is a pointer
  to a release, so it always names a real artifact and carries a version to
  display.
- Composite foreign keys over `(release_id, platform)` prevent a Windows channel
  from pointing at an Android release.

`min_supported.build` must not exceed `latest.build`. The maintenance routes keep
that true; the public endpoint also clamps and logs a misconfiguration rather
than sending a forced update to a build with no newer artifact.

Version strings are display values only. Every comparison is on `build` integers:
`"1.10.0"` sorts below `"1.9.0"` lexicographically.

## Publishing a release

The sequence is: **build → sign → tag → upload → verify the served bytes →
publish the row**. Never publish a row before the file exists at its URL.

1. Bump `pubspec.yaml` (for example `1.1.0+2`). Keep build numbers strictly
   increasing; never reuse or overwrite a published build.
2. Commit and tag the client commit: `client-v1.1.0-2`.
3. Build and sign the APK and the Windows artifact. Flutter Windows produces a
   folder (`stash-it.exe`, DLLs and `data/`), so upload a ZIP or an installer,
   not the bare `.exe`.
4. Verify the embedded version:
   - Android: `aapt dump badging app-release.apk` (`versionCode`, `versionName`)
   - Windows: run the app and check Settings (`PackageInfo.version` and
     `buildNumber`)
5. Sign first, then hash the final bytes:
   `sha256sum app-release.apk` (or `Get-FileHash -Algorithm SHA256`).
6. Upload to R2 at the immutable key, for example
   `android/stash-it-1.1.0-2.apk` or `windows/stash-it-1.1.0-2.zip`.
7. Download the public URL and hash the served bytes; it must equal the local
   hash.
8. Publish through the maintenance route:

   ```sh
   curl -X POST https://api.stashit.thoriqr.my.id/internal/maintenance/client-releases \
     -H "X-Release-Secret: $CLIENT_RELEASE_SECRET" \
     -H "Content-Type: application/json" \
     -d '{
       "platform": "android",
       "version": "1.1.0",
       "build": 2,
       "download_url": "https://downloads.stashit.thoriqr.my.id/android/stash-it-1.1.0-2.apk",
       "sha256": "9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",
       "file_size_bytes": 48231001,
       "release_notes": "- Perbaikan sinkronisasi koleksi",
       "git_tag": "client-v1.1.0-2",
       "force": false
     }'
   ```

   The route inserts the row and moves `latest` in one transaction. `force` is
   false by default. Use the SQL below only when the route cannot be called.

### Manual SQL fallback

```sql
BEGIN;

WITH new_release AS (
    INSERT INTO client_releases (
        platform,
        version,
        build,
        download_url,
        sha256,
        file_size_bytes,
        release_notes,
        git_tag
    )
    VALUES (
        'android',
        '1.1.0',
        2,
        'https://downloads.stashit.thoriqr.my.id/android/stash-it-1.1.0-2.apk',
        '9f2c4a7b1e8d3f605a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef',
        48231001,
        E'- Perbaikan sinkronisasi koleksi',
        'client-v1.1.0-2'
    )
    RETURNING id, platform
)
INSERT INTO client_release_channels (
    platform,
    latest_release_id,
    min_supported_release_id
)
SELECT
    platform,
    id,
    id
FROM new_release
ON CONFLICT (platform) DO UPDATE
SET latest_release_id = EXCLUDED.latest_release_id;

COMMIT;
```

The `ON CONFLICT` updates only `latest`. A new release is optional by default;
`min_supported` changes only when you deliberately force an update.

9. Verify the invariant returns no rows:

```sql
SELECT
    c.platform,
    latest.build AS latest_build,
    minimum.build AS min_supported_build
FROM client_release_channels AS c
JOIN client_releases AS latest
  ON latest.id = c.latest_release_id
JOIN client_releases AS minimum
  ON minimum.id = c.min_supported_release_id
WHERE minimum.build > latest.build;
```

### Forcing an update

Only after the new build has been verified:

```sh
curl -X POST https://api.stashit.thoriqr.my.id/internal/maintenance/client-releases/force \
  -H "X-Release-Secret: $CLIENT_RELEASE_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"platform":"android","build":2}'
```

The target must not be above the current latest.

### Rolling back a bad release

Move `latest` back to the previous good build. The artifact stays in R2; fix
forward with a new build rather than overwriting it.

```sh
curl -X POST https://api.stashit.thoriqr.my.id/internal/maintenance/client-releases/rollback \
  -H "X-Release-Secret: $CLIENT_RELEASE_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"platform":"android","build":1}'
```

The minimum is lowered only when it would otherwise be above the new latest.

## R2 layout and security

```text
stash-it-releases/
├── android/
│   └── stash-it-1.1.0-2.apk
└── windows/
    └── stash-it-1.1.0-2.zip
```

- Use the custom domain `downloads.stashit.thoriqr.my.id`, not `r2.dev`.
- The public custom domain is read-only: it serves GET and HEAD only. Random
  visitors cannot overwrite objects; writing requires an R2 API token.
- The write token exists only on the operator's machine or CI. The backend has no
  R2 credentials, and the client has none.
- Never overwrite an existing key. Keys include the build so a rebuild cannot
  collide.
- Set `Cache-Control: public, max-age=31536000, immutable` on objects. If the
  upload UI cannot set it, use a Cloudflare Cache Rule for `android/*` and
  `windows/*`.
- `sha256` is computed from the final signed bytes, stored lowercase in the
  database, returned in the response, and verified by the client before
  installing. Do not use the S3 ETag as the checksum.

`sha256` protects against a file being replaced at its URL. It does not protect
against a forged manifest from a compromised backend or DNS; that would need a
signed manifest, which is deliberately deferred. HTTPS and a client-side
allowlist of the download host are the current boundary.

## Client contract

- Parse `PackageInfo.buildNumber` to an integer. Do not fall back to zero; if it
  cannot be parsed, skip the check and continue.
- Send `platform = Platform.operatingSystem` and `build`.
- Use `status` only. Never compare version strings in the client.
- `required` is a blocking dialog; `optional` is dismissible; `none` does
  nothing.
- Download, hash the exact bytes, and compare with `sha256` before handing the
  file to the installer. Delete the file on mismatch.
- Reject a `download_url` whose host is not `downloads.stashit.thoriqr.my.id`.
- Fail open on network errors, 5xx responses or invalid JSON. The app stays
  usable.
- Check on launch and resume. Do not cache a `none` decision for days.
- "Required" is a UI block, not enforcement: direct APK/EXE distribution always
  needs the user's approval. Keep the old API available longer than adoption
  alone would suggest.

## Deliberately deferred

- Play Store distribution and Play In-App Updates.
- Staged rollout or percentage rollout, and A/B per version.
- An admin UI.
- A signed manifest.
- Delta/patch updates.
- Version analytics beyond the request logs.
- Signed download URLs. They gate who may download, not whether a file was
  replaced, and they would require R2 credentials in the backend.
