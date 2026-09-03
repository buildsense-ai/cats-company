# GPT-Image-2 / OpenAI Images compatibility

This branch exposes the CatsCo image relay with the same public HTTP shape used
by the OpenAI Images API. Clients use a normal OpenAI SDK, a CatsCo relay key as
the Bearer token, and a different `base_url`; no CatsCo-specific SDK fork is
required.

## Public contract

```python
from openai import OpenAI

client = OpenAI(
    api_key="<CatsCo bot relay key>",
    base_url="https://app.catsco.cc/v1",
)

created = client.images.generate(
    model="gpt-image-2",
    prompt="A forest-green ceramic mug on pale travertine",
    size="1024x1024",
    quality="medium",
    output_format="png",
)

with open("source.png", "rb") as source:
    edited = client.images.edit(
        model="gpt-image-2",
        image=source,
        prompt="Keep everything unchanged; make only the mug cobalt blue",
        size="1024x1024",
        quality="medium",
        output_format="png",
    )
```

Routes:

- `POST /v1/images/generations` with the official JSON body.
- `POST /v1/images/edits` with the official multipart body (`image` or
  `image[]`, plus optional `mask`).
- Historical CatsCo JSON data-URL edits remain accepted as an internal
  compatibility transport; they are not required by stock OpenAI clients.

Authentication:

```http
Authorization: Bearer <CatsCo relay key>
```

Provider credentials never reach the client. A provider pool may contain one,
two, or three upstream relays. Multiple configured relays race for the first
valid complete result; a one-provider configuration makes exactly one upstream
submission.

## GPT-Image-2 controls

| Capability | Gateway contract | Verification |
|---|---|---|
| Generate | JSON `/images/generations` | Real SDK + real Relay + real image passed |
| Edit/reference image | Official multipart `/images/edits` | Real SDK + multipart-to-JSON Relay adapter + real image passed |
| Model | `gpt-image-2` only | Backend tests passed |
| Prompt | Up to 32,000 Unicode characters | Backend tests passed |
| Multiple outputs | `n=1..10` | Forwarding and complete-result validation tested |
| Flexible size | `auto` or valid Image2 `WIDTHxHEIGHT` | Validation and forwarding tested |
| Quality | `low`, `medium`, `high`, `auto` | Validation and forwarding tested |
| Background | `transparent`, `opaque`, `auto` | Validation and forwarding tested |
| Format | `png`, `jpeg`, `webp` (`jpg` accepted as compatibility alias) | Validation and forwarding tested |
| Compression | `0..100` for JPEG/WebP | Validation and forwarding tested |
| Moderation | `auto`, `low` | Validation and forwarding tested |
| References | 1–16 PNG/JPEG/WebP files, at most 50 MiB each | Multipart parser and limits tested |
| Mask | One alpha PNG matching the first input dimensions | Parser, validation, and multipart forwarding tested |
| Input fidelity | `high`, `low` on edits | Forwarding tested |
| Streaming | `stream=true`, `partial_images=0..3` | Incremental SSE proxy tested |
| Idempotency | `Idempotency-Key` | Forwarding and race retry behavior tested |
| Responses | OpenAI body plus `Content-Type`, `Retry-After`, `X-Request-Id` | Proxy tests passed |

“Gateway contract verified” means CatsCo accepts, validates, and forwards the
field without replacing it. Each third-party Relay must still implement the
same field correctly. CatsCo cannot make a non-conforming upstream honor a
parameter it ignores.

## Codex-compatible Skill

`skills/imagegen` ports the bundled Codex image generation workflow. The
default model-facing request contains exactly three fields:

```json
{
  "prompt": "string",
  "referenced_image_paths": ["/absolute/path/to/image.png"],
  "num_last_images_to_include": 1
}
```

The image selectors are mutually exclusive. Omit both for a new image; provide
local paths for generation or editing with references. The host-only CLI
arguments are deliberately outside that model-facing schema:

```bash
python skills/imagegen/scripts/invoke_imagegen.py \
  --request /work/request.json \
  --out-dir /work/result
```

`invoke_imagegen.py` selects `generate` when no image is resolved and `edit`
when at least one image is resolved. It delegates to the ported
`scripts/image_gen.py`; it does not reimplement the OpenAI call or add a private
wire protocol.

Exact API controls such as a mask, `n`, format, compression, streaming, or
input fidelity remain available through the bundled explicit-control CLI:

```bash
python skills/imagegen/scripts/image_gen.py --help
```

## Real smoke evidence

On 2026-09-04 (Asia/Shanghai), commit
`8043235c6d8fc30f8d43b70fd08e109ef5e43170` was deployed to the isolated test
stack and executed this path:

```text
stock openai Python SDK
  -> tool-shaped CLI Skill
  -> CatsCo Bearer authentication
  -> new /v1/images/generations or /v1/images/edits route
  -> server-side Relay credential
  -> real Image2 response
```

Results:

- generation: HTTP 200, one upstream submission, 47.6 seconds;
- edit: HTTP 200, one upstream submission, 42.6 seconds;
- one reference image (1,862,798 bytes) was received and forwarded;
- generated and edited PNG files decoded successfully;
- the edit changed the green mug to cobalt blue while retaining the scene,
  lighting, surface, shadow, and framing closely;
- the temporary Bot identity and temporary Relay override were removed, and the
  test stack was restored after the run.

Run: <https://github.com/buildsense-ai/cats-company/actions/runs/33781405923>

Observed upstream caveat: the request asked for `1024x1024`; the older live
Relay returned a square `1254x1254` generation, while its edit result was
`1024x1024`. The new gateway forwarded the requested size unchanged. This is a
third-party output-conformance issue, not a request-schema translation issue.
The gateway currently returns a valid provider result instead of discarding it
solely for this adaptive-resolution difference.

## Deployment boundary

The branch is test-deployed and end-to-end verified. It is not yet the
production `app.catsco.cc` release. Until production is upgraded, an older
production gateway may reject some Bot Bearer keys or official multipart edits.
Do not install the new Skill onto a production XiaoBa Bot and claim zero-config
compatibility before the matching gateway revision is released.
