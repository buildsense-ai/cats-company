# Shared application management

Publishing via `POST /api/artifacts/apps` remains the sharing action. Merely
generating HTML does not add it to the catalog. Each application has one publishing
Agent. The catalog uses existing owned/friend Agent visibility; application data
permissions remain the application's responsibility.

## Attribution on first publication

Authenticated human publishers are their own initiators. A bot publishing a new
application must now provide `source_topic_id` and `source_message_id`, referring
to the human message that requested creation/publication. The server verifies the
bot's access to that topic and reads the sender from the stored message. An
arbitrary `creator_uid` supplied by a client is never accepted.

```json
{
  "id": "order-workbench",
  "title": "订单工作台",
  "publicKey": "<application tunnel public key>",
  "source_topic_id": "<conversation containing the initiating message>",
  "source_message_id": 123
}
```

The publishing integration must attach these fields from the original conversation,
including when creation is delegated. New bot publications without a verifiable
source return `400 artifact_source_message_required` before a gateway mutation.
Existing applications remain publishable without these fields. Their creator is
unknown unless already recorded; editing or republishing cannot claim them.

Attribution and initial presentation are reserved once before the gateway write,
so retries preserve the original initiator. A failed gateway registration can be
retried under the same Agent and ID without overwriting this reservation.

## Shared presentation

`GET /api/artifacts/apps?agent=<uid>` merges gateway app addresses with persistent
platform metadata. Responses include `creator_uid` when known, `description`,
`icon_url`, and a caller-specific `can_manage` flag. Treat absent `can_manage` as
false. Never infer permission from a local Agent label or the public gateway list.

`PATCH /api/artifacts/apps/<id>` replaces only the presentation:

```json
{ "title": "订单工作台", "description": "团队订单跟进", "icon_url": "/uploads/example.png" }
```

The recorded creator and the publishing Agent's current owner may edit these
fields. Friendship alone and the publishing bot's API key do not grant this
permission. It does not grant tunnel rotation, app deletion or ownership transfer.
Other viewers receive the same presentation on their next catalog load; already
open pages are not pushed live updates. Uploading new HTML or registering again
does not overwrite edited presentation. The generated HTML itself is unchanged.

The UI provides All / Manageable filtering plus an edit action on authorized
cards. It introduces no deletion, unsharing, multi-Agent sharing or application
data-permission UI.

## Rollout and verification

Both PostgreSQL and MySQL initialize `artifact_app_metadata` through `CreateSchema`.
Deploy the backend together with the publishing integration's source-message
fields before relying on management in the frontend. This repository change does
not deploy the production service or update externally hosted publishing skills.

The frontend reads the authenticated platform catalog in both development and
production. For an older development backend returning 404/405/501 only, it can
fall back to the public Vite catalog proxy in read-only mode. Authentication errors,
permission denials and server errors never fall back. Editing is never simulated
or stored in the browser.

For a local preview intentionally connected to a backend without management
support, set `VITE_ARTIFACT_APPS_CATALOG=public` in the ignored
`webapp/.env.development.local`. This explicitly selects the public Agent-scoped
catalog for browsing and forces `can_manage=false`. It is not an error-based
authentication fallback, and production ignores this setting. Remove it after
deploying the management backend to test real permissions and shared editing.

Run focused server and UI tests, the production build and build verification.
Database integration tests require `CATS_PG_TEST_DSN` or `CATS_MYSQL_TEST_DSN`.
For deployment acceptance, use an owner, an initiating colleague and an unrelated
friend: verify only the first two can edit, the third sees saved changes on reload,
and a content republish keeps metadata and original ownership.
