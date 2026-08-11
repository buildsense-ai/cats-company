# Cats Company OpenAI Relay Adapter

This directory is the source of truth for the OpenAI-compatible adapter running
in front of the production Bifrost relay. The initial source snapshot was read
from `/srv/cats-bifrost/adapter/openai_adapter.py` with SHA-256:

`3b68aea57e584af4ac79e878d96c868c9d117a4f5bef8bda4aaa2243c275c962`

The adapter owns provider affinity, bounded failover, provider circuit state,
and conversion of an exhausted provider pool into a retryable OpenAI-compatible
`503 provider_pool_unavailable` response. Caller authentication and budget
errors are returned by relay-admin preflight and never affect provider circuits.

Run the focused suite with:

```bash
python3 -m unittest discover -s relay/tests -p 'test_*.py'
```

Production changes must go through the repository workflow. Do not edit the
runtime file on the relay host directly.

## Production deployment

`Relay Adapter CI` validates changes under `relay/` and `deploy/relay/`. After
a successful run from this repository's `main` branch, `Deploy Relay Adapter
Prod` deploys the exact tested Git revision through the protected `relay-prod`
environment.

The environment requires these secrets:

- `RELAY_SSH_HOST`: production Relay hostname or IP address.
- `RELAY_SSH_USER`: restricted deployment account.
- `RELAY_SSH_PRIVATE_KEY`: private key for that account.
- `RELAY_SSH_KNOWN_HOSTS`: pre-verified OpenSSH known-hosts line for the Relay.

The deployment account must be able to write under `/srv/cats-bifrost` and run
`sudo -n systemctl restart cats-openai-adapter.service`. No credentials,
Provider Pool configuration, Bifrost files, systemd units, or environment files
are uploaded by this workflow.

The remote deployment script compiles the versioned source, atomically replaces
the adapter, restarts the service, and checks its health endpoint. A failed
health check restores the immediately preceding source before the workflow
fails. The workflow does not perform a second rollback.

For an operator-approved rollback, run the repository-managed script on the
Relay host:

```bash
/srv/cats-bifrost/deploy/remote-rollback.sh /srv/cats-bifrost
```

It restores the recorded predecessor, verifies health, and updates
`CURRENT_ADAPTER_REVISION` so status output remains accurate. The initial
SHA-256 above records source provenance at import time; subsequent deployed
versions are identified by their 40-character Git revision.
