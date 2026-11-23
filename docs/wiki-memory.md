# Chat Wiki sidebar

The chat Wiki sidebar reads published Wikis from Harness Memory. It is disabled by default. Configure the Cats Company API container and the Harness Memory service before opening the feature to users.

1. On Harness Memory, set `MEMORY_CATSCO_READ_TOKEN` and point `MEMORY_BOT_PROJECT_ID` to the project that stores bot uploads. See the [Harness Memory setup](https://github.com/buildsense-ai/harness-memory/pull/27).
2. In Cats Company's production environment file, set `CATSCO_WIKI_MEMORY_BASE_URL` to the Harness Memory origin reachable from the API container, for example `https://memory.internal.example:8460`. Set `CATSCO_WIKI_MEMORY_TOKEN` to the same service token. The URL accepts an HTTP(S) origin and an optional port. Keep the service token in server configuration.
3. Apply the API container configuration through the normal deployment. Sign into Cats Company and open a conversation with a bot. The chat toolbar shows **记忆** when both settings are valid.

Cats Company uses one service bearer token for both upstream routes.

| Method and path | Request body | Response |
| --- | --- | --- |
| `POST /internal/catsco/memory/catalog` | Optional `cursor` and `limit` | Wiki ID, Bot UID, topic ID, title, creation time and revision in `items`, plus `next_cursor` |
| `POST /internal/catsco/memory/wiki` | Wiki `id` | Markdown, source references, revision and an `ETag` header. A matching `If-None-Match` receives `304`. |

The server fetches all catalog pages at startup and every hour. It replaces its database snapshot only after receiving all pages successfully. The chat reads metadata from that snapshot, and loads Markdown when a Wiki opens. Cats Company applies current chat permissions to the conversation view and to each body request. Bot owners can also browse the complete Bot catalog. Members can browse their direct chat and eligible groups that include the Bot. Wikis without a single Cats Company topic appear under other sources in the owner's view.

The browser uses the same Cats Company origin. `GET /api/memories/availability` returns the enabled flag after login. Catalog and Wiki reads go through `/api/agents/{uid}/memories` and `/api/agents/{uid}/memories/{id}`. Deploy the matching Harness Memory contract with the Cats Company change.
