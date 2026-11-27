"""Lease-fenced worker for the CTYun managed Slurm CPU pilot."""

import argparse
import json
import logging
import os
import re
import sys
import time
import uuid
from datetime import datetime, timezone

from cloud import Cloud, MutationLockTimeout, Rejected, WorkerError, http_request, https_base, identifier
from slurm import Slurm, timestamp


TOKEN = re.compile(r"hpc_([0-9a-f]{32})")
FAILED_CLOUD = {"failed", "cancel", "console_failed"}
POLL_SECONDS = 4
CLAIM_SLICE_SECONDS = 30
RECOVERY_GRACE_SECONDS = 10 * 60
LOG = logging.getLogger("hpc-task-worker")


class LeaseError(Exception):
    pass


class FinishRejected(WorkerError):
    pass


class Cats:
    def __init__(self, base, token, transport=http_request):
        self.url = https_base(base) + "/api/compute/worker"
        self.headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}
        self.transport = transport

    def post(self, action, **payload):
        return self.transport("POST", self.url, self.headers, json.dumps(dict(action=action, **payload)))

    def claim(self):
        status, body = self.post("claim")
        if status == 204:
            return None
        if status != 200:
            raise WorkerError(f"Cats claim returned HTTP {status}")
        try:
            return json.loads(body)
        except ValueError:
            raise WorkerError("Cats claim returned invalid JSON") from None

    def update(self, action, run, progress):
        try:
            status, _ = self.post(action, quote_id=run["quote_id"], lease=run["lease"], progress=progress)
        except WorkerError as exc:
            raise LeaseError(f"Cats {action} did not acknowledge the lease") from exc
        if action == "finish" and status == 422:
            raise FinishRejected("Cats finish rejected release verification (HTTP 422)")
        if status != 204:
            raise LeaseError(f"Cats {action} returned HTTP {status}")


def now():
    return datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def direct(func, *args):
    return func(*args)


def same_enum(left, right):
    return type(left) is type(right) and left == right

def resource_budget_seconds(run):
    if not isinstance(run, dict) or not isinstance(run.get("task"), dict):
        return 0
    spec = run["task"]
    explicit = spec.get("max_resource_seconds_per_node")
    if type(explicit) is int and explicit > 0:
        return explicit
    nodes = spec.get("nodes")
    max_node_seconds = run.get("max_node_seconds")
    if type(nodes) is not int or nodes < 1 or type(max_node_seconds) is not int or max_node_seconds < 1:
        return 0
    if max_node_seconds % nodes:
        return 0
    return max_node_seconds // nodes


