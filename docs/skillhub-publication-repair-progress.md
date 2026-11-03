# Skill publication repair delivery checkpoint

Updated 2026-11-03. This tracks the publishing-repair line, not marketplace UI
redesign or automatic execution of arbitrary Skill code.

| Boundary | Evidence / current status | Still required |
| --- | --- | --- |
| Frozen rejected package and report | Cloud retained report/material readers; required TLS/PostgreSQL contracts | Later-round candidate ancestry |
| Live source identity | Dedicated XiaoBa capture/source verification; pinned paired contract | Production executor composition |
| Model dispatch journal | Durable first-round fence, private output, uncertain-outcome and takeover recovery | Task controller stop/recovery integration |
| Runtime + Cloud composition | CatsCo [#852](https://github.com/buildsense-ai/cats-company/pull/852) merged at `d00cade9`; main CI, dependency audit, test and production deploy all successful | Fixture provider, journal and local task authority are still synthetic in this paired test |
| Restricted model HTTP adapter | Implemented internally, default off; existing original-payer key, bounded single local attempt, exact untrusted text output | Review/merge; live relay no-retry/no-fallback/payer policy validation; production wiring |
| Runtime description confirmation | Existing retained proposal, explicit human confirmation and separate private-copy path | Complete trusted executor/entry-point continuation |
| Safe automatic format repair | Not complete | Bounded safe executor and controller; max three rounds; stop on functional/identity/security changes |
| Complete user publishing loop | Not complete | Conversation and workspace entry continuation; controlled rollout and real-user acceptance |
| Chinese manual and optional images | Product rules agreed, later phase | Asynchronous post-publish generation under publisher account; editable manual without changing SKILL.md/runtime description; optional author image management |

The next integration must not claim the local HTTP single-attempt guarantee
disables retries inside a relay. It must validate deployment policy before any
paid/production enablement. No automatic feature gate or production worker is
enabled by the current internal adapter. Existing publication, model-switching,
workspace, normal tools and marketplace behavior remain unchanged.
