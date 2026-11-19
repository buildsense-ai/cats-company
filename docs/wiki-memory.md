# Chat Wiki sidebar

The chat Wiki sidebar reads published Wikis from Harness Memory. It is disabled by default. Configure the Cats Company API container and the Harness Memory service before opening the feature to users.

1. On Harness Memory, set `MEMORY_CATSCO_READ_TOKEN` and point `MEMORY_BOT_PROJECT_ID` to the project that stores bot uploads. See the [Harness Memory setup](https://github.com/buildsense-ai/harness-memory/pull/27).
2. In Cats Company's production environment file, set `CATSCO_WIKI_MEMORY_BASE_URL` to the Harness Memory origin reachable from the API container, for example `https://memory.internal.example:8460`. Set `CATSCO_WIKI_MEMORY_TOKEN` to the same service token. The URL accepts an HTTP(S) origin and an optional port. Keep the service token in server configuration.
3. Apply the API container configuration through the normal deployment. Sign into Cats Company and open a conversation with a bot. The chat toolbar shows **记忆** when both settings are valid. Deployments with missing settings hide the entry. A temporary upstream error on a configured deployment can be retried in the sidebar.

The integration uses a server-to-server connection. Cats Company sends its verified viewer scope and the service bearer token to these Harness Memory routes.

| Method and path | Request body | Response |
| --- | --- | --- |
| `POST /internal/catsco/memory/catalog` | `scope` plus optional `topic_id`, `cursor`, and `limit` | Wiki metadata in `items` and `next_cursor` |
| `POST /internal/catsco/memory/wiki` | `scope` plus the Wiki `id` | Markdown, source references, revision and an `ETag` header. An authorized matching `If-None-Match` receives `304`. |

Every scope carries `bot_uid`. Cats Company uses `{"kind":"owner","bot_uid":"365"}` for the bot owner and `{"kind":"topics","bot_uid":"365","topic_ids":["p2p_7_365"]}` for a member who can read that conversation. Ordinary members can view Wikis from verified upload sources configured through `MEMORY_CATSCO_SOURCE_BINDINGS` on Harness Memory.

The browser uses the same Cats Company origin. `GET /api/memories/availability` returns the enabled flag after login without contacting Harness Memory. Catalog and Wiki reads go through `/api/agents/{uid}/memories` and `/api/agents/{uid}/memories/{id}`.
