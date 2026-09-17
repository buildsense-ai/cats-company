import re
from pathlib import Path


CONFIG_PATH = Path(__file__).with_name("nginx") / "nginx.conf"

UNIT_TO_MEGABYTES = {"k": 1 / 1024, "m": 1.0, "g": 1024.0}


def _megabytes(size: str, unit: str) -> float:
    if not unit:
        # nginx treats a bare number as bytes.
        return int(size) / 1024 / 1024
    return int(size) * UNIT_TO_MEGABYTES[unit.lower()]


def main() -> None:
    config = CONFIG_PATH.read_text(encoding="utf-8")
    matches = re.findall(r"client_max_body_size\s+(\d+)\s*([kKmMgG])?\s*;", config)
    if not matches:
        raise AssertionError("missing client_max_body_size in the web container nginx config")
    for size, unit in matches:
        megabytes = _megabytes(size, unit)
        if megabytes < 300:
            raise AssertionError(
                f"web container nginx caps request bodies at {megabytes:g}MB; "
                "uploads must not be rejected below the 300MB product limit"
            )


if __name__ == "__main__":
    main()
