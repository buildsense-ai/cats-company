#!/usr/bin/env python3
"""Tool-shaped CLI adapter for the Codex-compatible CatsCo imagegen skill.

The JSON request intentionally has the same three fields as Codex's built-in
image generation tool.  Filesystem/output controls are process arguments, not
part of the model-facing tool schema.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
from typing import Any


MAX_REFERENCES = 16
MAX_LAST_IMAGES = 5
ALLOWED_FIELDS = {
    "prompt",
    "referenced_image_paths",
    "num_last_images_to_include",
}


def fail(message: str) -> None:
    print(json.dumps({"ok": False, "error": message}, ensure_ascii=False))
    raise SystemExit(2)


def read_request(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"invalid request JSON: {exc}")
    if not isinstance(value, dict):
        fail("request must be a JSON object")
    unknown = set(value) - ALLOWED_FIELDS
    if unknown:
        fail("unsupported request field(s): " + ", ".join(sorted(unknown)))
    prompt = value.get("prompt")
    if not isinstance(prompt, str) or not prompt.strip():
        fail("prompt is required")
    paths = value.get("referenced_image_paths")
    recent = value.get("num_last_images_to_include")
    if paths is not None and recent is not None:
        fail(
            "referenced_image_paths and num_last_images_to_include are mutually exclusive"
        )
    if paths is not None:
        if (
            not isinstance(paths, list)
            or not paths
            or len(paths) > MAX_REFERENCES
            or not all(isinstance(item, str) and item.strip() for item in paths)
        ):
            fail("referenced_image_paths must contain 1-16 non-empty paths")
    if recent is not None:
        if isinstance(recent, bool) or not isinstance(recent, int):
            fail("num_last_images_to_include must be an integer")
        if not 1 <= recent <= MAX_LAST_IMAGES:
            fail("num_last_images_to_include must be between 1 and 5")
    return value


def read_conversation_images(path: Path) -> list[str]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"invalid conversation image manifest: {exc}")
    if isinstance(value, dict):
        value = value.get("images")
    if not isinstance(value, list):
        fail("conversation image manifest must be an array or {images: [...]}")
    resolved: list[str] = []
    for item in value:
        if isinstance(item, str):
            candidate = item
        elif isinstance(item, dict) and isinstance(item.get("path"), str):
            candidate = item["path"]
        else:
            fail("every conversation image entry must be a path or {path: string}")
        if candidate.strip():
            resolved.append(candidate)
    return resolved


def resolve_references(
    request: dict[str, Any], conversation_images: Path | None, request_dir: Path
) -> list[Path]:
    raw_paths = request.get("referenced_image_paths")
    recent = request.get("num_last_images_to_include")
    if recent is not None:
        if conversation_images is None:
            fail(
                "num_last_images_to_include requires --conversation-images; "
                "the host must export visible conversation images as local paths"
            )
        available = read_conversation_images(conversation_images)
        if len(available) < recent:
            fail(
                f"requested {recent} recent image(s), but only {len(available)} are available"
            )
        raw_paths = available[-recent:]
    if raw_paths is None:
        return []
    references: list[Path] = []
    for raw in raw_paths:
        candidate = Path(raw).expanduser()
        path = (
            candidate.resolve()
            if candidate.is_absolute()
            else (request_dir / candidate).resolve()
        )
        if not path.is_file():
            fail(f"referenced image not found: {path}")
        references.append(path)
    return references


def wants_transparency(prompt: str) -> bool:
    normalized = prompt.lower()
    return any(
        phrase in normalized
        for phrase in (
            "transparent background",
            "transparent-background",
            "genuinely transparent",
            "alpha channel",
            "透明背景",
            "透明底",
            "保留透明",
        )
    )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Invoke CatsCo imagegen with the Codex built-in tool schema"
    )
    parser.add_argument("--request", required=True)
    parser.add_argument("--out-dir", required=True)
    parser.add_argument("--conversation-images")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    request_path = Path(args.request).resolve()
    request = read_request(request_path)
    conversation_manifest = (
        Path(args.conversation_images).resolve() if args.conversation_images else None
    )
    references = resolve_references(request, conversation_manifest, request_path.parent)

    output_dir = Path(args.out_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    output = output_dir / "image.png"
    backend = Path(__file__).with_name("image_gen.py")
    command = [
        sys.executable,
        str(backend),
        "edit" if references else "generate",
        "--prompt",
        request["prompt"].strip(),
        "--model",
        os.getenv("IMAGE_GEN_MODEL", "gpt-image-2"),
        "--size",
        os.getenv("IMAGE_GEN_DEFAULT_SIZE", "auto"),
        "--quality",
        os.getenv("IMAGE_GEN_DEFAULT_QUALITY", "medium"),
        "--output-format",
        "png",
        "--no-augment",
        "--out",
        str(output),
    ]
    if wants_transparency(request["prompt"]):
        command.extend(["--background", "transparent"])
    for reference in references:
        command.extend(["--image", str(reference)])
    if args.dry_run:
        command.append("--dry-run")

    completed = subprocess.run(command, check=False)
    if completed.returncode != 0:
        fail(f"image generation backend exited with code {completed.returncode}")
    if args.dry_run:
        print(
            json.dumps(
                {
                    "ok": True,
                    "dry_run": True,
                    "mode": "edit" if references else "generate",
                    "reference_count": len(references),
                },
                ensure_ascii=False,
            )
        )
        return 0
    if not output.is_file() or output.stat().st_size == 0:
        fail("image generation completed without a usable output file")
    print(
        json.dumps(
            {
                "ok": True,
                "image_path": str(output),
                "output_hint": "Generated with the CatsCo imagegen CLI adapter",
                "mode": "edit" if references else "generate",
                "reference_count": len(references),
            },
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
