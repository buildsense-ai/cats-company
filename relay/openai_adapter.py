#!/usr/bin/env python3
"""OpenAI SDK adapter in front of Bifrost.

Bifrost owns provider keys and raw passthrough. This adapter owns CatsCo's
product-facing concerns for OpenAI-compatible clients:

- choose the Bifrost provider alias from the public model name;
- keep /v1/chat/completions on the OpenAI-compatible provider family by
  default, independent from the Anthropic-compatible /anthropic adapter;
- keep the old OpenAI-to-Anthropic bridge available only as an explicit
  operator escape hatch for broken upstream providers;
- run relay-admin model budget preflight;
- record passthrough usage so CatsCo user pages can show OpenAI SDK usage;
- proxy OpenAI Responses SSE incrementally so clients receive standard typed
  events while the model is still generating.
"""

from __future__ import annotations

import json
import logging
import os
import queue
import random
import re
import hashlib
import hmac
import threading
import time
import uuid
import urllib.error
import urllib.request
from copy import deepcopy
from dataclasses import dataclass
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, BinaryIO, Callable, Iterable


HOST = os.environ.get("CATS_OPENAI_ADAPTER_HOST", "127.0.0.1")
PORT = int(os.environ.get("CATS_OPENAI_ADAPTER_PORT", "18091"))
BIFROST_BASE_URL = os.environ.get("BIFROST_INTERNAL_URL", "http://127.0.0.1:18088")
TIMEOUT = float(os.environ.get("CATS_OPENAI_ADAPTER_TIMEOUT", "300"))
RESPONSES_STREAM_READ_SIZE = max(
    1024,
    int(os.environ.get("CATS_OPENAI_RESPONSES_STREAM_READ_SIZE", "16384")),
)
LOG_LEVEL = os.environ.get("CATS_OPENAI_ADAPTER_LOG_LEVEL", "INFO").upper()
PROVIDER_POOL_COOLDOWN_SECONDS = max(
    0.0,
    float(os.environ.get("CATS_OPENAI_PROVIDER_POOL_COOLDOWN_SECONDS", "300")),
)
PROVIDER_POOL_SLOT_WAIT_SECONDS = max(
    0.0,
    float(os.environ.get("CATS_OPENAI_PROVIDER_POOL_SLOT_WAIT_SECONDS", "10")),
)
PROVIDER_POOL_RETRY_AFTER_SECONDS = max(
    1,
    int(os.environ.get("CATS_OPENAI_PROVIDER_POOL_RETRY_AFTER_SECONDS", "2")),
)
PROVIDER_POOL_RETRY_AFTER_JITTER_SECONDS = max(
    0,
    int(os.environ.get("CATS_OPENAI_PROVIDER_POOL_RETRY_AFTER_JITTER_SECONDS", "2")),
)
PROVIDER_POOL_RATE_LIMIT_COOLDOWN_SECONDS = max(
    0.0,
    float(os.environ.get("CATS_OPENAI_PROVIDER_POOL_RATE_LIMIT_COOLDOWN_SECONDS", "30")),
)
PROVIDER_POOL_TRANSIENT_COOLDOWN_SECONDS = max(
    0.0,
    float(os.environ.get("CATS_OPENAI_PROVIDER_POOL_TRANSIENT_COOLDOWN_SECONDS", "30")),
)
PROVIDER_POOL_TIMEOUT_COOLDOWN_SECONDS = max(
    0.0,
    float(os.environ.get("CATS_OPENAI_PROVIDER_POOL_TIMEOUT_COOLDOWN_SECONDS", "60")),
)
PROVIDER_POOL_TRANSIENT_FAILURE_THRESHOLD = max(
    1,
    int(os.environ.get("CATS_OPENAI_PROVIDER_POOL_TRANSIENT_FAILURE_THRESHOLD", "2")),
)
PROVIDER_POOL_EARLY_PROBE_INTERVAL_SECONDS = max(
    0.0,
    float(os.environ.get("CATS_OPENAI_PROVIDER_POOL_EARLY_PROBE_INTERVAL_SECONDS", "5")),
)
PROVIDER_POOL_RECOVERY_WAIT_SECONDS = min(
    4.0,
    max(
        0.0,
        float(os.environ.get("CATS_OPENAI_PROVIDER_POOL_RECOVERY_WAIT_SECONDS", "5")),
    ),
)
PROVIDER_POOL_AFFINITY_ACTIVE_SKEW = max(
    0,
    int(os.environ.get("CATS_OPENAI_PROVIDER_AFFINITY_ACTIVE_SKEW", "0")),
)
PROVIDER_SOFT_LOAD_BALANCING_ENABLED = os.environ.get(
    "CATS_OPENAI_PROVIDER_SOFT_LOAD_BALANCING_ENABLED",
    "1",
).strip().lower() in {"1", "true", "yes", "on"}
PROVIDER_COORDINATED_RECOVERY_ENABLED = os.environ.get(
    "CATS_OPENAI_PROVIDER_COORDINATED_RECOVERY_ENABLED",
    "1",
).strip().lower() in {"1", "true", "yes", "on"}
PROVIDER_CONCURRENCY_LIMITS_ENABLED = os.environ.get(
    "CATS_OPENAI_PROVIDER_CONCURRENCY_LIMITS_ENABLED",
    "0",
).strip().lower() in {"1", "true", "yes", "on"}
CLIENT_POOL_MAX_CONCURRENCY = max(
    0,
    int(os.environ.get("CATS_OPENAI_CLIENT_MAX_CONCURRENCY", "0")),
)
CLIENT_POOL_SLOT_WAIT_SECONDS = max(
    0.0,
    float(os.environ.get("CATS_OPENAI_CLIENT_SLOT_WAIT_SECONDS", "2")),
)
RELAY_ADMIN_URL = os.environ.get("CATS_RELAY_ADMIN_URL", "http://127.0.0.1:18090").rstrip("/")
RELAY_ADMIN_TOKEN = os.environ.get("CATS_RELAY_ADMIN_TOKEN", "")
RELAY_ADMIN_ROUTE_TIMEOUT_SECONDS = max(
    0.1,
    float(os.environ.get("CATS_OPENAI_RELAY_ADMIN_ROUTE_TIMEOUT_SECONDS", "2")),
)
RELAY_ADMIN_PREFLIGHT_TIMEOUT_SECONDS = max(
    0.1,
    float(os.environ.get("CATS_OPENAI_RELAY_ADMIN_PREFLIGHT_TIMEOUT_SECONDS", "10")),
)
RELAY_USAGE_TIMEOUT_SECONDS = max(
    0.1,
    float(os.environ.get("CATS_OPENAI_RELAY_USAGE_TIMEOUT_SECONDS", "5")),
)
RELAY_USAGE_QUEUE_MAX_SIZE = max(
    100,
    int(os.environ.get("CATS_OPENAI_RELAY_USAGE_QUEUE_MAX_SIZE", "20000")),
)
RELAY_USAGE_WORKERS = max(
    1,
    int(os.environ.get("CATS_OPENAI_RELAY_USAGE_WORKERS", "2")),
)
RELAY_USAGE_MAX_ATTEMPTS = max(
    1,
    int(os.environ.get("CATS_OPENAI_RELAY_USAGE_MAX_ATTEMPTS", "3")),
)
SAFE_PROMPT_HASH = os.environ.get("CATS_RELAY_SAFE_PROMPT_HASH", "")
SAFE_PROMPT_HASH_SALT = os.environ.get("CATS_RELAY_SAFE_PROMPT_HASH_SALT", "")
PROMPT_CACHE_OBSERVE = os.environ.get("CATS_RELAY_PROMPT_CACHE_OBSERVE", "1")
REASONING_DEFAULTS_ENABLED = os.environ.get("CATS_RELAY_REASONING_DEFAULTS_ENABLED", "1")
REASONING_DEFAULTS_CANARY_UIDS = os.environ.get("CATS_RELAY_REASONING_DEFAULTS_CANARY_UIDS", "")

DEFAULT_MODEL_PROVIDER_PAIRS = [
    ("MiniMax-M2.7", "minimax-openai"),
    ("MiniMax-M3", "minimax-m3-openai"),
    ("deepseek-v4-flash", "deepseek-openai"),
]

# OpenAI-compatible must remain its own product surface. Do not add implicit
# defaults here; set the env vars below only for temporary production escape
# hatches when a provider's official OpenAI-compatible path is broken.
DEFAULT_ANTHROPIC_BRIDGE_PROVIDER_PAIRS: list[tuple[str, str]] = []
DEFAULT_ANTHROPIC_TOOL_BRIDGE_PROVIDER_PAIRS: list[tuple[str, str]] = []

DEFAULT_DISABLE_TOOL_CHOICE_BRIDGE_PROVIDERS = ["deepseek-anthropic"]
DEFAULT_PROVIDER_POOL_FAILOVER_STATUSES = {
    401,
    402,
    403,
    404,
    408,
    409,
    425,
    429,
    500,
    502,
    503,
    504,
    529,
}
DEFAULT_PROVIDER_POOL_FAILOVER_ERROR_CODES = {
    "bad_response_status_code",
    "concurrency_limit_exceeded",
    "daily_limit_exceeded",
    "insufficient_quota",
    "kernel_unavailable",
    "overloaded_error",
    "provider_unavailable",
    "quota_exceeded",
    "rate_limit_error",
    "rate_limit_exceeded",
    "request_timed_out",
    "server_is_overloaded",
    "service_unavailable",
    "upstream_error",
    "upstream_connection_error",
    "usage_limit_exceeded",
}
EXPLICIT_REQUEST_ERROR_CODES = {
    "content_filter",
    "content_policy_violation",
    "context_length_exceeded",
    "image_safety_violation",
    "input_too_large",
    "invalid_prompt",
    "request_too_large",
}
DEFAULT_OPENAI_REASONING_DEFAULTS = {
    "deepseek-v4-flash": {"thinking": {"type": "enabled"}, "reasoning_effort": "high"},
    # GLM-5.1 supports the thinking switch; explicit reasoning_effort is a
    # GLM-5.2+ control, so leave it to upstream defaults for 5.1.
    "glm-5.1": {"thinking": {"type": "enabled"}},
}
DEFAULT_ANTHROPIC_REASONING_DEFAULTS = {
    "deepseek-v4-flash": {"thinking": {"type": "enabled"}, "output_config": {"effort": "high"}},
    "glm-5.1": {"thinking": {"type": "enabled"}},
}
GPT56_MODELS = frozenset({"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"})
GPT56_REASONING_EFFORTS = ("none", "minimal", "low", "medium", "high", "xhigh")
MODEL_CAPABILITIES_SCHEMA = "catsco.model-capabilities.v1"
DEFAULT_MODEL_CAPABILITIES = {
    "minimax-m2.7": {"vision": False, "tool_calling": True, "streaming": True},
    "minimax-m3": {"vision": True, "tool_calling": True, "streaming": True},
    "deepseek-v4-flash": {"vision": False, "tool_calling": True, "streaming": True},
    **{
        model: {"vision": True, "tool_calling": True, "streaming": True}
        for model in GPT56_MODELS
    },
}

HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "host",
    "content-length",
}

logging.basicConfig(
    level=getattr(logging, LOG_LEVEL, logging.INFO),
    format="%(asctime)s %(levelname)s %(message)s",
)
LOGGER = logging.getLogger("cats-openai-adapter")
SAFE_ERROR_CODE_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,63}")
THINK_TAG_RE = re.compile(r"<think\b[^>]*>.*?</think>\s*", re.IGNORECASE | re.DOTALL)
LEADING_OPEN_THINK_RE = re.compile(r"^\s*<think\b[^>]*>.*$", re.IGNORECASE | re.DOTALL)


class RelayUsageDispatcher:
    """Deliver usage records outside the model-response request path."""

    def __init__(
        self,
        *,
        worker_count: int = RELAY_USAGE_WORKERS,
        max_queue_size: int = RELAY_USAGE_QUEUE_MAX_SIZE,
        max_attempts: int = RELAY_USAGE_MAX_ATTEMPTS,
    ):
        self.max_attempts = max(1, int(max_attempts))
        self._queue: queue.Queue[
            tuple[
                Callable[..., tuple[int, dict[str, Any]]],
                dict[str, Any],
                str,
            ]
        ] = queue.Queue(maxsize=max(1, int(max_queue_size)))
        for index in range(max(1, int(worker_count))):
            thread = threading.Thread(
                target=self._run,
                name=f"relay-usage-{index + 1}",
                daemon=True,
            )
            thread.start()

    def submit(
        self,
        sender: Callable[..., tuple[int, dict[str, Any]]],
        payload: dict[str, Any],
        context: str,
    ) -> bool:
        try:
            self._queue.put_nowait((sender, deepcopy(payload), context))
            return True
        except queue.Full:
            LOGGER.error(
                "relay usage queue full context=%s queued=%s",
                context,
                self._queue.qsize(),
            )
            return False

    def _run(self) -> None:
        while True:
            sender, payload, context = self._queue.get()
            try:
                self._deliver(sender, payload, context)
            finally:
                self._queue.task_done()

    def _deliver(
        self,
        sender: Callable[..., tuple[int, dict[str, Any]]],
        payload: dict[str, Any],
        context: str,
    ) -> None:
        last_status = 0
        last_body: dict[str, Any] = {}
        for attempt in range(1, self.max_attempts + 1):
            try:
                last_status, last_body = sender(
                    "/internal/passthrough/usage",
                    payload,
                    timeout=RELAY_USAGE_TIMEOUT_SECONDS,
                )
            except Exception as exc:
                last_status = HTTPStatus.BAD_GATEWAY
                last_body = {"error": f"{type(exc).__name__}: {exc}"}

            if 200 <= last_status < 300:
                return
            if 400 <= last_status < 500 and last_status not in {
                HTTPStatus.REQUEST_TIMEOUT,
                HTTPStatus.TOO_MANY_REQUESTS,
            }:
                break
            if attempt < self.max_attempts:
                time.sleep(min(2.0, 0.25 * (2 ** (attempt - 1))))

        LOGGER.error(
            "relay usage delivery failed context=%s status=%s body=%s",
            context,
            last_status,
            last_body,
        )


RELAY_USAGE_DISPATCHER = RelayUsageDispatcher()
THINKING_CACHE_TTL_SECONDS = int(os.environ.get("CATS_OPENAI_THINKING_CACHE_TTL_SECONDS", "1800"))
THINKING_CACHE_LOCK = threading.RLock()
THINKING_CACHE: dict[str, tuple[float, list[dict[str, Any]]]] = {}


