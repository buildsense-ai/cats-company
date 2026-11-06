# Native iOS push notifications

The native CatsCo app uses UserNotifications and APNs alert notifications. It does not depend on a running WebSocket or a background execution grant. The iOS application lives in https://github.com/buildsense-ai/catsco-ios.

## Server configuration

Configure the `test` / `prod` GitHub environments with `APNS_TEAM_ID`, `APNS_KEY_ID`, `APNS_BUNDLE_ID` (`cc.catsco.ios`) and `APNS_P8_BASE64` (base64 of a dedicated Apple Developer **APNs** P-256 key). An App Store Connect API key does not authorize APNs. The deploy workflow passes credentials over SSH stdin to `deploy/sync-apns-env.py`; it installs an owner-only key in the existing secrets volume and puts only its path (`APNS_KEY_FILE=/run/catsco-secrets/apns.p8`) in the deployment environment file. Never commit a private key, device token, session token or real registration payload.

The service is disabled when credentials are missing or invalid. Empty GitHub secrets explicitly disable the integration on the next deployment. Production and sandbox use Apple's fixed HTTP/2 endpoints. Alert delivery uses the configured bundle identifier, priority 10, a one-hour expiration and bounded retries for transport errors, 429 and 5xx. Invalid/removed device tokens are deleted by account + endpoint + registration generation; credential errors do not delete devices. Logs omit provider request URLs and device tokens.

## Registration API

All routes use JWT authentication. Enrollment and tests reuse the existing per-user rate limits:

- `GET /api/push/ios/config`: availability and configured bundle identifier.
- `POST /api/push/ios/registrations`: `token`, `environment` (`sandbox` / `production`), random `registration_id`, optional `show_previews` (default false).
- `DELETE /api/push/ios/registrations`: matching device/environment/registration. Revocation remains available when APNs delivery is disabled.
- `POST /api/push/ios/test`: current `registration_id`. HTTP 202 means Apple accepted the request; it does not prove banner delivery.

Native registrations reuse `PushSubscriptionStore` in an `apns://environment/bundle/token` namespace with an `apns-v1` marker. This preserves atomic endpoint transfer and the existing combined limit of ten devices per account without a database migration. Native endpoints never pass through Web Push, and the browser API rejects them. Native and browser turn deduplication use separate coordinator instances so a status-only iOS alert cannot consume a later browser message notification.

Message alerts follow the existing visible-message filters, bot turn settling and per-conversation mute preferences. Task statuses additionally produce waiting-for-confirmation alerts and completion/failure fallbacks when no final reply exists. Superseded runs and repeated status heartbeats do not create duplicate task alerts. Channel-managed group traffic remains excluded. Native foreground presentation suppresses the currently open conversation on that device, while other devices remain eligible.

## iOS lifecycle and privacy

The native app asks for permission from Settings → 消息通知. Enrollments refresh on login/restore/foreground and APNs token changes. Logout, account changes and disabling notifications retire the registration; failed revocations remain in device-only Keychain for a subsequent retry. Already accepted/queued notifications and revocation while the device has no network are necessarily best effort. Native notification routes validate the current account registration generation before opening an existing conversation. Cold-start notification taps wait for account restoration. Tokens and pending revocation credentials stay out of UserDefaults.

The default alert hides employee names and message excerpts. Users can opt into message previews; lock-screen presentation also follows iOS settings. No badge count is sent because the server does not currently maintain an authoritative cross-device unread badge. Ordinary APNs alerts also carry `aps.content-available: 1`, preserving the alert, sound, routing and default redaction. This permits the application to refresh its widget snapshot when iOS grants a background launch; it does not guarantee a background launch or timeline refresh.

## Remote Live Activities

JWT-authenticated `POST` and `DELETE /api/push/ios/activities` accept exactly:

```json
{
  "token": "<hex ActivityKit activity update token>",
  "environment": "production",
  "registration_id": "<random UUID generation, 16-64 characters>",
  "topic_id": "p2p_7_8",
  "run_id": "<exact current persisted run>",
  "details": false
}
```

