# Operator plan management

The commercial operations `plans` endpoint supports:

- `GET plans?view=usage&ids=1,2`: admin-only counts for up to 50 selected plan IDs, including distinct current users, scheduled users, valid invitations, and unresolved orders. The response also states whether removal is permitted and why it is blocked.
- `GET plans?view=users&plan_id=1&after_uid=0&limit=25`: distinct current user IDs ordered by UID, with `has_more` and `next_uid`. Limits are 1–100. A user with multiple grants and an entitlement appears once. These statistics are never added to public plan responses.
- `POST plans?action=delete` with `{ "id": 1, "slug": "example" }`: archive a plan. Requires the existing commercial write scope and records a `plans.delete` audit event. Public/gray sale plans, system/trial plans, live or scheduled benefits, usable invitations, and unresolved orders block the action. Archived records remain available to historical joins, but disappear from catalogs and cannot be edited back into use.

Archiving takes a plan row lock before checking dependencies. Reference triggers acquire a conflicting shared lock for grant, entitlement, invitation, and order creation/reassignment. Concurrent creation either completes before the removal check or observes the archived plan and fails. No historical relationship is deleted or nulled.

Hidden, free-to-assign internal plans may set `duration_days: -1` for permanent entitlement validity. Existing omitted/zero duration still defaults to 30 days; public paid products retain positive durations. Operator assignment and invitation redemption use a null expiry for a permanent plan, while quota grants continue to reset monthly (`1M`). A permanent replacement does not automatically recreate a Free baseline and increase its configured shared total; revoking it permits normal Free restoration again.

Admin summaries expose `current_entitlement`. It and adjustment previews follow the database operation ordering: active and already started, unexpired, explicit package before Free/legacy baseline, then expiry descending (null last), start time descending, and ID descending. The displayed expiry belongs to that same entitlement. Consumers must parse timestamps as dates; lexical sorting of RFC3339 timestamps fails when fractional seconds are omitted.

Deploy CatsCompany before the Relay dashboard update. Historical data is retained. Create or assign permanent plans only after verifying the updated backend is live; older backend versions do not support permanent custom durations.