def json_bytes(payload: Any) -> bytes:
    serialized = json.dumps(payload, ensure_ascii=False, separators=(",", ":"))
    try:
        return serialized.encode("utf-8")
    except UnicodeEncodeError:
        normalized = normalize_json_unicode(payload)
        return json.dumps(normalized, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def normalize_json_unicode(value: Any) -> Any:
    if isinstance(value, str):
        try:
            value.encode("utf-8")
            return value
        except UnicodeEncodeError:
            return value.encode("utf-16", errors="surrogatepass").decode(
                "utf-16",
                errors="replace",
            )
    if isinstance(value, list):
        return [normalize_json_unicode(item) for item in value]
    if isinstance(value, tuple):
        return [normalize_json_unicode(item) for item in value]
    if isinstance(value, dict):
        return {
            normalize_json_unicode(key) if isinstance(key, str) else key: normalize_json_unicode(item)
            for key, item in value.items()
        }
    return value


def call_relay_admin_service(
    path: str,
    body: dict[str, Any],
    *,
    timeout: float | None = None,
) -> tuple[int, dict[str, Any]]:
    if not RELAY_ADMIN_TOKEN:
        return HTTPStatus.SERVICE_UNAVAILABLE, {"error": "relay accounting token is not configured"}
    request = urllib.request.Request(
        RELAY_ADMIN_URL + path,
        data=json_bytes(body),
        headers={
            "Accept": "application/json",
            "Content-Type": "application/json",
            "Authorization": f"Bearer {RELAY_ADMIN_TOKEN}",
        },
        method="POST",
    )
    request_timeout = TIMEOUT if timeout is None else max(0.1, timeout)
    try:
        with urllib.request.urlopen(request, timeout=request_timeout) as response:
            raw = response.read()
            try:
                payload = json.loads(raw.decode("utf-8")) if raw else {}
            except (json.JSONDecodeError, UnicodeDecodeError):
                LOGGER.error("relay accounting service returned invalid JSON path=%s", path)
                return HTTPStatus.BAD_GATEWAY, {
                    "error": "relay accounting service returned invalid JSON"
                }
            if not isinstance(payload, dict):
                return HTTPStatus.BAD_GATEWAY, {
                    "error": "relay accounting service returned an invalid payload"
                }
            return response.status, payload
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        try:
            payload = json.loads(raw.decode("utf-8")) if raw else {}
        except (json.JSONDecodeError, UnicodeDecodeError):
            payload = {"error": raw.decode("utf-8", errors="replace")}
        if not isinstance(payload, dict):
            payload = {"error": "relay accounting service returned an invalid payload"}
        return exc.code, payload
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        return HTTPStatus.BAD_GATEWAY, {"error": f"relay accounting service unavailable: {exc}"}


def normalize_model_name(model: Any) -> str:
    return "".join(str(model or "").strip().lower().split())


def public_model_name(model: Any) -> str:
    value = str(model or "").strip()
    if "/" in value:
        value = value.rsplit("/", 1)[-1]
    return value


def reasoning_capability_for_model(model: Any) -> dict[str, Any] | None:
    if normalize_model_name(public_model_name(model)) not in GPT56_MODELS:
        return None
    return {
        "effort_values": list(GPT56_REASONING_EFFORTS),
        "default": "upstream",
        "chat_parameter": "reasoning_effort",
        "responses_parameter": "reasoning.effort",
    }


def normalize_model_capabilities(value: Any) -> dict[str, bool]:
    if not isinstance(value, dict):
        return {}
    normalized: dict[str, bool] = {}
    for key in ("vision", "tool_calling", "streaming"):
        if isinstance(value.get(key), bool):
            normalized[key] = value[key]
    return normalized


def load_model_capabilities() -> dict[str, dict[str, bool]]:
    capabilities = {
        normalize_model_name(model): normalize_model_capabilities(value)
        for model, value in DEFAULT_MODEL_CAPABILITIES.items()
    }
    raw = os.environ.get("CATS_OPENAI_MODEL_CAPABILITIES_JSON", "").strip()
    if not raw:
        return capabilities
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        LOGGER.error("invalid CATS_OPENAI_MODEL_CAPABILITIES_JSON; using defaults")
        return capabilities
    if not isinstance(parsed, dict):
        LOGGER.error("CATS_OPENAI_MODEL_CAPABILITIES_JSON must be an object; using defaults")
        return capabilities
    for model, value in parsed.items():
        model_key = normalize_model_name(model)
        if not model_key:
            continue
        override = normalize_model_capabilities(value)
        if override:
            capabilities[model_key] = {**capabilities.get(model_key, {}), **override}
    return capabilities


def model_capabilities_for_model(model: Any) -> dict[str, Any]:
    capabilities: dict[str, Any] = dict(
        MODEL_CAPABILITIES.get(normalize_model_name(public_model_name(model)), {})
    )
    reasoning = reasoning_capability_for_model(model)
    if reasoning:
        capabilities["reasoning"] = reasoning
    if capabilities.get("vision") is True:
        capabilities["input_modalities"] = ["text", "image"]
    elif capabilities.get("vision") is False:
        capabilities["input_modalities"] = ["text"]
    if capabilities:
        capabilities["schema"] = MODEL_CAPABILITIES_SCHEMA
    return capabilities


def normalize_gpt56_reasoning_controls(body: dict[str, Any], *, endpoint: str) -> dict[str, Any]:
    if normalize_model_name(public_model_name(body.get("model"))) not in GPT56_MODELS:
        return body

    normalized = deepcopy(body)
    if endpoint == "chat":
        if "reasoning_effort" not in normalized:
            return normalized
        effort = normalized.get("reasoning_effort")
        if effort is None:
            normalized.pop("reasoning_effort", None)
            return normalized
        parameter = "reasoning_effort"
    elif endpoint == "responses":
        if "reasoning" not in normalized:
            return normalized
        reasoning = normalized.get("reasoning")
        if reasoning is None:
            normalized.pop("reasoning", None)
            return normalized
        if not isinstance(reasoning, dict):
            raise ValueError("reasoning must be an object for GPT-5.6 models")
        if "effort" not in reasoning:
            if not reasoning:
                normalized.pop("reasoning", None)
            return normalized
        effort = reasoning.get("effort")
        if effort is None:
            reasoning.pop("effort", None)
            if not reasoning:
                normalized.pop("reasoning", None)
            return normalized
        parameter = "reasoning.effort"
    else:
        raise ValueError(f"unsupported reasoning endpoint: {endpoint}")

    if not isinstance(effort, str) or effort.strip().lower() not in GPT56_REASONING_EFFORTS:
        allowed = ", ".join(GPT56_REASONING_EFFORTS)
        raise ValueError(f"{parameter} must be one of: {allowed}")

    effort = effort.strip().lower()
    if endpoint == "chat":
        normalized["reasoning_effort"] = effort
    else:
        normalized["reasoning"]["effort"] = effort
    return normalized


def truthy_env(value: str) -> bool:
    return str(value or "").strip().lower() in {"1", "true", "yes", "on"}


def prompt_cache_observe_enabled() -> bool:
    return truthy_env(PROMPT_CACHE_OBSERVE)


def csv_values(value: str) -> set[str]:
    return {item.strip() for item in str(value or "").split(",") if item.strip()}


def reasoning_defaults_enabled(canary_uid: Any | None = None) -> bool:
    if truthy_env(REASONING_DEFAULTS_ENABLED):
        return True
    canary_uids = csv_values(REASONING_DEFAULTS_CANARY_UIDS)
    if not canary_uids:
        return False
    uid = str(canary_uid or "").strip()
    return "*" in canary_uids or bool(uid and uid in canary_uids)


def merge_override(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    merged = deepcopy(base)
    for key, value in override.items():
        if isinstance(merged.get(key), dict) and isinstance(value, dict):
            merged[key] = merge_override(merged[key], value)
        else:
            merged[key] = deepcopy(value)
    return merged


def load_reasoning_defaults(env_name: str, default: dict[str, dict[str, Any]]) -> dict[str, dict[str, Any]]:
    normalized: dict[str, dict[str, Any]] = {}
    for model, config in default.items():
        if isinstance(config, dict):
            normalized[normalize_model_name(public_model_name(model))] = deepcopy(config)

    raw = os.environ.get(env_name, "").strip()
    if not raw:
        return normalized
    else:
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            LOGGER.error("invalid %s; using built-in reasoning defaults", env_name)
            return normalized
        else:
            if not isinstance(parsed, dict):
                LOGGER.error("%s must be a JSON object; using built-in reasoning defaults", env_name)
                return normalized

    for model, config in parsed.items():
        if isinstance(config, dict):
            key = normalize_model_name(public_model_name(model))
            normalized[key] = merge_override(normalized.get(key, {}), config)
    return normalized


OPENAI_REASONING_DEFAULTS = load_reasoning_defaults(
    "CATS_OPENAI_REASONING_DEFAULTS_JSON",
    DEFAULT_OPENAI_REASONING_DEFAULTS,
)
ANTHROPIC_REASONING_DEFAULTS = load_reasoning_defaults(
    "CATS_ANTHROPIC_REASONING_DEFAULTS_JSON",
    DEFAULT_ANTHROPIC_REASONING_DEFAULTS,
)


def merge_missing(target: dict[str, Any], defaults: dict[str, Any]) -> bool:
    changed = False
    for key, default_value in defaults.items():
        if key not in target or target.get(key) is None:
            target[key] = deepcopy(default_value)
            changed = True
            continue
        if isinstance(target.get(key), dict) and isinstance(default_value, dict):
            changed = merge_missing(target[key], default_value) or changed
    return changed


def thinking_is_disabled(body: dict[str, Any]) -> bool:
    thinking = body.get("thinking")
    return isinstance(thinking, dict) and str(thinking.get("type") or "").strip().lower() == "disabled"


def apply_reasoning_defaults(
    body: dict[str, Any],
    defaults: dict[str, dict[str, Any]],
    *,
    model: Any | None = None,
    canary_uid: Any | None = None,
) -> dict[str, Any]:
    if not reasoning_defaults_enabled(canary_uid):
        return body
    config = defaults.get(normalize_model_name(public_model_name(model if model is not None else body.get("model"))))
    if not config:
        return body
    upstream = deepcopy(body)
    if thinking_is_disabled(upstream) or thinking_is_disabled(config):
        config = {
            key: value
            for key, value in config.items()
            if key not in {"output_config", "reasoning_effort"}
        }
    merge_missing(upstream, config)
    return upstream


def default_model_providers() -> tuple[dict[str, str], list[str]]:
    return (
        {normalize_model_name(model): provider for model, provider in DEFAULT_MODEL_PROVIDER_PAIRS},
        [model for model, _provider in DEFAULT_MODEL_PROVIDER_PAIRS],
    )


def load_model_providers() -> tuple[dict[str, str], list[str]]:
    raw = os.environ.get("CATS_OPENAI_MODEL_PROVIDERS_JSON", "").strip()
    if not raw:
        return default_model_providers()
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        LOGGER.error("invalid CATS_OPENAI_MODEL_PROVIDERS_JSON; using defaults")
        return default_model_providers()

    pairs: list[tuple[Any, Any]] = []
    if isinstance(parsed, dict):
        pairs = list(parsed.items())
    elif isinstance(parsed, list):
        for item in parsed:
            if isinstance(item, dict):
                pairs.append((item.get("model"), item.get("provider")))
    else:
        LOGGER.error("CATS_OPENAI_MODEL_PROVIDERS_JSON must be object or list; using defaults")
        return default_model_providers()

    mapping: dict[str, str] = {}
    display_names: list[str] = []
    for model, provider in pairs:
        model_key = normalize_model_name(model)
        display_name = str(model or "").strip()
        provider_value = str(provider or "").strip()
        if model_key and provider_value:
            mapping[model_key] = provider_value
            display_names.append(display_name)
    return (mapping, display_names) if mapping else default_model_providers()


def load_model_provider_pools() -> tuple[dict[str, tuple[str, ...]], list[str]]:
    raw = os.environ.get("CATS_OPENAI_MODEL_PROVIDER_POOLS_JSON", "").strip()
    if not raw:
        return {}, []
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        LOGGER.error("invalid CATS_OPENAI_MODEL_PROVIDER_POOLS_JSON; ignoring provider pools")
        return {}, []

    pairs: list[tuple[Any, Any]] = []
    if isinstance(parsed, dict):
        pairs = list(parsed.items())
    elif isinstance(parsed, list):
        for item in parsed:
            if isinstance(item, dict):
                pairs.append((item.get("model"), item.get("providers")))
    else:
        LOGGER.error("CATS_OPENAI_MODEL_PROVIDER_POOLS_JSON must be object or list; ignoring provider pools")
        return {}, []

    pools: dict[str, tuple[str, ...]] = {}
    display_names: list[str] = []
    for model, providers in pairs:
        model_key = normalize_model_name(model)
        display_name = str(model or "").strip()
        values = providers if isinstance(providers, list) else [providers]
        normalized = tuple(dict.fromkeys(str(provider or "").strip() for provider in values if str(provider or "").strip()))
        if model_key and normalized:
            pools[model_key] = normalized
            display_names.append(display_name)
    return pools, display_names


def load_provider_pool_failover_statuses() -> set[int]:
    raw = os.environ.get("CATS_OPENAI_PROVIDER_POOL_FAILOVER_STATUSES", "").strip()
    if not raw:
        return set(DEFAULT_PROVIDER_POOL_FAILOVER_STATUSES)
    statuses: set[int] = set()
    for value in raw.split(","):
        try:
            status = int(value.strip())
        except ValueError:
            LOGGER.error("invalid provider pool failover status %r; using defaults", value)
            return set(DEFAULT_PROVIDER_POOL_FAILOVER_STATUSES)
        if 100 <= status <= 599:
            statuses.add(status)
    return statuses or set(DEFAULT_PROVIDER_POOL_FAILOVER_STATUSES)


def load_provider_pool_failover_error_codes() -> set[str]:
    raw = os.environ.get("CATS_OPENAI_PROVIDER_POOL_FAILOVER_ERROR_CODES", "").strip()
    if not raw:
        return set(DEFAULT_PROVIDER_POOL_FAILOVER_ERROR_CODES)
    error_codes = {value.strip().lower() for value in raw.split(",") if value.strip()}
    return error_codes or set(DEFAULT_PROVIDER_POOL_FAILOVER_ERROR_CODES)


def load_provider_max_concurrency() -> dict[str, int]:
    raw = os.environ.get("CATS_OPENAI_PROVIDER_MAX_CONCURRENCY_JSON", "").strip()
    if not raw:
        return {}
    if not PROVIDER_CONCURRENCY_LIMITS_ENABLED:
        LOGGER.warning(
            "CATS_OPENAI_PROVIDER_MAX_CONCURRENCY_JSON is configured but local provider "
            "limits are disabled; set CATS_OPENAI_PROVIDER_CONCURRENCY_LIMITS_ENABLED=1 "
            "only for temporary incident control"
        )
        return {}
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        LOGGER.error("invalid CATS_OPENAI_PROVIDER_MAX_CONCURRENCY_JSON; disabling local limits")
        return {}
    if not isinstance(parsed, dict):
        LOGGER.error("CATS_OPENAI_PROVIDER_MAX_CONCURRENCY_JSON must be an object; disabling local limits")
        return {}
    limits: dict[str, int] = {}
    for provider, value in parsed.items():
        name = str(provider or "").strip()
        try:
            limit = int(value)
        except (TypeError, ValueError):
            LOGGER.error("invalid provider concurrency limit provider=%r value=%r", provider, value)
            continue
        if name and limit > 0:
            limits[name] = limit
    return limits


def default_anthropic_bridge_providers() -> dict[str, str]:
    return {
        normalize_model_name(model): provider
        for model, provider in DEFAULT_ANTHROPIC_BRIDGE_PROVIDER_PAIRS
        if provider
    }


def default_anthropic_tool_bridge_providers() -> dict[str, str]:
    return {
        normalize_model_name(model): provider
        for model, provider in DEFAULT_ANTHROPIC_TOOL_BRIDGE_PROVIDER_PAIRS
        if provider
    }


def load_bridge_provider_mapping(env_name: str, default: dict[str, str]) -> dict[str, str]:
    raw = os.environ.get(env_name, "").strip()
    if not raw:
        return dict(default)
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        LOGGER.error("invalid %s; using defaults", env_name)
        return dict(default)

    pairs: list[tuple[Any, Any]] = []
    if isinstance(parsed, dict):
        pairs = list(parsed.items())
    elif isinstance(parsed, list):
        for item in parsed:
            if isinstance(item, dict):
                pairs.append((item.get("model"), item.get("provider")))
    else:
        LOGGER.error("%s must be object or list; using defaults", env_name)
        return dict(default)

    mapping: dict[str, str] = {}
    for model, provider in pairs:
        model_key = normalize_model_name(model)
        provider_value = str(provider or "").strip()
        if model_key and provider_value:
            mapping[model_key] = provider_value
    return mapping


def load_provider_set(env_name: str, default: Iterable[str]) -> set[str]:
    raw = os.environ.get(env_name, "").strip()
    if not raw:
        return {str(item).strip() for item in default if str(item).strip()}
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        LOGGER.error("invalid %s; using defaults", env_name)
        return {str(item).strip() for item in default if str(item).strip()}
    if isinstance(parsed, str):
        items = [parsed]
    elif isinstance(parsed, list):
        items = parsed
    else:
        LOGGER.error("%s must be string or list; using defaults", env_name)
        return {str(item).strip() for item in default if str(item).strip()}
    return {str(item).strip() for item in items if str(item).strip()}


MODEL_PROVIDERS, _MODEL_NAMES = load_model_providers()
MODEL_PROVIDER_POOLS, _POOL_MODEL_NAMES = load_model_provider_pools()
MODEL_NAMES = list(dict.fromkeys([*_MODEL_NAMES, *_POOL_MODEL_NAMES]))
MODEL_CAPABILITIES = load_model_capabilities()
PROVIDER_POOL_FAILOVER_STATUSES = load_provider_pool_failover_statuses()
PROVIDER_POOL_FAILOVER_ERROR_CODES = load_provider_pool_failover_error_codes()
PROVIDER_MAX_CONCURRENCY = load_provider_max_concurrency()
PROVIDER_POOL_LOCK = threading.RLock()
PROVIDER_POOL_CONDITION = threading.Condition(PROVIDER_POOL_LOCK)
PROVIDER_POOL_UNAVAILABLE_UNTIL: dict[str, float] = {}
PROVIDER_POOL_FAILURE_GENERATION: dict[str, int] = {}
PROVIDER_POOL_CONSECUTIVE_FAILURES: dict[str, int] = {}
PROVIDER_POOL_HALF_OPEN_INFLIGHT: set[str] = set()
PROVIDER_POOL_LAST_PROBE_AT: dict[str, float] = {}
PROVIDER_POOL_ACTIVE: dict[str, int] = {}
CLIENT_POOL_LOCK = threading.Lock()
CLIENT_POOL_ACTIVE: dict[str, int] = {}
ANTHROPIC_BRIDGE_PROVIDERS = load_bridge_provider_mapping(
    "CATS_OPENAI_ANTHROPIC_BRIDGE_PROVIDERS_JSON",
    default_anthropic_bridge_providers(),
)
ANTHROPIC_TOOL_BRIDGE_PROVIDERS = load_bridge_provider_mapping(
    "CATS_OPENAI_ANTHROPIC_TOOL_BRIDGE_PROVIDERS_JSON",
    default_anthropic_tool_bridge_providers(),
)
DISABLE_TOOL_CHOICE_BRIDGE_PROVIDERS = load_provider_set(
    "CATS_OPENAI_ANTHROPIC_BRIDGE_DISABLE_TOOL_CHOICE_PROVIDERS_JSON",
    DEFAULT_DISABLE_TOOL_CHOICE_BRIDGE_PROVIDERS,
)


def provider_for_model(model: Any) -> str:
    model_key = normalize_model_name(model)
    pool = MODEL_PROVIDER_POOLS.get(model_key, ())
    return pool[0] if pool else MODEL_PROVIDERS.get(model_key, "")


def provider_candidates_for_model(
    model: Any,
    *,
    preferred_provider: str = "",
    now: float | None = None,
    include_unavailable_fallbacks: bool = False,
) -> list[str]:
    model_key = normalize_model_name(model)
    providers = MODEL_PROVIDER_POOLS.get(model_key, ())
    if not providers:
        provider = MODEL_PROVIDERS.get(model_key, "")
        return [provider] if provider else []

    preferred = str(preferred_provider or "").strip()
    ordered = list(providers)
    if preferred in ordered:
        ordered = [preferred, *(provider for provider in ordered if provider != preferred)]
    current_time = time.monotonic() if now is None else now
    with PROVIDER_POOL_LOCK:
        available = [
            provider
            for provider in ordered
            if PROVIDER_POOL_UNAVAILABLE_UNTIL.get(provider, 0.0) <= current_time
        ]
        if available:
            if not PROVIDER_SOFT_LOAD_BALANCING_ENABLED:
                selected = available
            else:
                minimum_active = min(
                    PROVIDER_POOL_ACTIVE.get(provider, 0)
                    for provider in available
                )
                if (
                    preferred in available
                    and PROVIDER_POOL_ACTIVE.get(preferred, 0)
                    <= minimum_active + PROVIDER_POOL_AFFINITY_ACTIVE_SKEW
                ):
                    selected = available
                else:
                    position = {provider: index for index, provider in enumerate(available)}
                    selected = sorted(
                        available,
                        key=lambda provider: (
                            PROVIDER_POOL_ACTIVE.get(provider, 0),
                            position[provider],
                        ),
                    )
            if not include_unavailable_fallbacks:
                return selected
            cooling = [provider for provider in ordered if provider not in available]
            cooling.sort(
                key=lambda provider: PROVIDER_POOL_UNAVAILABLE_UNTIL.get(provider, 0.0)
            )
            return [*selected, *cooling]
        if provider_pool_recovery_gate_enabled(model):
            # Emergency provider limits keep one rate-limited recovery path
            # visible so a broken upstream cannot receive a probe stampede.
            return [
                min(
                    ordered,
                    key=lambda provider: PROVIDER_POOL_UNAVAILABLE_UNTIL.get(provider, 0.0),
                )
            ]
        # Normal operation exposes every cooling provider so separate requests
        # can coordinate one recovery probe per upstream.
        return sorted(
            ordered,
            key=lambda provider: PROVIDER_POOL_UNAVAILABLE_UNTIL.get(provider, 0.0),
        )


@dataclass(frozen=True)
class ProviderPoolLease:
    provider: str
    generation: int
    half_open_probe: bool


@dataclass(frozen=True)
class ClientPoolLease:
    subject: str


def provider_pool_all_cooling(model: Any, *, now: float | None = None) -> bool:
    providers = MODEL_PROVIDER_POOLS.get(normalize_model_name(model), ())
    if not providers:
        return False
    current_time = time.monotonic() if now is None else now
    with PROVIDER_POOL_LOCK:
        return all(
            PROVIDER_POOL_UNAVAILABLE_UNTIL.get(provider, 0.0) > current_time
            for provider in providers
        )


def provider_pool_recovery_gate_enabled(model: Any) -> bool:
    return any(
        PROVIDER_MAX_CONCURRENCY.get(provider, 0) > 0
        for provider in MODEL_PROVIDER_POOLS.get(normalize_model_name(model), ())
    )


def provider_pool_request_wait_seconds(model: Any) -> float:
    if (
        PROVIDER_COORDINATED_RECOVERY_ENABLED
        and provider_pool_all_cooling(model)
        and not provider_pool_recovery_gate_enabled(model)
    ):
        return PROVIDER_POOL_RECOVERY_WAIT_SECONDS
    return PROVIDER_POOL_SLOT_WAIT_SECONDS


def provider_pool_probe_key(model_key: str, provider: str, *, incident_mode: bool) -> str:
    if incident_mode or PROVIDER_COORDINATED_RECOVERY_ENABLED:
        return model_key
    return provider


def provider_pool_has_capacity(provider: str) -> bool:
    limit = PROVIDER_MAX_CONCURRENCY.get(provider, 0)
    return limit <= 0 or PROVIDER_POOL_ACTIVE.get(provider, 0) < limit


def provider_pool_should_spill(
    pool: Iterable[str],
    provider: str,
    *,
    now: float,
) -> bool:
    if not PROVIDER_SOFT_LOAD_BALANCING_ENABLED:
        return False
    if PROVIDER_POOL_UNAVAILABLE_UNTIL.get(provider, 0.0) > now:
        return False
    eligible = [
        candidate
        for candidate in pool
        if PROVIDER_POOL_UNAVAILABLE_UNTIL.get(candidate, 0.0) <= now
        and provider_pool_has_capacity(candidate)
    ]
    if provider not in eligible or len(eligible) <= 1:
        return False
    minimum_active = min(PROVIDER_POOL_ACTIVE.get(candidate, 0) for candidate in eligible)
    return (
        PROVIDER_POOL_ACTIVE.get(provider, 0)
        > minimum_active + PROVIDER_POOL_AFFINITY_ACTIVE_SKEW
    )


def acquire_provider_pool_lease(
    model: Any,
    provider: str,
    *,
    now: float | None = None,
    deadline: float | None = None,
) -> ProviderPoolLease | None:
    model_key = normalize_model_name(model)
    if provider not in MODEL_PROVIDER_POOLS.get(model_key, ()):
        return ProviderPoolLease(provider=provider, generation=0, half_open_probe=False)
    wait_deadline = (
        time.monotonic() + PROVIDER_POOL_SLOT_WAIT_SECONDS
        if deadline is None
        else deadline
    )
    incident_mode = provider_pool_recovery_gate_enabled(model)
    coordinated_recovery = incident_mode or PROVIDER_COORDINATED_RECOVERY_ENABLED
    while True:
        current_time = time.monotonic() if now is None else now
        with PROVIDER_POOL_CONDITION:
            pool = MODEL_PROVIDER_POOLS.get(model_key, ())
            unavailable_until = PROVIDER_POOL_UNAVAILABLE_UNTIL.get(provider, 0.0)
            early_probe = False
            wait_for_recovery = False
            all_cooling = False
            if unavailable_until > current_time and coordinated_recovery:
                all_cooling = bool(pool) and all(
                    PROVIDER_POOL_UNAVAILABLE_UNTIL.get(candidate, 0.0) > current_time
                    for candidate in pool
                )
                if not all_cooling:
                    return None
                earliest = min(
                    pool,
                    key=lambda candidate: PROVIDER_POOL_UNAVAILABLE_UNTIL.get(candidate, 0.0),
                )
                probe_key = provider_pool_probe_key(
                    model_key,
                    provider,
                    incident_mode=incident_mode,
                )
                last_probe = PROVIDER_POOL_LAST_PROBE_AT.get(probe_key, float("-inf"))
                early_probe = (
                    all_cooling
                    and (not incident_mode or provider == earliest)
                    and provider not in PROVIDER_POOL_HALF_OPEN_INFLIGHT
                    and current_time - last_probe >= PROVIDER_POOL_EARLY_PROBE_INTERVAL_SECONDS
                )
                if not early_probe:
                    another_probe_available = any(
                        candidate != provider
                        and (not incident_mode or candidate == earliest)
                        and candidate not in PROVIDER_POOL_HALF_OPEN_INFLIGHT
                        and current_time
                        - PROVIDER_POOL_LAST_PROBE_AT.get(
                            provider_pool_probe_key(
                                model_key,
                                candidate,
                                incident_mode=incident_mode,
                            ),
                            float("-inf"),
                        )
                        >= PROVIDER_POOL_EARLY_PROBE_INTERVAL_SECONDS
                        for candidate in pool
                    )
                    if another_probe_available:
                        return None
                    wait_for_recovery = True
            half_open = (
                coordinated_recovery
                and provider in PROVIDER_POOL_UNAVAILABLE_UNTIL
            )
            if half_open and provider in PROVIDER_POOL_HALF_OPEN_INFLIGHT:
                if any(
                    candidate != provider
                    and PROVIDER_POOL_UNAVAILABLE_UNTIL.get(candidate, 0.0) <= current_time
                    and provider_pool_has_capacity(candidate)
                    for candidate in pool
                ):
                    return None
                wait_for_recovery = True
            if not wait_for_recovery:
                if (
                    not half_open
                    and provider_pool_should_spill(pool, provider, now=current_time)
                ):
                    return None
                active = PROVIDER_POOL_ACTIVE.get(provider, 0)
                if provider_pool_has_capacity(provider):
                    PROVIDER_POOL_ACTIVE[provider] = active + 1
                    if half_open:
                        PROVIDER_POOL_HALF_OPEN_INFLIGHT.add(provider)
                        if early_probe:
                            PROVIDER_POOL_LAST_PROBE_AT[
                                provider_pool_probe_key(
                                    model_key,
                                    provider,
                                    incident_mode=incident_mode,
                                )
                            ] = current_time
                    return ProviderPoolLease(
                        provider=provider,
                        generation=PROVIDER_POOL_FAILURE_GENERATION.get(provider, 0),
                        half_open_probe=half_open,
                    )
                for candidate in MODEL_PROVIDER_POOLS.get(model_key, ()):
                    if candidate == provider:
                        continue
                    if PROVIDER_POOL_UNAVAILABLE_UNTIL.get(candidate, 0.0) > current_time:
                        continue
                    if provider_pool_has_capacity(candidate):
                        return None
            if now is not None:
                return None
            remaining = wait_deadline - time.monotonic()
            if remaining <= 0:
                return None
            wait_timeout = remaining
            if all_cooling:
                next_events = [
                    max(
                        0.0,
                        PROVIDER_POOL_UNAVAILABLE_UNTIL.get(candidate, 0.0) - current_time,
                    )
                    for candidate in pool
                ]
                next_events.extend(
                    max(
                        0.0,
                        PROVIDER_POOL_LAST_PROBE_AT.get(
                            provider_pool_probe_key(
                                model_key,
                                candidate,
                                incident_mode=incident_mode,
                            ),
                            float("-inf"),
                        )
                        + PROVIDER_POOL_EARLY_PROBE_INTERVAL_SECONDS
                        - current_time,
                    )
                    for candidate in pool
                    if candidate not in PROVIDER_POOL_HALF_OPEN_INFLIGHT
                )
                positive_events = [delay for delay in next_events if delay > 0]
                if positive_events:
                    wait_timeout = min(wait_timeout, min(positive_events))
            PROVIDER_POOL_CONDITION.wait(timeout=max(0.001, wait_timeout))


def release_provider_pool_lease(lease: ProviderPoolLease) -> None:
    with PROVIDER_POOL_CONDITION:
        active = PROVIDER_POOL_ACTIVE.get(lease.provider, 0)
        if active <= 1:
            PROVIDER_POOL_ACTIVE.pop(lease.provider, None)
        else:
            PROVIDER_POOL_ACTIVE[lease.provider] = active - 1
        if not lease.half_open_probe:
            PROVIDER_POOL_CONDITION.notify_all()


def abandon_provider_pool_lease(lease: ProviderPoolLease) -> None:
    with PROVIDER_POOL_CONDITION:
        active = PROVIDER_POOL_ACTIVE.get(lease.provider, 0)
        if active <= 1:
            PROVIDER_POOL_ACTIVE.pop(lease.provider, None)
        else:
            PROVIDER_POOL_ACTIVE[lease.provider] = active - 1
        if lease.half_open_probe:
            PROVIDER_POOL_HALF_OPEN_INFLIGHT.discard(lease.provider)
        PROVIDER_POOL_CONDITION.notify_all()


def record_provider_pool_result(
    model: Any,
    provider: str,
    *,
    available: bool,
    now: float | None = None,
    lease: ProviderPoolLease | None = None,
    cooldown_seconds: float | None = None,
    failure_threshold: int = 1,
    error_fingerprint: str = "",
) -> None:
    model_key = normalize_model_name(model)
    if provider not in MODEL_PROVIDER_POOLS.get(model_key, ()):
        return
    with PROVIDER_POOL_CONDITION:
        if lease is None or lease.half_open_probe:
            PROVIDER_POOL_HALF_OPEN_INFLIGHT.discard(provider)
        if available:
            generation = PROVIDER_POOL_FAILURE_GENERATION.get(provider, 0)
            # Completion order defines a consecutive failure streak. Even when
            # an older success cannot close a newer circuit, it still proves
            # that failures were not consecutive.
            PROVIDER_POOL_CONSECUTIVE_FAILURES.pop(provider, None)
            if lease is None or lease.generation == generation:
                previous_until = PROVIDER_POOL_UNAVAILABLE_UNTIL.pop(provider, None)
                if previous_until is not None:
                    LOGGER.info(
                        "provider circuit closed model=%s provider=%s probe=%s",
                        model_key,
                        provider,
                        bool(lease and lease.half_open_probe),
                    )
                    log_provider_pool_event(
                        "circuit_closed",
                        model=model_key,
                        provider=provider,
                        probe=bool(lease and lease.half_open_probe),
                    )
        else:
            current_time = time.monotonic() if now is None else now
            PROVIDER_POOL_FAILURE_GENERATION[provider] = (
                PROVIDER_POOL_FAILURE_GENERATION.get(provider, 0) + 1
            )
            failures = PROVIDER_POOL_CONSECUTIVE_FAILURES.get(provider, 0) + 1
            PROVIDER_POOL_CONSECUTIVE_FAILURES[provider] = failures
            if not (lease is not None and lease.half_open_probe) and failures < max(
                1,
                failure_threshold,
            ):
                PROVIDER_POOL_CONDITION.notify_all()
                return
            cooldown = (
                PROVIDER_POOL_COOLDOWN_SECONDS
                if cooldown_seconds is None
                else max(0.0, cooldown_seconds)
            )
            cooldown_until = current_time + cooldown
            PROVIDER_POOL_UNAVAILABLE_UNTIL[provider] = cooldown_until
            LOGGER.warning(
                "provider circuit opened model=%s provider=%s failures=%s cooldown_seconds=%.3f fingerprint=%s",
                model_key,
                provider,
                failures,
                cooldown,
                error_fingerprint or "unknown",
            )
            log_provider_pool_event(
                "circuit_opened",
                model=model_key,
                provider=provider,
                failures=failures,
                cooldown_seconds=cooldown,
                cooldown_until_monotonic=round(cooldown_until, 6),
                fingerprint=error_fingerprint or "unknown",
                probe=bool(lease and lease.half_open_probe),
            )
        PROVIDER_POOL_CONDITION.notify_all()


def provider_payload_is_unavailable(payload: bytes) -> bool:
    if not payload:
        return False
    preview = payload[:8192].decode("utf-8", errors="replace").lower()
    return any(
        marker in preview
        for marker in (
            "failed to execute http request to provider api",
            "network error occurred while connecting to provider api",
        )
    )


def provider_error_fingerprint(status: int, error_code: str | None) -> str:
    normalized = str(error_code or "unknown").strip().lower() or "unknown"
    return hashlib.sha256(f"{int(status)}:{normalized}".encode("utf-8")).hexdigest()[:16]


def should_failover_provider(
    status: int,
    *,
    valid_payload: bool = True,
    payload: bytes = b"",
    error_code: str | None = None,
) -> bool:
    normalized_error_code = str(error_code or "").strip().lower()
    if normalized_error_code in EXPLICIT_REQUEST_ERROR_CODES:
        return False
    return (
        500 <= status <= 599
        or status in PROVIDER_POOL_FAILOVER_STATUSES
        or normalized_error_code in PROVIDER_POOL_FAILOVER_ERROR_CODES
        or (200 <= status < 300 and not valid_payload)
        or provider_payload_is_unavailable(payload)
    )


def provider_failure_opens_circuit(
    status: int,
    *,
    valid_payload: bool,
    payload: bytes,
    error_code: str | None,
) -> bool:
    normalized = str(error_code or "").strip().lower()
    if normalized in EXPLICIT_REQUEST_ERROR_CODES:
        return False
    account_or_transport_markers = (
        "concurrency",
        "daily_limit",
        "insufficient_quota",
        "kernel_unavailable",
        "overload",
        "provider_unavailable",
        "quota",
        "rate_limit",
        "request_timed_out",
        "service_unavailable",
        "timeout",
        "upstream_connection",
        "usage_limit",
    )
    return (
        status in {401, 402, 403, 408, 429, 524}
        or 500 <= status <= 599
        or any(marker in normalized for marker in account_or_transport_markers)
        or provider_payload_is_unavailable(payload)
        or (200 <= status < 300 and not valid_payload)
    )


def provider_failure_threshold(
    status: int,
    *,
    error_code: str | None,
) -> int:
    normalized = str(error_code or "").strip().lower()
    immediate_markers = (
        "auth",
        "concurrency",
        "daily_limit",
        "insufficient_quota",
        "invalid_api_key",
        "kernel_unavailable",
        "quota",
        "rate_limit",
        "usage_limit",
    )
    if status in {401, 402, 403, 429}:
        return 1
    if any(marker in normalized for marker in immediate_markers):
        return 1
    return PROVIDER_POOL_TRANSIENT_FAILURE_THRESHOLD


def provider_cooldown_seconds(
    headers: dict[str, str],
    *,
    status: int,
    error_code: str | None,
) -> float:
    retry_after = next(
        (value for key, value in headers.items() if key.lower() == "retry-after"),
        "",
    )
    try:
        parsed = float(str(retry_after).strip())
    except (TypeError, ValueError):
        parsed = 0
    if parsed > 0:
        return min(3600.0, max(1.0, parsed))
    normalized = str(error_code or "").strip().lower()
    if status in {408, 504, 524} or "timeout" in normalized or "timed_out" in normalized:
        return PROVIDER_POOL_TIMEOUT_COOLDOWN_SECONDS
    if (
        status in {401, 402, 403}
        or "quota" in normalized
        or "daily_limit" in normalized
        or "usage_limit" in normalized
    ):
        return PROVIDER_POOL_COOLDOWN_SECONDS
    if (
        status == 429
        or "rate_limit" in normalized
        or "concurrency" in normalized
    ):
        return PROVIDER_POOL_RATE_LIMIT_COOLDOWN_SECONDS
    return PROVIDER_POOL_TRANSIENT_COOLDOWN_SECONDS


def client_pool_subject(api_key: str) -> str:
    return hashlib.sha256(str(api_key or "").encode("utf-8")).hexdigest()


def acquire_client_pool_lease(
    api_key: str,
    *,
    now: float | None = None,
) -> ClientPoolLease | None:
    if CLIENT_POOL_MAX_CONCURRENCY <= 0:
        return ClientPoolLease(subject="")
    subject = client_pool_subject(api_key)
    deadline = time.monotonic() + CLIENT_POOL_SLOT_WAIT_SECONDS
    while True:
        with CLIENT_POOL_LOCK:
            active = CLIENT_POOL_ACTIVE.get(subject, 0)
            if active < CLIENT_POOL_MAX_CONCURRENCY:
                CLIENT_POOL_ACTIVE[subject] = active + 1
                return ClientPoolLease(subject=subject)
        if now is not None or time.monotonic() >= deadline:
            return None
        time.sleep(min(0.01, max(0.0, deadline - time.monotonic())))


def release_client_pool_lease(lease: ClientPoolLease) -> None:
    if not lease.subject:
        return
    with CLIENT_POOL_LOCK:
        active = CLIENT_POOL_ACTIVE.get(lease.subject, 0)
        if active <= 1:
            CLIENT_POOL_ACTIVE.pop(lease.subject, None)
        else:
            CLIENT_POOL_ACTIVE[lease.subject] = active - 1


def chat_completion_payload_is_valid(payload: Any) -> bool:
    if not isinstance(payload, dict):
        return False
    choices = payload.get("choices")
    return isinstance(choices, list) and bool(choices)


def anthropic_bridge_provider_for_model(model: Any) -> str:
    return ANTHROPIC_BRIDGE_PROVIDERS.get(normalize_model_name(model), "")


def anthropic_tool_bridge_provider_for_model(model: Any) -> str:
    return ANTHROPIC_TOOL_BRIDGE_PROVIDERS.get(normalize_model_name(model), "")


def bridge_provider_disables_tool_choice(provider: str) -> bool:
    return str(provider or "").strip() in DISABLE_TOOL_CHOICE_BRIDGE_PROVIDERS


def body_has_tool_state(body: dict[str, Any]) -> bool:
    if isinstance(body.get("tools"), list) and body["tools"]:
        return True
    messages = body.get("messages")
    if not isinstance(messages, list):
        return False
    for message in messages:
        if not isinstance(message, dict):
            continue
        if message.get("role") == "tool":
            return True
        if isinstance(message.get("tool_calls"), list) and message["tool_calls"]:
            return True
    return False


def relay_api_key(headers: Any) -> str:
    auth = str(headers.get("Authorization") or headers.get("authorization") or "").strip()
    if auth.lower().startswith("bearer "):
        return auth[7:].strip()
    return str(headers.get("x-api-key") or headers.get("X-Api-Key") or "").strip()


def client_prompt_hash_id(headers: Any) -> str:
    return str(
        headers.get("x-catsco-prompt-hash-id")
        or headers.get("X-Catsco-Prompt-Hash-Id")
        or ""
    ).strip()


def openai_error(message: str, error_type: str = "api_error", code: str | None = None) -> dict[str, Any]:
    return {
        "error": {
            "message": message,
            "type": error_type,
            "param": None,
            "code": code,
        }
    }


def safe_error_code_value(value: Any) -> str:
    if isinstance(value, bool) or not isinstance(value, (str, int)):
        return ""
    candidate = str(value).strip()
    if candidate.lower().startswith(("sk-", "sk_", "bearer:")):
        return ""
    return candidate if SAFE_ERROR_CODE_RE.fullmatch(candidate) else ""


def safe_error_code_from_object(value: Any) -> str:
    if not isinstance(value, dict):
        return ""

    response = value.get("response") if isinstance(value.get("response"), dict) else {}
    error_containers = [
        value.get("error"),
        response.get("error"),
        value.get("incomplete_details"),
        response.get("incomplete_details"),
    ]
    for container in error_containers:
        if not isinstance(container, dict):
            continue
        for key in ("error_code", "code", "type", "reason"):
            error_code = safe_error_code_value(container.get(key))
            if error_code:
                return error_code
    for container in (value, response):
        for key in ("error_code", "code", "reason"):
            error_code = safe_error_code_value(container.get(key))
            if error_code:
                return error_code
    return ""


def iter_responses_sse_events(payload: bytes) -> Iterable[tuple[str, dict[str, Any]]]:
    normalized = payload.replace(b"\r\n", b"\n").replace(b"\r", b"\n")
    for block in normalized.split(b"\n\n"):
        event_name = ""
        data_lines: list[bytes] = []
        for raw_line in block.split(b"\n"):
            line = raw_line.strip()
            if line.startswith(b"event:"):
                event_name = line[len(b"event:") :].strip().decode("ascii", errors="ignore")
            elif line.startswith(b"data:"):
                data_lines.append(line[len(b"data:") :].strip())
        if not data_lines:
            continue
        raw_data = b"\n".join(data_lines)
        if raw_data == b"[DONE]":
            continue
        try:
            event = json.loads(raw_data.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            continue
        if not isinstance(event, dict):
            continue
        event_type = str(event.get("type") or event_name).strip().lower()
        yield event_type, event


class ResponsesSSEObserver:
    """Observe a forwarded Responses SSE stream without rewriting its bytes."""

    TERMINAL_EVENTS = {
        "response.completed",
        "response.failed",
        "response.incomplete",
        "response.cancelled",
        "response.error",
        "error",
    }

    def __init__(self) -> None:
        self.buffer = b""
        self.usage: dict[str, Any] = {}
        self.terminal = ""
        self.error_code = ""

    def feed(self, chunk: bytes) -> None:
        self.buffer += chunk
        while True:
            match = re.search(br"\r\n\r\n|\n\n|\r\r", self.buffer)
            if match is None:
                return
            block = self.buffer[: match.start()]
            self.buffer = self.buffer[match.end() :]
            self._observe(block)

    def finish(self) -> None:
        if self.buffer.strip():
            self._observe(self.buffer)
        self.buffer = b""

    def _observe(self, block: bytes) -> None:
        for event_type, event in iter_responses_sse_events(block + b"\n\n"):
            candidate = event.get("usage")
            response = event.get("response")
            if not isinstance(candidate, dict) and isinstance(response, dict):
                candidate = response.get("usage")
            if isinstance(candidate, dict):
                self.usage = candidate
            if event_type in self.TERMINAL_EVENTS:
                self.terminal = event_type
                self.error_code = safe_error_code_from_object(event)

    def outcome(self) -> dict[str, Any]:
        if self.terminal == "response.completed":
            return {
                "valid_payload": True,
                "succeeded": True,
                "usage": self.usage,
                "terminal": self.terminal,
                "error_code": None,
            }
        if self.terminal:
            return {
                "valid_payload": True,
                "succeeded": False,
                "usage": self.usage,
                "terminal": self.terminal,
                "error_code": self.error_code or self.terminal.replace(".", "_"),
            }
        return {
            "valid_payload": False,
            "succeeded": False,
            "usage": self.usage,
            "terminal": "",
            "error_code": "missing_terminal_event",
        }


def safe_error_code_from_payload(payload: bytes) -> str:
    if not payload:
        return ""
    try:
        parsed = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        parsed = None
    error_code = safe_error_code_from_object(parsed)
    if error_code:
        return error_code
    for _event_type, event in iter_responses_sse_events(payload):
        candidate = safe_error_code_from_object(event)
        if candidate:
            error_code = candidate
    return error_code


def safe_prompt_hash_enabled() -> bool:
    return SAFE_PROMPT_HASH.lower() in {"1", "true", "yes"}


def stable_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), default=str)


def safe_prompt_hash(value: Any) -> str:
    data = value if isinstance(value, str) else stable_json(value)
    raw = data.encode("utf-8", errors="replace")
    if SAFE_PROMPT_HASH_SALT:
        return hmac.new(SAFE_PROMPT_HASH_SALT.encode("utf-8"), raw, hashlib.sha256).hexdigest()[:16]
    return hashlib.sha256(raw).hexdigest()[:16]


def compact_type(value: Any) -> str:
    if value is None:
        return "null"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return type(value).__name__


def strip_none(value: Any) -> Any:
    if isinstance(value, list):
        return [strip_none(item) for item in value]
    if isinstance(value, dict):
        return {key: strip_none(item) for key, item in value.items() if item is not None}
    return value


def summarize_prompt_messages(messages: Any) -> dict[str, Any]:
    if not isinstance(messages, list):
        text = stable_json(messages)
        return {"kind": compact_type(messages), "len": len(text), "hash": safe_prompt_hash(text)}

    items: list[dict[str, Any]] = []
    normalized: list[Any] = []
    total_content_len = 0
    for index, message in enumerate(messages):
        if not isinstance(message, dict):
            text = stable_json(message)
            total_content_len += len(text)
            items.append({
                "index": index,
                "role": compact_type(message),
                "content_kind": compact_type(message),
                "content_len": len(text),
                "content_hash": safe_prompt_hash(text),
            })
            normalized.append(message)
            continue

        content = message.get("content")
        content_text = stable_json(content)
        tool_calls = message.get("tool_calls")
        tool_call_id = message.get("tool_call_id")
        normalized_message = {
            "role": message.get("role"),
            "name": message.get("name"),
            "tool_call_id": tool_call_id,
            "content": content,
            "tool_calls": tool_calls if isinstance(tool_calls, list) else None,
        }
        normalized.append(strip_none(normalized_message))
        total_content_len += len(content_text)
        items.append(strip_none({
            "index": index,
            "role": message.get("role"),
            "name": message.get("name"),
            "content_kind": compact_type(content),
            "content_len": len(content_text),
            "content_hash": safe_prompt_hash(content_text),
            "tool_call_count": len(tool_calls) if isinstance(tool_calls, list) else 0,
            "tool_calls_hash": safe_prompt_hash(tool_calls) if isinstance(tool_calls, list) else None,
            "tool_call_id_hash": safe_prompt_hash(tool_call_id) if isinstance(tool_call_id, str) else None,
        }))

    return {
        "count": len(messages),
        "roles": ",".join(str(item.get("role") or "") for item in items),
        "total_content_len": total_content_len,
        "hash": safe_prompt_hash(normalized),
        "items": items,
    }


def summarize_prompt_tools(tools: Any) -> dict[str, Any]:
    if not isinstance(tools, list):
        text = stable_json(tools)
        return {"kind": compact_type(tools), "len": len(text), "hash": safe_prompt_hash(text)}

    names: list[str] = []
    for tool in tools:
        if isinstance(tool, dict) and isinstance(tool.get("name"), str):
            names.append(tool["name"])
            continue
        function = tool.get("function") if isinstance(tool, dict) else None
        if isinstance(function, dict) and isinstance(function.get("name"), str):
            names.append(function["name"])
        else:
            names.append("")
    return {"count": len(tools), "names": names, "hash": safe_prompt_hash(tools)}


def safe_extra(extra: dict[str, Any]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in extra.items():
        if isinstance(value, (str, int, float, bool)) or value is None:
            result[key] = value
            continue
        text = stable_json(value)
        result[key] = {"kind": compact_type(value), "len": len(text), "hash": safe_prompt_hash(text)}
    return result


def as_int(value: Any) -> int:
    try:
        return max(0, int(value or 0))
    except (TypeError, ValueError):
        return 0


def cache_tokens_from_usage(usage: Any) -> tuple[int, int]:
    data = usage if isinstance(usage, dict) else {}
    if not data:
        return 0, 0

    read_tokens = max(
        as_int(data.get("cached_read_tokens")),
        as_int(data.get("cache_read_tokens")),
        as_int(data.get("cache_read_input_tokens")),
        as_int(data.get("prompt_cache_hit_tokens")),
    )
    write_tokens = max(
        as_int(data.get("cached_write_tokens")),
        as_int(data.get("cache_write_tokens")),
        as_int(data.get("cache_creation_input_tokens")),
        as_int(data.get("cache_creation_tokens")),
    )
    details_candidates = [
        data.get("prompt_tokens_details"),
        data.get("input_tokens_details"),
        data.get("input_token_details"),
        data.get("usage", {}).get("prompt_tokens_details") if isinstance(data.get("usage"), dict) else None,
        data.get("usage", {}).get("input_tokens_details") if isinstance(data.get("usage"), dict) else None,
    ]
    total_cached = as_int(data.get("cached_tokens"))
    for details in details_candidates:
        if not isinstance(details, dict):
            continue
        read_tokens = max(
            read_tokens,
            as_int(details.get("cached_read_tokens")),
            as_int(details.get("cache_read_tokens")),
            as_int(details.get("cache_read_input_tokens")),
            as_int(details.get("prompt_cache_hit_tokens")),
        )
        write_tokens = max(
            write_tokens,
            as_int(details.get("cached_write_tokens")),
            as_int(details.get("cache_write_tokens")),
            as_int(details.get("cache_creation_input_tokens")),
            as_int(details.get("cache_creation_tokens")),
        )
        details_cached_tokens = as_int(details.get("cached_tokens"))
        total_cached = max(total_cached, details_cached_tokens)
        read_tokens = max(read_tokens, details_cached_tokens)
        write_details = details.get("cached_write_token_details")
        if isinstance(write_details, dict):
            nested_write = as_int(write_details.get("cached_write_tokens_5m")) + as_int(
                write_details.get("cached_write_tokens_1h")
            )
            write_tokens = max(write_tokens, nested_write)
    if total_cached > 0:
        read_tokens = max(read_tokens, total_cached)
    return read_tokens, write_tokens


def usage_counts_from_payload(usage: Any) -> dict[str, Any]:
    if not isinstance(usage, dict):
        usage = {}
    prompt_tokens = as_int(usage.get("prompt_tokens"))
    input_tokens = as_int(usage.get("input_tokens"))
    completion_tokens = as_int(usage.get("output_tokens", usage.get("completion_tokens")))
    cached_read_tokens, cached_write_tokens = cache_tokens_from_usage(usage)
    if prompt_tokens <= 0:
        prompt_tokens = input_tokens
        if input_tokens > 0 and (
            usage.get("cache_read_input_tokens") is not None
            or usage.get("cache_creation_input_tokens") is not None
            or usage.get("cache_read_tokens") is not None
            or usage.get("cache_write_tokens") is not None
        ):
            prompt_tokens += cached_read_tokens + cached_write_tokens
    if prompt_tokens <= 0:
        prompt_tokens = as_int(usage.get("prompt_cache_hit_tokens")) + as_int(usage.get("prompt_cache_miss_tokens"))
    total_tokens = as_int(usage.get("total_tokens")) or prompt_tokens + completion_tokens
    return {
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": total_tokens,
        "cached_read_tokens": cached_read_tokens,
        "cached_write_tokens": cached_write_tokens,
        "cached_tokens": cached_read_tokens + cached_write_tokens,
        "cache_read_rate": round(cached_read_tokens / prompt_tokens, 6) if prompt_tokens > 0 else 0,
        "cache_write_rate": round(cached_write_tokens / prompt_tokens, 6) if prompt_tokens > 0 else 0,
        "cache_token_rate": round((cached_read_tokens + cached_write_tokens) / prompt_tokens, 6)
        if prompt_tokens > 0
        else 0,
    }


def log_prompt_cache_usage(
    *,
    boundary: str,
    request_id: str,
    client_prompt_hash_id: str = "",
    provider: str = "",
    model: Any = "",
    stream: bool = False,
    status: str,
    http_status: int,
    latency_ms: int,
    usage: Any,
) -> None:
    if not prompt_cache_observe_enabled():
        return
    payload = {
        "boundary": boundary,
        "request_id": request_id,
        "client_prompt_hash_id": client_prompt_hash_id or None,
        "provider": provider or None,
        "model": model or None,
        "stream": stream,
        "status": status,
        "http_status": http_status,
        "latency_ms": latency_ms,
        "usage": usage_counts_from_payload(usage),
    }
    LOGGER.info("[PROMPT_CACHE_USAGE] %s", stable_json(strip_none(payload)))


def log_provider_pool_event(event: str, **fields: Any) -> None:
    payload = {"event": event, **strip_none(fields)}
    LOGGER.info("[PROVIDER_POOL] %s", stable_json(payload))


def log_safe_prompt_hash(
    *,
    boundary: str,
    request_id: str,
    client_prompt_hash_id: str = "",
    provider: str = "",
    model: Any = "",
    stream: bool = False,
    body: Any = None,
    messages: Any = None,
    tools: Any = None,
    system: Any = None,
    extra: dict[str, Any] | None = None,
) -> None:
    if not safe_prompt_hash_enabled():
        return

    payload: dict[str, Any] = {
        "boundary": boundary,
        "request_id": request_id,
        "client_prompt_hash_id": client_prompt_hash_id or None,
        "provider": provider or None,
        "model": model or None,
        "stream": stream,
    }
    if body is not None:
        text = stable_json(body)
        payload["body"] = {"len": len(text), "hash": safe_prompt_hash(text)}
    if system is not None:
        text = stable_json(system)
        payload["system"] = {"len": len(text), "hash": safe_prompt_hash(text)}
    if messages is not None:
        payload["messages"] = summarize_prompt_messages(messages)
    if tools is not None:
        payload["tools"] = summarize_prompt_tools(tools)
    if extra:
        payload["extra"] = safe_extra(extra)

    LOGGER.info("[SAFE_PROMPT_HASH] %s", stable_json(strip_none(payload)))


def sanitize_openai_text(text: str) -> str:
    sanitized = THINK_TAG_RE.sub("", text)
    if LEADING_OPEN_THINK_RE.match(sanitized):
        return ""
    return sanitized.strip()


def sanitize_openai_response(response: dict[str, Any]) -> dict[str, Any]:
    sanitized = deepcopy(response)
    choices = sanitized.get("choices")
    if not isinstance(choices, list):
        return sanitized

    for choice in choices:
        if not isinstance(choice, dict):
            continue
        message = choice.get("message")
        if not isinstance(message, dict):
            continue
        content = message.get("content")
        if isinstance(content, str):
            message["content"] = sanitize_openai_text(content)
        message.pop("reasoning", None)
        message.pop("reasoning_content", None)
        message.pop("reasoning_details", None)
    return sanitized


def parse_json_object(value: Any) -> Any:
    if not isinstance(value, str) or not value.strip():
        return {}
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError:
        return {}
    return parsed if isinstance(parsed, (dict, list)) else {}


def thinking_cache_key(api_key: str, tool_use_id: str) -> str:
    digest = hashlib.sha256(str(api_key or "").encode("utf-8")).hexdigest()
    return f"{digest}:{tool_use_id}"


def openai_reasoning_cache_key(
    api_key: str,
    model: Any,
    tool_call_id: str,
    tool_call: dict[str, Any],
    prefix_messages: list[Any],
) -> str:
    digest = hashlib.sha256(str(api_key or "").encode("utf-8")).hexdigest()
    model_key = normalize_model_name(public_model_name(model))
    call_hash = safe_prompt_hash({
        "type": tool_call.get("type"),
        "function": tool_call.get("function"),
    })
    prefix_hash = safe_prompt_hash(prefix_messages)
    return f"{digest}:openai:{model_key}:{tool_call_id}:{call_hash}:{prefix_hash}"


def clear_expired_thinking_cache(now: float | None = None) -> None:
    now = now if now is not None else time.monotonic()
    with THINKING_CACHE_LOCK:
        expired = [key for key, (expires_at, _) in THINKING_CACHE.items() if expires_at <= now]
        for key in expired:
            THINKING_CACHE.pop(key, None)


def remember_anthropic_thinking(api_key: str, message: dict[str, Any]) -> None:
    content = message.get("content") if isinstance(message.get("content"), list) else []
    pending: list[dict[str, Any]] = []
    expires_at = time.monotonic() + max(60, THINKING_CACHE_TTL_SECONDS)
    clear_expired_thinking_cache()
    with THINKING_CACHE_LOCK:
        for block in content:
            if not isinstance(block, dict):
                continue
            block_type = block.get("type")
            if block_type in {"thinking", "redacted_thinking"}:
                pending.append(deepcopy(block))
                continue
            if block_type == "tool_use" and block.get("id") and pending:
                THINKING_CACHE[thinking_cache_key(api_key, str(block["id"]))] = (expires_at, deepcopy(pending))
                pending = []


def cached_anthropic_thinking(api_key: str, tool_use_id: str) -> list[dict[str, Any]]:
    clear_expired_thinking_cache()
    with THINKING_CACHE_LOCK:
        cached = THINKING_CACHE.get(thinking_cache_key(api_key, tool_use_id))
        return deepcopy(cached[1]) if cached else []


def remember_openai_reasoning(api_key: str, response: dict[str, Any], request_body: dict[str, Any] | None = None) -> None:
    choices = response.get("choices")
    if not isinstance(choices, list):
        return
    request_messages = request_body.get("messages") if isinstance(request_body, dict) else []
    if not isinstance(request_messages, list):
        request_messages = []
    model = request_body.get("model") if isinstance(request_body, dict) else response.get("model")
    expires_at = time.monotonic() + max(60, THINKING_CACHE_TTL_SECONDS)
    clear_expired_thinking_cache()
    with THINKING_CACHE_LOCK:
        for choice in choices:
            if not isinstance(choice, dict):
                continue
            message = choice.get("message")
            if not isinstance(message, dict):
                continue
            reasoning = message.get("reasoning_content")
            tool_calls = message.get("tool_calls")
            if not isinstance(reasoning, str) or not reasoning.strip() or not isinstance(tool_calls, list):
                continue
            cache_blocks = [{"type": "openai_reasoning", "reasoning_content": reasoning}]
            for tool_call in tool_calls:
                if not isinstance(tool_call, dict):
                    continue
                tool_call_id = str(tool_call.get("id") or "").strip()
                if not tool_call_id:
                    continue
                THINKING_CACHE[openai_reasoning_cache_key(
                    api_key,
                    model,
                    tool_call_id,
                    tool_call,
                    request_messages,
                )] = (
                    expires_at,
                    deepcopy(cache_blocks),
                )


def cached_openai_reasoning(
    api_key: str,
    model: Any,
    tool_call_id: str,
    tool_call: dict[str, Any],
    prefix_messages: list[Any],
) -> str:
    clear_expired_thinking_cache()
    with THINKING_CACHE_LOCK:
        cached = THINKING_CACHE.get(openai_reasoning_cache_key(
            api_key,
            model,
            tool_call_id,
            tool_call,
            prefix_messages,
        ))
        blocks = deepcopy(cached[1]) if cached else []
    for block in blocks:
        if not isinstance(block, dict):
            continue
        if block.get("type") == "openai_reasoning" and isinstance(block.get("reasoning_content"), str):
            return block["reasoning_content"]
    return ""


def compact_openai_content_preview(content: Any, limit: int = 1200) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        text = sanitize_openai_text(content)
    else:
        text = stable_json(content)
    text = " ".join(text.split())
    if len(text) <= limit:
        return text
    return f"{text[:limit]}... [truncated {len(text) - limit} chars]"


def summarize_openai_reasoning_cache_miss(messages: list[Any], index: int) -> tuple[dict[str, str], int]:
    message = messages[index]
    tool_calls = message.get("tool_calls") if isinstance(message, dict) else []
    if not isinstance(tool_calls, list):
        tool_calls = []
    tool_call_ids = {
        str(tool_call.get("id") or "").strip()
        for tool_call in tool_calls
        if isinstance(tool_call, dict) and str(tool_call.get("id") or "").strip()
    }
    result_by_id: dict[str, list[str]] = {}
    next_index = index + 1
    while next_index < len(messages):
        candidate = messages[next_index]
        if not isinstance(candidate, dict) or candidate.get("role") != "tool":
            break
        tool_call_id = str(candidate.get("tool_call_id") or "").strip()
        if tool_call_id not in tool_call_ids:
            break
        preview = compact_openai_content_preview(candidate.get("content"))
        result_by_id.setdefault(tool_call_id, []).append(preview)
        next_index += 1

    lines = [
        "[历史工具上下文摘要：DeepSeek 隐藏推理回放缓存缺失，原始 assistant tool_calls 已转为摘要以避免向上游回放不完整工具历史。]",
    ]
    for tool_call in tool_calls:
        if not isinstance(tool_call, dict):
            continue
        function = tool_call.get("function") if isinstance(tool_call.get("function"), dict) else {}
        name = str(function.get("name") or "unknown_tool").strip()
        tool_call_id = str(tool_call.get("id") or "").strip()
        args_hash = safe_prompt_hash(function.get("arguments")) if function.get("arguments") is not None else ""
        results = [item for item in result_by_id.get(tool_call_id, []) if item]
        result_text = " | ".join(results) if results else "无可用工具结果摘要"
        lines.append(f"- 工具: {name}; id: {tool_call_id or 'unknown'}; arguments_hash: {args_hash}; result: {result_text}")

    return {"role": "user", "content": "\n".join(lines)}, next_index


def responses_usage_from_payload(payload: bytes) -> dict[str, Any]:
    if not payload:
        return {}
    try:
        parsed = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        parsed = None
    if isinstance(parsed, dict) and isinstance(parsed.get("usage"), dict):
        return parsed["usage"]

    usage: dict[str, Any] = {}
    for _event_type, event in iter_responses_sse_events(payload):
        candidate = event.get("usage")
        response = event.get("response")
        if not isinstance(candidate, dict) and isinstance(response, dict):
            candidate = response.get("usage")
        if isinstance(candidate, dict):
            usage = candidate
    return usage


def responses_outcome_from_payload(payload: bytes, *, stream: bool) -> dict[str, Any]:
    if not stream:
        try:
            parsed = json.loads(payload.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            parsed = None
        if not isinstance(parsed, dict):
            return {
                "valid_payload": False,
                "succeeded": False,
                "usage": {},
                "terminal": "",
                "error_code": "invalid_response",
            }
        usage = parsed.get("usage") if isinstance(parsed.get("usage"), dict) else {}
        response_status = str(parsed.get("status") or "").strip().lower()
        has_error = isinstance(parsed.get("error"), dict) or str(parsed.get("object") or "").lower() == "error"
        if response_status in {"failed", "incomplete", "cancelled", "error"} or has_error:
            terminal = response_status or "error"
            return {
                "valid_payload": True,
                "succeeded": False,
                "usage": usage,
                "terminal": terminal,
                "error_code": safe_error_code_from_object(parsed) or f"response_{terminal}",
            }
        return {
            "valid_payload": True,
            "succeeded": True,
            "usage": usage,
            "terminal": response_status,
            "error_code": None,
        }

    terminal_events = {
        "response.completed",
        "response.failed",
        "response.incomplete",
        "response.cancelled",
        "response.error",
        "error",
    }
    usage: dict[str, Any] = {}
    terminal = ""
    terminal_error_code = ""
    for event_type, event in iter_responses_sse_events(payload):
        candidate = event.get("usage")
        response = event.get("response")
        if not isinstance(candidate, dict) and isinstance(response, dict):
            candidate = response.get("usage")
        if isinstance(candidate, dict):
            usage = candidate
        if event_type in terminal_events:
            terminal = event_type
            terminal_error_code = safe_error_code_from_object(event)

    if terminal == "response.completed":
        return {
            "valid_payload": True,
            "succeeded": True,
            "usage": usage,
            "terminal": terminal,
            "error_code": None,
        }
    if terminal:
        return {
            "valid_payload": True,
            "succeeded": False,
            "usage": usage,
            "terminal": terminal,
            "error_code": terminal_error_code or terminal.replace(".", "_"),
        }
    return {
        "valid_payload": False,
        "succeeded": False,
        "usage": usage,
        "terminal": "",
        "error_code": "missing_terminal_event",
    }


def apply_cached_openai_reasoning(body: dict[str, Any], api_key: str) -> dict[str, Any]:
    messages = body.get("messages")
    if not isinstance(messages, list):
        return body

    repaired_messages: list[Any] = []
    index = 0
    changed = False
    model = body.get("model")
    while index < len(messages):
        message = messages[index]
        if not isinstance(message, dict) or message.get("role") != "assistant":
            repaired_messages.append(message)
            index += 1
            continue
        if isinstance(message.get("reasoning_content"), str) and message["reasoning_content"].strip():
            repaired_messages.append(message)
            index += 1
            continue
        tool_calls = message.get("tool_calls")
        if not isinstance(tool_calls, list) or not tool_calls:
            repaired_messages.append(message)
            index += 1
            continue
        patched_message = deepcopy(message)
        for tool_call in tool_calls:
            if not isinstance(tool_call, dict):
                continue
            tool_call_id = str(tool_call.get("id") or "").strip()
            if not tool_call_id:
                continue
            reasoning = cached_openai_reasoning(
                api_key,
                model,
                tool_call_id,
                tool_call,
                messages[:index],
            )
            if reasoning:
                patched_message["reasoning_content"] = reasoning
                break
        if isinstance(patched_message.get("reasoning_content"), str) and patched_message["reasoning_content"].strip():
            repaired_messages.append(patched_message)
            changed = True
            index += 1
            continue

        summary_message, next_index = summarize_openai_reasoning_cache_miss(messages, index)
        repaired_messages.append(summary_message)
        index = next_index
        changed = True
    if changed:
        body["messages"] = repaired_messages
    return body


def openai_content_to_anthropic_blocks(content: Any) -> list[dict[str, Any]]:
    if isinstance(content, str):
        return [{"type": "text", "text": content}] if content else []
    if not isinstance(content, list):
        return []

    blocks: list[dict[str, Any]] = []
    for item in content:
        if not isinstance(item, dict):
            continue
        item_type = item.get("type")
        if item_type in {"text", "input_text"}:
            text = item.get("text")
            if isinstance(text, str) and text:
                blocks.append({"type": "text", "text": text})
            continue
        if item_type in {"image_url", "input_image"}:
            image_url = item.get("image_url")
            url = image_url.get("url") if isinstance(image_url, dict) else item.get("image_url")
            if not isinstance(url, str) or not url.startswith("data:") or ";base64," not in url:
                continue
            meta, data = url.split(";base64,", 1)
            media_type = meta.removeprefix("data:") or "image/png"
            blocks.append({"type": "image", "source": {"type": "base64", "media_type": media_type, "data": data}})
    return blocks


def openai_tool_result_content_to_anthropic(content: Any) -> Any:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    blocks = openai_content_to_anthropic_blocks(content)
    if blocks:
        return blocks
    return json.dumps(content, ensure_ascii=False)


def openai_messages_to_anthropic(messages: Any, api_key: str = "") -> tuple[list[dict[str, Any]], str | None]:
    if not isinstance(messages, list):
        return [], None

    anthropic_messages: list[dict[str, Any]] = []
    system_parts: list[str] = []
    for message in messages:
        if not isinstance(message, dict):
            continue
        role = str(message.get("role") or "").strip()
        if role == "system":
            blocks = openai_content_to_anthropic_blocks(message.get("content"))
            system_parts.extend(block.get("text", "") for block in blocks if block.get("type") == "text")
            continue
        if role == "assistant":
            content_blocks = openai_content_to_anthropic_blocks(message.get("content"))
            tool_calls = message.get("tool_calls")
            if isinstance(tool_calls, list):
                for tool_call in tool_calls:
                    if not isinstance(tool_call, dict):
                        continue
                    function = tool_call.get("function")
                    if not isinstance(function, dict):
                        continue
                    name = function.get("name")
                    if not isinstance(name, str) or not name:
                        continue
                    tool_use_id = str(tool_call.get("id") or f"call_{uuid.uuid4().hex}")
                    content_blocks.extend(cached_anthropic_thinking(api_key, tool_use_id))
                    content_blocks.append(
                        {
                            "type": "tool_use",
                            "id": tool_use_id,
                            "name": name,
                            "input": parse_json_object(function.get("arguments")),
                        }
                    )
            if content_blocks:
                anthropic_messages.append({"role": "assistant", "content": content_blocks})
            continue
        if role == "tool":
            tool_use_id = str(message.get("tool_call_id") or "").strip()
            if not tool_use_id:
                continue
            anthropic_messages.append(
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "tool_result",
                            "tool_use_id": tool_use_id,
                            "content": openai_tool_result_content_to_anthropic(message.get("content")),
                        }
                    ],
                }
            )
            continue

        content_blocks = openai_content_to_anthropic_blocks(message.get("content"))
        if content_blocks:
            anthropic_messages.append({"role": "user", "content": content_blocks})

    system = "\n".join(part for part in system_parts if part)
    return anthropic_messages, system or None


