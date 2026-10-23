"""Fail closed unless CI and the test deployment approve the same main revision."""

import json
import os
from pathlib import Path
import re
import time
from urllib.parse import urlencode
from urllib.request import Request, urlopen


REQUIRED_WORKFLOWS = ("ci.yml", "deploy-test.yml")
ALLOWED_EVENTS = {"push", "workflow_dispatch"}

# CI and the test deployment are dispatched by the same push but never finish
# together (CI takes minutes longer). The gate used to decide on its first
# glance: whichever workflow finished first triggered production, saw the other
# one still running, and returned "not ready" - the workflow then went green
# with deploy-prod *skipped*, so production silently stayed on the old
# revision and the rollout only happened if a human noticed and reran it.
# Now the gate waits (bounded) for runs that are still moving.
READY = "ready"
WAITING = "waiting"
BLOCKED = "blocked"

GATE_TIMEOUT_ENV = "RELEASE_GATE_TIMEOUT_SECONDS"
GATE_POLL_ENV = "RELEASE_GATE_POLL_SECONDS"
GATE_DEFAULT_TIMEOUT_SECONDS = 1800
GATE_DEFAULT_POLL_SECONDS = 20


def trusted_run(run, repository, revision):
    return (
        run.get("head_sha") == revision
        and run.get("head_branch") == "main"
        and (run.get("head_repository") or {}).get("full_name") == repository
        and run.get("event") in ALLOWED_EVENTS
    )


def release_decision_detail(trigger, repository, main_revision, workflow_runs):
    """Return (state, reason) where state is READY, WAITING, or BLOCKED.

    WAITING means a required workflow is still running: retrying later can
    legitimately approve the release. Everything else that is not READY is
    terminal, so retrying would only burn time.
    """
    revision = trigger.get("head_sha", "")
    if not re.fullmatch(r"[0-9a-f]{40}", revision) or not trusted_run(trigger, repository, revision):
        return BLOCKED, "The triggering run is not a trusted main run."
    if revision != main_revision:
        return BLOCKED, "A newer main revision superseded this release; historical reruns cannot roll production back."
    if trigger.get("conclusion") != "success" or trigger.get("status") != "completed":
        return BLOCKED, "The triggering workflow did not succeed."

    latest = []
    pending = []
    for workflow in REQUIRED_WORKFLOWS:
        candidates = [run for run in workflow_runs.get(workflow, []) if trusted_run(run, repository, revision)]
        if not candidates:
            # The push dispatches both workflows, so this is normally a
            # registration lag rather than a permanent absence.
            pending.append(f"{workflow} has not been dispatched yet for {revision}")
            continue
        # A prior success must not hide a newer failed, queued, or rerunning run.
        run = max(candidates, key=lambda item: int(item["id"]))
        latest.append(run)
        if run.get("status") != "completed":
            pending.append(f"{workflow} is still running")
            continue
        if run.get("conclusion") != "success":
            return BLOCKED, f"Latest {workflow} did not succeed ({run.get('conclusion') or 'unknown'})."

    if not any(
        (int(trigger["id"]), int(trigger.get("run_attempt", 1)))
        == (int(run["id"]), int(run.get("run_attempt", 1)))
        for run in latest
    ):
        return BLOCKED, "A newer upstream run or attempt superseded this event."
    if pending:
        # Keep workflow filenames verbatim; only the lead-in is prose.
        return WAITING, "Still waiting: " + "; ".join(pending) + "."
    return READY, f"CI and test deployment both succeeded for {revision}."


def release_decision(trigger, repository, main_revision, workflow_runs):
    state, reason = release_decision_detail(trigger, repository, main_revision, workflow_runs)
    return state == READY, reason


def env_seconds(name, default):
    try:
        return max(0, int(os.environ.get(name, default)))
    except ValueError:
        return default


