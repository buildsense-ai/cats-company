"""Gauz Platform API — settings loaded from environment."""

import os
from dotenv import load_dotenv

load_dotenv()

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
CATSCOMPANY_URL = os.getenv("CATSCOMPANY_URL", "http://localhost:6061")
CATSCOMPANY_WS_URL = os.getenv("CATSCOMPANY_WS_URL", "ws://localhost:6061/v0/channels")
GAUZMEM_URL = os.getenv("GAUZMEM_URL", "http://localhost:1235")

# Legacy multitenant orchestration
XIAOBA_COMPOSE_FILE = os.getenv("XIAOBA_COMPOSE_FILE", "/opt/services/xiaoba/deploy/docker-compose.multitenant.yml")
XIAOBA_REPO_ROOT = os.getenv("XIAOBA_REPO_ROOT", "/opt/services/xiaoba")

# Managed deploy service orchestration
XIAOBA_BASE_IMAGE = os.getenv("XIAOBA_BASE_IMAGE", "xiaoba-base:latest")
TENANTS_DIR = os.getenv("TENANTS_DIR", "/opt/services/xiaoba/tenants")
TEMPLATES_DIR = os.getenv("TEMPLATES_DIR", "/opt/services/gauz-platform/templates")

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
LLM_PROXY_PROVIDER = os.getenv("LLM_PROXY_PROVIDER", "anthropic")
LLM_PROXY_API_BASE = _normalize_llm_api_base(
    LLM_PROXY_PROVIDER,
    os.getenv("LLM_PROXY_API_BASE", ""),
)
LLM_PROXY_API_KEY = os.getenv("LLM_PROXY_API_KEY", "")
LLM_PROXY_MODEL = os.getenv("LLM_PROXY_MODEL", "claude-sonnet-4-20250514")

# Default git repo for managed bot tenants
DEFAULT_REPO_URL = os.getenv("DEFAULT_REPO_URL", "https://github.com/buildsense-ai/XiaoBa-CLI.git")
DEFAULT_BRANCH = os.getenv("DEFAULT_BRANCH", "main")
