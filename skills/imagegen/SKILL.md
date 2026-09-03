---
name: "imagegen"
description: "Generate or edit raster images when the task benefits from AI-created bitmap visuals such as photos, illustrations, textures, sprites, mockups, or transparent-background cutouts. Use when Codex should create a brand-new image, transform an existing image, or derive visual variants from references, and the output should be a bitmap asset rather than repo-native code or vector. Do not use when the task is better handled by editing existing SVG/vector/code-native assets, extending an established icon or logo system, or building the visual directly in HTML/CSS/canvas."
---

# Image Generation Skill

Generates or edits images for the current project (for example website assets, game assets, UI mockups, product mockups, wireframes, logo design, photorealistic images, or infographics).

## Execution contract

This is a CLI-backed port of the Codex `imagegen` skill. It preserves the
Codex prompt policy, input-image semantics, iteration rules, and tool input
schema, but replaces the privileged built-in `image_gen` tool with local
scripts.

Use one of two entry points:

- **Tool-compatible mode (default):** `scripts/invoke_imagegen.py`. Its request
  JSON has exactly the Codex tool fields `prompt`,
  `referenced_image_paths`, and `num_last_images_to_include`; the two image
  selectors are mutually exclusive. See `schemas/imagegen-tool.schema.json`.
- **Explicit API-control mode:** `scripts/image_gen.py`. It exposes `generate`,
  `edit`, and `generate-batch`, including GPT Image parameters such as model,
  size, quality, `n`, mask, output format, compression, background, moderation,
  and supported input-fidelity controls.

Rules:
- Use tool-compatible mode for ordinary generation, editing, reference-guided
  generation, transparency requests, and variants.
- Use explicit API-control mode only when the user or an upstream workflow
  requests exact API parameters, a mask, multiple outputs in one request, or a
  batch job.
- Do not create one-off image SDK runners. Use the bundled scripts.
- Default to `gpt-image-2`; never silently switch models or provider families.
- Use one tool-compatible invocation per distinct asset or requested variant.
  In explicit mode, `n` means variants of one prompt; distinct assets require
  separate jobs or `generate-batch`.
- Read credentials only from environment variables. Never place a key in a
  prompt, request JSON, command argument, output, or log.

Authentication and routing:
- On XiaoBa/CatsCo, derive the OpenAI-compatible base from
  `CATSCO_HTTP_BASE_URL` and authenticate with the current `CATSCO_API_KEY`.
  Both are sent with the standard OpenAI `Bearer` convention, so no custom
  SDK transport is required. A current user token is supported when no bot key
  exists.
- `CATSCO_IMAGE_API_BASE` may explicitly override the relay base.
- A deliberate direct-provider run may use `IMAGE_GEN_API_BASE` plus
  `OPENAI_API_KEY`.
- The browser and Artifact must never receive any provider or relay key.

Output policy:
- Write each run to a fresh project or work directory and save
  non-destructively.
- If the user names a destination, copy or move the selected output there.
- If the image is project-bound, leave the final file in the workspace.
- Do not overwrite an existing asset unless explicitly requested; otherwise
  create a versioned sibling filename.

Shared prompt guidance lives in `references/prompting.md` and
`references/sample-prompts.md`. Read `references/cli.md` and
`references/image-api.md` for explicit API-control mode.

## When to use
- Generate a new image (concept art, product shot, cover, website hero)
- Generate a new image using one or more reference images for style, composition, or mood
- Edit an existing image (inpainting, lighting or weather transformations, background replacement, object removal, compositing, transparent background)
- Produce many assets or variants for one task

## When not to use
- Extending or matching an existing SVG/vector icon set, logo system, or illustration library inside the repo
- Creating simple shapes, diagrams, wireframes, or icons that are better produced directly in SVG, HTML/CSS, or canvas
- Making a small project-local asset edit when the source file already exists in an editable native format
- Any task where the user clearly wants deterministic code-native output instead of a generated bitmap

## Decision tree

Think about two separate questions:

1. **Intent:** is this a new image or an edit of an existing image?
2. **Execution strategy:** is this one asset or many assets/variants?

Intent:
- If the user wants to modify an existing image while preserving parts of it, treat the request as **edit**.
- If the user provides images only as references for style, composition, mood, or subject guidance, treat the request as **generate**.
- If the user provides no images, treat the request as **generate**.