class Task:
    def __init__(self, cats, cloud, slurm, run):
        if not isinstance(run, dict) or not all(
            isinstance(run.get(key), str) and TOKEN.fullmatch(run[key]) for key in ("quote_id", "lease")
        ):
            raise WorkerError("invalid Cats claim identity")
        if run.get("state") != "active" or not isinstance(run.get("progress"), dict):
            raise WorkerError("invalid Cats claim state or progress")
        self.cats, self.cloud, self.slurm, self.run = cats, cloud, slurm, run
        self.progress = dict(run["progress"])
        hex_id = TOKEN.fullmatch(run["quote_id"])[1]
        self.queue = "ct" + hex_id[:13]
        self.job = "cats-hpc-" + hex_id
        self.spec = run.get("task")
        self.confirmed = run.get("confirmed_at")
        self.steps = {
            "prepare": self.prepare, "create_intent": self.created, "create_rejected": self.create_rejected,
            "queue_created": self.expand, "expand_intent": self.expanded, "expand_rejected": self.delete_empty,
            "discover_disks": self.discover_disks, "nodes_ready": self.submit, "submit_intent": self.recover_submit,
            "slurm_running": self.slurm_terminal, "slurm_done": self.shrink, "shrink_intent": self.shrunk,
            "nodes_released": self.delete_empty, "delete_intent": self.deleted,
            "released": self.finish, "rejected": self.finish,
        }

    def save(self):
        self.cats.update("save", self.run, self.progress)

    def move(self, phase, **fields):
        self.progress.update(dict(fields, phase=phase, problem=fields.get("problem", "")))
        self.save()
        return "next"

    def check(self, func, *args):
        self.save()
        return func(*args)
    def advance(self):
        phase = self.progress.get("phase") or "prepare"
        if phase == "reconcile":
            phase = (self.progress.get("problem") or "").split(":", 1)[0]
        if phase not in self.steps:
            self.move("reconcile", problem="unrecognized_phase: operator must recover task progress")
            return "reconcile"
        try:
            if self.progress.get("queue_name", self.queue) != self.queue:
                raise WorkerError("persisted queue name does not match the task")
            self.validate_spec()
            if phase == "create_intent" and not self.progress.get("queue_create_operation"):
                return self.recover_create_intent()
            if phase == "expand_intent" and not self.progress.get("expand_operation"):
                return self.recover_expand_intent()
            if phase == "shrink_intent" and not self.progress.get("shrink_operation"):
                return self.recover_shrink_intent()
            if phase == "delete_intent" and not self.progress.get("queue_delete_operation"):
                return self.recover_delete_intent()
            return self.steps[phase]()
        except FinishRejected as exc:
            problem = f"manual_finish: {phase}: {exc}"
            self.move("reconcile", problem=problem)
            LOG.error("%s %s", self.run["quote_id"], problem)
            return "reconcile"
        except WorkerError as exc:
            problem = f"{phase}: {exc}"
            if self.progress.get("problem") != problem:
                self.move("reconcile", problem=problem)
            LOG.warning("%s %s", self.run["quote_id"], problem)
            return "reconcile"

    def validate_spec(self):
        spec = self.spec
        if not isinstance(spec, dict) or any(type(spec.get(key)) is not int or spec[key] < 1 for key in (
            "nodes", "logical_cpus", "memory_mib", "wall_seconds",
        )):
            raise WorkerError("invalid pilot allocation")
        if spec["nodes"] > 2 or spec["wall_seconds"] > 300 or spec["wall_seconds"] % 60:
            raise WorkerError("allocation is outside the CPU pilot limits")
        if "max_resource_seconds_per_node" in spec:
            cap = spec["max_resource_seconds_per_node"]
            if type(cap) is not int or cap < spec["wall_seconds"] or cap % 60:
                raise WorkerError("invalid resource budget")
        if resource_budget_seconds(self.run) < spec["wall_seconds"]:
            raise WorkerError("invalid resource budget")
        for key, capacity in (("logical_cpus", "sku_logical_cpus"), ("memory_mib", "sku_memory_mib")):
            if type(self.run.get(capacity)) is not int or spec[key] > self.run[capacity]:
                raise WorkerError("allocation exceeds the quoted SKU capacity")
        timestamp(self.confirmed)

    def recovery_mature(self):
        value = self.progress.get("provision_started_at") or self.confirmed
        try:
            return (datetime.now(timezone.utc) - timestamp(value)).total_seconds() >= RECOVERY_GRACE_SECONDS
        except WorkerError:
            return False

    def recovered_operation(self, kind):
        return f"recovered-{kind}-{self.run['quote_id'][4:]}"

    def clear_capacity_progress(self, keep_queue=False):
        clear = {
            "queue_id", "queue_create_operation", "queue_delete_operation", "expand_operation", "shrink_operation",
            "master_order_id", "provision_started_at", "node_ids", "node_names", "instance_ids", "volume_ids",
            "slurm_job_id", "slurm_state", "slurm_exit_code", "slurm_elapsed_seconds", "slurm_allocated_cpus",
            "slurm_requested_memory", "slurm_node_list",
        }
        if keep_queue:
            clear.discard("queue_id")
        self.progress = {key: value for key, value in self.progress.items() if key not in clear}


    def reject_without_capacity(self, problem):
        self.clear_capacity_progress()
        self.progress.update(phase="rejected", problem=problem, released_at=now())
        self.save()
        return "next"

    def recover_create_intent(self):
        queue = self.queue_record()
        if queue is not None:
            self.queue_record(bound=True)
            if self.own_queue_nodes():
                raise WorkerError("queue appeared with nodes while queue creation was unconfirmed")
            return self.move("queue_created", queue_id=queue["queueUUID"])
        if self.own_queue_nodes():
            raise WorkerError("queue creation was unconfirmed but task nodes remain")
        if not self.recovery_mature():
            return "wait"
        return self.reject_without_capacity("queue creation was unconfirmed and no task queue or nodes remain")

    def recover_expand_intent(self):
        queue = self.queue_record()
        if queue is None:
            if self.own_queue_nodes():
                raise WorkerError("expansion was unconfirmed but task nodes remain without a queue")
            if not self.recovery_mature():
                return "wait"
            if any(self.progress.get(key) for key in ("node_ids", "instance_ids", "volume_ids")):
                raise WorkerError("expanded resources are unconfirmed and persisted ownership is incomplete")
            return self.reject_without_capacity("expansion was unconfirmed and no task queue or nodes remain")
        actual = self.own_queue_nodes()
        if not actual:
            if not self.recovery_mature():
                return "wait"
            raise WorkerError("expansion was unconfirmed but the task queue remained empty")
        ownership = self.ownership()
        if not self.progress.get("provision_started_at"):
            ownership["provision_started_at"] = now()
        return self.move("discover_disks", expand_operation=self.recovered_operation("expand"), **ownership)

    def recover_shrink_intent(self):
        if not self.recovery_mature():
            return "wait"
        if not self.absence(with_resources=True):
            return "wait"
        return self.move("nodes_released", shrink_operation=self.recovered_operation("shrink"))

    def recover_delete_intent(self):
        if not self.recovery_mature():
            return "wait"
        if self.queue_record() is not None or not self.absence(with_resources=bool(self.progress.get("shrink_operation"))):
            return "wait"
        return self.move("delete_intent", queue_delete_operation=self.recovered_operation("delete-queue"))

    def queue_record(self, bound=False):
        queues = self.cloud.pages("list_queues", {}, self.check)
        ids = [identifier(row["queueUUID"]) for row in queues]
        names = [identifier(row["queueName"]) for row in queues]
        if len(set(ids)) != len(ids) or len(set(names)) != len(names):
            raise WorkerError("queue list has repeated names or UUIDs")
        queue = next((row for row in queues if row["queueName"] == self.queue), None)
        expected = self.progress.get("queue_id")
        if expected and ((queue is None and expected in ids) or (queue is not None and queue["queueUUID"] != expected)):
            raise WorkerError("persisted queue UUID and name no longer agree")
        if bound:
            if queue is None:
                raise WorkerError("task queue is not visible")
            if not same_enum(queue["default"], self.cloud.nondefault_value):
                raise WorkerError("task queue is not confirmed non-default")
        return queue

    def nodes(self):
        records = self.cloud.pages("list_nodes", {}, self.check)
        ids = [identifier(row["nodeUUID"]) for row in records]
        if len(set(ids)) != len(ids):
            raise WorkerError("cluster node list has repeated UUIDs")
        return records

    def own_queue_nodes(self):
        return [row for row in self.nodes() if row["queueName"] == self.queue]

    def request(self, phase, action, receipt, context, intent=None, rejected=None):
        request_id = str(uuid.uuid4())
        trace = f"EOP request ID {request_id}"

        def save_intent():
            # Refresh the lease after acquiring the cloud lock and before sending the mutation.
            self.move(phase, problem=trace, **(intent or {}))

        try:
            result = self.cloud.outcome(action, direct, request_id, before_send=save_intent, **context)
        except MutationLockTimeout:
            return "wait"
        except WorkerError as exc:
            if isinstance(exc, Rejected) and rejected is not None:
                if action == "expand":
                    return self.move(rejected, provision_started_at=None, problem=str(exc))
                return self.move(rejected, problem=str(exc))
            self.move("reconcile", problem=f"{phase}: {exc}; {trace}; jobUUID unconfirmed")
            return "reconcile"
        evidence = {receipt: result["jobUUID"]}
        if action == "expand" and result.get("masterOrderID"):
            evidence["master_order_id"] = result["masterOrderID"]
        return self.move(phase, **evidence)

    def operation_status(self, receipt):
        job_uuid = self.progress.get(receipt)
        if not job_uuid:
            trace = re.search(r"EOP request ID [0-9a-f-]{36}", self.progress.get("problem", ""))
            suffix = f"; {trace[0]}" if trace else ""
            raise WorkerError(f"jobUUID unconfirmed for {receipt}; provider operation ID required{suffix}")
        status = self.cloud.status(job_uuid, self.check)
        if status == "success" or status in FAILED_CLOUD or status == "force_completed" or status in self.cloud.pending:
            return status
        raise WorkerError("provider returned an unrecognized job result")

    def confirmed_job(self, receipt):
        status = self.operation_status(receipt)
        if status == "success":
            return True
        if status in FAILED_CLOUD or status == "force_completed":
            raise WorkerError(f"provider operation result is {status}")
        return False

    def prepare(self):
        self.sku()
        if self.queue_record() is not None:
            raise WorkerError("queue name existed before create")
        return self.request("create_intent", "create_queue", "queue_create_operation", {"queueName": self.queue},
                            intent={"queue_name": self.queue}, rejected="create_rejected")

    def created(self):
        if not self.confirmed_job("queue_create_operation"):
            return "wait"
        queue = self.queue_record(bound=True)
        return self.move("queue_created", queue_id=queue["queueUUID"])

    def create_rejected(self):
        if self.queue_record() is not None:
            raise WorkerError("queue appeared after the explicit create rejection")
        return self.move("rejected", released_at=now(), problem="provider explicitly rejected queue creation")

    def sku(self):
        sku = self.run.get("sku")
        if not isinstance(sku, str) or sku not in self.cloud.skus:
            raise WorkerError("quoted SKU has no operator mapping")
        return self.cloud.skus[sku]

    def expand(self):
        self.queue_record(bound=True)
        if self.own_queue_nodes():
            raise WorkerError("owned queue was not empty before expand")
        sku = self.sku()
        context = dict(queueUUID=self.progress["queue_id"], count=self.spec["nodes"],
                       imageUUID=sku["imageUUID"], specificationUUID=sku["specificationUUID"])
        return self.request("expand_intent", "expand", "expand_operation", context,
                            rejected="expand_rejected")

    def ownership(self):
        actual = self.own_queue_nodes()
        if len(actual) != self.spec["nodes"]:
            raise WorkerError("task queue does not contain the exact quoted node count")
        identifiers = {"node_ids": "nodeUUID", "node_names": "nodeName", "instance_ids": "instanceID"}
        fields = {key: [identifier(row[field]) for row in actual] for key, field in identifiers.items()}
        if any(len(set(values)) != len(values) for values in fields.values()):
            raise WorkerError("task queue does not uniquely identify nodes and instances")
        sku = self.sku()
        if any(row["specificationName"] != sku["specificationName"] or any(
            not same_enum(row[key], value) for key, value in self.cloud.node_values.items()
        ) for row in actual):
            raise WorkerError("queue nodes do not match the quoted VM on-demand manual compute SKU")
        # Three arrays describe the same ordered set of nodes.
        order = sorted(range(len(actual)), key=fields["node_ids"].__getitem__)
        return {key: [values[index] for index in order] for key, values in fields.items()}

    def verify_ownership(self):
        self.queue_record(bound=True)
        if any(self.progress.get(key) != value for key, value in self.ownership().items()):
            raise WorkerError("queue nodes no longer match persisted ownership")

    def expanded(self):
        if not self.confirmed_job("expand_operation"):
            return "wait"
        self.queue_record(bound=True)
        ownership = self.ownership()
        if not self.progress.get("provision_started_at"):
            ownership["provision_started_at"] = now()
        return self.move("discover_disks", **ownership)

    def disks(self):
        volumes = []
        for instance in self.progress["instance_ids"]:
            record = self.cloud.by_id("ecs", instance, self.check)
            if record is None:
                raise WorkerError("owned instance is not yet visible in ECS")
            volume = identifier(record["volumeID"])
            disk = self.cloud.by_id("evs", volume, self.check)
            if disk is None or disk["instanceID"] != instance or disk["system"] is not True:
                raise WorkerError("EVS did not verify the instance system disk")
            volumes.append(volume)
        if len(set(volumes)) != len(volumes):
            raise WorkerError("ECS did not identify distinct system disks")
        return volumes

    def discover_disks(self):
        self.verify_ownership()
        return self.move("nodes_ready", volume_ids=self.disks())

    def submit(self):
        self.verify_ownership()
        names = self.progress["node_names"]
        if not self.slurm.ready(self.queue, names, self.check):
            return "wait"
        if self.slurm.recover(self.queue, self.job, self.confirmed, self.check):
            raise WorkerError("Slurm job name existed before submission intent")
        self.move("submit_intent")
        try:
            job_id = self.slurm.submit(self.queue, names, self.job, self.spec, direct)
        except WorkerError as exc:
            self.move("reconcile", problem=f"submit_intent: {exc}; Slurm submission unconfirmed")
            return "reconcile"
        return self.move("slurm_running", slurm_job_id=job_id)

    def recover_submit(self):
        job_id = self.slurm.recover(self.queue, self.job, self.confirmed, self.check)
        if job_id is None:
            raise WorkerError("Slurm submission unconfirmed by squeue and sacct")
        return self.move("slurm_running", slurm_job_id=job_id)

    def slurm_terminal(self):
        result = self.slurm.terminal(self.queue, self.progress["node_names"], self.job, self.progress["slurm_job_id"],
                                     self.confirmed, self.spec, self.check)
        if result is None:
            return "wait"
        return self.move("slurm_done", **result)

    def shrink(self):
        self.verify_ownership()
        if self.disks() != self.progress.get("volume_ids"):
            raise WorkerError("ECS system disks no longer match persisted ownership")
        if self.slurm.busy(self.queue, self.progress["node_names"], self.check):
            return "wait"
        return self.request("shrink_intent", "shrink", "shrink_operation", {
            "queueUUID": self.progress["queue_id"], "nodeUUIDList": self.progress["node_ids"],
        }, rejected="slurm_done")

    def absence(self, with_resources):
        node_ids = set(self.progress.get("node_ids", ()))
        if any(row["queueName"] == self.queue or row["nodeUUID"] in node_ids for row in self.nodes()):
            return False
        if not with_resources:
            if any(self.progress.get(key) for key in ("instance_ids", "volume_ids", "expand_operation", "provision_started_at")):
                raise WorkerError("zero-usage path contains evidence of capacity provisioning")
            return True
        for service, field in (("ecs", "instance_ids"), ("evs", "volume_ids")):
            ids = self.progress.get(field, ())
            if len(ids) != self.spec["nodes"] or len(set(ids)) != len(ids):
                raise WorkerError(f"missing owned {service} resource IDs for release verification")
            for identity in ids:
                if self.cloud.by_id(service, identity, self.check) is not None:
                    return False
        return True

    def retry_shrink(self):
        if self.absence(with_resources=True):
            return self.move("nodes_released")
        self.verify_ownership()
        if self.disks() != self.progress.get("volume_ids"):
            raise WorkerError("ECS system disks no longer match persisted ownership")
        if self.slurm.busy(self.queue, self.progress["node_names"], self.check):
            return "wait"
        return self.request("shrink_intent", "shrink", "shrink_operation", {
            "queueUUID": self.progress["queue_id"], "nodeUUIDList": self.progress["node_ids"],
        }, intent={"shrink_operation": None}, rejected="slurm_done")

    def shrunk(self):
        operation = self.progress.get("shrink_operation", "")
        if operation.startswith("recovered-"):
            status = "success"
        else:
            status = self.operation_status("shrink_operation")
        if status in self.cloud.pending:
            return "wait"
        if status in FAILED_CLOUD or status == "force_completed":
            return self.retry_shrink()
        if not self.absence(with_resources=True):
            return "wait"
        return self.move("nodes_released")

    def delete_empty(self):
        queue = self.queue_record()
        with_resources = bool(self.progress.get("shrink_operation"))
        if queue is None:
            if not self.absence(with_resources=with_resources):
                return "wait"
            return self.move("delete_intent", queue_delete_operation=self.recovered_operation("delete-queue"))
        self.queue_record(bound=True)
        if not self.absence(with_resources=with_resources):
            return "wait"
        return self.request("delete_intent", "delete_queue", "queue_delete_operation", {"queueUUID": self.progress["queue_id"]},
                            rejected="nodes_released")

    def deleted(self):
        operation = self.progress.get("queue_delete_operation", "")
        if operation.startswith("recovered-"):
            status = "success"
        else:
            status = self.operation_status("queue_delete_operation")
        if status in self.cloud.pending:
            return "wait"
        with_resources = bool(self.progress.get("shrink_operation"))
        if status in FAILED_CLOUD or status == "force_completed":
            if self.queue_record() is not None:
                self.queue_record(bound=True)
                if not self.absence(with_resources):
                    return "wait"
                return self.request("delete_intent", "delete_queue", "queue_delete_operation", {
                    "queueUUID": self.progress["queue_id"]
                }, intent={"queue_delete_operation": None}, rejected="nodes_released")
        if self.queue_record() is not None or not self.absence(with_resources):
            return "wait"
        if not with_resources:
            return self.move("rejected", released_at=now(), problem="provider explicitly rejected capacity expansion")
        state = self.progress.get("slurm_state")
        code = self.progress.get("slurm_exit_code")
        if not isinstance(state, str) or not isinstance(code, str):
            raise WorkerError("released task is missing Slurm terminal evidence")
        problem = "" if (state, code) == ("COMPLETED", "0:0") else f"Slurm ended in {state} with exit code {code}"
        if not problem and self.progress.get("slurm_elapsed_seconds", 0) > self.spec["wall_seconds"]:
            problem = "Slurm elapsed time exceeded the quoted wall limit"
        return self.move("released", released_at=now(), problem=problem)


    def finish(self):
        self.cats.update("finish", self.run, self.progress)
        LOG.info("%s %s", self.run["quote_id"], self.progress["phase"])
        return "finish"


