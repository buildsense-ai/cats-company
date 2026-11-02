# Active-task device grant renewal

This additive change does not enable or force multi-device v2. The WebSocket hi
features advertise `device_grant_renewal.v1` and `remote_shell_timeout.v1`; clients
that do not understand them retain the existing protocol and original expiry.

`device_grant_renewal` accepts `type: request`, `request_id`, and an opaque
`grant_id`. Authority comes only from the stored live grant, not from client
identity/device/operation fields. The direct response echoes the request and
original grant IDs and includes either a replacement grant or a generic denial.
The endpoint is for the authenticated, current ordinary Bot runtime, not a
Connector, human client, Wiki runtime or model-facing tool.

Before renewal the server checks the current Bot body lease/runtime credential,
active actor/Bot, conversation access, own-account ownership, selected revision,
explicit-target negotiation, target desktop incarnation, live route, original
operation set and device logout watermark. Only grant ID and expiry change; the
original creation time and all authority remain fixed. Each renewal is at most
the original ten-minute grant duration, and never later than 24 hours from the
initial creation. Pre-upgrade grants use the same creation-based upper bound.
The old grant is not immediately removed: in-flight old requests may finish
within its original deadline. Logout and a selection revision change still
invalidate old and renewed authorization. Logging back in on the same stable
device ID does not revive a pre-logout task.

Authenticated grant-backed thin shell RPC waits are bounded by the requested
deadline, grant expiry and 310 seconds (300 execution + 10 result delivery).
Other tools and old shorter request deadlines retain their prior bounds. A
network timeout is not evidence that a write or shell command did not execute.

Deployment may precede the client PR or follow it: no old client is required to
send the new message, and new clients wait for advertised support. An older
cloud runtime keeps the old ten-minute limitation, even after the server update.
Both sides must be updated for the repair to take effect. Desktop receiver
protocols, account login and updater contracts are not changed. No migration,
global v2 switch or production rollout is performed by these local changes.

Local automated coverage includes renewal across the expiry boundary,
rejection after expiry/revocation/logout/selection changes, default vs explicit
target isolation, live-route and capability checks, and both PR merge orders.
The paired tests use simulated devices/storage, not real-device production E2E.
Real acceptance should use a harmless long task, interruption/reconnection and
logout/relogin, and check that uncertain writes are not blindly replayed.
