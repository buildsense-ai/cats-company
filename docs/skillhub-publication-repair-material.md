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

## Remaining trusted caller obligations (NOT implemented here)

Before **and after** reading, a future adapter/controller must recheck the current
CatsCo human/Bot/private-topic/task/lease and live Runtime source. It must strictly
parse the retained candidate with bounds, verify the semantic source snapshot
hash, and produce a bounded safe model projection. Transport SHA256 alone does
not establish any of these. No Skill code may be executed during extraction.
This reader handles revision 1 only, not later-round private candidate ancestry.

There is no real provider invocation, billing, task loop, human confirmation,
candidate mutation, source Skill change or publication in this slice. Existing
production gates remain off. Runtime description drafts still need explicit
human confirmation; they are not the Chinese marketplace manual.

## Verification

Unit/real TLS client tests cover strict config/receipts, redirects, header/body
corruption, timeout/cancel and concurrency limits. The required
`publication_repair_material_paired` CI job checks out Cloud commit
`f14df745048aeba14f22424a814b6590518a7523`, migrates a throwaway PostgreSQL database,
and runs the Go client against the real Node HTTPS handler and store under race
detection. A locally generated test CA is trusted explicitly; TLS verification
is never disabled.

`TestPublicationRepairMaterialCloudPostgres` proves lost response without retry,
client/handler reconstruction with identical retained bytes, active publisher
checks, cancellation denial and zero publication commits/permits. The fixture's
upstream upload-grant introspection is synthetic. Runtime/source/provider are
not exercised and this is not a production/paid end-to-end test.

Local runs without PostgreSQL/Cloud dependencies explicitly skip the paired test;
CI sets `CATSCO_REQUIRE_REPAIR_MATERIAL_CONTRACT=1` so missing dependencies fail.

```sh
go test ./server -run '^TestPublicationRepairMaterial' -count=1 -v
```
