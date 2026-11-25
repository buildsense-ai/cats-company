"""Submit and inspect the fixed Slurm CPU workload."""

import os
import re
import shlex
import subprocess
from datetime import datetime, timedelta, timezone

from cloud import IO_TIMEOUT, WorkerError, identifier


TERMINAL = {
    "BOOT_FAIL", "CANCELLED", "COMPLETED", "DEADLINE", "FAILED", "NODE_FAIL",
    "OUT_OF_MEMORY", "PREEMPTED", "REVOKED", "TIMEOUT",
}
ACTIVE = {
    "COMPLETING", "CONFIGURING", "PENDING", "POWER_UP_NODE", "REQUEUED", "REQUEUE_FED",
    "REQUEUE_HOLD", "RESIZING", "RUNNING", "SIGNALING", "SPECIAL_EXIT", "STAGE_OUT", "STOPPED", "SUSPENDED",
}
ACCOUNTING = (
    "JobIDRaw%64,JobName%96,Partition%32,Account%128,State%40,ExitCode%20,"
    "ElapsedRaw%20,AllocCPUS%12,ReqMem%32,NodeList%256"
)
ENV_NAMES = ("PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "SLURM_CONF", "SLURM_CONF_SERVER", "LD_LIBRARY_PATH")


def run_command(argv, script=None):
    env = {key: os.environ[key] for key in ENV_NAMES if key in os.environ}
    env["LC_ALL"] = "C"
    env["TZ"] = "UTC"
    try:
        result = subprocess.run(
            argv, input=script or "", capture_output=True, text=True, encoding="utf-8",
            env=env, timeout=IO_TIMEOUT, check=True,
        )
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        raise WorkerError(f"{argv[0]} failed or timed out") from exc
    return result.stdout


def rows(text, columns):
    result = [tuple(value.strip() for value in line.split("|")) for line in text.splitlines() if line.strip()]
    if any(len(row) != columns for row in result):
        raise WorkerError("unexpected Slurm output format")
    return result


def slurm_id(value):
    if not isinstance(value, str) or not re.fullmatch(r"[1-9][0-9]{0,19}", value):
        raise WorkerError("unexpected Slurm job ID")
    return value


def timestamp(value):
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (AttributeError, ValueError):
        raise WorkerError("missing or invalid confirmation timestamp") from None
    if result.tzinfo is None:
        raise WorkerError("confirmation timestamp requires a timezone")
    return result.astimezone(timezone.utc)


class Slurm:
    def __init__(self, config, run=run_command):
        self.account = identifier(config["account"])
        self.srun = config.get("srun", "/usr/bin/srun")
        if not re.fullmatch(r"/[A-Za-z0-9_./-]+", self.srun):
            raise ValueError("srun must be an absolute path on the compute nodes")
        self.run = run

    def preflight(self):
        for command in ("sbatch", "sacct", "squeue", "scontrol", "sinfo"):
            self.run([command, "--version"])
        self.run(["sacct", "--local", "--allocations", "--noheader", "--starttime=now-5minutes", "--format=JobIDRaw"])
        self.run(["squeue", "--all", "--noheader", "--format=%i"])

    def ready(self, queue, names, call):
        out = call(self.run, ["sinfo", "--Node", "--noheader", f"--partition={queue}", "--format=%N|%T"])
        found = rows(out, 2)
        if set(node for node, _ in found) - set(names):
            raise WorkerError("Slurm partition contains an unowned node")
        return len(found) == len(names) and {node for node, _ in found} == set(names) and all(state == "idle" for _, state in found)

    def submit(self, queue, names, job_name, task, call):
        count, cpus = task["nodes"], task["logical_cpus"]
        minutes = task["wall_seconds"] // 60
        allocation = [f"--nodes={count}", f"--ntasks={count * cpus}", f"--ntasks-per-node={cpus}", "--cpus-per-task=1"]
        command = [self.srun, f"--account={self.account}", "--exact", "--kill-on-bad-exit=1", *allocation, "/usr/bin/awk",
                   "BEGIN { x=1; for (i=0; i<1000000; i++) x=(x*48271)%2147483647; print x }"]
        script = "#!/bin/sh\nset -eu\nexec " + shlex.join(command) + "\n"
        out = call(self.run, [
            "sbatch", "--parsable", f"--partition={queue}", f"--nodelist={','.join(names)}", f"--account={self.account}",
            f"--job-name={job_name}", "--export=NIL", "--no-requeue", *allocation,
            f"--mem={task['memory_mib']}M", f"--time={minutes}", f"--deadline=now+{minutes + 2}minutes",
            "--chdir=/tmp", "--output=/dev/null", "--error=/dev/null",
        ], script)
        job_id, separator, cluster = out.strip().partition(";")
        if separator:
            identifier(cluster)
        return slurm_id(job_id)

    def accounting(self, job_name, confirmed_at, call, job_id=None):
        start = (timestamp(confirmed_at) - timedelta(minutes=5)).strftime("%Y-%m-%dT%H:%M:%S")
        query = f"--jobs={slurm_id(job_id)}" if job_id else f"--name={job_name}"
        result = call(self.run, [
            "sacct", "--local", "--allocations", "--duplicates", "--noheader", "--parsable2", query,
            f"--starttime={start}", f"--format={ACCOUNTING}",
        ])
        return rows(result, 10)

    def recover(self, queue, job_name, confirmed_at, call):
        out = call(self.run, ["squeue", "--all", "--noheader", f"--name={job_name}", "--format=%i|%j|%P|%a"])
        live = rows(out, 4)
        history = [row[:4] for row in self.accounting(job_name, confirmed_at, call)]
        ids = set()
        for job_id, name, partition, account in live + history:
            if (name, partition, account) != (job_name, queue, self.account):
                raise WorkerError("Slurm recovery found conflicting job identity")
            ids.add(slurm_id(job_id))
        if len(ids) > 1:
            raise WorkerError("Slurm recovery found multiple job IDs")
        return next(iter(ids), None)

    def terminal(self, queue, names, job_name, job_id, confirmed_at, task, call):
        history = self.accounting(job_name, confirmed_at, call, job_id=job_id)
        if not history:
            return None
        if len(history) != 1:
            raise WorkerError("Slurm accounting does not uniquely identify the task job")
        record = history[0]
        state, exit_code, elapsed, cpus, memory, hostlist = record[4:]
        # slurmdbd can record a step before it receives the parent job metadata.
        if record[:4] == (job_id, "allocation", "", self.account) and memory == "0":
            return None
        if record[:4] != (job_id, job_name, queue, self.account):
            raise WorkerError("Slurm accounting does not uniquely identify the task job")
        state = state.split(maxsplit=1)[0] if state else ""
        if state in ACTIVE:
            return None
        if state not in TERMINAL or not re.fullmatch(r"[0-9]+:[0-9]+", exit_code):
            raise WorkerError("Slurm accounting did not verify terminal state and exit code")
        if not elapsed.isdecimal() or not cpus.isdecimal():
            raise WorkerError("Slurm accounting did not report elapsed time and CPU allocation")
        expanded = []
        if hostlist not in ("", "None assigned", "(null)", "None", "Unknown"):
            if not re.fullmatch(r"[A-Za-z0-9_.\[\],-]+", hostlist) or hostlist.startswith("-"):
                raise WorkerError("Slurm returned an invalid node list")
            out = call(self.run, ["scontrol", "show", "hostnames", hostlist])
            expanded = [identifier(node.strip()) for node in out.splitlines() if node.strip()]
            if not expanded or len(expanded) != len(set(expanded)) or set(expanded) - set(names):
                raise WorkerError("Slurm accounting lists nodes outside the task allocation")
        if state == "COMPLETED" and exit_code == "0:0":
            if set(expanded) != set(names) or int(cpus) < task["nodes"] * task["logical_cpus"]:
                raise WorkerError("successful Slurm job did not use the quoted allocation")
            if not self.memory_satisfies(memory, task):
                raise WorkerError("successful Slurm job did not account for the requested memory")
        return dict(slurm_state=state, slurm_exit_code=exit_code, slurm_elapsed_seconds=int(elapsed),
                    slurm_allocated_cpus=int(cpus), slurm_requested_memory=memory, slurm_node_list=expanded)

    @staticmethod
    def memory_satisfies(value, task):
        match = re.fullmatch(r"([0-9]+)([KMGTP])([cn]?)", value)
        if not match:
            return False
        quantity, unit, scope = match.groups()
        kib = int(quantity) * 1024 ** "KMGTP".index(unit)
        if scope == "c":
            kib *= task["logical_cpus"]
        return kib >= task["memory_mib"] * 1024

    def busy(self, queue, names, call):
        for scope in (f"--nodelist={','.join(names)}", f"--partition={queue}"):
            out = call(self.run, ["squeue", "--all", "--noheader", "--states=all", scope, "--format=%i|%T"])
            if any(state not in TERMINAL for _, state in rows(out, 2)):
                return True
        return False
