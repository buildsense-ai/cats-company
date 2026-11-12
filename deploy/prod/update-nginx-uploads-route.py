#!/usr/bin/env python3
"""Replace legacy app-vhost upload locations with the managed snippet include."""

from __future__ import annotations

import argparse
import os
import re
import tempfile
from pathlib import Path


_INCLUDE = re.compile(
    r"^[ \t]*include[ \t]+/etc/nginx/snippets/uploads-zos\.conf;[ \t]*$",
    re.MULTILINE,
)
_LOCATION = re.compile(
    r"^(?P<indent>[ \t]*)location[ \t]+(?:\^~[ \t]+)?/uploads/[ \t]*\{",
    re.MULTILINE,
)
_NAMED_LOCATION = re.compile(
    r"^(?P<indent>[ \t]*)location[ \t]+(?P<name>@uploads_(?:authenticated|fallback))[ \t]*\{",
    re.MULTILINE,
)
_INCLUDE_LINE = "include /etc/nginx/snippets/uploads-zos.conf;"


def _block_end(text: str, opening: int) -> int:
    depth = 0
    quote: str | None = None
    escaped = False
    comment = False
    for index in range(opening, len(text)):
        char = text[index]
        if comment:
            if char == "\n":
                comment = False
            continue
        if quote:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == quote:
                quote = None
            continue
        if char == "#":
            comment = True
        elif char in ('"', "'"):
            quote = char
        elif char == "{":
            depth += 1
        elif char == "}":
            depth -= 1
            if depth == 0:
                return index + 1
    raise ValueError("unterminated upload location")


def _span(text: str, match: re.Match[str]) -> tuple[int, int]:
    opening = text.find("{", match.start(), match.end())
    end = _block_end(text, opening)
    if end < len(text) and text[end] == "\n":
        end += 1
    return match.start(), end


def migrate_text(text: str) -> tuple[str, bool]:
    includes = list(_INCLUDE.finditer(text))
    locations = list(_LOCATION.finditer(text))
    named = list(_NAMED_LOCATION.finditer(text))
    names = [match.group("name") for match in named]

    if len(includes) == 1 and not locations and not named:
        return text, False
    if includes or len(locations) != 1 or len(names) != len(set(names)):
        raise ValueError(
            "expected one /uploads/ location or one managed include, with no duplicate managed routes"
        )

    main = locations[0]
    indent = main.group("indent")
    replacement = f"{indent}# Upload routing is managed by the compatibility snippet.\n{indent}{_INCLUDE_LINE}\n"
    changes = [(_span(text, main), replacement)]
    changes.extend((_span(text, match), "") for match in named)

    updated = text
    for (start, end), value in sorted(changes, reverse=True):
        updated = updated[:start] + value + updated[end:]
    return updated, True


def update_file(path: Path) -> bool:
    original = path.read_text(encoding="utf-8")
    updated, changed = migrate_text(original)
    if not changed:
        return False

    mode = path.stat().st_mode
    fd, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="") as output:
            output.write(updated)
        os.chmod(temporary_name, mode)
        os.replace(temporary_name, path)
    finally:
        if os.path.exists(temporary_name):
            os.unlink(temporary_name)
    return True


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("vhost", type=Path)
    args = parser.parse_args()
    try:
        changed = update_file(args.vhost)
    except (OSError, ValueError) as error:
        parser.error(str(error))
    print("migrated inline upload routes" if changed else "managed uploads include already present")


if __name__ == "__main__":
    main()