Image-input semantics:
- For every local target image, visually inspect it before editing when an
  image-reading capability is available.
- For a brand-new image, omit both image selector fields.
- When every target/reference has a local path, use
  `referenced_image_paths` in the intended order.
- Use `num_last_images_to_include` only for conversation images without stable
  paths. Export the visible conversation images into a temporary manifest and
  pass it through the host-only `--conversation-images` option. Use the
  smallest number that includes all target images, at most five.
- Never provide both image selectors. If neither mechanism can include every
  required image, ask the user to attach the missing images again.
- Generate directly without reconfirmation or clarification unless required
  images are missing and must be attached again.
- For edits, preserve invariants aggressively and save non-destructively.

Execution strategy:
- In tool-compatible mode, produce many assets or variants with one invocation
  per asset or variant.
- In explicit mode, use `generate-batch` for many prompts/assets.
- Do not use `n` as a substitute for distinct prompts.

Assume the user wants a new image unless they clearly ask to change an existing one.

## Workflow
1. Use tool-compatible mode by default; choose explicit API-control mode only
   for requested controls that are outside the three-field tool schema.
2. Decide the intent: `generate` or `edit`.
3. Decide whether the output is preview-only or meant to be consumed by the current project.
4. Decide the execution strategy: one invocation per asset/variant, or explicit
   `generate-batch` for many different prompts.
5. Collect inputs up front: prompt(s), exact text (verbatim), constraints/avoid list, and any input images.
6. For every input image, label its role explicitly:
   - reference image
   - edit target
   - supporting insert/style/compositing input
7. Inspect local edit targets when possible, then pass their paths in the same
   order used by the prompt.
8. If the user asked for a raster asset, invoke this Skill rather than
   substituting SVG/HTML/CSS placeholders. Prefer direct native-format editing
   only when the user clearly wants code-native or vector output.
9. Augment the prompt based on specificity:
   - If the user's prompt is already specific and detailed, normalize it into a clear spec without adding creative requirements.
   - If the user's prompt is generic, add tasteful augmentation only when it materially improves output quality.
10. Write the exact tool-shaped request JSON and run:

    ```bash
    python "<SKILL_DIR>/scripts/invoke_imagegen.py" \
      --request "<run-dir>/request.json" \
      --out-dir "<run-dir>" \
      [--conversation-images "<run-dir>/conversation-images.json"]
    ```

11. For transparent-output requests, explicitly request a genuinely transparent
    background in the prompt and preserve the returned alpha channel.
12. Inspect outputs and validate: subject, style, composition, text accuracy, and invariants/avoid items.
13. Iterate with a single targeted change, then re-check.
14. For preview-only work, send or render the generated file immediately.
15. For project-bound work, move or copy the selected artifact from the run directory into the workspace and update any consuming code or references. Never leave a project-referenced asset only in a temporary run directory.
16. For batches or multi-asset requests, persist every requested deliverable final in the workspace unless the user explicitly asked to keep outputs preview-only. Discarded variants do not need to be kept unless requested.
17. In explicit API-control mode, use `scripts/image_gen.py`; do not reimplement
    the API call in shell or another script.
18. Always report the final saved path(s), the final prompt or prompt set, and
    whether tool-compatible or explicit API-control mode was used.

## Transparent image requests

Ask for a genuinely transparent background and preserve its alpha. The
tool-compatible adapter maps explicit transparency wording to
`background=transparent` with PNG output.

## Prompt augmentation

Reformat user prompts into a structured, production-oriented spec. Make the user's goal clearer and more actionable, but do not blindly add detail.

Treat this as prompt-shaping guidance, not a closed schema. Use only the lines that help, and add a short extra labeled line when it materially improves clarity.

### Specificity policy

Use the user's prompt specificity to decide how much augmentation is appropriate:

- If the prompt is already specific and detailed, preserve that specificity and only normalize/structure it.
- If the prompt is generic, you may add tasteful augmentation when it will materially improve the result.

Allowed augmentations:
- composition or framing hints
- polish level or intended-use hints
- practical layout guidance
- reasonable scene concreteness that supports the stated request