def openai_tools_to_anthropic(tools: Any) -> list[dict[str, Any]]:
    if not isinstance(tools, list):
        return []

    converted: list[dict[str, Any]] = []
    for tool in tools:
        if not isinstance(tool, dict) or tool.get("type") != "function":
            continue
        function = tool.get("function")
        if not isinstance(function, dict):
            continue
        name = function.get("name")
        if not isinstance(name, str) or not name:
            continue
        input_schema = function.get("parameters")
        converted.append(
            {
                "name": name,
                "description": str(function.get("description") or ""),
                "input_schema": input_schema if isinstance(input_schema, dict) else {"type": "object", "properties": {}},
            }
        )
    return converted


def openai_tool_choice_to_anthropic(tool_choice: Any) -> Any:
    if tool_choice in (None, "auto"):
        return None
    if tool_choice == "required":
        return {"type": "any"}
    if tool_choice == "none":
        return {"type": "none"}
    if isinstance(tool_choice, dict) and tool_choice.get("type") == "function":
        function = tool_choice.get("function")
        if isinstance(function, dict) and function.get("name"):
            return {"type": "tool", "name": str(function["name"])}
    return None


def openai_body_to_anthropic_body(
    body: dict[str, Any],
    api_key: str = "",
    *,
    disable_tool_choice: bool = False,
) -> dict[str, Any]:
    messages, system = openai_messages_to_anthropic(body.get("messages"), api_key)
    try:
        max_tokens = int(body.get("max_tokens") or 1024)
    except (TypeError, ValueError):
        max_tokens = 1024
    upstream: dict[str, Any] = {
        "model": body.get("model"),
        "stream": False,
        "max_tokens": max_tokens,
        "messages": messages,
    }
    if system:
        upstream["system"] = system
    for key in ("temperature", "top_p", "stop_sequences", "metadata"):
        if key in body:
            upstream[key] = body[key]
    for key in ("thinking", "output_config"):
        if key in body:
            upstream[key] = deepcopy(body[key])
    if (
        "reasoning_effort" in body
        and normalize_model_name(public_model_name(body.get("model"))) == "deepseek-v4-flash"
    ):
        output_config = deepcopy(upstream.get("output_config")) if isinstance(upstream.get("output_config"), dict) else {}
        if "effort" not in output_config and body.get("reasoning_effort") is not None:
            output_config["effort"] = body["reasoning_effort"]
        if output_config:
            upstream["output_config"] = output_config
    if "stop" in body:
        stop = body["stop"]
        upstream["stop_sequences"] = stop if isinstance(stop, list) else [stop]
    tools = openai_tools_to_anthropic(body.get("tools"))
    if tools:
        upstream["tools"] = tools
        if not disable_tool_choice:
            tool_choice = openai_tool_choice_to_anthropic(body.get("tool_choice"))
            if tool_choice:
                upstream["tool_choice"] = tool_choice
    return upstream


