# CatsCo streaming STT

CatsCo exposes a dedicated backend WebSocket for browser voice input. The
browser captures microphone audio, converts it to 16 kHz mono PCM16LE, and
streams 100 ms frames to CatsCo. CatsCo forwards those frames to Volcengine and
returns normalized `ready`, `partial`, `final`, and `error` events.

Audio and partial transcripts are memory-only. They are not written to
`/uploads`, message history, or the database. Only the final transcript is
inserted into the browser composer draft.

## Provider

The server uses the `STTProvider` interface so another provider can be added
without changing the browser protocol. The only implemented and accepted
provider is currently:

```text
volcengine-doubao-streaming-v2
```

It uses Volcengine's optimized bidirectional streaming endpoint:

```text
wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_async
```

The `bigmodel` path is the current protocol name shared by the product family.
Doubao streaming speech recognition 2.0 is selected by resource ID
`volc.seedasr.sauc.duration` (hourly billing) or
`volc.seedasr.sauc.concurrent` (concurrency billing). CatsCo does not implement
an automatic fallback to the 1.0 resource IDs.

## Configuration

```dotenv
CATSCO_STT_ENABLED=1
CATSCO_STT_PROVIDER=volcengine-doubao-streaming-v2

VOLCENGINE_STT_WS_URL=wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_async
VOLCENGINE_STT_API_KEY=<api-key-from-new-speech-console>
VOLCENGINE_STT_RESOURCE_ID=volc.seedasr.sauc.duration
```

The credentials remain on the server. An authenticated browser first calls
`POST /api/stt/sessions` and receives a one-use, short-lived signed ticket. The
ticket is then used only for `GET /api/stt/realtime?ticket=...`.

## Limits

Defaults can be overridden through environment variables:

| Setting | Default | Meaning |
|---|---:|---|
| `CATSCO_STT_TICKET_TTL_SECONDS` | 45 | Ticket validity before WebSocket upgrade |
| `CATSCO_STT_MAX_SESSION_SECONDS` | 90 | Maximum audio duration per session |
| `CATSCO_STT_IDLE_TIMEOUT_MS` | 15000 | Stop after this much time without voiced PCM |
| `CATSCO_STT_MAX_CONCURRENT` | 40 | Active sessions per server instance |
| `CATSCO_STT_MAX_HOURLY_SECONDS` | 600 | Audio seconds per user in a rolling hour |
| `CATSCO_STT_MAX_DAILY_SECONDS` | 3600 | Audio seconds per user in rolling 24 hours |
| `CATSCO_STT_CONNECT_TIMEOUT_MS` | 2000 | Volcengine WebSocket handshake timeout |
| `CATSCO_STT_FINAL_TIMEOUT_MS` | 1200 | Wait for the final result after stop |

Only one active session is permitted per user. Browser and server audio queues
are capped at 160 KB. A hidden page or suspended audio context stops capture.

The current concurrency and usage counters are process-local. Before running
multiple CatsCo server replicas, move ticket replay protection and quota state
to the configured Redis runtime so limits remain global across replicas.

## Operational signals

Every completed connection emits a structured server log line containing:

- provider
- provider model/resource ID
- outcome, error code, and stop reason
- accepted audio milliseconds
- connection duration and retry audio milliseconds
- provider connect latency
- first partial latency
- stop-to-final latency

`billed_usage_ms` is reported as `-1` until the provider exposes final billing
usage in its streaming response. CatsCo does not infer vendor billing from
client duration.

Do not log audio bytes, partial text, final text, access tokens, or STT tickets.
The dedicated Nginx WebSocket location disables access logging because the
short-lived ticket is carried in the query string.

## CI/CD configuration

Configure this GitHub Environment Secret independently in both the `test` and
`prod` environments:

- `VOLCENGINE_STT_API_KEY`

The deployment workflow sends the value over SSH as NUL-delimited standard
input. `deploy/sync-stt-env.py` then atomically updates the remote
`test.env` or `prod.env` with mode `0600` before deployment.

`CATSCO_STT_ENABLED` is derived rather than stored as another secret. A
non-empty API key writes `CATSCO_STT_ENABLED=1`; an empty API key writes
`CATSCO_STT_ENABLED=0` and removes both the current key and any stale legacy
AppID, Access Token, or Cluster values.

Official protocol reference:
[Volcengine large-model streaming ASR API](https://www.volcengine.com/docs/6561/1354869).