Not allowed augmentations:
- extra characters or objects that are not implied by the request
- brand names, slogans, palettes, or narrative beats that are not implied
- arbitrary side-specific placement unless the surrounding layout supports it

## Use-case taxonomy (exact slugs)

Classify each request into one of these buckets and keep the slug consistent across prompts and references.

Generate:
- photorealistic-natural — candid/editorial lifestyle scenes with real texture and natural lighting.
- product-mockup — product/packaging shots, catalog imagery, merch concepts.
- ui-mockup — app/web interface mockups and wireframes; specify the desired fidelity.
- infographic-diagram — diagrams/infographics with structured layout and text.
- scientific-educational — classroom explainers, scientific diagrams, and learning visuals with required labels and accuracy constraints.
- ads-marketing — campaign concepts and ad creatives with audience, brand position, scene, and exact tagline/copy.
- productivity-visual — slide, chart, workflow, and data-heavy business visuals.
- logo-brand — logo/mark exploration, vector-friendly.
- illustration-story — comics, children’s book art, narrative scenes.
- stylized-concept — style-driven concept art, 3D/stylized renders.
- historical-scene — period-accurate/world-knowledge scenes.

Edit:
- text-localization — translate/replace in-image text, preserve layout.
- identity-preserve — try-on, person-in-scene; lock face/body/pose.
- precise-object-edit — remove/replace a specific element (including interior swaps).
- lighting-weather — time-of-day/season/atmosphere changes only.
- background-extraction — transparent background / clean cutout. Request actual transparency and preserve alpha.
- style-transfer — apply reference style while changing subject/scene.
- compositing — multi-image insert/merge with matched lighting/perspective.
- sketch-to-render — drawing/line art to photoreal render.

## Shared prompt schema

Use the following labeled spec as shared prompt scaffolding for both top-level modes:

```text
Use case: <taxonomy slug>
Asset type: <where the asset will be used>
Primary request: <user's main prompt>
Input images: <Image 1: role; Image 2: role> (optional)
Scene/backdrop: <environment>
Subject: <main subject>
Style/medium: <photo/illustration/3D/etc>
Composition/framing: <wide/close/top-down; placement>
Lighting/mood: <lighting + mood>
Color palette: <palette notes>
Materials/textures: <surface details>
Text (verbatim): "<exact text>"
Constraints: <must keep/must avoid>
Avoid: <negative constraints>
```

Notes:
- `Asset type` and `Input images` are prompt scaffolding, not dedicated CLI flags.
- `Scene/backdrop` refers to the visual setting. It is not the same as the explicit API `background` parameter, which controls output transparency behavior.
- `Quality:`, `Input fidelity:`, masks, output format, and output paths belong in explicit API-control mode. They are not fields in the three-field tool-compatible schema.

Augmentation rules:
- Keep it short.
- Add only the details needed to improve the prompt materially.
- For edits, explicitly list invariants (`change only X; keep Y unchanged`).
- If any critical detail is missing and blocks success, ask a question; otherwise proceed.

## Examples

### Generation example (hero image)
```text
Use case: product-mockup
Asset type: landing page hero
Primary request: a minimal hero image of a ceramic coffee mug
Style/medium: clean product photography
Composition/framing: wide composition with usable negative space for page copy if needed
Lighting/mood: soft studio lighting
Constraints: no logos, no text, no watermark
```

### Edit example (invariants)
```text
Use case: precise-object-edit
Asset type: product photo background replacement
Primary request: replace only the background with a warm sunset gradient
Constraints: change only the background; keep the product and its edges unchanged; no text; no watermark
```

## Prompting best practices
- Structure prompt as scene/backdrop -> subject -> details -> constraints.
- Include intended use (ad, UI mock, infographic) to set the mode and polish level.
- Use camera/composition language for photorealism.
- Only use SVG/vector stand-ins when the user explicitly asked for vector output or a non-image placeholder.
- Quote exact text and specify typography + placement.
- For tricky words, spell them letter-by-letter and require verbatim rendering.
- For multi-image inputs, reference images by index and describe how they should be used.
- For edits, repeat invariants every iteration to reduce drift.
- Iterate with single-change follow-ups.
- If the prompt is generic, add only the extra detail that will materially help.
- If the prompt is already detailed, normalize it instead of expanding it.
- For explicit API-control mode, see `references/cli.md` and `references/image-api.md` for model, `quality`, `input_fidelity`, masks, output format, and output-path guidance.
- For transparent images, request actual transparency and preserve its alpha.

