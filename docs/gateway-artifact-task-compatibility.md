# Gateway application task compatibility

The gateway application list and the platform Artifact registry are separate
data sources. A gateway registration (`GET https://artifact.catsco.cc/api/apps`)
does not by itself create a versioned platform Artifact or a task manifest.

The webapp therefore binds a gateway iframe to the task host only when the
active platform registry contains the same `id` with a positive
`publish_version`. The server then validates that identity and loads the
manifest from the exact registry version before creating a task. Gateway-only
applications remain browsable and show an actionable warning instead of
submitting a request that the server must reject.

The panel's explicitly selected bot also identifies a gateway iframe when the
legacy `cloud_artifacts_enabled` flag is missing or the conversation has multiple
human members. Those conditions disable ordinary chat Artifact attachment, but
must not disable a versioned application opened from the gateway panel. A
different known session bot still prevents binding. Closing the panel invalidates
the frame so it cannot submit further tasks.

The production `promo-content-studio` application was checked on 2026-10-02:

- Gateway: `id=promo-content-studio`, URL
  `https://artifact.catsco.cc/promo-content-studio/`.
- Agent: `1071`.
- Platform registry: `publish_version=18`, URL rooted at
  `https://agent-1071.artifacts.catsco.fun:19991/artifacts/`.
- Version 18 declares `works.batch.create.v1` and runtime completion mode.

The fixture in `webapp/src/test-fixtures/promo-gateway.js` records these public
identity fields for the cross-document binding test. It does not grant a
version to gateway-only applications.
