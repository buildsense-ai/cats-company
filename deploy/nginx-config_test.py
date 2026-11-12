import re
from pathlib import Path


CONFIG_PATH = Path(__file__).with_name("nginx") / "nginx.conf"

UNIT_TO_MEGABYTES = {"k": 1 / 1024, "m": 1.0, "g": 1024.0}


def _megabytes(size: str, unit: str) -> float:
    if not unit:
        # nginx treats a bare number as bytes.
        return int(size) / 1024 / 1024
    return int(size) * UNIT_TO_MEGABYTES[unit.lower()]


def _assert_live_api_resolution(config: str) -> None:
    if "resolver 127.0.0.11 valid=10s ipv6=off;" not in config:
        raise AssertionError(
            "web container nginx must resolve the API container through Docker DNS per request"
        )
    if "upstream api {" in config:
        raise AssertionError(
            "a static upstream pins nginx to the previous API address; the follow-up web "
            "recreate it forces is a large part of the deploy 502 window"
        )
    if "set $api_upstream http://server:6061;" not in config:
        raise AssertionError("missing the $api_upstream definition")
    for line in config.splitlines():
        stripped = line.strip()
        if stripped.startswith("proxy_pass") and "$api_upstream" not in stripped:
            raise AssertionError(
                f"proxy_pass must go through $api_upstream for live DNS resolution: {stripped}"
            )


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
    _assert_live_api_resolution(config)


if __name__ == "__main__":
    main()