`environment` is `production` or `sandbox`; token validation matches native alert enrollment (32-256 hex characters, even length). Registration IDs accept ASCII letters, digits and hyphens. Tokens are normalized to lowercase. These are per-activity update tokens, not ordinary device tokens or ActivityKit push-to-start tokens. The configured bundle ID is never supplied by the caller.

Enrollment returns HTTP 201 `{"registered":true}`. It requires an active human account, a canonical readable topic, current friendship/ownership or group membership, and the exact current nonterminal topic/source run. Channel-managed groups remain inaccessible to human surface readers. Expired tasks, mismatched runs and terminal runs return 409; access failures return 401/403. No caller may register another account's topic. DELETE matches the account, token, generation, topic and run, returns `{"registered":false}`, and remains available with APNs credentials disabled. A late teardown cannot delete another generation. A generation cannot be rebound to a different run.

Activity registrations expire at most eight hours after enrollment. Repeating an active enrollment preserves both its original expiry and delivery watermark. Changing `details` updates metadata with a version fence. Presentation defaults to `details:false`, transmitting only generic title/agent strings and an empty summary; private title, agent name and summary are absent from the transmitted bytes.

All accepted task status paths enqueue surface work before the ordinary native alert state filter. Running/waiting/idle statuses send `event:"update"`; completed/failed/cancelled/stale statuses send `event:"end"`. Provider headers are:

| Header | Value |
| --- | --- |
| `apns-push-type` | `liveactivity` |
| `apns-topic` | configured bundle ID + `.push-type.liveactivity` |
| `apns-priority` | `5` for updates, `10` for terminal events |
| `apns-expiration` | registration expiry, Unix seconds |
| `apns-collapse-id` | hash of the activity endpoint |

The body is `{"aps":{"timestamp":<integer seconds>,"event":"update|end","content-state":{...}}}`. Content state matches `CatsCoTaskAttributes.ContentState`:

```json
{
  "title": "CatsCo",
  "agent": "CatsCo",
  "state": "running",
  "summary": "",
  "completed": 0,
  "total": 0,
  "updated": 1793851200,
  "details": false
}
```

With explicit details enabled, bounded conversation/agent names and the persisted task summary replace the generic values. Error text, tool arguments and message history are never included. `completed` and `total` stay zero because the current task store has no authoritative step counts. `updated` uses server-observed task time in Unix seconds. Updates include `stale-date` bounded by five minutes, task expiry and activity expiry; terminal events include an immediate `dismissal-date`.

Workers reload account state, current membership/access, the exact registered topic/source/run from the durable run ledger, and registration version before every provider attempt. The ledger records each run's latest receipt but no conclusive cross-run execution/start order. A higher insertion ID or latest publisher timestamp alone cannot establish supersession: a delayed first-seen terminal A must not retire already-running B. Registrations therefore stay bound to their exact run until its own terminal event or expiry, or definitive access/registration revocation; no cross-run supersession is guessed. Completing enrolled B while older A remains active still sends B an end event. Parent aggregate/source projection semantics are unchanged. Native authorization preserves database lookup errors separately from definitive access denial; transient errors suppress delivery without deleting the registration. Expired/inaccessible registrations and registrations whose exact ledger run is absent are retired when encountered by workers. Durable compare-and-swap fingerprints coalesce identical content and fence competing workers across replicas. Fresh authoritative heartbeats may refresh unchanged content at most once every two minutes to renew its stale date. The payload timestamp increases within the registration, including same-second transitions; nonterminal bursts exceeding a 30-second timestamp lead are coalesced; a terminal event remains eligible to end the activity. Accepted terminal pushes retire their registration. Provider 410 and invalid-token/topic 400 responses retire only the matching metadata/generation/auth version. Failed provider attempts release their claim when possible so another accepted status can retry.

