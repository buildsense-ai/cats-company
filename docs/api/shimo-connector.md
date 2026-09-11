# Shimo Skill connector

The Shimo connector is the server boundary used by the `shimo-reader` Skill. It accepts only short-lived capabilities minted from CatsCo's server-canonical chat identity. Request bodies cannot choose or override `agent_uid` or `actor_user_id`.

## Local MVP

Set these values only in a local development environment:

```text
CATSCO_SHIMO_ACTOR_SECRET=<at-least-32-random-bytes>
CATSCO_SHIMO_PUBLIC_BASE_URL=http://127.0.0.1:6061
CATSCO_SHIMO_CONNECTOR_URL=http://127.0.0.1:6061
CATSCO_SHIMO_SKILL_ID=catsco/shimo-reader
CATSCO_SHIMO_MOCK_ENABLED=true
```

Mock mode exercises the product contract without a real Shimo account. It creates a one-time connection URL, shows a mock completion button, isolates connection state by agent and actor, and returns fixed spreadsheet or document data. Production must leave `CATSCO_SHIMO_MOCK_ENABLED` disabled.

## API

All `/v1/shimo/*` requests require `Authorization: Bearer <short-lived-actor-capability>` and an exact `X-CatsCo-Skill-ID` match.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/v1/shimo/connection` | Get the current actor's connection state |
| `POST` | `/v1/shimo/connection-link` | Create a five-minute, one-time login link |
| `DELETE` | `/v1/shimo/connection` | Delete the current actor's connection |
| `POST` | `/v1/shimo/sheets/list` | List sheets in one Shimo spreadsheet |
| `POST` | `/v1/shimo/sheets/read` | Read one sheet and cell range |
| `POST` | `/v1/shimo/documents/read` | Read document text |

The public login route is `/connect/shimo/{one-time-token}`. The server stores only its SHA-256 digest. In mock mode, `POST` to the same route consumes the link and marks only its bound actor as connected.

The real Worker receives a separate opaque completion token and the internal callback URL `CATSCO_SHIMO_WORKER_CALLBACK_URL`. After it saves the encrypted Shimo session, it calls `/internal/shimo/login-complete` with the shared Worker authorization header. CatsCo consumes that token once and sends a transient continuation event to the original bot and topic. The event uses sequence zero, is never stored in message history, and carries a newly signed Skill grant.

## Actor capability

`GenerateShimoActorToken` accepts identities that the CatsCo message pipeline has already verified. The token is limited to ten minutes and binds:

- `agent_uid`
- `actor_user_id`
- `task_ref`
- `skill_id`
- audience `catsco-shimo-connector`
- capability `shimo:connect shimo:read`

For a live server-canonical user message, CatsCo adds an ephemeral `catsco_skill_connectors` grant only to the bot recipient. It is not stored and is omitted from history replay. XiaoBa removes connector variables from the shared Runtime environment, verifies the installed SkillHub package id, and injects the grant only into that package's direct Node.js child process. Other Skills and ordinary shell commands receive no connector credential.

## Browser Worker

`services/shimo-browser-worker` is an isolated Node.js service. CatsCo sends it only an HMAC-derived `connection_binding`; canonical actor and agent identities never cross the service boundary. The Worker:

- opens a temporary Chromium context for one login attempt;
- serves the user a short-lived browser surface at `/shimo-login/{token}/` for QR login, clicks, text input, and supported keys;
- detects a successful Shimo login through the authenticated Shimo session;
- exports Playwright storage state, encrypts it with AES-256-GCM, and closes the temporary browser;
- restores a fresh headless context from that encrypted state for every read;
- uses the connection binding as authenticated encryption AAD, so copying one user's encrypted file to another binding cannot decrypt it.
- calls the private CatsCo completion endpoint after the session is safely stored, retrying temporary bot-offline or network failures for a short window.

Enable the Worker with Compose profile `shimo` only after setting independent random values for `CATSCO_SHIMO_ACTOR_SECRET`, `CATSCO_SHIMO_WORKER_TOKEN`, and `SHIMO_WORKER_SESSION_KEY`. Set `CATSCO_SHIMO_WORKER_CALLBACK_URL=http://server:6061/internal/shimo/login-complete` in Compose. The Worker API and completion callback stay on the internal Docker network; only `/shimo-login/`, `/connect/shimo/`, and the Skill API are publicly routed.

## Current limitation

The browser Worker, real read path, per-turn Skill credential path, and trusted login-completion resume event are implemented locally. A live cloud login has not yet been accepted against the deployment environment. Login and resume tickets are process-local in this first version, so the CatsCo server must remain on one instance during a login attempt; moving those short-lived tickets to Redis is required before horizontal server scaling.
