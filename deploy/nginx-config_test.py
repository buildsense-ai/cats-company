from pathlib import Path
import re


CONFIG_PATH = Path(__file__).with_name("nginx") / "nginx.conf"


def main() -> None:
    config = CONFIG_PATH.read_text(encoding="utf-8")
    match = re.search(r"client_max_body_size\s+(\d+)\s*([kKmM])?\s*;", config)
    if not match:
        raise AssertionError("missing client_max_body_size in the web container nginx config")
    size = int(match.group(1))
    unit = (match.group(2) or "").lower()
    megabytes = size / 1024 if unit == "k" else size
    if megabytes < 300:
        raise AssertionError(
            f"web container nginx caps request bodies at {megabytes:g}MB; "
            "uploads must not be rejected below the 300MB product limit"
        )


if __name__ == "__main__":
    main()
