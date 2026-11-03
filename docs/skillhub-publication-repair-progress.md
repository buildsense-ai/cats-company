# Skill publication repair delivery checkpoint

Updated 2026-11-03. This tracks the publishing-repair line, not marketplace UI
redesign or automatic execution of arbitrary Skill code.

| Boundary | Evidence / current status | Still required |
| --- | --- | --- |
| Frozen rejected package and report | Cloud retained report/material readers; required TLS/PostgreSQL contracts | Later-round candidate ancestry |
| Live source identity | Dedicated XiaoBa capture/source verification; pinned paired contract | Production executor composition |
| Model dispatch journal | Durable first-round fence, private output, uncertain-outcome and takeover recovery | Task controller stop/recovery integration |
| Runtime + Cloud composition | CatsCo [#852](https://github.com/buildsense-ai/cats-company/pull/852) merged at `d00cade9`; main CI, dependency audit, test and production deploy all successful | Fixture provider, journal and local task authority are still synthetic in this paired test |
| Restricted model HTTP adapter | CatsCo [#859](https://github.com/buildsense-ai/cats-company/pull/859) merged at `e2ecd437`; main CI, dependency audit, test and production deploy all successful; internal/default off, original-payer key and bounded single local attempt | Live relay no-retry/no-fallback/payer policy validation; production wiring |
| First-round description attempt | CatsCo [#869](https://github.com/buildsense-ai/cats-company/pull/869) merged at `0f7e3dc3`; merge CI and test deployment passing, production deployment in progress; composes model coordinator, retained draft and read-only recovery | Verify production deployment; production executor/controller composition; no paid or real-user E2E yet |
| Uncertain-result stop checkpoint | Internal helper writes and verifies `stopped/model_result_uncertain` only for a typed uncertain model outcome; transient peer failures and ambiguous draft writes remain recoverable; pending PR/CI | Review/merge; end-to-end controller policy and production executor remain unwired |
| Runtime description confirmation | Existing retained proposal, explicit human confirmation and separate private-copy path | Complete trusted executor/entry-point continuation |
| Safe automatic format repair | Not complete | Bounded safe executor and controller; max three rounds; stop on functional/identity/security changes |
| Complete user publishing loop | Not complete | Conversation and workspace entry continuation; controlled rollout and real-user acceptance |
| Chinese manual and optional images | Product rules agreed, later phase | Asynchronous post-publish generation under publisher account; editable manual without changing SKILL.md/runtime description; optional author image management |

The next integration must not claim the local HTTP single-attempt guarantee
disables retries inside a relay. It must validate deployment policy before any
paid/production enablement. No automatic feature gate or production worker is
enabled by the current internal adapter. Existing publication, model-switching,
workspace, normal tools and marketplace behavior remain unchanged.

## Current integration boundary

The first-round attempt checks CURRENT draft authority before a pending execution
lease, so restart after a committed proposal can recover the same unconfirmed
or human-confirmed draft without a new model dispatch or round. The real Runtime
source verifier has a separate read-only recovery path for finished/expired
leases; this is not permission to execute, confirm, write files or publish.

Verification stays explicit about which peers are real: local TLS HTTP-adapter
composition uses synthetic persistence/source/report peers; the mandatory pinned
Runtime recovery contract uses synthetic draft/task authority; PostgreSQL
attempt-recovery contracts use synthetic source/material/provider ports. The
existing Runtime+Cloud/PG dual-mode material contracts remain required. These
separate contracts are not a full paid-model or real-user end-to-end test.

Next: complete trusted controller stop/recovery behavior and safe candidate
repair/continuation without broadening the agreed boundaries. Keep max-three-round
budget, explicit confirmation for runtime description, original human payer and
both publication entry points. Then validate relay policy/accounting and stage a
controlled rollout. Chinese manuals and optional author images remain later work.