def anthropic_response_to_openai(response: dict[str, Any], model: str) -> dict[str, Any]:
    content = response.get("content") if isinstance(response.get("content"), list) else []
    text_parts: list[str] = []
    tool_calls: list[dict[str, Any]] = []
    for block in content:
        if not isinstance(block, dict):
            continue
        block_type = block.get("type")
        if block_type == "text" and isinstance(block.get("text"), str):
            text = sanitize_openai_text(block["text"])
            if text:
                text_parts.append(text)
            continue
        if block_type == "tool_use":
            tool_calls.append(
                {
                    "id": str(block.get("id") or f"call_{uuid.uuid4().hex}"),
                    "type": "function",
                    "function": {
                        "name": str(block.get("name") or "tool"),
                        "arguments": json.dumps(block.get("input") or {}, ensure_ascii=False, separators=(",", ":")),
                    },
                }
            )

    message: dict[str, Any] = {"role": "assistant", "content": "\n".join(text_parts) or None}
    finish_reason = "stop"
    if tool_calls:
        message["tool_calls"] = tool_calls
        finish_reason = "tool_calls"
    usage = response.get("usage") if isinstance(response.get("usage"), dict) else {}
    prompt_tokens = int(usage.get("input_tokens") or 0)
    completion_tokens = int(usage.get("output_tokens") or 0)
    return {
        "id": response.get("id") or f"chatcmpl-cats-{int(time.time() * 1000)}",
        "object": "chat.completion",
        "created": int(time.time()),
        "model": model,
        "choices": [{"index": 0, "message": message, "finish_reason": finish_reason}],
        "usage": {
            "prompt_tokens": prompt_tokens,
            "completion_tokens": completion_tokens,
            "total_tokens": prompt_tokens + completion_tokens,
        },
    }


