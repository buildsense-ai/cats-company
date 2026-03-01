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
    lines = [
        f"# Auto-generated for tenant: {tenant}",
        "",
        f"GAUZ_LLM_PROVIDER={llm_provider}",
        f"GAUZ_LLM_API_BASE={llm_api_base}",
        f"GAUZ_LLM_API_KEY={llm_api_key}",
        f"GAUZ_LLM_MODEL={llm_model}",
        "",
        f"CATSCOMPANY_SERVER_URL={CATSCOMPANY_WS_URL}",
        f"CATSCOMPANY_API_KEY={cc_api_key}",
        "",
        f"GIT_REPO_URL={repo_url}",
        f"GIT_BRANCH={branch}",
        f"AUTO_PULL={'true' if auto_pull else 'false'}",
        "",
        f"TENANT={tenant}",
        "",
        "GAUZ_TOOL_ALLOW=",
        "",
    ]
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