More principles shared by both modes: `references/prompting.md`.
Copy/paste specs shared by both modes: `references/sample-prompts.md`.

## Guidance by asset type
Asset-type templates (website assets, game assets, wireframes, logo) are consolidated in `references/sample-prompts.md`.

## gpt-image-2 guidance for explicit API-control mode

The CLI defaults to `gpt-image-2`.

- Use `gpt-image-2` for new CLI/API workflows unless the user confirms a different model.
- Current GPT-Image-2 supports `background=transparent` in preview. Use PNG or WebP when alpha is required.
- In explicit API-control mode, use `input_fidelity=high` for identity-, typography-, layout-, or product-sensitive edits and `low` only for looser reinterpretation.
- `gpt-image-2` supports `quality` values `low`, `medium`, `high`, and `auto`.
- Use `quality low` for fast drafts, thumbnails, and quick iterations. Use `medium`, `high`, or `auto` for final assets, dense text, diagrams, identity-sensitive edits, or high-resolution outputs.
- Square images are typically fastest to generate. Use `1024x1024` for fast square drafts.
- If the user asks for 4K-style output, use `3840x2160` for landscape or `2160x3840` for portrait.
- `gpt-image-2` size may be `auto` or `WIDTHxHEIGHT` if all constraints hold: max edge `<= 3840px`, both edges multiples of `16px`, long-to-short ratio `<= 3:1`, total pixels between `655,360` and `8,294,400`.

Popular `gpt-image-2` sizes:
- `1024x1024` square
- `1536x1024` landscape
- `1024x1536` portrait
- `2048x2048` 2K square
- `2048x1152` 2K landscape
- `3840x2160` 4K landscape
- `2160x3840` 4K portrait
- `auto`

## Explicit API-control mode

### Temp and output conventions
These conventions apply to direct use of `scripts/image_gen.py`.
- Use `tmp/imagegen/` for intermediate files (for example JSONL batches); delete them when done.
- Write final artifacts under `output/imagegen/`.
- Use `--out` or `--out-dir` to control output paths; keep filenames stable and descriptive.

### Dependencies
Prefer `uv` for dependency management in this repo.

Required Python package:
```bash
uv pip install -r "<SKILL_DIR>/requirements.txt"
```

Portability note:
- If you are using the installed skill outside this repo, install dependencies into that environment with its package manager.
- In uv-managed environments, `uv pip install ...` remains the preferred path.

### Environment
- On CatsCo, use the runtime-provided `CATSCO_API_KEY` and `CATSCO_HTTP_BASE_URL`.
- For a deliberate direct OpenAI call, set `OPENAI_API_KEY` and optionally `IMAGE_GEN_API_BASE`.
- Never ask the user to paste the full key in chat. Ask them to set it locally and confirm when ready.

If the key is missing, give the user these steps:
1. Create an API key in the OpenAI platform UI: https://platform.openai.com/api-keys
2. Set `OPENAI_API_KEY` as an environment variable in their system.
3. Offer to guide them through setting the environment variable for their OS/shell if needed.

If installation is not possible in this environment, tell the user which dependency is missing and how to install it into their active environment.

### Script-mode notes
- CLI commands + examples: `references/cli.md`
- API parameter quick reference: `references/image-api.md`
- Network approvals / sandbox settings for CLI mode: `references/codex-network.md`

## Reference map
- `references/prompting.md`: shared prompting principles for both modes.
- `references/sample-prompts.md`: shared copy/paste prompt recipes for both modes.
- `schemas/imagegen-tool.schema.json`: exact tool-compatible input schema.
- `scripts/invoke_imagegen.py`: default tool-shaped CLI adapter.
- `references/cli.md`: explicit API-control usage via `scripts/image_gen.py`.
- `references/image-api.md`: API/CLI parameter reference.
- `references/codex-network.md`: network/sandbox troubleshooting.
- `references/catsco-relay.md`: required OpenAI-compatible relay behavior and
  deployment variables.
- `references/tool-contract.md`: the preserved model-facing tool contract and
  host-side CLI translation.
- `scripts/image_gen.py`: full GPT Image CLI backend.
