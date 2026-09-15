# Shimo browser worker

This internal CatsCo service owns interactive Shimo login and read-only browser sessions. It never receives a CatsCo user ID, agent ID, task message, password, verification code, or actor token. CatsCo identifies a session with an opaque HMAC binding.

## Runtime flow

1. CatsCo calls `POST /v1/sessions/login` with an opaque `connection_binding`.
2. The Worker starts a temporary Chromium context and returns a random `/shimo-login/{token}/` URL.
3. The user operates the isolated Shimo page directly in a live canvas. A same-origin WebSocket streams frames and forwards pointer (including double-click), keyboard, paste, Chinese IME, and wheel input; recoverable input validation errors are shown inline without ending the login attempt. The older HTTP screenshot endpoints remain as a connection fallback. QR login works by opening the WeChat option and scanning the displayed code.
4. The Worker detects `https://shimo.im/lizard-api/users/me`, exports Playwright storage state, encrypts it with AES-256-GCM, and closes the login browser.
5. Every read creates a fresh headless browser context from that user's encrypted state.

The login URL is a bearer secret. It expires after ten minutes, is excluded from referrers and caches, and must never be logged by reverse proxies.

## Sheet reads are chunked server-side

Shimo's sheet values endpoint rejects any request covering more than 5000 cells
(`400 {"error":"限制最多获取 5000 个单元格的数据"}`), and it counts the requested
range rather than the populated rows — so `A1:Z1000` (26000 cells) can never be read
in one call. `POST /v1/sheets/read` therefore splits the requested range into row
bands of at most 4000 cells (80% of the hard cap), reads them sequentially inside one
browser context, and concatenates the rows. A range narrower than the budget still
issues exactly one upstream request.

Response fields on `/v1/sheets/read`:

| Field | Meaning |
|---|---|
| `values` | Rows concatenated across bands, in sheet order |
| `requested_range` | The A1 range the caller asked for |
| `requests` | How many upstream requests were issued, including a band that timed out |
| `covered_through_row` | End row of the last band the Worker asked Shimo for |
| `stopped_early` | Always `false`; kept so existing callers keep working |
| `truncated` | The requested range was **not** fully covered (band cap or time budget) |

Shimo trims leading and trailing empty rows and columns from every response, so an
empty band is **not** evidence that the sheet has no data after it. The Worker reads
every planned band instead of stopping at empty bands; all-blank rows at a band
boundary can still be dropped, so callers must not treat `values` as row-indexed.

A single band never exceeds the 5000-cell hard cap: ranges of 4001~5000 columns are read
one row per upstream request, and only a range whose *single row* still exceeds 5000
cells (`A1:ZZZ1`) is rejected with `RANGE_TOO_LARGE` instead of firing requests that are
guaranteed to fail.

Reads are bounded in time as well as in band count: the whole call gets a 65s budget
that starts when the Worker receives the request — including any wait in the concurrency
queue (2 concurrent reads, 8 queued by default) — and each band request receives the
remaining part of that budget (capped at 30s) as its Playwright request timeout, so a
hanging request is aborted instead of holding a Worker concurrency slot past the
server's own 75s timeout. Queue wait shortens a queued call's own read window instead
of extending it past the server timeout.
A band that times out after rows were already read returns those rows with
`truncated: true`; a timeout before the first row is reported as an error, never as an
empty sheet.

## Configuration

```text
SHIMO_WORKER_TOKEN=<at-least-32-random-characters>
SHIMO_WORKER_SESSION_KEY=<64-hex-characters-or-base64-for-exactly-32-bytes>
SHIMO_WORKER_PUBLIC_BASE_URL=https://app.catsco.cc/shimo-login
SHIMO_WORKER_STATE_DIR=/var/lib/catsco-shimo
SHIMO_WORKER_MAX_CONCURRENCY=2
SHIMO_WORKER_MAX_QUEUE=8
CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium
```

`SHIMO_WORKER_MAX_CONCURRENCY` bounds how many Chromium sessions run at once and
`SHIMO_WORKER_MAX_QUEUE` bounds how many further requests wait for a slot. Over
that bound the Worker answers `503 WORKER_BUSY` instead of starting another
browser, which keeps a burst of reads from exhausting container memory. The
compose stacks also cap the container (`deploy.resources.limits`); raise both
together.

The internal API listens on port `7070`. Only `/shimo-login/` should be exposed through the CatsCo HTTPS origin. Its reverse proxy must forward WebSocket upgrades and must not buffer or access-log the bearer-token path. `/v1/` must remain on the private Docker network and requires `SHIMO_WORKER_TOKEN`.

## Checks

Domestic test and production builds via `deploy/remote-build-source.sh` use
USTC for both Debian Bookworm and security packages. Override
`REMOTE_DEBIAN_MIRROR` and `REMOTE_DEBIAN_SECURITY_MIRROR` when needed.
Standalone Docker builds retain Debian's official sources unless the
`DEBIAN_MIRROR` / `DEBIAN_SECURITY_MIRROR` build arguments are supplied.
HTTP supports bootstrapping `ca-certificates` in the slim base image;
Debian's archive keyring, signature checks, suites and package list are unchanged.
APT retries transient failures three times, with a 30-second connection timeout,
and fails the build if any required repository index cannot be fetched.

Mirror documentation: https://mirrors.ustc.edu.cn/help/debian.html

```bash
npm ci
npm test
docker build -t cats-company-shimo-worker .
```

The unit suite verifies service authentication, identity-field rejection, per-binding isolation, encrypted-at-rest state, authenticated-encryption binding, disconnect behavior, the global concurrency bound, and the live login stream's input and origin checks. A real smoke test additionally needs an authorized Shimo test account.