def log_upstream_error(
    label: str,
    status: int,
    body: dict[str, Any],
    payload: bytes,
    *,
    error_code: str | None = None,
) -> None:
    safe_error_code = safe_error_code_value(error_code) or safe_error_code_from_payload(payload) or f"http_{status}"
    LOGGER.warning(
        "%s upstream_error status=%s error_code=%s model=%s stream=%s tools=%s",
        label,
        status,
        safe_error_code,
        body.get("model"),
        body.get("stream") is True,
        len(body.get("tools")) if isinstance(body.get("tools"), list) else 0,
    )


def sse_payload(payload: dict[str, Any]) -> bytes:
    return b"data: " + json_bytes(payload) + b"\n\n"


def to_openai_sse(response: dict[str, Any]) -> Iterable[bytes]:
    choices = response.get("choices") if isinstance(response.get("choices"), list) else []
    first_choice = choices[0] if choices and isinstance(choices[0], dict) else {}
    message = first_choice.get("message") if isinstance(first_choice.get("message"), dict) else {}
    finish_reason = first_choice.get("finish_reason") or "stop"
    created = int(response.get("created") or time.time())
    model = response.get("model") or message.get("model") or "unknown"
    response_id = response.get("id") or f"chatcmpl-cats-{int(time.time() * 1000)}"
    usage = response.get("usage") if isinstance(response.get("usage"), dict) else None

    base = {
        "id": response_id,
        "object": "chat.completion.chunk",
        "created": created,
        "model": model,
    }

    role_chunk = dict(base)
    role_chunk["choices"] = [{"index": 0, "delta": {"role": "assistant"}, "finish_reason": None}]
    yield sse_payload(role_chunk)

    content = message.get("content")
    if isinstance(content, str) and content:
        content_chunk = dict(base)
        content_chunk["choices"] = [{"index": 0, "delta": {"content": content}, "finish_reason": None}]
        yield sse_payload(content_chunk)

    tool_calls = message.get("tool_calls")
    if isinstance(tool_calls, list) and tool_calls:
        normalized = []
        for index, tool_call in enumerate(tool_calls):
            if not isinstance(tool_call, dict):
                continue
            current = deepcopy(tool_call)
            current["index"] = index
            normalized.append(current)
        if normalized:
            tool_chunk = dict(base)
            tool_chunk["choices"] = [{"index": 0, "delta": {"tool_calls": normalized}, "finish_reason": None}]
            yield sse_payload(tool_chunk)

    final_chunk = dict(base)
    final_chunk["choices"] = [{"index": 0, "delta": {}, "finish_reason": finish_reason}]
    if usage is not None:
        final_chunk["usage"] = usage
    yield sse_payload(final_chunk)
    yield b"data: [DONE]\n\n"


