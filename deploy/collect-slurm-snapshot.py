#!/usr/bin/env python3
"""Publish one compute preview snapshot from a homogeneous Slurm partition."""

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path


SINFO_FORMAT = "%P|%a|%D|%T|%c|%m|%G"
SCHEDULABLE_STATES = {"idle", "allocated", "mixed", "completing"}
SHAPE_FIELDS = ("logical_cpus_per_node", "memory_mib_per_node", "gpus_per_node")
PRICE_FIELDS = ("cny_micros_per_node_period", "rate_period_s", "billing_increment_s", "valid_until")


def read_config(path):
    with path.open("rb") as stream:
        encoded = stream.read(65537)
    if len(encoded) > 65536:
        raise ValueError("config exceeds 64 KiB")
    config = json.loads(encoded)
    partition = config.get("partition") if isinstance(config, dict) else None
    if not isinstance(partition, str) or not re.fullmatch(r"[A-Za-z0-9_.-]+", partition):
        raise ValueError("config must specify one partition name")
    for field in SHAPE_FIELDS:
        value = config.get(field)
        minimum = 0 if field == "gpus_per_node" else 1
        if type(value) is not int or value < minimum:
            raise ValueError(f"{field} must be an integer >= {minimum}")
    gpu_memory = config.get("memory_mib_per_gpu")
    if gpu_memory is not None and (type(gpu_memory) is not int or gpu_memory <= 0 or config["gpus_per_node"] == 0):
        raise ValueError("memory_mib_per_gpu requires a positive GPU count and memory")
    if "pricing" in config and not isinstance(config["pricing"], dict):
        raise ValueError("pricing must be an object")
    return config


def gpu_count(gres):
    count = 0
    for item in re.sub(r"\([^()]*\)", "", gres).split(","):
        fields = item.split(":")
        if fields[0] == "gpu":
            if len(fields) not in (2, 3) or not fields[-1].isdigit():
                raise ValueError(f"unrecognized Slurm GPU GRES: {item}")
            count += int(fields[-1])
    return count


def sample_partition(config, sinfo_output, sampled_at):
    partition = config["partition"]
    shape = tuple(config[field] for field in SHAPE_FIELDS)
    nodes = idle_nodes = 0
    rows = [line for line in sinfo_output.splitlines() if line.strip()]
    if not rows:
        raise ValueError(f"Slurm returned no rows for partition {partition}")
    for line in rows:
        name, availability, count, state, cpus, memory, gres = (part.strip() for part in line.split("|"))
        if name not in (partition, f"{partition}*"):
            raise ValueError(f"unexpected Slurm partition: {name}")
        count = int(count)
        if count < 0:
            raise ValueError("negative node count in Slurm output")
        if count == 0:
            continue
        observed = (int(cpus), int(memory), gpu_count(gres))
        if observed != shape:
            raise ValueError(f"partition {partition} contains nodes with shape {observed}, expected {shape}")
        # State flags matter: for example idle~ is powered off, idle* is
        # unresponsive, and idle- is planned for backfill. Only plain idle
        # is counted as immediately available.
        if availability == "up" and state in SCHEDULABLE_STATES:
            nodes += count
            if state == "idle":
                idle_nodes += count

    snapshot = {field: config[field] for field in SHAPE_FIELDS}
    snapshot.update(sampled_at=sampled_at, nodes=nodes, idle_nodes=idle_nodes)
    if config.get("memory_mib_per_gpu") is not None:
        snapshot["memory_mib_per_gpu"] = config["memory_mib_per_gpu"]
    if "pricing" in config:
        snapshot["pricing"] = {key: config["pricing"][key] for key in PRICE_FIELDS if key in config["pricing"]}
    return snapshot


def publish(output, data):
    if not output.is_absolute():
        raise ValueError("snapshot output path must be absolute")
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=output.parent, prefix=f".{output.name}.", delete=False) as stream:
            temporary = Path(stream.name)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        temporary.chmod(0o640)
        os.replace(temporary, output)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path, help="partition and expected node shape JSON")
    parser.add_argument("--output", type=Path, help="snapshot absolute path (defaults to standard output)")
    args = parser.parse_args()
    try:
        config = read_config(args.config)
        sampled_at = datetime.now(timezone.utc).isoformat(timespec="seconds")
        env = {
            key: value for key, value in os.environ.items()
            if not key.startswith("SINFO_") and key != "SLURM_CLUSTERS"
        }
        env["LC_ALL"] = "C"
        result = subprocess.run(
            [
                "sinfo", "--local", "--exact", "--noheader",
                f"--partition={config['partition']}", f"--format={SINFO_FORMAT}",
            ],
            encoding="utf-8", capture_output=True, check=True, timeout=15, env=env,
        )
        snapshot = sample_partition(config, result.stdout, sampled_at)
        data = (json.dumps(snapshot, separators=(",", ":"), allow_nan=False) + "\n").encode("utf-8")
        if len(data) > 65536:
            raise ValueError("snapshot exceeds 64 KiB")
        if args.output is None:
            sys.stdout.buffer.write(data)
        else:
            publish(args.output, data)
    except subprocess.CalledProcessError as exc:
        print(f"sinfo failed ({exc.returncode}): {exc.stderr.strip()}", file=sys.stderr)
        return 1
    except (OSError, ValueError, subprocess.TimeoutExpired) as exc:
        print(f"Slurm snapshot failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
