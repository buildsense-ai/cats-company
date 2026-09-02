# Worker release retention timer

`catsco-worker-release-prune.timer` runs the server container's
`prune-worker-releases.sh --apply` once per day. The script lists the dedicated
worker artifact bucket, keeps the newest three application releases, and always
protects application versions reported by `status-worker.sh`.

The timer passes the explicit deletion confirmation, while the TOS credentials
remain in the running server container's owner-only production environment.
The service fails closed if the credentials, status probe, or bucket listing is
not usable. It never touches worker images or files under `/opt/catsco/releases`.
