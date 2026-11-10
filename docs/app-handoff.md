# Conversation application handoff (platform implementation, not end-to-end completion)

## Scope and missing integration

This adds a declarative handoff to existing gateway applications. It does not create a background job platform, artifact version, runtime task, database, publication or arbitrary browser automation. Existing `catsco.app-open.v1` remains open-only. Text mention matching remains a convenience, not a semantic task planner and never runs an application.

The platform transport and its companion runtime sender are separate repository changes. The XiaoBa-CLI companion branch `review/platform-handoff-sender-20261110`, commit `9d5047992eae6d4630cbd1f204f27520ca3d0442`, implements the authenticated `open_app` tool with catalogue discovery, v1 open-only compatibility and v2 initialize/run metadata emission. Both changes must be reviewed and installed together; neither local implementation nor a send acknowledgement proves a deployed application opened or ran. Actual gateway applications must implement the receiving handshake and their own authorization, input validation and execution. No actual application or biological analysis was run. Do not describe this platform-only implementation as end-to-end complete.

## Agent/runtime calling contract

Discover the authenticated Agent's own existing applications with `GET /api/artifacts/apps`; authenticated callers viewing another accessible Agent use `GET /api/artifacts/apps?agent=<uid>` (use the API client's existing list operation). Select semantically by capabilities and the user's actual task, not by hardcoded application names or biological keywords. Application-specific capability/schema knowledge must come from a trusted app integration description; the existing gateway catalogue does not currently expose this. If capability is unknown, ask/initialize only; never invent a run capability or parameters.

Emit an ordinary Agent reply with this metadata (not a code block parsed from prose):

```json
{
  "app_open": {
    "contract_version": "catsco.app-open.v2",
    "app_id": "existing-catalog-id",
    "handoff": {
      "request_id": "unique-request-uuid",
      "topic_id": "p2p_7_9",
      "action": "initialize",
      "parameters": { "server": "compute-1", "data_path": "/data/sample" },
      "expires_at": 1794190000000,
      "user_requested_run": false
    }
  }
}
```

`expires_at` is epoch milliseconds; replace the illustrative value with now + 60 seconds, never more than 5 minutes. A request ID is 8–80 ASCII letters/digits/underscore/hyphen, unique per logical user task, reused only for transport duplicates. `agent_uid` is optional on ingress, always stamped by the server from the authenticated sender. `topic_id` must equal the message's authenticated conversation. The server checks bot membership and app ownership; the viewer also checks message sender and current Agent/topic, then resolves only the Agent catalogue's URL.

Use `action: run` only when the user's task actually requests execution and the integration declares support; set `user_requested_run: true` only for that actual user intent. This flag is only an Agent/runtime self-assertion, NOT user consent or authorization for dangerous work. It must never bypass the application's existing execution authorization or dangerous-operation confirmation. An application must independently enforce those policies using its authenticated user session before any job starts; advertising supports_run is capability only, not permission to launch arbitrary tasks. The receiver must explicitly advertise `supports_run: true`, or no parameters are delivered for run. A mere panel open is never success.

## Parameter boundary

At most 16 flat scalar parameters, 4096 serialized UTF-8 bytes, 512 Unicode code points per string (including schema maxLength). The format rejects nesting, files, recognized URLs, executable command/script fields and multiline values; these checks do not guarantee that values contain no credentials. Common credential names, private-key markers, URL and Bearer values are rejected before metadata persistence. This parameter denylist is NOT a privacy guarantee. An ordinary string under an innocuous key can still hide a secret, and these checks cannot filter all secrets: the runtime must never put a password, token, private key or other secret under any innocuous name, in the surrounding prose, or in arbitrary metadata. Only non-sensitive server references and data paths belong here. The receiving application resolves server credentials locally from its own authorized store; the handoff must not provide them.

No handoff parameter is appended to an iframe URL or persisted in the panel navigation record. The originating message metadata, including parameters, IS persisted in conversation storage and is visible to authorized conversation readers; panel non-persistence does not make message metadata ephemeral. Never send sensitive filesystem paths or identifiers either.

The app's ready message supplies a bounded schema, validated before delivery. Schema is a map of parameter names to `{ type, required?, maxLength?, minimum?, maximum?, values? }`. Supported types: `path` (absolute path without `..` or shell metacharacters), `server_ref` (opaque short hostname/alias), `string`, `number`, `boolean`, `enum` (at most 32 string choices). Unknown fields are rejected. Applications must repeat schema/business validation on receipt, enforce server/path allowlists and resolve data access within the user's permissions. Paths are data, never shell interpolation.

