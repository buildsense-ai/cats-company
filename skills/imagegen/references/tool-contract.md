# Codex imagegen tool-compatible contract

The model-facing request intentionally preserves the Codex image-generation
tool surface. It contains only:

```json
{
  "prompt": "string",
  "referenced_image_paths": ["absolute-or-request-relative-path"],
  "num_last_images_to_include": 1
}
```

Rules:

- `prompt` is required.
- Omit both image selectors for a brand-new image.
- Use `referenced_image_paths` when every target/reference image has a local
  path. Preserve the prompt's image-role order.
- If a local image has not been inspected yet, inspect it before editing.
- Use `num_last_images_to_include` only when one or more required conversation
  images do not have stable local paths. Use the smallest number that includes
  all targets, from 1 through 5.
- Never provide both image selectors.
- If neither mechanism can include every required image, ask the user to attach
  the missing image again.
- Do not reconfirm an otherwise complete image request.
- Use this image-generation path for raster editing unless the user explicitly
  asks for a different mechanism.

`invoke_imagegen.py` translates this request into the same generate-versus-edit
decision:

- no resolved images -> `image_gen.py generate`
- one or more resolved images -> `image_gen.py edit`

`--request`, `--out-dir`, and `--conversation-images` are host execution
arguments, not extra model-facing tool fields. The host exports visible
conversation images as local paths before invoking the CLI; the Skill never
guesses paths or reads browser credentials.

The CLI result is:

```json
{
  "ok": true,
  "image_path": "/absolute/path/to/image.png",
  "output_hint": "Generated with the CatsCo imagegen CLI adapter",
  "mode": "generate|edit",
  "reference_count": 0
}
```

The host should render or send `image_path` as the generated image result.
