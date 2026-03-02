"""Container orchestration — manages XiaoBa tenant containers."""

import json
import os
import shutil
import subprocess
from pathlib import Path

from config import (
    CATSCOMPANY_WS_URL,
    DEFAULT_BRANCH,
    DEFAULT_REPO_URL,
    MANAGED_TENANT_ENV_DEFAULTS,
    TENANTS_DIR,
    TEMPLATES_DIR,
    XIAOBA_BASE_IMAGE,
    XIAOBA_COMPOSE_FILE,
    XIAOBA_REPO_ROOT,
)

TENANT_DIR = Path(TENANTS_DIR)
TEMPLATES = Path(TEMPLATES_DIR)
REPO_ROOT = Path(XIAOBA_REPO_ROOT)
COMPOSE_FILE = Path(XIAOBA_COMPOSE_FILE)

DEFAULT_RUNTIME = {"cpus": "0.4", "mem_limit": "1g", "pids_limit": "512"}
DATA_FOLDERS = ("files", "logs", "workspace", "extracted", "docs_analysis", "docs_runs", "docs_ppt", "audit")
RUNTIME_ENV_KEYS = (
    "GAUZ_TOOL_ALLOW",
    "GAUZ_FS_ALLOW_OUTSIDE_READ",
    "GAUZ_FS_ALLOW_OUTSIDE",
    "GAUZ_BASH_ALLOW_DANGEROUS",
    "GAUZ_FS_ALLOW_DOTENV",
    "CONTEXT_DEBUG",
)
STORAGE_ENV_KEYS = ("MINIO_ENDPOINT", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY", "MINIO_BUCKET", "MINERU_TOKEN", "SEARXNG_BASE_URL")
CATSCOMPANY_ENV_KEYS = ("CATSCOMPANY_SERVER_URL", "CATSCOMPANY_HTTP_BASE_URL", "CATSCOMPANY_API_KEY")


def _run(
    args: list[str],
    cwd: Path | None = None,
    env: dict | None = None,
    timeout: int = 120,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        args,
        cwd=cwd,
        env=env,
        capture_output=True,
        text=True,
        timeout=timeout,
        check=False,
    )


def _docker_env(tenant: str) -> dict[str, str]:
    env = os.environ.copy()
    env["TENANT"] = tenant
    env["TENANTS_DIR"] = str(TENANT_DIR)
    env["TEMPLATES_DIR"] = str(TEMPLATES)
    env["XIAOBA_BASE_IMAGE"] = XIAOBA_BASE_IMAGE
    for k, v in DEFAULT_RUNTIME.items():
        env[f"TENANT_{k.upper()}"] = v
    return env


def _template_compose_file() -> Path:
    return TEMPLATES / "docker-compose.yml"


def _legacy_compose_file() -> Path:
    return COMPOSE_FILE


def _section_lines(env: dict[str, str], title: str, keys: list[str], include_empty: bool = False) -> list[str]:
    lines: list[str] = []
    section_entries: list[str] = []
    for key in keys:
        if key not in env and not include_empty:
            continue
        section_entries.append(f"{key}={env.get(key, '')}")
    if not section_entries:
        return lines
    lines.append(f"# {title}")
    lines.extend(section_entries)
    lines.append("")
    return lines


def _matching_keys(env: dict[str, str], prefix: str) -> list[str]:
    return sorted(key for key in env if key.startswith(prefix))


def build_tenant_env(
    tenant: str,
    cc_api_key: str,
    repo_url: str = DEFAULT_REPO_URL,
    branch: str = DEFAULT_BRANCH,
    auto_pull: bool = True,
    llm_provider: str = "",
    llm_api_base: str = "",
    llm_api_key: str = "",
    llm_model: str = "",
) -> str:
    """Generate .env content for a managed tenant container."""
    env = dict(MANAGED_TENANT_ENV_DEFAULTS)
    env["GAUZ_LLM_PROVIDER"] = llm_provider or env.get("GAUZ_LLM_PROVIDER", "")
    env["GAUZ_LLM_API_BASE"] = llm_api_base or env.get("GAUZ_LLM_API_BASE", "")
    env["GAUZ_LLM_API_KEY"] = llm_api_key or env.get("GAUZ_LLM_API_KEY", "")
    env["GAUZ_LLM_MODEL"] = llm_model or env.get("GAUZ_LLM_MODEL", "")
    env["CATSCOMPANY_SERVER_URL"] = CATSCOMPANY_WS_URL
    env["CATSCOMPANY_API_KEY"] = cc_api_key

    llm_keys = [
        "GAUZ_LLM_PROVIDER",
        "GAUZ_LLM_API_BASE",
        "GAUZ_LLM_API_KEY",
        "GAUZ_LLM_MODEL",
    ]
    llm_keys.extend(
        key for key in _matching_keys(env, "GAUZ_LLM_BACKUP_")
        if key not in llm_keys
    )
    llm_keys.extend(
        key for key in (
            "GAUZ_LLM_FAILOVER_ON_ANY_ERROR",
            "GAUZ_STREAM_FAILOVER_ON_PARTIAL",
            "GAUZ_STREAM_RETRY",
            "GAUZ_LLM_MAX_PROMPT_TOKENS",
        )
        if key in env
    )

    vision_keys = [
        key for key in (
            "GAUZ_VISION_PROVIDER",
            "GAUZ_VISION_API_BASE",
            "GAUZ_VISION_API_KEY",
            "GAUZ_VISION_MODEL",
        )
        if key in env
    ]
    vision_keys.extend(
        key for key in _matching_keys(env, "GAUZ_VISION_BACKUP_")
        if key not in vision_keys
    )
    vision_keys.extend(
        key for key in (
            "GAUZ_VISION_RETRY_MAX",
            "GAUZ_VISION_RETRY_BASE_SECONDS",
            "GAUZ_VISION_FAILOVER_ON_ANY_ERROR",
            "GAUZ_VISION_DEFAULT_MIME",
        )
        if key in env
    )

    memory_keys = [
        key for key in (
            "GAUZ_MEM_ENABLED",
            "GAUZ_MEM_BASE_URL",
            "GAUZ_MEM_PROJECT_ID",
            "GAUZ_MEM_USER_ID",
            "GAUZ_MEM_AGENT_ID",
            "GAUZ_MEM_AGENT_NAME",
            "GAUZ_MEM_OWNER_NAME",
        )
        if key in env
    ]

    lines = [f"# Auto-generated for tenant: {tenant}", ""]
    lines.extend(_section_lines(env, "LLM", llm_keys))
    lines.extend(_section_lines(env, "Vision", vision_keys))
    lines.extend(_section_lines(env, "Memory", memory_keys))
    lines.extend(_section_lines(env, "Storage", [key for key in STORAGE_ENV_KEYS if key in env]))
    lines.extend(_section_lines(env, "Runtime", list(RUNTIME_ENV_KEYS), include_empty=True))
    lines.extend(_section_lines(env, "CatsCompany", list(CATSCOMPANY_ENV_KEYS)))
    lines.extend([
        "# Source",
        f"GIT_REPO_URL={repo_url}",
        f"GIT_BRANCH={branch}",
        f"AUTO_PULL={'true' if auto_pull else 'false'}",
        "",
        f"TENANT={tenant}",
        "",
    ])
    return "\n".join(lines) + "\n"


def scaffold_tenant(tenant: str, env_content: str) -> None:
    """Create tenant directory structure and write .env."""
    base = TENANT_DIR / tenant
    data = base / "data"
    app_dir = base / "app"
    for folder in DATA_FOLDERS:
        (data / folder).mkdir(parents=True, exist_ok=True)
    app_dir.mkdir(parents=True, exist_ok=True)
    (base / ".env").write_text(env_content, encoding="utf-8")
    (base / "runtime.json").write_text(json.dumps(DEFAULT_RUNTIME, indent=2) + "\n", encoding="utf-8")
    # XiaoBa container runs as uid 10001, must own data + app dirs
    _run(["chown", "-R", "10001:10001", str(data)], timeout=30)
    _run(["chown", "-R", "10001:10001", str(app_dir)], timeout=30)


def start_tenant(tenant: str) -> tuple[bool, str]:
    """Start a legacy tenant container. Returns (success, message)."""
    cmd = ["docker", "compose", "-p", f"xiaoba-{tenant}", "-f", str(_legacy_compose_file()), "up", "-d"]
    result = _run(cmd, cwd=REPO_ROOT, env=_docker_env(tenant), timeout=900)
    if result.returncode != 0:
        return False, (result.stderr or result.stdout).strip()
    return True, "started"


def stop_tenant(tenant: str) -> tuple[bool, str]:
    cmd = ["docker", "compose", "-p", f"xiaoba-{tenant}", "-f", str(_legacy_compose_file()), "down"]
    result = _run(cmd, cwd=REPO_ROOT, env=_docker_env(tenant), timeout=120)
    if result.returncode != 0:
        return False, (result.stderr or result.stdout).strip()
    return True, "stopped"


def start_managed_tenant(tenant: str) -> tuple[bool, str]:
    """Start a managed tenant container using the template compose file."""
    cmd = ["docker", "compose", "-p", f"xiaoba-{tenant}", "-f", str(_template_compose_file()), "up", "-d"]
    result = _run(cmd, env=_docker_env(tenant), timeout=300)
    if result.returncode != 0:
        return False, (result.stderr or result.stdout).strip()
    return True, "started"


def stop_managed_tenant(tenant: str) -> tuple[bool, str]:
    cmd = ["docker", "compose", "-p", f"xiaoba-{tenant}", "-f", str(_template_compose_file()), "down"]
    result = _run(cmd, env=_docker_env(tenant), timeout=120)
    if result.returncode != 0:
        return False, (result.stderr or result.stdout).strip()
    return True, "stopped"


def tenant_status(tenant: str) -> str:
    result = _run(["docker", "inspect", "-f", "{{.State.Status}}", f"xiaoba-{tenant}"], timeout=30)
    if result.returncode == 0 and result.stdout.strip():
        return result.stdout.strip()
    return "not_created"


def remove_tenant(tenant: str) -> None:
    """Stop a legacy tenant and remove its directory."""
    stop_tenant(tenant)
    base = TENANT_DIR / tenant
    if base.exists():
        shutil.rmtree(base)


def remove_managed_tenant(tenant: str) -> None:
    """Stop a managed tenant and remove its directory."""
    stop_managed_tenant(tenant)
    base = TENANT_DIR / tenant
    if base.exists():
        shutil.rmtree(base)