This uses the existing APNs signer, fixed hosts, HTTP/2 transport, redirect rejection, 15-second request timeout, 20-second queued-work deadline and two bounded provider attempts. Activity work uses a separate bounded two-worker, 128-job queue; ordinary alerts retain their existing eight-worker, 128-job queue. Both share the deployed signer and HTTP/2 client. Activity discovery, account/access/membership/title reads and exact task reads use QueryContext/QueryRowContext with the worker deadline, and durable mutations use ExecContext. Cancellation is checked before discovery and between recipients. Slow activity discovery cannot occupy ordinary alert workers. Task handlers do not wait for provider networking. The queue remains best effort: overflow, process termination after a durable claim, exhausted retries, and an already in-flight request during revocation can lose or race a notification. There is no new durable APNs delivery outbox or promise of exactly-once delivery. A subsequent status can recover a failed released claim; a crash after claiming identical content may leave it coalesced until content changes. ActivityKit stale presentation and normal foreground reconciliation remain necessary.

## Read-only widget snapshot capability

JWT-authenticated `POST /api/push/ios/surfaces` accepts:

```json
{
  "access_token": "<app-generated cryptographically random lowercase 64hex secret>",
  "registration_id": "<random UUID generation>",
  "scope": "<lowercase 64hex account scope hash>"
}
```

It returns HTTP 201:

```json
{
  "registered": true,
  "access_token": "<uid>.<64hex>",
  "expires_at": "2026-12-05T00:00:00Z"
}
```

The returned canonical token is also accepted as enrollment/revocation input. The UID prefix must match the JWT account. Store it only in the shared app/widget Keychain, never UserDefaults or a public URL. This is a separate read-only capability, not a public bearer session. Repeated active enrollment with identical input preserves expiry and one durable row. A different secret for the same active registration rotates the hash and preserves its original expiry. Maximum lifetime is 30 days; expired registrations may be explicitly enrolled again. The scope is an opaque caller-supplied account hash, stored and returned exactly; the server does not infer an account identity from that hash.

JWT-authenticated `DELETE /api/push/ios/surfaces` accepts the same body, verifies scope, capability hash and ownership, and returns `{"registered":false}`. Missing owned rows are an idempotent success; a stale secret or wrong scope for an existing row returns 403. Deletion works with APNs disabled. Metadata/auth-version deletion prevents a late revocation from removing a concurrent rotation.

`GET /api/push/ios/snapshot` accepts only `Authorization: Surface <uid>.<64hex>`. It has no JWT middleware and never accepts a JWT, API key, session cookie or UID context as snapshot authentication. Every read verifies the durable hash/expiry/revocation, active human account and current roster/topic permissions. It returns:

```json
{
  "scope": "<stored scope>",
  "generated_at": "2026-11-05T00:00:00Z",
  "agents": [],
  "conversations": []
}
```

Agents reuse the existing authorized `/api/agents` roster projection. Conversations reuse `/api/conversations` list construction, title resolution and task projection in a message-free mode: it does not query latest messages. Both arrays are capped at 20 and the encoded response at 64 KiB. Conversation fields are explicitly allowlisted: IDs, bounded name, group/bot/task flags, agent IDs, and task `topic_id`, `run_id`, `state`, `source_uid`, `updated_at`, `expires_at`. Message previews, summaries/errors, history, project text, tool arguments, hidden reasoning and runtime credentials are omitted. Agent names/usernames are bounded; avatar URLs are omitted. Existing native models may decode these shapes and then save their own bounded presentation snapshot.

All new endpoints set `Cache-Control: no-store`. Enrollment bodies share the existing strict 8 KiB JSON decoder and per-user subscription rate limit. Invalid/missing snapshot authentication returns 401; unavailable persistence/projection returns 503. The widget can fetch on its system timeline with iOS 18 compatible APIs; neither timeline scheduling nor OS background launch is guaranteed.

## Durable storage and limits