## Receiving handshake and minimal integration sketch

On the iframe wire, topic_id and agent_uid are strings. The handoff message carries the original expires_at epoch-millisecond deadline; receivers must reject expired deadlines and authorize these identities against their authenticated backend, not trust them as credentials.

Install the receiver before the frame load event, with a fixed trusted platform origin from configuration, never from a query parameter. Do not use `*`. Bind to `window.parent` and verify the live user/topic inside the application's authenticated backend before execution. This initialize-only sketch illustrates the wire contract; replace `validateAndAuthorize` and `fillForm` with real application functions. Run support must stay false until the app implements its existing execution authorization, dangerous-operation confirmation and backend request-ID deduplication. Never wire a received run action directly to an arbitrary task launcher.

```js
const platformOrigin = 'https://YOUR-PLATFORM-ORIGIN'; // trusted deployment configuration
const protocol = 'catsco.app-handoff.v1';
const schema = { server: { type: 'server_ref', required: true }, data_path: { type: 'path', required: true } };
let session = null;
const seen = new Set(); // backend must also deduplicate execution request IDs across reloads
window.addEventListener('message', async event => {
  if (event.source !== parent || event.origin !== platformOrigin) return;
  const d = event.data;
  if (!d || d.protocol !== protocol || typeof d.nonce !== 'string') return;
  const reply = extra => parent.postMessage({ protocol, nonce: d.nonce, request_id: d.request_id, ...extra }, platformOrigin);
  if (d.type === 'hello') {
    session = { nonce: d.nonce, request_id: d.request_id, topic_id: d.topic_id, agent_uid: d.agent_uid };
    reply({ type: 'ready', schema, supports_run: false });
    return;
  }
  if (d.type !== 'handoff' || !session || d.nonce !== session.nonce || d.request_id !== session.request_id
    || d.topic_id !== session.topic_id || d.agent_uid !== session.agent_uid || seen.has(d.request_id)) return;
  seen.add(d.request_id);
  try {
    // Validate schema again and authorize topic, server and path using the app's authenticated session.
    await validateAndAuthorize(d);
    if (d.action !== 'initialize') { reply({ type: 'ack', status: 'unsupported' }); return; }
    fillForm(d.parameters);
    reply({ type: 'ack', status: 'initialized' });
    // A run-capable app MUST independently require its existing authenticated-user
    // execution authorization AND dangerous-operation confirmation before starting.
    // The Agent's user_requested_run assertion is NOT that authorization.
    // Only after approval and backend deduplication may it accept an allowlisted task,
    // reply running, then completed/failed after the actual job result.
  } catch { reply({ type: 'ack', status: 'failed' }); } // no secrets/errors reflected to messages
});
```

## Lifecycle and status ownership

The panel is the only handoff status owner. It sends one hello on iframe load to the exact HTTPS catalogue origin (no credentials, query, or fragment); matching `origin`, `source`, nonce and request ID are mandatory. Only a matching ready receives parameters, after rechecking expires_at against the current clock even if the timeout callback was throttled. No automatic retries. Ack `initialized` means form initialization only; `running` is not completion; `completed` and `failed` are application-reported outcomes, not independently verified job results. Missing ready/ack yields visible timeout/unknown outcome. Long jobs must expose their durable job/result interface inside the app; no background polling is invented here.

Catalogue lookup failures are visible and do not consume a request; a duplicate may recover before delivery. In-flight lookups are deduplicated and cancelled on topic/Agent change. Only a successful panel delivery is marked handled. Reload, close, auth change and topic change retire the listener/timer. Consumed identities (not parameters) live in a module set and sessionStorage so panel remount/reload cannot silently rerun a request in the same tab. Requests expire within five minutes and history loading does not trigger them. There is no global cross-tab exactly-once guarantee: a run-capable app MUST implement backend idempotency by request ID. Unknown results must not be retried with a new ID until the app checks whether the original ran.

## Validation

Node 26.7.0 is required for this worktree's Vitest 4; the shared existing node_modules is linked read-only in practice (no dependency install/update performed). A local temporary Vite config permits resolving that symlink; it is not a repository change. Relevant Go tests and utility/panel/conversation tests were run. Test details and limitations are in the implementation handoff to the reviewer. Real browser/network/app/runtime end-to-end execution remains unverified until both external integrations exist.
