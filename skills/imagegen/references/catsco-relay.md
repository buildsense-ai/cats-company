# CatsCo GPT-Image-2 relay contract

The Skill expects an OpenAI-compatible Images API at a base URL ending in
`/v1`.

Authentication follows the official OpenAI client convention:

```http
Authorization: Bearer <CATSCO_API_KEY>
```

After the compatible gateway version is deployed, a stock OpenAI SDK client
needs only the CatsCo base URL and bot relay key; it does not need custom
headers or a CatsCo-specific transport adapter. The historical
`Authorization: ApiKey ...` form remains accepted for older CatsCo clients.

Required routes:

- `POST /v1/images/generations` using the official JSON request body.
- `POST /v1/images/edits` using the official multipart body with repeated
  `image` or `image[]` parts and an optional `mask` part.

Required GPT-Image-2 controls:

- `model=gpt-image-2`
- `prompt`
- `n` from 1 through 10
- `size=auto` or a valid flexible `WIDTHxHEIGHT`
- `quality=low|medium|high|auto`
- `background=transparent|opaque|auto`
- `output_format=png|jpeg|webp`
- `output_compression` from 0 through 100
- `moderation=auto|low`
- up to 16 edit images, each under 50 MiB
- optional PNG alpha mask matching the first image dimensions
- `input_fidelity=high|low` on edits
- `stream` and `partial_images` from 0 through 3

The relay preserves an explicit `input_fidelity=high|low` value on edits and
does not replace it with a hidden provider default.

Compatibility rules:

- Preserve requested parameters. Do not force `n=1`, replace an exact size with
  a ratio label, or silently change quality/output format/background.
- A provider-specific upstream model alias may be configured internally, but a
  client request for another model must not silently become GPT-Image-2.
- The public OpenAI-compatible route must default to Image2 server-side and
  must never require a CatsCo-only request header or silently fall back to
  another model family.
- A non-streaming race winner is valid only after all requested `n` images are
  present and decodable.
- Streaming responses proxy `text/event-stream` incrementally.
- Forward `Idempotency-Key` unchanged so a host retry can remain the same
  logical paid request.
- Preserve OpenAI-compatible response bodies, usage data, errors,
  `Content-Type`, `Retry-After`, and `X-Request-Id`.
- Keep provider credentials server-side. Never return or log them.

Runtime variables used by the Skill:

```text
CATSCO_HTTP_BASE_URL=https://app.catsco.cc
CATSCO_API_KEY=<current bot relay key>
IMAGE_GEN_MODEL=gpt-image-2
IMAGE_GEN_DEFAULT_SIZE=auto
IMAGE_GEN_DEFAULT_QUALITY=medium
```

Use `CATSCO_IMAGE_API_BASE` only when the Images API has a different base URL.
Use `OPENAI_API_KEY` and `IMAGE_GEN_API_BASE` only for a deliberate direct
provider run.

Equivalent stock OpenAI SDK setup:

```python
from openai import OpenAI

client = OpenAI(
    api_key="<CatsCo bot relay key>",
    base_url="https://app.catsco.cc/v1",
)
```