No database migration or in-memory subscription registry is added. Background creation uses atomic insert-if-absent, serialized with ordinary push writes by the existing account-row lock and combined count. Expired capability renewal or deliberate activity generation replacement updates only the exact observed owner/endpoint/generation/metadata/auth version, without transferring ownership or evicting another row. Conflicts trigger at most three fenced attempts: activity refresh preserves the winning expiry and watermark; a losing widget creation/renewal can return the identical winning capability but cannot rotate back to its stale secret. A conflicting rotation returns 409 so the client can reconcile and retry explicitly. An operation that observed an existing version never becomes initial creation: if its versioned write loses and rereading finds the row absent, it returns 409 and leaves the successful revocation intact. Only requests that have never observed a row may retry initial insert-if-absent. Native paths never call unconditional subscription upsert. All transports continue sharing the existing maximum of ten `push_subscriptions` rows per account, enforced atomically by the SQL adapters. Expired background rows are lazily removed on background enrollment; workers remove expired/revoked activity rows they encounter. Rows for an offline or removed group member can remain until the next account enrollment/cleanup, but cannot authorize a read/send after revocation or expiry.

| Surface | `endpoint` (max 512) | `p256dh` (max 256) | `auth` (max 128) |
| --- | --- | --- | --- |
| Ordinary APNs | `apns://environment/bundle/token` | `generic` or `preview` | `apns-v1` |
| ActivityKit | `activitykit://environment/bundle/token` | compact JSON `{"t":topic,"r":run,"s":sourceUID,"e":expiryUnix,"d":details}` | `activity-v1`, then `activity-v1:eventNano:payloadTimestamp:sha256Hex` |
| Widget | `surface://uid/registration_id` | compact JSON `{"s":scope,"e":expiryUnix}` | `surface-v1:sha256Hex(canonicalToken)` |

Each row also uses existing `uid`, case-sensitive `registration_id` (max 64), timestamps and unique endpoint. Metadata that cannot fit the existing column is rejected. Widget secrets are never persisted; the stored digest covers the complete canonical `<uid>.<64hex>` value. APNs update tokens must remain in durable endpoints to address Apple, with the same privacy requirements as existing device tokens. Background namespaces never reach browser delivery or ordinary APNs alerts.

The optional `PushSubscriptionVersionStore` boundary is implemented in both MySQL and Postgres: `CompareAndSwapPushSubscriptionAuth` claims delivery/rotates a hash, `CompareAndSwapPushSubscriptionMetadata` changes privacy metadata without resetting the delivery watermark, and `DeletePushSubscriptionVersion` retires exactly the matching account/endpoint/generation/metadata/auth. No ordinary transport ownership, browser delivery policy or account limit is weakened.

## Validation

Run `go test ./server/...`, focused `go test -race ./server -run 'TestNative|Test.*Push|Test.*TaskStatus'`, and `python3 deploy/sync-apns-env_test.py`. `TestNativeSurface*` covers actual HTTP handler/auth boundaries, persistent registration reads, cross-account/run rejection, raw-byte redaction, provider events/headers, expiry/revocation, bounds, queued status hooks and an in-process HTTP/2 provider without listening sockets. Both SQL adapters have atomic insert/replace/version-fence SQL tests and cancellation tests for each native SQL read stage. Regression tests cover delayed absent/expired enrollment versus watermark/secret rotation, a queued overlapping-run completion, continued B updates/end after a late first-seen older terminal A, expired-widget/activity-metadata renewal losing to DELETE, intermittent managed-group and membership errors, and saturated activity discovery while ordinary alerts still deliver. `TestMySQLPushSurfaceDurability` and `TestPostgresPushSurfaceDurability` additionally exercise concurrent CAS, insert conflicts, stale expired-version replacement, cross-account deletion, combined limits and actual overlapping run ledger writes and late first-seen terminal ingestion followed by B lifecycle continuation on real engines when `CATS_MYSQL_TEST_DSN` / `CATS_PG_TEST_DSN` are configured. The native repository checks Debug/Release builds, signed production APNs entitlements, notification-settings UI on iPhone/iPad, and existing Swift core tests.

Physical-device acceptance: install the matching TestFlight build; enable notification permission; send a test; background and lock the phone; exercise human/new agent replies, completed/failed work and waiting confirmation; tap a notification from cold launch; confirm muted topics, account changes and disabling notifications. Apple acceptance, simulator UI checks and successful signing cannot establish physical-device banner/sound delivery.
