# Operator plan management

The commercial operations `plans` endpoint supports:

- `GET plans?view=usage&ids=1,2`: admin-only counts for up to 50 selected plan IDs, including distinct current users, scheduled users, valid invitations, and unresolved orders. The response also states whether removal is permitted and why it is blocked.
- `GET plans?view=users&plan_id=1&after_uid=0&limit=25`: distinct current user IDs ordered by UID, with `has_more` and `next_uid`. Limits are 1–100. A user with multiple grants and an entitlement appears once. These statistics are never added to public plan responses.
- `POST plans?action=delete` with `{ "id": 1, "slug": "example" }`: archive a plan. Requires the existing commercial write scope and records a `plans.delete` audit event. Public/gray sale plans, system/trial plans, live or scheduled benefits, usable invitations, and unresolved orders block the action. Archived records remain available to historical joins, but disappear from catalogs and cannot be edited back into use.

Archiving takes a plan row lock before checking dependencies. Reference triggers acquire a conflicting shared lock for grant, entitlement, invitation, and order creation/reassignment. Concurrent creation either completes before the removal check or observes the archived plan and fails. No historical relationship is deleted or nulled.

Hidden, free-to-assign internal plans may set `duration_days: -1` for permanent entitlement validity. Existing omitted/zero duration still defaults to 30 days; public paid products retain positive durations. Operator assignment and invitation redemption use a null expiry for a permanent plan, while quota grants continue to reset monthly (`1M`). A permanent replacement does not automatically recreate a Free baseline and increase its configured shared total; revoking it permits normal Free restoration again.

Admin summaries expose `current_entitlement`. It and adjustment previews follow the database operation ordering: active and already started, unexpired, explicit package before Free/legacy baseline, then expiry descending (null last), start time descending, and ID descending. The displayed expiry belongs to that same entitlement. Consumers must parse timestamps as dates; lexical sorting of RFC3339 timestamps fails when fractional seconds are omitted.

Deploy CatsCompany before the Relay dashboard update. Historical data is retained. Create or assign permanent plans only after verifying the updated backend is live; older backend versions do not support permanent custom durations.

## Shared Agent validity and recovery

New `packed_shared` operations persist an explicit `worker_expiry_mode`:

| Mode | Machine validity | Package changes |
| --- | --- | --- |
| `follow_plan` | Follows actual machine entitlement; null expiry only for a permanent machine entitlement | Renewal, replacement, invitation upgrades and refunds recompute the remaining machine entitlement |
| `fixed` | An absolute deadline fixed when the operation is accepted | Independent of model package renewal or refund |
| `permanent` | No expiry, explicitly granted by an operator | Independent of model package renewal or refund |
| `legacy` | Existing behavior for historical records and requests without the new field | No bulk migration or inference of permanent validity |

`cloud_worker_package_term` in the commercial summary is the shared machine authorization, separate from the primary model package. An explicit null means no machine entitlement; an object with `expires_at: null` means a permanent machine entitlement. Free/legacy baselines never authorize a shared machine, even if their plan is incorrectly marked as including one. Invitations with explicitly attached machine credits count even when their model plan does not include a machine.

Machine validity includes purchased future periods connected to the current authorization without a gap. An isolated future entitlement does not authorize use today. Removing or refunding a period recomputes the surviving connected validity; it must not preserve a refunded deadline or convert the Free baseline into a permanent machine. Independent fixed/permanent machine terms remain unchanged.

Expiry first pauses the original container. For ordinary `retain_data=false` machines, `delete_after` is the absolute machine deadline plus the recorded retention days (default 3 days for packages of 7 days or less, otherwise 7 days). The dashboard displays the actual machine deadline, data retention deadline and non-negative remaining time; once the deadline passes, cleanup can run and the original machine cannot be recovered after deletion. Renewal before deletion synchronizes the deadline and resumes the original eligible machine. Manual suspension and security isolation remain in effect after renewal.

Existing credit environments with `retain_data=true` keep their recorded promise: expiry pauses service but does not schedule automatic data deletion (`delete_after` is null). This is not a promise of free compute, permanent unlimited storage, or immunity from explicit deletion. Display this rule instead of inventing a 7/15-day countdown. Unknown or missing deadlines must be displayed as unknown, never inferred from the current model plan. Existing ECS provider retention windows and individually granted terms remain unchanged.

The protected operator billing endpoint accepts `action: set_term` with `tenant_name`, `expiry_mode`, an absolute future `expires_at` for fixed terms, and a required reason in `note`. It stores the target before calling the cluster. A remote failure leaves `sync_pending`; `action: sync_term` retries that same target and the original machine without charging, buying another cycle or creating a replacement. A missing container cannot report successful renewal. The platform also retries pending synchronization with backoff.

Shared lifecycle operations use the control-plane entry point and tenant operation lock. Expiry and deletion compare the expected deadline again under the lock; an older collection task must skip a newly renewed machine. Retention timers are enabled only on the control plane and removed from other members. Publish the cluster scripts and platform adapters before CatsCompany, then publish the Relay dashboard. Existing ECS subscription billing is unchanged.

## Mixed ECS and shared machines

An account may own dedicated ECS and shared workers at the same time. User creation spends an available machine credit; owning an ECS worker does not prevent spending a shared credit. Model credits, legacy model budgets and machine creation credits remain separate.

Independent `add_worker` operations require an explicit deployment choice in the dashboard. `additional` always appends one machine and preserves the model package, existing machine configuration and each existing deadline. `ensure` adopts only the requested deployment class: `private_nat` and `public_ip` are dedicated ECS, while `packed_shared` is shared. If only the other class exists, it creates the requested class alongside it. Renewals remain scoped to the entitled machine class.

Preview tokens bind worker identities and recorded profiles. Execution reads them again, and adoption checks owner and deployment class inside the database transaction. Unknown recorded profiles fail closed. Retrying a submitted task uses its original operation ID and parameters.

The advanced manual creation-credit endpoint only grants credits for later consumption; its saved expiry is not an instruction to follow future package changes. Use the commercial action flow for explicit shared `follow_plan`, `fixed` or `permanent` machine terms.
