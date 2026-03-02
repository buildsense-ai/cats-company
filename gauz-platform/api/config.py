"""Gauz Platform API — settings loaded from environment."""

import os
import re
from dotenv import load_dotenv

load_dotenv()


def _env(name: str, default: str = "") -> str:
    return os.getenv(name, default).strip()

# Database
DATABASE_URL = os.getenv(
    "DATABASE_URL",
    "mysql+pymysql://root:Aa@123456@localhost:3306/gauz_platform",
)

# JWT
JWT_SECRET = os.getenv("JWT_SECRET", "change-me-in-production")
JWT_ALGORITHM = "HS256"
JWT_EXPIRE_HOURS = int(os.getenv("JWT_EXPIRE_HOURS", "168"))  # 7 days

# Downstream services
CATSCOMPANY_URL = _env("CATSCOMPANY_URL", "http://localhost:6061")
CATSCOMPANY_WS_URL = _env("CATSCOMPANY_WS_URL", "ws://localhost:6061/v0/channels")
CATSCOMPANY_HTTP_BASE_URL = _env("CATSCOMPANY_HTTP_BASE_URL", CATSCOMPANY_URL)
GAUZMEM_URL = _env("GAUZMEM_URL", "http://localhost:1235")

# Legacy multitenant orchestration
XIAOBA_COMPOSE_FILE = _env("XIAOBA_COMPOSE_FILE", "/opt/services/xiaoba/deploy/docker-compose.multitenant.yml")
XIAOBA_REPO_ROOT = _env("XIAOBA_REPO_ROOT", "/opt/services/xiaoba")

# Managed deploy service orchestration
XIAOBA_BASE_IMAGE = _env("XIAOBA_BASE_IMAGE", "xiaoba-base:latest")
TENANTS_DIR = _env("TENANTS_DIR", "/opt/services/xiaoba/tenants")
TEMPLATES_DIR = _env("TEMPLATES_DIR", "/opt/services/gauz-platform/templates")

def _normalize_llm_api_base(provider: str, api_base: str) -> str:
    api_base = api_base.strip()
    if not api_base:
        return api_base

    normalized = api_base.rstrip("/")
    # XiaoBa's openai-compatible runtime expects a full chat completions URL.
    if provider.lower() == "openai":
        if normalized.endswith("/chat/completions"):
            return normalized
        if normalized.endswith("/v1"):
            return f"{normalized}/chat/completions"
        return f"{normalized}/v1/chat/completions"

    return api_base


# LLM proxy (provided to tenants)
LLM_PROXY_PROVIDER = _env("GAUZ_LLM_PROVIDER") or _env("LLM_PROXY_PROVIDER", "anthropic")
LLM_PROXY_API_BASE = _normalize_llm_api_base(
    LLM_PROXY_PROVIDER,
    _env("GAUZ_LLM_API_BASE") or _env("LLM_PROXY_API_BASE", ""),
)
LLM_PROXY_API_KEY = _env("GAUZ_LLM_API_KEY") or _env("LLM_PROXY_API_KEY", "")
LLM_PROXY_MODEL = _env("GAUZ_LLM_MODEL") or _env("LLM_PROXY_MODEL", "claude-sonnet-4-20250514")

_MANAGED_ENV_KEYS = (
    "GAUZ_LLM_FAILOVER_ON_ANY_ERROR",
    "GAUZ_STREAM_FAILOVER_ON_PARTIAL",
    "GAUZ_STREAM_RETRY",
    "GAUZ_LLM_MAX_PROMPT_TOKENS",
    "GAUZ_VISION_PROVIDER",
    "GAUZ_VISION_API_BASE",
    "GAUZ_VISION_API_KEY",
    "GAUZ_VISION_MODEL",
    "GAUZ_VISION_RETRY_MAX",
    "GAUZ_VISION_RETRY_BASE_SECONDS",
    "GAUZ_VISION_FAILOVER_ON_ANY_ERROR",
    "GAUZ_VISION_DEFAULT_MIME",
    "GAUZ_MEM_ENABLED",
    "GAUZ_MEM_BASE_URL",
    "GAUZ_MEM_PROJECT_ID",
    "GAUZ_MEM_USER_ID",
    "GAUZ_MEM_AGENT_ID",
    "GAUZ_MEM_AGENT_NAME",
    "GAUZ_MEM_OWNER_NAME",
    "MINIO_ENDPOINT",
    "MINIO_ACCESS_KEY",
    "MINIO_SECRET_KEY",
    "MINIO_BUCKET",
    "MINERU_TOKEN",
    "CATSCOMPANY_HTTP_BASE_URL",
    "SEARXNG_BASE_URL",
    "GAUZ_TOOL_ALLOW",
    "GAUZ_FS_ALLOW_OUTSIDE_READ",
    "GAUZ_FS_ALLOW_OUTSIDE",
    "GAUZ_BASH_ALLOW_DANGEROUS",
    "GAUZ_FS_ALLOW_DOTENV",
    "CONTEXT_DEBUG",
)
_DYNAMIC_MANAGED_ENV_PATTERNS = (
    re.compile(r"^GAUZ_LLM_BACKUP(?:_\d+)?_(?:PROVIDER|API_BASE|API_KEY|MODEL)$"),
    re.compile(r"^GAUZ_VISION_BACKUP(?:_\d+)?_(?:PROVIDER|API_BASE|API_KEY|MODEL)$"),
)


def _build_managed_tenant_env_defaults() -> dict[str, str]:
    env: dict[str, str] = {
        "GAUZ_LLM_PROVIDER": LLM_PROXY_PROVIDER,
        "GAUZ_LLM_API_BASE": LLM_PROXY_API_BASE,
        "GAUZ_LLM_API_KEY": LLM_PROXY_API_KEY,
        "GAUZ_LLM_MODEL": LLM_PROXY_MODEL,
        "CATSCOMPANY_HTTP_BASE_URL": CATSCOMPANY_HTTP_BASE_URL,
    }

    for key in _MANAGED_ENV_KEYS:
        if key in env:
            continue

        default = ""
        if key == "GAUZ_MEM_BASE_URL":
            default = GAUZMEM_URL
        elif key == "CATSCOMPANY_HTTP_BASE_URL":
            default = CATSCOMPANY_HTTP_BASE_URL

        value = _env(key, default)
        if value or os.getenv(key) is not None:
            env[key] = value

    for key, value in os.environ.items():
        if not any(pattern.match(key) for pattern in _DYNAMIC_MANAGED_ENV_PATTERNS):
            continue
        value = value.strip()
        if value:
            env[key] = value

    for key, value in list(env.items()):
        if not key.endswith("API_BASE") or not value:
            continue
        prefix = key[:-len("API_BASE")]
        provider = env.get(f"{prefix}PROVIDER", "")
        env[key] = _normalize_llm_api_base(provider, value)

    return env


MANAGED_TENANT_ENV_DEFAULTS = _build_managed_tenant_env_defaults()

# Default git repo for managed bot tenants
DEFAULT_REPO_URL = _env("DEFAULT_REPO_URL", "https://github.com/buildsense-ai/XiaoBa-CLI.git")
DEFAULT_BRANCH = _env("DEFAULT_BRANCH", "main")
