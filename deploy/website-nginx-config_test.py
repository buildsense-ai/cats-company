from pathlib import Path
import re


CONFIG_PATH = Path(__file__).with_name("website-nginx.conf")


def main() -> None:
    config = CONFIG_PATH.read_text(encoding="utf-8")
    media_location = re.search(
        r"location\s+~\*\s+\^/\[\^/\]\+\\\.\(\?:([^)]*)\)\$\s*\{(?P<body>[^}]*)\}",
        config,
        re.DOTALL,
    )
    if not media_location:
        raise AssertionError("missing root-level public media location")

    extensions = set(media_location.group(1).split("|"))
    public_root = CONFIG_PATH.parent.parent / "website" / "public"
    public_suffixes = {
        path.suffix.lower().lstrip(".")
        for path in public_root.iterdir()
        if path.is_file() and path.suffix
    }
    for suffix in public_suffixes:
        expected = "jpe?g" if suffix in {"jpg", "jpeg"} else suffix
        if expected not in extensions:
            raise AssertionError(
                f"root-level public media location does not serve .{suffix} files"
            )

    body = media_location.group("body")
    if "root /opt/catsco-website-public;" not in body:
        raise AssertionError("public media must use the mounted website-public root")
    if "try_files $uri =404;" not in body:
        raise AssertionError("public media must fail closed instead of falling back to index.html")


if __name__ == "__main__":
    main()
