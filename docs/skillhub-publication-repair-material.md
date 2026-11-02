# Private original publication candidate read (M3 internal slice)

This package-private, default-off client reads the **original frozen rejected
candidate wire bytes** retained by Cloud PR71. It is not a model-material parser,
source verifier, task controller, worker, public tool or execution authorization.
It has no environment loader, Hub registration or production wiring.

## Contract

- The caller supplies a trusted stored `SkillHubPublicationCloudReceipt`, never a
  receipt from the browser/model. The configured issuer/audience are independent.
- Only a dedicated `pubmaterial_` peer credential is accepted. The destination is
  constructor-pinned HTTPS + `/api/skills/publication-repair/internal/material`.
  No proxy, cookie jar, redirects, compression or automatic application retry.
- Whole call/body timeout 15s; maximum two active reads, held through body close;
  original request size 1..32MiB. The receipt's wire SHA256 and exact length must
  match. Content type/length/no-store/nosniff and unencoded response are mandatory.
- On network loss, denial or malformed/partial data, return no bytes and a static
  error, not a transport URL/private upstream message. A read may be explicitly
  reconstructed/repeated but is never a fresh publication/model dispatch.
- Cloud rechecks current active user/publisher binding, live task/delegation,
  immutable receipt fields, unexpired DB time and complete retained rejection.
  Cancellation and read share the task lock boundary. No writes/validation rerun.
  Bytes already transmitted cannot be retroactively revoked.

## Bounded original model projection

`projectPublicationRepairMaterial` is a pure, package-private parser. It accepts
the current Runtime's **source-only files** envelope (text or canonical padded
Base64), not ZIP or explicit manifest/form. All files must fit the complete
1MiB text projection; binary/invalid UTF-8/NUL/control bytes, oversize or unsupported
input fail with no partial material. No truncation, omitted files, inference,
filesystem access, YAML parsing or Skill execution is performed.

It verifies the pending first-round task identity and full retained rejection
evidence, exact original wire hash/length, safe unique paths (including directory
prefix collisions), and independently recomputes Cloud's
`skillhub-publish-candidate-v1` hash from each decoded file's path/byte-size/SHA256
and empty explicit manifest. Canonical JSON uses Cloud UTF-8 bytewise sorting and
JSON.stringify string escaping; source order is separately preserved for report
entry indexes. Duplicate/case-mismatched/unknown/null keys and unpaired JSON
Unicode surrogates fail instead of silent replacement. The single supported
description selector must resolve to the exact retained file, not a model path.

The output is deterministic JSON text tagged `untrusted-skill-source`, with
original indexes/paths/complete text, at most 1MiB including JSON escaping. It is
untrusted data, not sanitized instructions or execution authority. Report nested
pointers are cloned. Default serialization still omits private source text.

This does not re-run validation, confirm a description or alter source files.
The function remains unwired to any production material/provider/controller port.

`preparePublicationRepairModelMaterial` is the next internal composition step.
It accepts only a pending first-round task snapshot, reads the historical
validation report through the authenticated publication Cloud client, requires
the exact stored receipt and admission evidence, then reads the original bytes
through the private material client and passes them to the strict projector.
Peer errors are reduced to a static failure and a mismatched report prevents the
candidate read. The function is not a public route or model tool, and is not
registered as a worker/provider callback. Its caller must still bracket the
operation with fresh current-task/lease and live Runtime source checks.

## Remaining trusted caller obligations (NOT implemented here)

Before **and after** reading, a future adapter/controller must recheck the current
CatsCo human/Bot/private-topic/task/lease and live Runtime source. It must obtain
the exact verified retained report and call this parser with trusted stored task
facts. Parser checks are integrity checks, not current authority. Transport
SHA256 alone does not establish semantic identity or source authority.
This reader handles revision 1 only, not later-round private candidate ancestry.

There is no real provider invocation, billing, task loop, human confirmation,
candidate mutation, source Skill change or publication in this slice. The
required combined paired contract uses the exact candidate bytes and entry/hash
binding captured by pinned XiaoBa Runtime, sends those bytes through real Cloud
intake/PostgreSQL/TLS, reads its retained report/material through the real Cloud
clients, and checks the live Runtime source at each coordinator fence. Provider
invocation and dispatch journal remain explicit synthetic adapters; production
gates remain off. Runtime description drafts still need explicit human
confirmation; they are not the Chinese marketplace manual.

## Verification

Unit/real TLS client tests cover strict config/receipts, redirects, header/body
corruption, timeout/cancel and concurrency limits. The required
`publication_repair_material_paired` CI job checks out Cloud commit
`f14df745048aeba14f22424a814b6590518a7523` and XiaoBa Runtime commit
`062c7cf1b11fa49fdc44466c1cbfe80b041d11c4`, migrates a throwaway PostgreSQL
database, and runs the Go client against the real Node HTTPS handler and store
under race detection. A locally generated test CA is trusted explicitly; TLS
verification is never disabled.

`TestPublicationRepairMaterialCloudPostgres` proves lost response without retry,
client/handler reconstruction with identical retained bytes, active publisher
checks, cancellation denial and zero publication commits/permits. In mandatory
Runtime+Cloud mode it exports the exact candidate captured from the isolated
Runtime, verifies matching entry ID/request hash/source hash/byte length at
Cloud admission, then routes that retained Cloud report and original bytes
through the coordinator while every source fence calls the live pinned Runtime.
The fixture's upstream upload-grant introspection, local task, provider and
dispatch journal are synthetic. The same mandatory runner then runs the test
again in `cloud-exotic-vectors` mode, explicitly clearing the Runtime settings
and candidate override. That second run uses Cloud's non-BMP/BMP-private-use
paths, U+2028/U+2029/ampersand escaping and Base64 vectors, while preserving the
original source entry index. Only that second run uses a synthetic Runtime
verifier; it still uses real Cloud HTTPS/PostgreSQL report/material reads and
the coordinator. Both runs must pass without skips. This is not a
production/paid end-to-end test.

Local runs without PostgreSQL/Cloud dependencies explicitly skip the paired test.
The mandatory runner sets `CATSCO_REQUIRE_REPAIR_MATERIAL_CONTRACT=1` for both
modes so missing Cloud/PostgreSQL dependencies fail. It additionally sets
`CATSCO_REQUIRE_REPAIR_RUNTIME_CLOUD_CONTRACT=1` for `runtime-cloud`, requiring
both `XIAOBA_REPAIR_PAIRED_ROOT` and `XIAOBA_REPAIR_PAIRED_NODE`; missing either
fails with the dependency names instead of silently falling back to a synthetic
Runtime. The runner requires the Node executable setting and database URL up
front. Neither flag applies to the separate client/projection-only unit step.

```sh
go test ./server -run '^TestPublicationRepairMaterial' -count=1 -v
```
