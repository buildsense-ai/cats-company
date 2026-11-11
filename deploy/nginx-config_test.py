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


def _location_block(config: str, marker: str) -> str:
    start = config.find(marker)
    if start < 0:
        raise AssertionError(f"missing Nginx location: {marker}")
    opening = config.find("{", start)
    depth = 0
    for index in range(opening, len(config)):
        if config[index] == "{":
            depth += 1
        elif config[index] == "}":
            depth -= 1
            if depth == 0:
                return config[start:index + 1]
    raise AssertionError(f"unterminated Nginx location: {marker}")


def _assert_attachment_compat_route(config_path: Path) -> None:
    config = config_path.read_text(encoding="utf-8")
    legacy = _location_block(config, "location ^~ /uploads/ {")
    authenticated = _location_block(config, "location @uploads_authenticated {")
    fallback = _location_block(config, "location @uploads_fallback {")

    if 'if ($http_authorization != "") { return 419; }' not in legacy:
        raise AssertionError("authenticated attachment requests must leave the legacy ZOS route")
    if "error_page 419 = @uploads_authenticated;" not in legacy:
        raise AssertionError("authenticated requests must route to the app location")
    if "proxy_pass http://100.86.2.1/catscompany$request_uri;" not in legacy:
        raise AssertionError("unauthenticated legacy attachment requests must retain the ZOS route")
    if "proxy_intercept_errors on;" not in legacy:
        raise AssertionError("legacy ZOS error fallback behavior must remain enabled")

    if "proxy_pass http://127.0.0.1:28080$request_uri;" not in authenticated:
        raise AssertionError("authenticated attachment requests must reach the app")
    if "proxy_set_header Authorization $http_authorization;" not in authenticated:
        raise AssertionError("the app must receive the client's authorization header")
    if "proxy_intercept_errors off;" not in authenticated or "error_page" in authenticated:
        raise AssertionError("app authorization errors must not fall back to ZOS")
    if "100.86.2.1" in authenticated:
        raise AssertionError("authenticated route must not reach ZOS")
    if "proxy_pass http://127.0.0.1:28080$request_uri;" not in fallback:
        raise AssertionError("legacy ZOS fallback must continue to use the app")


def _assert_tencent_attachment_compat_routes() -> None:
    nginx_dir = Path(__file__).with_name("tencent") / "nginx"
    for name in ("catscompany-app.conf", "catscompany-app-cn.conf"):
        _assert_attachment_compat_route(nginx_dir / name)



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
    _assert_tencent_attachment_compat_routes()


if __name__ == "__main__":
    main()
