# Shimo browser worker

This internal CatsCo service owns interactive Shimo login and read-only browser sessions. It never receives a CatsCo user ID, agent ID, task message, password, verification code, or actor token. CatsCo identifies a session with an opaque HMAC binding.

## Runtime flow

1. CatsCo calls `POST /v1/sessions/login` with an opaque `connection_binding`.
2. The Worker starts a temporary Chromium context and returns a random `/shimo-login/{token}/` URL.
3. The user operates that isolated page through screenshots, clicks, text input, or supported keys. QR login works by opening the WeChat option and scanning the displayed code.
4. The Worker detects `https://shimo.im/lizard-api/users/me`, exports Playwright storage state, encrypts it with AES-256-GCM, and closes the login browser.
5. Every read creates a fresh headless browser context from that user's encrypted state.

The login URL is a bearer secret. It expires after ten minutes, is excluded from referrers and caches, and must never be logged by reverse proxies.

## Configuration

```text
SHIMO_WORKER_TOKEN=<at-least-32-random-characters>
SHIMO_WORKER_SESSION_KEY=<64-hex-characters-or-base64-for-exactly-32-bytes>
SHIMO_WORKER_PUBLIC_BASE_URL=https://app.catsco.cc/shimo-login
SHIMO_WORKER_STATE_DIR=/var/lib/catsco-shimo
CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium
```

The internal API listens on port `7070`. Only `/shimo-login/` should be exposed through the CatsCo HTTPS origin. `/v1/` must remain on the private Docker network and requires `SHIMO_WORKER_TOKEN`.

## Checks

```bash
npm ci
npm test
docker build -t cats-company-shimo-worker .
```

The unit suite verifies service authentication, identity-field rejection, per-binding isolation, encrypted-at-rest state, authenticated-encryption binding, and disconnect behavior. A real smoke test additionally needs an authorized Shimo test account.