def preflight(cloud, slurm):
    status = cloud.status(cloud.probe_job_id, direct)
    if status not in cloud.pending | FAILED_CLOUD | {"success", "force_completed"}:
        raise WorkerError("cloud job status preflight returned an unrecognized result")
    slurm.preflight()


def loop(cats, cloud, slurm, once=False):
    while True:
        try:
            run = cats.claim()
            if run is not None:
                task = Task(cats, cloud, slurm, run)
                deadline = time.monotonic() + CLAIM_SLICE_SECONDS
                while time.monotonic() < deadline:
                    result = task.advance()
                    if result in ("finish", "reconcile"):
                        break
                    if result == "wait":
                        time.sleep(POLL_SECONDS)
            if once:
                return
        except (WorkerError, LeaseError) as exc:
            LOG.warning("%s", exc)
            if once:
                raise
        time.sleep(POLL_SECONDS)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, help="path to operator JSON configuration")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--preflight", action="store_true", help="check cloud job-read and Slurm access")
    mode.add_argument("--once", action="store_true", help="claim at most one task and run one polling slice")
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    try:
        with open(args.config, encoding="utf-8") as source:
            config = json.load(source)
        token, ak, sk = (os.environ.get(key, "") for key in ("CATS_COMPUTE_WORKER_TOKEN", "CTYUN_AK", "CTYUN_SK"))
        if len(token) < 32 or not ak or not sk:
            raise WorkerError("CATS_COMPUTE_WORKER_TOKEN, CTYUN_AK and CTYUN_SK are required")
        cats = Cats(config["cats_url"], token)
        cloud = Cloud(config["cloud"], ak, sk)
        slurm = Slurm(config["slurm"])
        preflight(cloud, slurm)
        if not args.preflight:
            loop(cats, cloud, slurm, args.once)
    except (KeyError, ValueError, TypeError, OSError, WorkerError, LeaseError) as exc:
        LOG.error("%s", exc)
        return 1
    except KeyboardInterrupt:
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
