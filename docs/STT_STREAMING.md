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

## Browser session lifecycle

The browser requests the authenticated session ticket and starts microphone
capture in parallel. Until the CatsCo WebSocket emits `ready`, captured PCM
frames remain only in the browser's bounded in-memory preconnect queue. The
browser must then send those frames to CatsCo in FIFO order before sending
newer live frames.

The preconnect queue is part of the recording, not a best-effort preview. When
the user performs a normal stop before the ticket request or WebSocket
handshake finishes, the browser must stop local capture while retaining the
already captured frames. Once the connection becomes ready, it must drain those
frames in FIFO order, send the `stop` control message, and wait for the final
transcript using the normal server timeout. This preserves short hold-to-talk
utterances and speech spoken immediately after recording begins.

`cancel` has different semantics from `stop`. Cancellation must discard queued
preconnect PCM and must not establish or retain a connection solely to upload
it. If admission or connection fails before a normal stop can be completed,
the browser reports the error and discards the queue.

A hidden page or suspended/interrupted audio context must stop local capture
immediately. This requirement also applies while microphone setup is still
awaiting: after capture initialization resolves, the browser must check the
current page visibility before accepting or forwarding any PCM. No PCM sampled
after the page becomes hidden or the audio context is suspended may be sent to
CatsCo.

Partial transcripts are presentation data only. The browser may coalesce them
to at most one visible update every 80 ms, but it must retain and publish the
newest partial. If a final result arrives while a newer partial is pending, the
client must publish that partial first. The composer must give that published
partial an opportunity to render before clearing it for the final transcript;
state batching must not make the newest live transcription unobservable.

The final transcript remains the only transcript inserted into the composer
draft or persisted by later message submission.

### Required browser regression coverage

- Speech captured before the session ticket resolves is sent after `ready`.
- Releasing hold-to-talk before `ready` sends the captured preconnect frames,
  then `stop`, and can still produce a final transcript.
- Cancelling before `ready` sends no queued PCM and does not open a socket only
  to drain it.
- Hiding the page while capture initialization is pending stops capture when it
  resolves and sends no post-hide PCM.
- A coalesced partial immediately followed by `final` has a renderer-level
  assertion that the newest partial can be committed visibly before final state
  replaces it.

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

Only one active session is permitted per user. Browser preconnect and server
audio queues are capped at 160 KB. Exceeding either cap fails the session closed
rather than silently dropping audio.

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