def upstream_body_for_chat(
    body: dict[str, Any],
    provider: str,
    canary_uid: Any | None = None,
    api_key: str = "",
) -> dict[str, Any]:
    upstream = apply_reasoning_defaults(deepcopy(body), OPENAI_REASONING_DEFAULTS, canary_uid=canary_uid)
    if provider == "deepseek-openai":
        apply_cached_openai_reasoning(upstream, api_key)
    model = str(upstream.get("model") or "").strip()
    if model and "/" not in model:
        upstream["model"] = f"{provider}/{model}"
    if provider == "deepseek-openai" and isinstance(upstream.get("tools"), list):
        tool_choice = upstream.get("tool_choice")
        if tool_choice not in (None, "auto", "none"):
            upstream["tool_choice"] = "auto"
    upstream["stream"] = False
    upstream.pop("stream_options", None)
    return upstream


def upstream_body_for_responses(body: dict[str, Any], provider: str) -> dict[str, Any]:
    upstream = deepcopy(body)
    model = str(upstream.get("model") or "").strip()
    if model and "/" not in model:
        upstream["model"] = f"{provider}/{model}"
    return upstream


class Handler(BaseHTTPRequestHandler):
    server_version = "CatsOpenAIAdapter/0.1"

    def log_message(self, fmt: str, *args: Any) -> None:
        LOGGER.info("%s - %s", self.address_string(), fmt % args)

    def do_GET(self) -> None:
        path = self.normalized_path()
        if path == "/health":
            self.send_json(HTTPStatus.OK, {"ok": True})
            return
        if path in {"/v1/models", "/models"}:
            self.send_json(HTTPStatus.OK, self.model_list_payload())
            return
        self.proxy_to_bifrost()

    def do_POST(self) -> None:
        path = self.normalized_path()
        if path not in {"/v1/chat/completions", "/chat/completions", "/v1/responses", "/responses"}:
            self.proxy_to_bifrost()
            return

        try:
            body = self.read_json_body()
        except (ValueError, json.JSONDecodeError) as exc:
            self.send_json(HTTPStatus.BAD_REQUEST, openai_error(str(exc), "invalid_request_error"))
            return

        model = body.get("model") or ""
        provider_pool = list(MODEL_PROVIDER_POOLS.get(normalize_model_name(model), ()))
        client_lease: ClientPoolLease | None = None
        if provider_pool:
            api_key = relay_api_key(self.headers)
            client_lease = acquire_client_pool_lease(api_key)
            if client_lease is None:
                logical_request_id = f"cats-openai-rejected-logical-{uuid.uuid4()}"
                endpoint = "/v1/responses" if path in {"/v1/responses", "/responses"} else "/v1/chat/completions"
                self.record_pool_rejection(
                    api_key=api_key,
                    model=model,
                    logical_request_id=logical_request_id,
                    endpoint=endpoint,
                    error_code="client_concurrency_limit",
                    http_status=HTTPStatus.TOO_MANY_REQUESTS,
                    provider_pool=provider_pool,
                )
                self.send_bytes(
                    HTTPStatus.TOO_MANY_REQUESTS,
                    json_bytes(
                        openai_error(
                            "too many concurrent requests for this relay key; retry shortly",
                            "server_error",
                            "client_concurrency_limit",
                        )
                    ),
                    {"Content-Type": "application/json", "Retry-After": "1"},
                )
                return
        try:
            if path in {"/v1/responses", "/responses"}:
                self.handle_responses(body)
                return
            self.handle_chat_completion(body)
        finally:
            if client_lease is not None:
                release_client_pool_lease(client_lease)

    def normalized_path(self) -> str:
        path = self.path.split("?", 1)[0]
        if path.startswith("/openai/"):
            return path[len("/openai") :]
        return path

    def model_list_payload(self) -> dict[str, Any]:
        names = sorted(MODEL_NAMES, key=normalize_model_name)
        models = []
        for name in names:
            model = {
                "id": name,
                "object": "model",
                "created": 0,
                "owned_by": "catsco",
            }
            capabilities = model_capabilities_for_model(name)
            if capabilities:
                model["capabilities"] = capabilities
            models.append(model)
        return {
            "object": "list",
            "data": models,
        }

    def read_json_body(self) -> dict[str, Any]:
        length = int(self.headers.get("content-length") or "0")
        raw = self.rfile.read(length) if length else b"{}"
        parsed = json.loads(raw.decode("utf-8"))
        if not isinstance(parsed, dict):
            raise ValueError("JSON body must be an object")
        return parsed

    def routed_provider_candidates(self, model: Any, api_key: str) -> tuple[list[str], int | None]:
        model_key = normalize_model_name(model)
        provider_pool = list(MODEL_PROVIDER_POOLS.get(model_key, ()))
        if len(provider_pool) <= 1:
            return provider_candidates_for_model(model), None

        status, payload = self.call_relay_admin(
            "/internal/passthrough/route",
            {
                "api_key": api_key,
                "model": model,
                "providers": provider_pool,
            },
            timeout=RELAY_ADMIN_ROUTE_TIMEOUT_SECONDS,
        )
        preferred = str(payload.get("provider") or "").strip() if 200 <= status < 300 else ""
        if preferred not in provider_pool:
            LOGGER.error(
                "provider affinity lookup failed model=%s status=%s; using configured provider order",
                model,
                status,
            )
            preferred = ""
        try:
            affinity_revision = int(payload.get("affinity_revision") or 0)
        except (TypeError, ValueError):
            affinity_revision = 0
        return (
            provider_candidates_for_model(
                model,
                preferred_provider=preferred,
                include_unavailable_fallbacks=True,
            ),
            affinity_revision if affinity_revision > 0 else None,
        )

    def record_pool_rejection(
        self,
        *,
        api_key: str,
        model: Any,
        logical_request_id: str,
        endpoint: str,
        error_code: str,
        http_status: int,
        provider_pool: list[str],
    ) -> None:
        request_id = f"cats-openai-rejected-{uuid.uuid4()}"
        self.submit_relay_usage(
            {
                "api_key": api_key,
                "request_id": request_id,
                "logical_request_id": logical_request_id,
                "endpoint": endpoint,
                "http_status": int(http_status),
                "error_code": error_code,
                "provider": "relay-provider-pool",
                "model": model,
                "status": "error",
                "latency_ms": 0,
                "usage": {},
                "provider_pool": provider_pool,
            },
            context=f"provider-pool/{model}/{error_code}/{request_id}",
        )

    def submit_relay_usage(self, payload: dict[str, Any], *, context: str) -> None:
        RELAY_USAGE_DISPATCHER.submit(call_relay_admin_service, payload, context)

    def handle_chat_completion(self, body: dict[str, Any]) -> None:
        try:
            body = normalize_gpt56_reasoning_controls(body, endpoint="chat")
        except ValueError as exc:
            self.send_json(
                HTTPStatus.BAD_REQUEST,
                openai_error(str(exc), "invalid_request_error", "invalid_reasoning_effort"),
            )
            return
        model = body.get("model") or ""
        logical_request_id = f"cats-openai-logical-{uuid.uuid4()}"
        tool_bridge_provider = anthropic_tool_bridge_provider_for_model(model) if body_has_tool_state(body) else ""
        if tool_bridge_provider:
            self.handle_anthropic_bridge_chat_completion(
                body,
                tool_bridge_provider,
                logical_request_id=logical_request_id,
            )
            return

        bridge_provider = anthropic_bridge_provider_for_model(model)
        if bridge_provider:
            self.handle_anthropic_bridge_chat_completion(
                body,
                bridge_provider,
                logical_request_id=logical_request_id,
            )
            return

        api_key = relay_api_key(self.headers)
        provider_pool = list(MODEL_PROVIDER_POOLS.get(normalize_model_name(model), ()))
        providers, affinity_revision = self.routed_provider_candidates(model, api_key)
        if not providers:
            if provider_pool:
                self.record_pool_rejection(
                    api_key=api_key,
                    model=model,
                    logical_request_id=logical_request_id,
                    endpoint="/v1/chat/completions",
                    error_code="provider_pool_unavailable",
                    http_status=HTTPStatus.SERVICE_UNAVAILABLE,
                    provider_pool=provider_pool,
                )
                self.send_retryable_pool_error(
                    "all relay providers are cooling down; retry shortly",
                    "provider_pool_unavailable",
                )
                return
            self.send_json(
                HTTPStatus.BAD_REQUEST,
                openai_error(f"model is not enabled for OpenAI-compatible relay: {model}", "invalid_request_error"),
            )
            return

        prompt_hash_id = client_prompt_hash_id(self.headers)
        attempt = 0
        provider_wait_deadline = (
            time.monotonic() + provider_pool_request_wait_seconds(model)
        )
        for provider in providers:
            request_id = f"cats-openai-{provider}-{uuid.uuid4()}"
            lease = acquire_provider_pool_lease(
                model,
                provider,
                deadline=provider_wait_deadline,
            )
            if lease is None:
                LOGGER.info(
                    "openai provider skipped unavailable_or_saturated model=%s provider=%s",
                    model,
                    provider,
                )
                continue
            started = time.monotonic()
            upstream_attempted = False
            try:
                preflight_status, preflight_body = self.call_relay_admin(
                    "/internal/passthrough/preflight",
                    {
                        "api_key": api_key,
                        "provider": provider,
                        "model": model,
                        "request_id": request_id,
                    },
                    timeout=RELAY_ADMIN_PREFLIGHT_TIMEOUT_SECONDS,
                )
                if preflight_status < 200 or preflight_status >= 300:
                    message = str(preflight_body.get("error") or "relay budget preflight failed")
                    log_provider_pool_event(
                        "preflight_rejected",
                        logical_request_id=logical_request_id,
                        request_id=request_id,
                        endpoint="/v1/chat/completions",
                        model=model,
                        provider=provider,
                        http_status=preflight_status,
                        classification="caller_or_budget_error",
                    )
                    self.send_json(preflight_status, openai_error(message, "invalid_request_error"))
                    return

                upstream_body = upstream_body_for_chat(
                    body,
                    provider,
                    canary_uid=preflight_body.get("uid"),
                    api_key=api_key,
                )
                log_safe_prompt_hash(
                    boundary="relay.openai.passthrough.upstream_body",
                    request_id=request_id,
                    client_prompt_hash_id=prompt_hash_id,
                    provider=provider,
                    model=model,
                    stream=body.get("stream") is True,
                    body=upstream_body,
                    messages=upstream_body.get("messages"),
                    tools=upstream_body.get("tools"),
                    extra={
                        "client_body": body,
                        "client_messages": body.get("messages"),
                        "client_tools": body.get("tools"),
                    },
                )
                attempt += 1
                upstream_attempted = True
                try:
                    status, headers, payload = self.call_bifrost(
                        upstream_body,
                        path="/v1/chat/completions",
                        extra_headers={
                            "x-bf-model-provider": provider,
                            "x-model-provider": provider,
                            "x-request-id": request_id,
                        },
                    )
                except Exception as exc:
                    LOGGER.exception(
                        "openai passthrough unexpected upstream exception provider=%s",
                        provider,
                    )
                    status = HTTPStatus.BAD_GATEWAY
                    headers = {"Content-Type": "application/json"}
                    payload = json_bytes(
                        openai_error(
                            f"upstream relay failed: {type(exc).__name__}",
                            "server_error",
                            "upstream_connection_error",
                        )
                    )
            finally:
                if upstream_attempted:
                    release_provider_pool_lease(lease)
                else:
                    abandon_provider_pool_lease(lease)
            duration_ms = int((time.monotonic() - started) * 1000)

            message: dict[str, Any] | None = None
            if payload:
                try:
                    parsed = json.loads(payload.decode("utf-8"))
                    if isinstance(parsed, dict):
                        message = parsed
                except (json.JSONDecodeError, UnicodeDecodeError):
                    message = None

            valid_payload = chat_completion_payload_is_valid(message)
            attempt_succeeded = 200 <= status < 300 and valid_payload
            error_code = None
            if not attempt_succeeded:
                error_code = safe_error_code_from_payload(payload)
                if not error_code:
                    error_code = f"http_{status}" if status < 200 or status >= 300 else "invalid_response"
            usage = (message or {}).get("usage") if isinstance((message or {}).get("usage"), dict) else {}
            failover = should_failover_provider(
                status,
                valid_payload=valid_payload,
                payload=payload,
                error_code=error_code,
            )
            opens_circuit = provider_failure_opens_circuit(
                status,
                valid_payload=valid_payload,
                payload=payload,
                error_code=error_code,
            )
            record_provider_pool_result(
                model,
                provider,
                available=not opens_circuit,
                lease=lease,
                cooldown_seconds=provider_cooldown_seconds(
                    headers,
                    status=status,
                    error_code=error_code,
                ),
                failure_threshold=provider_failure_threshold(
                    status,
                    error_code=error_code,
                ),
                error_fingerprint=provider_error_fingerprint(status, error_code),
            )
            log_provider_pool_event(
                "provider_attempt",
                logical_request_id=logical_request_id,
                request_id=request_id,
                endpoint="/v1/chat/completions",
                model=model,
                provider=provider,
                attempt=attempt,
                http_status=status,
                error_code=error_code,
                fingerprint=provider_error_fingerprint(status, error_code) if error_code else None,
                classification=(
                    "success"
                    if attempt_succeeded
                    else "explicit_request_error"
                    if str(error_code or "").lower() in EXPLICIT_REQUEST_ERROR_CODES
                    else "provider_line_failure"
                    if failover
                    else "terminal_upstream_error"
                ),
                failover=failover,
                circuit_open=PROVIDER_POOL_UNAVAILABLE_UNTIL.get(provider, 0.0) > time.monotonic(),
                usage=usage_counts_from_payload(usage),
            )
            log_prompt_cache_usage(
                boundary="relay.openai.passthrough.usage",
                request_id=request_id,
                client_prompt_hash_id=prompt_hash_id,
                provider=provider,
                model=model,
                stream=body.get("stream") is True,
                status="success" if attempt_succeeded else "error",
                http_status=status,
                latency_ms=duration_ms,
                usage=usage,
            )
            self.submit_relay_usage(
                {
                    "api_key": api_key,
                    "request_id": request_id,
                    "logical_request_id": logical_request_id,
                    "attempt": attempt,
                    "endpoint": "/v1/chat/completions",
                    "http_status": status,
                    "error_code": error_code,
                    "provider": provider,
                    "model": model,
                    "status": "success" if attempt_succeeded else "error",
                    "latency_ms": duration_ms,
                    "usage": usage,
                    "provider_pool": provider_pool,
                    "affinity_revision": affinity_revision,
                },
                context=f"chat/{provider}/{request_id}",
            )

            if failover and attempt < len(providers):
                log_upstream_error("openai_passthrough_failover", status, upstream_body, payload)
                LOGGER.warning(
                    "openai provider failover model=%s provider=%s status=%s attempt=%s/%s classification=provider_line_failure fingerprint=%s",
                    model,
                    provider,
                    status,
                    attempt,
                    len(providers),
                    provider_error_fingerprint(status, error_code),
                )
                continue
            break

        if attempt == 0:
            rejection_code = (
                "provider_pool_unavailable"
                if provider_pool_all_cooling(model)
                else "provider_pool_busy"
            )
            self.record_pool_rejection(
                api_key=api_key,
                model=model,
                logical_request_id=logical_request_id,
                endpoint="/v1/chat/completions",
                error_code=rejection_code,
                http_status=HTTPStatus.SERVICE_UNAVAILABLE,
                provider_pool=provider_pool,
            )
            self.send_retryable_pool_error(
                (
                    "all relay providers are cooling down; retry shortly"
                    if rejection_code == "provider_pool_unavailable"
                    else "all relay providers are busy; retry shortly"
                ),
                rejection_code,
            )
            return
        if not attempt_succeeded and failover:
            self.record_pool_rejection(
                api_key=api_key,
                model=model,
                logical_request_id=logical_request_id,
                endpoint="/v1/chat/completions",
                error_code="provider_pool_unavailable",
                http_status=HTTPStatus.SERVICE_UNAVAILABLE,
                provider_pool=provider_pool,
            )
            LOGGER.error(
                "openai provider pool exhausted model=%s attempts=%s providers=%s final_status=%s fingerprint=%s",
                model,
                attempt,
                len(provider_pool),
                status,
                provider_error_fingerprint(status, error_code),
            )
            log_provider_pool_event(
                "pool_exhausted",
                logical_request_id=logical_request_id,
                endpoint="/v1/chat/completions",
                model=model,
                attempts=attempt,
                provider_count=len(provider_pool),
                final_http_status=status,
                fingerprint=provider_error_fingerprint(status, error_code),
                returned_http_status=HTTPStatus.SERVICE_UNAVAILABLE,
                returned_error_code="provider_pool_unavailable",
            )
            self.send_retryable_pool_error(
                "all relay providers failed; retry shortly",
                "provider_pool_unavailable",
            )
            return
        if status < 200 or status >= 300:
            log_upstream_error("openai_passthrough", status, upstream_body, payload)
            self.send_bytes(status, payload, headers)
            return

        if message is None:
            self.send_json(
                HTTPStatus.BAD_GATEWAY,
                openai_error("upstream returned invalid JSON", "server_error"),
            )
            return

        if provider == "deepseek-openai":
            remember_openai_reasoning(api_key, message, upstream_body)
        message = sanitize_openai_response(message)

        if body.get("stream") is True:
            self.send_response(HTTPStatus.OK)
            self.send_header("Content-Type", "text/event-stream; charset=utf-8")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.send_header("X-Accel-Buffering", "no")
            self.end_headers()
            try:
                for chunk in to_openai_sse(message):
                    self.wfile.write(chunk)
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                LOGGER.info("client disconnected during openai_passthrough_sse path=%s request_id=%s", self.path, request_id)
                return
            self.close_connection = True
            return

        self.send_json(status, message)

    def handle_responses(self, body: dict[str, Any]) -> None:
        try:
            body = normalize_gpt56_reasoning_controls(body, endpoint="responses")
        except ValueError as exc:
            self.send_json(
                HTTPStatus.BAD_REQUEST,
                openai_error(str(exc), "invalid_request_error", "invalid_reasoning_effort"),
            )
            return
        model = body.get("model") or ""
        logical_request_id = f"cats-openai-responses-logical-{uuid.uuid4()}"
        api_key = relay_api_key(self.headers)
        provider_pool = list(MODEL_PROVIDER_POOLS.get(normalize_model_name(model), ()))
        providers, affinity_revision = self.routed_provider_candidates(model, api_key)
        if not providers:
            if provider_pool:
                self.record_pool_rejection(
                    api_key=api_key,
                    model=model,
                    logical_request_id=logical_request_id,
                    endpoint="/v1/responses",
                    error_code="provider_pool_unavailable",
                    http_status=HTTPStatus.SERVICE_UNAVAILABLE,
                    provider_pool=provider_pool,
                )
                self.send_retryable_pool_error(
                    "all relay providers are cooling down; retry shortly",
                    "provider_pool_unavailable",
                )
                return
            self.send_json(
                HTTPStatus.BAD_REQUEST,
                openai_error(f"model is not enabled for OpenAI-compatible relay: {model}", "invalid_request_error"),
            )
            return

        stream = body.get("stream") is True
        valid_payload = False
        attempt = 0
        response_committed = False
        downstream_disconnected = False
        outcome: dict[str, Any] = {
            "valid_payload": False,
            "succeeded": False,
            "usage": {},
            "terminal": "",
            "error_code": "upstream_connection_error",
        }
        provider_wait_deadline = (
            time.monotonic() + provider_pool_request_wait_seconds(model)
        )
        for provider in providers:
            request_id = f"cats-openai-responses-{provider}-{uuid.uuid4()}"
            lease = acquire_provider_pool_lease(
                model,
                provider,
                deadline=provider_wait_deadline,
            )
            if lease is None:
                LOGGER.info(
                    "openai responses provider skipped unavailable_or_saturated model=%s provider=%s",
                    model,
                    provider,
                )
                continue
            started = time.monotonic()
            upstream_attempted = False
            try:
                preflight_status, preflight_body = self.call_relay_admin(
                    "/internal/passthrough/preflight",
                    {
                        "api_key": api_key,
                        "provider": provider,
                        "model": model,
                        "request_id": request_id,
                    },
                    timeout=RELAY_ADMIN_PREFLIGHT_TIMEOUT_SECONDS,
                )
                if preflight_status < 200 or preflight_status >= 300:
                    message = str(preflight_body.get("error") or "relay budget preflight failed")
                    log_provider_pool_event(
                        "preflight_rejected",
                        logical_request_id=logical_request_id,
                        request_id=request_id,
                        endpoint="/v1/responses",
                        model=model,
                        provider=provider,
                        http_status=preflight_status,
                        classification="caller_or_budget_error",
                    )
                    self.send_json(preflight_status, openai_error(message, "invalid_request_error"))
                    return

                upstream_body = upstream_body_for_responses(body, provider)
                attempt += 1
                upstream_attempted = True
                try:
                    request_headers = {
                        "x-bf-model-provider": provider,
                        "x-model-provider": provider,
                        "x-request-id": request_id,
                    }
                    if stream:
                        status, headers, upstream_stream = self.open_bifrost_stream(
                            upstream_body,
                            path="/v1/responses",
                            extra_headers=request_headers,
                        )
                        content_type = str(
                            headers.get("Content-Type") or headers.get("content-type") or ""
                        ).lower()
                        if 200 <= status < 300 and "text/event-stream" in content_type:
                            response_committed = True
                            outcome, downstream_disconnected = self.forward_responses_stream(
                                status,
                                headers,
                                upstream_stream,
                            )
                            payload = b""
                            if downstream_disconnected:
                                LOGGER.info(
                                    "openai responses downstream disconnected while streaming provider=%s request_id=%s",
                                    provider,
                                    request_id,
                                )
                        else:
                            try:
                                payload = upstream_stream.read()
                            finally:
                                upstream_stream.close()
                    else:
                        status, headers, payload = self.call_bifrost(
                            upstream_body,
                            path="/v1/responses",
                            extra_headers=request_headers,
                        )
                except Exception as exc:
                    LOGGER.exception(
                        "openai responses unexpected upstream exception provider=%s",
                        provider,
                    )
                    if response_committed:
                        return
                    status = HTTPStatus.BAD_GATEWAY
                    headers = {"Content-Type": "application/json"}
                    payload = json_bytes(
                        openai_error(
                            f"upstream relay failed: {type(exc).__name__}",
                            "server_error",
                            "upstream_connection_error",
                        )
                    )
            finally:
                if upstream_attempted:
                    release_provider_pool_lease(lease)
                else:
                    abandon_provider_pool_lease(lease)
            duration_ms = int((time.monotonic() - started) * 1000)
            try:
                if not response_committed:
                    outcome = responses_outcome_from_payload(payload, stream=stream)
            except Exception:
                LOGGER.exception(
                    "openai responses failed to inspect upstream payload provider=%s",
                    provider,
                )
                status = HTTPStatus.BAD_GATEWAY
                headers = {"Content-Type": "application/json"}
                payload = json_bytes(
                    openai_error(
                        "upstream returned an invalid response",
                        "server_error",
                        "invalid_response",
                    )
                )
                outcome = {
                    "valid_payload": False,
                    "succeeded": False,
                    "usage": {},
                    "error_code": "invalid_response",
                }
            valid_payload = bool(outcome["valid_payload"])
            attempt_succeeded = 200 <= status < 300 and bool(outcome["succeeded"])
            provider_payload_valid = valid_payload if response_committed else attempt_succeeded
            usage = outcome["usage"]
            error_code = None
            if not attempt_succeeded:
                if status < 200 or status >= 300:
                    error_code = safe_error_code_from_payload(payload) or f"http_{status}"
                else:
                    error_code = outcome["error_code"]
            failover = not downstream_disconnected and should_failover_provider(
                status,
                valid_payload=provider_payload_valid,
                payload=payload,
                error_code=error_code,
            )
            opens_circuit = not downstream_disconnected and provider_failure_opens_circuit(
                status,
                valid_payload=provider_payload_valid,
                payload=payload,
                error_code=error_code,
            )
            record_provider_pool_result(
                model,
                provider,
                available=not opens_circuit,
                lease=lease,
                cooldown_seconds=provider_cooldown_seconds(
                    headers,
                    status=status,
                    error_code=error_code,
                ),
                failure_threshold=provider_failure_threshold(
                    status,
                    error_code=error_code,
                ),
                error_fingerprint=provider_error_fingerprint(status, error_code),
            )
            log_provider_pool_event(
                "provider_attempt",
                logical_request_id=logical_request_id,
                request_id=request_id,
                endpoint="/v1/responses",
                model=model,
                provider=provider,
                attempt=attempt,
                http_status=status,
                error_code=error_code,
                fingerprint=provider_error_fingerprint(status, error_code) if error_code else None,
                classification=(
                    "client_disconnected"
                    if downstream_disconnected
                    else
                    "success"
                    if attempt_succeeded
                    else "explicit_request_error"
                    if str(error_code or "").lower() in EXPLICIT_REQUEST_ERROR_CODES
                    else "provider_line_failure"
                    if failover
                    else "terminal_upstream_error"
                ),
                failover=failover,
                circuit_open=PROVIDER_POOL_UNAVAILABLE_UNTIL.get(provider, 0.0) > time.monotonic(),
                usage=usage_counts_from_payload(usage),
            )
            log_prompt_cache_usage(
                boundary="relay.openai.responses.usage",
                request_id=request_id,
                provider=provider,
                model=model,
                stream=stream,
                status="success" if attempt_succeeded else "error",
                http_status=status,
                latency_ms=duration_ms,
                usage=usage,
            )
            self.submit_relay_usage(
                {
                    "api_key": api_key,
                    "request_id": request_id,
                    "logical_request_id": logical_request_id,
                    "attempt": attempt,
                    "endpoint": "/v1/responses",
                    "http_status": status,
                    "error_code": error_code,
                    "provider": provider,
                    "model": model,
                    "status": (
                        "client_disconnected"
                        if downstream_disconnected
                        else "success"
                        if attempt_succeeded
                        else "error"
                    ),
                    "latency_ms": duration_ms,
                    "usage": usage,
                    "provider_pool": provider_pool,
                    "affinity_revision": affinity_revision,
                },
                context=f"responses/{provider}/{request_id}",
            )
            if response_committed:
                if not attempt_succeeded and not downstream_disconnected:
                    LOGGER.warning(
                        "openai responses committed stream ended unsuccessfully provider=%s terminal=%s error_code=%s",
                        provider,
                        outcome.get("terminal"),
                        error_code,
                    )
                return
            if failover and attempt < len(providers):
                log_upstream_error(
                    "openai_responses_failover",
                    status,
                    upstream_body,
                    payload,
                    error_code=error_code,
                )
                LOGGER.warning(
                    "openai responses provider failover model=%s provider=%s status=%s attempt=%s/%s classification=provider_line_failure fingerprint=%s",
                    model,
                    provider,
                    status,
                    attempt,
                    len(providers),
                    provider_error_fingerprint(status, error_code),
                )
                continue
            break

        if attempt == 0:
            rejection_code = (
                "provider_pool_unavailable"
                if provider_pool_all_cooling(model)
                else "provider_pool_busy"
            )
            self.record_pool_rejection(
                api_key=api_key,
                model=model,
                logical_request_id=logical_request_id,
                endpoint="/v1/responses",
                error_code=rejection_code,
                http_status=HTTPStatus.SERVICE_UNAVAILABLE,
                provider_pool=provider_pool,
            )
            self.send_retryable_pool_error(
                (
                    "all relay providers are cooling down; retry shortly"
                    if rejection_code == "provider_pool_unavailable"
                    else "all relay providers are busy; retry shortly"
                ),
                rejection_code,
            )
            return
        if not attempt_succeeded and failover:
            self.record_pool_rejection(
                api_key=api_key,
                model=model,
                logical_request_id=logical_request_id,
                endpoint="/v1/responses",
                error_code="provider_pool_unavailable",
                http_status=HTTPStatus.SERVICE_UNAVAILABLE,
                provider_pool=provider_pool,
            )
            LOGGER.error(
                "openai responses provider pool exhausted model=%s attempts=%s providers=%s final_status=%s fingerprint=%s",
                model,
                attempt,
                len(provider_pool),
                status,
                provider_error_fingerprint(status, error_code),
            )
            log_provider_pool_event(
                "pool_exhausted",
                logical_request_id=logical_request_id,
                endpoint="/v1/responses",
                model=model,
                attempts=attempt,
                provider_count=len(provider_pool),
                final_http_status=status,
                fingerprint=provider_error_fingerprint(status, error_code),
                returned_http_status=HTTPStatus.SERVICE_UNAVAILABLE,
                returned_error_code="provider_pool_unavailable",
            )
            self.send_retryable_pool_error(
                "all relay providers failed; retry shortly",
                "provider_pool_unavailable",
            )
            return
        if status < 200 or status >= 300:
            log_upstream_error(
                "openai_responses",
                status,
                upstream_body,
                payload,
                error_code=error_code,
            )
        elif not valid_payload:
            self.send_json(
                HTTPStatus.BAD_GATEWAY,
                openai_error("upstream returned an invalid response", "server_error"),
            )
            return
        self.send_bytes(status, payload, headers)

    def handle_anthropic_bridge_chat_completion(
        self,
        body: dict[str, Any],
        provider: str,
        *,
        logical_request_id: str | None = None,
    ) -> None:
        model = body.get("model") or ""
        if not provider_for_model(model):
            self.send_json(
                HTTPStatus.BAD_REQUEST,
                openai_error(f"model is not enabled for OpenAI-compatible relay: {model}", "invalid_request_error"),
            )
            return

        logical_request_id = logical_request_id or f"cats-openai-logical-{uuid.uuid4()}"
        request_id = f"cats-openai-bridge-{provider}-{uuid.uuid4()}"
        prompt_hash_id = client_prompt_hash_id(self.headers)
        api_key = relay_api_key(self.headers)
        preflight_status, preflight_body = self.call_relay_admin(
            "/internal/passthrough/preflight",
            {
                "api_key": api_key,
                "provider": provider,
                "model": model,
                "request_id": request_id,
            },
            timeout=RELAY_ADMIN_PREFLIGHT_TIMEOUT_SECONDS,
        )
        if preflight_status < 200 or preflight_status >= 300:
            message = str(preflight_body.get("error") or "relay budget preflight failed")
            self.send_json(preflight_status, openai_error(message, "invalid_request_error"))
            return

        started = time.monotonic()
        upstream_body = openai_body_to_anthropic_body(
            body,
            api_key,
            disable_tool_choice=bridge_provider_disables_tool_choice(provider),
        )
        upstream_body = apply_reasoning_defaults(
            upstream_body,
            ANTHROPIC_REASONING_DEFAULTS,
            model=model,
            canary_uid=preflight_body.get("uid"),
        )
        log_safe_prompt_hash(
            boundary="relay.openai.anthropic_bridge.upstream_body",
            request_id=request_id,
            client_prompt_hash_id=prompt_hash_id,
            provider=provider,
            model=model,
            stream=body.get("stream") is True,
            body=upstream_body,
            system=upstream_body.get("system"),
            messages=upstream_body.get("messages"),
            tools=upstream_body.get("tools"),
            extra={
                "client_body": body,
                "client_messages": body.get("messages"),
                "client_tools": body.get("tools"),
            },
        )
        status, headers, payload = self.call_bifrost(
            upstream_body,
            path="/anthropic_passthrough/v1/messages",
            extra_headers={
                "x-model-provider": provider,
                "x-request-id": request_id,
            },
        )
        duration_ms = int((time.monotonic() - started) * 1000)

        message: dict[str, Any] | None = None
        if payload:
            try:
                parsed = json.loads(payload.decode("utf-8"))
                if isinstance(parsed, dict):
                    message = parsed
            except json.JSONDecodeError:
                message = None

        attempt_succeeded = 200 <= status < 300 and message is not None
        error_code = None
        if not attempt_succeeded:
            error_code = safe_error_code_from_payload(payload)
            if not error_code:
                error_code = f"http_{status}" if status < 200 or status >= 300 else "invalid_response"
        usage = (message or {}).get("usage") if isinstance((message or {}).get("usage"), dict) else {}
        log_prompt_cache_usage(
            boundary="relay.openai.anthropic_bridge.usage",
            request_id=request_id,
            client_prompt_hash_id=prompt_hash_id,
            provider=provider,
            model=model,
            stream=body.get("stream") is True,
            status="success" if attempt_succeeded else "error",
            http_status=status,
            latency_ms=duration_ms,
            usage=usage,
        )
        self.submit_relay_usage(
            {
                "api_key": api_key,
                "request_id": request_id,
                "logical_request_id": logical_request_id,
                "attempt": 1,
                "endpoint": "/v1/chat/completions",
                "http_status": status,
                "error_code": error_code,
                "provider": provider,
                "model": model,
                "status": "success" if attempt_succeeded else "error",
                "latency_ms": duration_ms,
                "usage": usage,
            },
            context=f"anthropic-bridge/{provider}/{request_id}",
        )

        if status < 200 or status >= 300:
            log_upstream_error("openai_anthropic_bridge", status, upstream_body, payload)
            self.send_bytes(status, payload, headers)
            return

        if message is None:
            self.send_json(HTTPStatus.BAD_GATEWAY, openai_error("upstream returned invalid JSON", "server_error"))
            return

        remember_anthropic_thinking(api_key, message)
        openai_message = anthropic_response_to_openai(message, str(model))
        if body.get("stream") is True:
            self.send_response(HTTPStatus.OK)
            self.send_header("Content-Type", "text/event-stream; charset=utf-8")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.send_header("X-Accel-Buffering", "no")
            self.end_headers()
            try:
                for chunk in to_openai_sse(openai_message):
                    self.wfile.write(chunk)
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                LOGGER.info("client disconnected during openai_anthropic_bridge_sse path=%s request_id=%s", self.path, request_id)
                return
            self.close_connection = True
            return

        self.send_json(status, openai_message)

    def proxy_to_bifrost(self, json_body: dict[str, Any] | None = None) -> None:
        status, headers, payload = self.call_bifrost(json_body)
        self.send_bytes(status, payload, headers)

    def call_bifrost(
        self,
        json_body: dict[str, Any] | None = None,
        *,
        path: str | None = None,
        extra_headers: dict[str, str] | None = None,
    ) -> tuple[int, dict[str, str], bytes]:
        target = BIFROST_BASE_URL.rstrip("/") + (path or self.path)
        data: bytes | None = None
        if self.command in {"POST", "PUT", "PATCH"}:
            if json_body is None:
                length = int(self.headers.get("content-length") or "0")
                data = self.rfile.read(length) if length else b""
            else:
                data = json_bytes(json_body)

        headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP_BY_HOP_HEADERS}
        if json_body is not None:
            headers["Content-Type"] = "application/json"
        if extra_headers:
            headers.update(extra_headers)

        request = urllib.request.Request(target, data=data, headers=headers, method=self.command)
        try:
            with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
                return response.status, dict(response.headers.items()), response.read()
        except urllib.error.HTTPError as exc:
            return exc.code, dict(exc.headers.items()), exc.read()
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            LOGGER.error("bifrost request failed target=%s error=%s", target, exc)
            return HTTPStatus.BAD_GATEWAY, {"Content-Type": "application/json"}, json_bytes(
                openai_error("upstream relay is unavailable", "server_error")
            )

    def open_bifrost_stream(
        self,
        json_body: dict[str, Any],
        *,
        path: str,
        extra_headers: dict[str, str] | None = None,
    ) -> tuple[int, dict[str, str], BinaryIO]:
        target = BIFROST_BASE_URL.rstrip("/") + path
        headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP_BY_HOP_HEADERS}
        headers["Content-Type"] = "application/json"
        headers["Accept-Encoding"] = "identity"
        if extra_headers:
            headers.update(extra_headers)
        request = urllib.request.Request(
            target,
            data=json_bytes(json_body),
            headers=headers,
            method=self.command,
        )
        try:
            response = urllib.request.urlopen(request, timeout=TIMEOUT)
            return response.status, dict(response.headers.items()), response
        except urllib.error.HTTPError as exc:
            return exc.code, dict(exc.headers.items()), exc

    def forward_responses_stream(
        self,
        status: int,
        headers: dict[str, str],
        upstream: BinaryIO,
    ) -> tuple[dict[str, Any], bool]:
        observer = ResponsesSSEObserver()
        client_disconnected = False
        forwarded_headers = {name.lower(): value for name, value in headers.items()}
        try:
            self.send_response(status)
            self.send_header("Content-Type", "text/event-stream; charset=utf-8")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("X-Accel-Buffering", "no")
            self.send_header("Connection", "close")
            for name in ("x-request-id", "openai-request-id", "request-id"):
                value = forwarded_headers.get(name)
                if value:
                    self.send_header(name, value)
            self.end_headers()
            self.wfile.flush()
            while True:
                chunk = self.read_stream_chunk(upstream)
                if not chunk:
                    break
                observer.feed(chunk)
                self.wfile.write(chunk)
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            client_disconnected = True
        finally:
            upstream.close()
            self.close_connection = True
        observer.finish()
        return observer.outcome(), client_disconnected

    @staticmethod
    def read_stream_chunk(upstream: BinaryIO) -> bytes:
        read1 = getattr(upstream, "read1", None)
        if callable(read1):
            return read1(RESPONSES_STREAM_READ_SIZE)
        return upstream.read(RESPONSES_STREAM_READ_SIZE)

    def call_relay_admin(
        self,
        path: str,
        body: dict[str, Any],
        *,
        timeout: float | None = None,
    ) -> tuple[int, dict[str, Any]]:
        return call_relay_admin_service(path, body, timeout=timeout)

    def send_json(self, status: int, payload: dict[str, Any]) -> None:
        self.send_bytes(status, json_bytes(payload), {"Content-Type": "application/json"})

    def send_retryable_pool_error(self, message: str, error_code: str) -> None:
        retry_after = (
            PROVIDER_POOL_RETRY_AFTER_SECONDS
            + random.randint(0, PROVIDER_POOL_RETRY_AFTER_JITTER_SECONDS)
        )
        self.send_bytes(
            HTTPStatus.SERVICE_UNAVAILABLE,
            json_bytes(openai_error(message, "server_error", error_code)),
            {
                "Content-Type": "application/json",
                "Retry-After": str(retry_after),
            },
        )

    def send_bytes(self, status: int, payload: bytes, headers: dict[str, str]) -> None:
        self.send_response(status)
        content_type = headers.get("Content-Type") or headers.get("content-type") or "application/json"
        self.send_header("Content-Type", content_type)
        retry_after = headers.get("Retry-After") or headers.get("retry-after")
        if retry_after:
            self.send_header("Retry-After", retry_after)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        try:
            self.wfile.write(payload)
        except (BrokenPipeError, ConnectionResetError):
            LOGGER.info("client disconnected before response body status=%s path=%s", status, self.path)


def main() -> int:
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    LOGGER.info("listening on %s:%s, bifrost=%s, relay_admin=%s", HOST, PORT, BIFROST_BASE_URL, RELAY_ADMIN_URL)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        LOGGER.info("shutting down")
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
