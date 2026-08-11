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