def github_get(path):
    request = Request(
        f"{os.environ.get('GITHUB_API_URL', 'https://api.github.com')}/repos/{os.environ['GITHUB_REPOSITORY']}/{path}",
        headers={
            "Authorization": f"Bearer {os.environ['GH_TOKEN']}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    with urlopen(request, timeout=30) as response:
        return json.load(response)


def workflow_runs(workflow, revision):
    runs = []
    # A SHA normally has one run; paginate so PR/manual runs cannot conceal a
    # newer eligible run. GitHub caps filtered searches at 1,000; fail closed.
    for page in range(1, 11):
        query = urlencode({"branch": "main", "head_sha": revision, "per_page": 100, "page": page})
        result = github_get(f"actions/workflows/{workflow}/runs?{query}")
        if result.get("total_count", 0) >= 1000:
            raise RuntimeError("Too many workflow runs to establish a safe release decision")
        batch = result["workflow_runs"]
        runs.extend(batch)
        if len(batch) < 100:
            return runs
    raise RuntimeError("Workflow run pagination did not finish")


def deployed_job(jobs):
    return any(
        job.get("name") == "deploy-prod"
        and job.get("status") == "completed"
        and job.get("conclusion") == "success"
        and any(step.get("name") == "Deploy prod stack" and step.get("conclusion") == "success"
                for step in job.get("steps", []))
        for job in jobs
    )


def already_deployed(revision):
    for run in workflow_runs("deploy-prod.yml", revision):
        if str(run["id"]) == os.environ["GITHUB_RUN_ID"]:
            continue
        # workflow_run's head SHA is the default branch revision, which can
        # differ from the upstream revision. Match our explicit release title.
        if run.get("display_title") != f"Deploy production {revision}" or run.get("event") != "workflow_run":
            continue
        # Inspect the actual deploy step: a skipped release gate also makes the
        # outer workflow successful and must never count as a deployment.
        for page in range(1, 11):
            result = github_get(f"actions/runs/{run['id']}/jobs?filter=latest&per_page=100&page={page}")
            if deployed_job(result["jobs"]):
                return True
            if len(result["jobs"]) < 100:
                break
        else:
            raise RuntimeError("Production job pagination did not finish")
    return False


def main():
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text(encoding="utf-8"))
    trigger = event["workflow_run"]
    repository = os.environ["GITHUB_REPOSITORY"]
    revision = trigger["head_sha"]
    if not re.fullmatch(r"[0-9a-f]{40}", revision) or not trusted_run(trigger, repository, revision):
        raise RuntimeError("Untrusted release trigger")

    timeout = env_seconds(GATE_TIMEOUT_ENV, GATE_DEFAULT_TIMEOUT_SECONDS)
    poll = max(1, env_seconds(GATE_POLL_ENV, GATE_DEFAULT_POLL_SECONDS))
    deadline = time.monotonic() + timeout
    while True:
        runs = {workflow: workflow_runs(workflow, revision) for workflow in REQUIRED_WORKFLOWS}
        # Read main on every attempt: a new push during the wait must stop this
        # rollout instead of letting it deploy a superseded revision.
        main_revision = github_get("git/ref/heads/main")["object"]["sha"]
        state, reason = release_decision_detail(trigger, repository, main_revision, runs)
        if state != WAITING:
            break
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            reason = f"Gave up waiting after {timeout}s: {reason}"
            break
        wait_for = min(poll, remaining)
        print(f"{reason} Retrying in {wait_for:.0f}s.", flush=True)
        time.sleep(wait_for)

    ready = state == READY
    # The deployment job serializes production and runs this check again. This
    # handles both completion orders and same-second completions without using
    # lossy timestamp comparisons or cancelling a rollout already in progress.
    if ready and already_deployed(revision):
        ready, reason = False, "This revision already completed a successful production deployment."
    print(reason)
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        output.write(f"ready={str(ready).lower()}\nrevision={revision}\n")


if __name__ == "__main__":
    main()
