import copy
import json
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

from cloud import API_FIELDS, Cloud, WorkerError, query_string, signed_headers
from slurm import Slurm
from worker import Cats, LeaseError, Task, main, preflight


QUOTE = "hpc_" + "0123456789abcdef" * 2
QUEUE = "ct0123456789abc"
JOB = "cats-hpc-" + QUOTE[4:]
OWNED = dict(queueName=QUEUE, nodeUUID="node-task", nodeName="wn01", instanceID="instance-task",
             role=1, kind="VM", billing=True, creation="manual", specificationName="c8e.large.2")
MUTATIONS = {
    "create-queue": "create_intent", "expand": "expand_intent", "shrink": "shrink_intent", "delete-queue": "delete_intent",
}


def config():
    api = {}
    for name, (params, fields) in API_FIELDS.items():
        paged = name.startswith("list_")
        api[name] = dict(
            path=f"/fixture/{name.replace('_', '-')}", method="GET",
            params={key: key for key in params + (("page", "page_size") if paged else ())},
            fields={key: key if paged else f"returnObj.{key}" for key in fields},
        )
        if paged:
            api[name].update(items="returnObj.items", total="returnObj.total", first_page=1)
    for service in ("ecs", "evs"):
        api[service].update(error_code="errorCode", absent_codes=["FixtureNotFound"])
    for action in ("create_queue", "expand", "shrink", "delete_queue"):
        api[action].update(method="POST", error_code="errorCode", rejected_codes=["FixtureNoSideEffect"])
    api["expand"]["fields"]["masterOrderID"] = "returnObj.masterOrderID"
    return dict(hosts={key: f"https://{key}.invalid" for key in ("hpc", "ecs", "evs")},
                regionID="region-1", azName="az-1", clusterUUID="cluster-1", compute_role_uuid="role-1",
                subnet="subnet-1", system_disk_type="SSD", system_disk_size_gb=100,
                default_queue_value=True, nondefault_queue_value=False,
                skus={"cpu": {"imageUUID": "image-1", "specificationUUID": "spec-cpu", "specificationName": "c8e.large.2"}},
                probe_job_uuid="read-probe", pending_job_statuses=["running"], api=api)


class Fixture:
    def __init__(self):
        self.run = dict(quote_id=QUOTE, lease="hpc_" + "a" * 32, state="active", progress={}, sku="cpu",
                        confirmed_at="2026-01-01T00:00:00Z", sku_logical_cpus=4, sku_memory_mib=1024,
                        task=dict(nodes=1, logical_cpus=2, memory_mib=512, wall_seconds=60))
        self.calls = []
        self.mutations = []
        self.queues = []
        self.nodes = []
        self.instances = {}
        self.volumes = {}
        self.statuses = {"read-probe": "success"}
        self.finished = None
        self.slurm_job = False
        self.slurm_state = "COMPLETED"
        self.exit_code = "0:0"
        self.busy = False
        self.lose_ack = None
        self.reject = None
        self.force = None
        self.save_conflict = None
        self.keep_volume = False
        self.node_names = ["wn01"]
        self.missing_master = False
        self.cats = Cats("https://cats.invalid", "fixture-token", self.http)
        self.cloud = Cloud(config(), "fixture-ak", "fixture-sk", self.http)
        self.slurm = Slurm({"account": "fixed"}, self.command)

    def claimed(self):
        return Task(self.cats, self.cloud, self.slurm, self.cats.claim())

    def advance_to(self, phase):
        task = self.claimed()
        for _ in range(25):
            if self.run["progress"].get("phase") == phase:
                return task
            if task.advance() != "next":
                break
        raise AssertionError(self.run["progress"])

    def http(self, method, url, headers, body):
        parsed = urlsplit(url)
        payload = json.loads(body) if body else {key: values[0] for key, values in parse_qs(parsed.query).items()}
        name = parsed.path.rsplit("/", 1)[1]
        if name == "expand-cluster":
            assert parsed.path == "/v4/cthpc/hpc/expand-cluster"
            name = "expand"
        self.calls.append((name, payload))
        if parsed.hostname == "cats.invalid":
            assert method == "POST" and parsed.path == "/api/compute/worker"
            assert headers["Authorization"] == "Bearer fixture-token"
            action = payload["action"]
            if action == "claim":
                assert payload == {"action": "claim"}
                return (204, "") if self.finished else (200, json.dumps(self.run))
            assert payload["quote_id"] == QUOTE and payload["lease"] == self.run["lease"]
            if action == "save":
                if self.save_conflict and self.save_conflict(payload["progress"]):
                    return 409, ""
                self.run["progress"] = copy.deepcopy(payload["progress"])
            else:
                assert action == "finish" and payload["progress"] == self.run["progress"]
                self.finished = payload["progress"]
            return 204, ""
        assert parsed.hostname in ("hpc.invalid", "ecs.invalid", "evs.invalid")
        assert headers == signed_headers("fixture-ak", "fixture-sk", parsed.query, body or "",
                                         headers["ctyun-eop-request-id"], headers["Eop-date"])
        if name in MUTATIONS:
            assert self.run["progress"]["phase"] == MUTATIONS[name]
            assert self.run["progress"]["problem"] == "EOP request ID " + headers["ctyun-eop-request-id"]
            self.mutations.append(name)
            if self.reject == name:
                return 200, json.dumps(dict(statusCode=900, errorCode="FixtureNoSideEffect"))
            if name == "create-queue":
                assert payload == dict(regionID="region-1", azName="az-1", clusterUUID="cluster-1", queueName=QUEUE)
                self.queues.append(dict(queueName=QUEUE, queueUUID="queue-task", default=False))
            elif name == "expand":
                assert payload == dict(regionID="region-1", azName="az-1", clusterUUID="cluster-1",
                                       clusterTypeRoleUUID="role-1", nodeCreateMethod="AutoCreate", onDemand=True,
                                       ecsComputeNodeOrders=[dict(queueUUID="queue-task", orderCount=self.run["task"]["nodes"],
                                           imageUUID="image-1", specificationUUID="spec-cpu", subnetID="subnet-1",
                                           systemDiskType="SSD", systemDiskSize=100)])
                self.nodes = [dict(OWNED)]
                if self.run["task"]["nodes"] == 2:
                    self.nodes.append(dict(OWNED, nodeUUID="node-task2", nodeName="wn02", instanceID="instance-task2"))
                self.node_names = [row["nodeName"] for row in self.nodes]
                for row in self.nodes:
                    instance = row["instanceID"]
                    volume = instance.replace("instance-", "volume-")
                    self.instances[instance] = dict(id=instance, volumeID=volume)
                    self.volumes[volume] = dict(id=volume, instanceID=instance, system=True)
            elif name == "shrink":
                assert payload["nodeUUIDList"] == [row["nodeUUID"] for row in self.nodes] and not self.busy
                self.nodes.clear()
                self.instances.clear()
                if not self.keep_volume:
                    self.volumes.clear()
            else:
                assert payload["queueUUID"] == "queue-task" and not self.nodes and not self.instances and not self.volumes
                self.queues.clear()
            self.statuses[name] = "force_completed" if self.force == name else "success"
            if self.lose_ack == name:
                raise WorkerError("fixture HTTP timeout after acceptance")
            result = dict(jobUUID=name)
            if name == "expand" and not self.missing_master:
                result["masterOrderID"] = "order-task"
        elif name == "query-job-result":
            result = dict(status=self.statuses[payload["jobUUID"]])
        elif name in ("list-queues", "list-nodes"):
            assert payload["clusterUUID"] == "cluster-1" and "queueUUID" not in payload
            rows = self.queues if name == "list-queues" else self.nodes
            index = (int(payload["page"]) - 1) * 20
            result = dict(items=rows[index:index + 20], total=len(rows))
        elif name in ("ecs", "evs"):
            if name == "ecs":
                result = self.instances.get(payload["instanceID"])
            else:
                result = self.volumes.get(payload["volumeID"])
            if result is None:
                return 200, json.dumps(dict(statusCode="900", errorCode="FixtureNotFound"))
        else:
            raise AssertionError(name)
        return 200, json.dumps(dict(statusCode=800, returnObj=result))

    def command(self, argv, script=None):
        name = argv[0]
        self.calls.append((name, argv))
        if "--version" in argv or "--starttime=now-5minutes" in argv:
            return ""
        if name == "sinfo":
            assert "--all" not in argv
            return "".join(f"{node}|idle\n" for node in self.node_names)
        if name == "sbatch":
            assert self.run["progress"]["phase"] == "submit_intent"
            count = self.run["task"]["nodes"]
            assert set(argv) >= {f"--partition={QUEUE}", f"--nodelist={','.join(self.node_names)}", "--account=fixed",
                                 "--export=NIL", "--no-requeue", f"--nodes={count}", f"--ntasks={count * 2}",
                                 "--mem=512M", "--time=1"}
            assert self.slurm_job is False and self.run["progress"]["volume_ids"] == list(self.volumes)
            assert script.startswith("#!/bin/sh\nset -eu\nexec /usr/bin/srun ") and "/usr/bin/awk" in script
            self.slurm_job = True
            self.mutations.append("sbatch")
            if self.lose_ack == "sbatch":
                raise WorkerError("fixture sbatch timeout after acceptance")
            return "123;local\n"
        if name == "squeue":
            if f"--name={JOB}" in argv:
                return f"123|{JOB}|{QUEUE}|fixed\n" if self.slurm_job else ""
            return "999\n" if self.busy else ""
        if name == "sacct":
            count = self.run["task"]["nodes"]
            nodes = ",".join(self.node_names)
            return f"123|{JOB}|{QUEUE}|fixed|{self.slurm_state}|{self.exit_code}|4|{count * 2}|512Mn|{nodes}\n" if self.slurm_job else ""
        if name == "scontrol":
            assert argv[1:] == ["show", "hostnames", ",".join(self.node_names)]
            return "\n".join(self.node_names) + "\n"
        raise AssertionError(name)


class WorkerTest(unittest.TestCase):
    def test_lifecycle_full_progress_and_finish(self):
        f = Fixture()
        preflight(f.cloud, f.slurm)
        task = f.advance_to("released")
        self.assertEqual(task.advance(), "finish")
        self.assertIsNone(f.cats.claim())
        self.assertEqual(f.mutations, ["create-queue", "expand", "sbatch", "shrink", "delete-queue"])
        for key, expected in dict(queue_name=QUEUE, queue_id="queue-task", queue_create_operation="create-queue",
                                  queue_delete_operation="delete-queue", expand_operation="expand", shrink_operation="shrink",
                                  master_order_id="order-task", node_ids=["node-task"], node_names=["wn01"],
                                  instance_ids=["instance-task"], volume_ids=["volume-task"], slurm_job_id="123",
                                  slurm_state="COMPLETED", slurm_exit_code="0:0", slurm_elapsed_seconds=4,
                                  slurm_allocated_cpus=2, slurm_requested_memory="512Mn", slurm_node_list=["wn01"]).items():
            self.assertEqual(f.finished[key], expected, key)
        self.assertTrue(f.finished["provision_started_at"], f.finished)
        self.assertTrue(f.finished["released_at"], f.finished)

    def test_two_node_release_waits_for_last_system_disk(self):
        f = Fixture()
        f.run["task"]["nodes"] = 2
        task = f.advance_to("slurm_done")
        f.keep_volume = True
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.nodes, [])
        self.assertEqual(f.instances, {})
        del f.volumes["volume-task"]
        self.assertEqual(task.advance(), "wait")
        self.assertIsNone(f.finished)
        self.assertNotIn("delete-queue", f.mutations)
        del f.volumes["volume-task2"]
        self.assertEqual(f.advance_to("released").advance(), "finish")
        self.assertEqual(f.finished["slurm_node_list"], ["wn01", "wn02"])
        self.assertEqual(f.finished["volume_ids"], ["volume-task", "volume-task2"])

    def test_disk_must_be_system_disk_of_owned_instance(self):
        for changes in (dict(instanceID="other-instance"), dict(system=False)):
            with self.subTest(changes=changes):
                f = Fixture()
                task = f.advance_to("discover_disks")
                f.volumes["volume-task"].update(changes)
                self.assertEqual(task.advance(), "reconcile")
                self.assertIn("system disk", f.run["progress"]["problem"])
                self.assertNotIn("sbatch", f.mutations)

    def test_resume_after_ambiguous_expand_or_shrink_requires_operation_uuid(self):
        cases = [("expand", "queue_created", "expand_operation", "discover_disks"),
                 ("shrink", "slurm_done", "shrink_operation", "nodes_released")]
        for op, phase, field, recovered in cases:
            with self.subTest(operation=op):
                f = Fixture()
                task = f.advance_to(phase)
                f.lose_ack = op
                self.assertEqual(task.advance(), "reconcile")
                self.assertNotIn(field, f.run["progress"])
                trace = f.run["progress"]["problem"].split("EOP request ID ")[1].split(";")[0]
                self.assertEqual(f.claimed().advance(), "reconcile")
                self.assertIn("EOP request ID " + trace, f.run["progress"]["problem"])
                self.assertEqual(f.mutations.count(op), 1)
                self.assertIsNone(f.finished)
                f.run["progress"][field] = op
                self.assertEqual(f.claimed().advance(), "next")
                self.assertEqual(f.run["progress"]["phase"], recovered)
                self.assertEqual(f.mutations.count(op), 1)

    def test_sbatch_lost_ack_recovers_exact_named_job(self):
        f = Fixture()
        task = f.advance_to("nodes_ready")
        f.lose_ack = "sbatch"
        self.assertEqual(task.advance(), "reconcile")
        self.assertEqual(f.claimed().advance(), "next")
        self.assertEqual(f.run["progress"]["slurm_job_id"], "123")
        self.assertEqual(f.advance_to("released").advance(), "finish")
        self.assertEqual(f.mutations.count("sbatch"), 1)

    def test_busy_node_disk_presence_and_failed_slurm(self):
        f = Fixture()
        f.slurm_state, f.exit_code = "FAILED", "1:0"
        task = f.advance_to("slurm_done")
        f.busy = True
        self.assertEqual(task.advance(), "wait")
        self.assertNotIn("shrink", f.mutations)
        f.busy, f.keep_volume = False, True
        self.assertEqual(task.advance(), "next")
        self.assertEqual(task.advance(), "wait")
        self.assertNotIn("delete-queue", f.mutations)
        f.volumes.clear()
        self.assertEqual(f.advance_to("released").advance(), "finish")
        self.assertEqual(f.finished["slurm_state"], "FAILED")
        self.assertEqual(f.finished["slurm_exit_code"], "1:0")

    def test_preexisting_name_and_default_queue_block_mutations(self):
        f = Fixture()
        f.queues.append(dict(queueName=QUEUE, queueUUID="foreign", default=False))
        self.assertEqual(f.claimed().advance(), "reconcile")
        self.assertFalse(f.mutations)
        f = Fixture()
        task = f.advance_to("create_intent")
        f.queues[0]["default"] = True
        self.assertEqual(task.advance(), "reconcile")
        self.assertEqual(f.mutations, ["create-queue"])

    def test_explicit_rejection_with_no_provisioning(self):
        for op in ("create-queue", "expand"):
            with self.subTest(operation=op):
                f = Fixture()
                f.reject = op
                self.assertEqual(f.advance_to("rejected").advance(), "finish")
                self.assertFalse(f.queues)
                self.assertFalse(f.finished.get("provision_started_at"))
                self.assertTrue(f.finished["released_at"])
                self.assertNotIn("shrink", f.mutations)

    def test_force_completed_and_failure_do_not_confirm_expansion(self):
        for state in ("force_completed", "failed", "cancel", "console_failed"):
            with self.subTest(status=state):
                f = Fixture()
                task = f.advance_to("expand_intent")
                f.statuses["expand"] = state
                self.assertEqual(task.advance(), "reconcile")
                self.assertEqual(f.mutations, ["create-queue", "expand"])
                self.assertIn(state, f.run["progress"]["problem"])

    def test_bad_scope_or_missing_queue_association_blocks_submit(self):
        for field, bad in (("queueName", None), ("specificationName", "wrong"), ("billing", False),
                           ("creation", "auto"), ("role", 2), ("kind", "bareMetal")):
            with self.subTest(field=field):
                f = Fixture()
                task = f.advance_to("expand_intent")
                f.nodes[0][field] = bad
                self.assertEqual(task.advance(), "reconcile")
                self.assertIn("expand_operation", f.run["progress"])
                self.assertNotIn("sbatch", f.mutations)

    def test_lease_lost_fences_intent_and_lost_result_is_not_replayed(self):
        f = Fixture()
        f.save_conflict = lambda p: p.get("phase") == "create_intent"
        with self.assertRaises(LeaseError):
            f.claimed().advance()
        self.assertFalse(f.mutations)
        f.save_conflict = lambda p: bool(p.get("queue_create_operation"))
        with self.assertRaises(LeaseError):
            f.claimed().advance()
        f.save_conflict = None
        self.assertEqual(f.claimed().advance(), "reconcile")
        self.assertEqual(f.mutations, ["create-queue"])

    def test_missing_optional_order_keeps_job_receipt(self):
        f = Fixture()
        f.missing_master = True
        f.advance_to("expand_intent")
        self.assertEqual(f.run["progress"]["expand_operation"], "expand")
        self.assertNotIn("master_order_id", f.run["progress"])

    def test_preflight_denial_never_claims(self):
        args = ["worker.py", "--config", "fixture.json", "--once"]
        values = {"CATS_COMPUTE_WORKER_TOKEN": "x" * 32, "CTYUN_AK": "x", "CTYUN_SK": "x"}
        with patch("sys.argv", args), patch.dict("os.environ", values), patch("builtins.open"), \
                patch("worker.json.load", return_value={"cats_url": "https://cats.invalid", "cloud": {}, "slurm": {}}), \
                patch("worker.Cloud"), patch("worker.Slurm"), patch("worker.loop") as claim, \
                patch("worker.preflight", side_effect=WorkerError("fixture denied")):
            self.assertEqual(main(), 1)
            claim.assert_not_called()

    def test_eop_query_and_signature_vector(self):
        query = query_string({"q": "a b+/", "jobUUID": "op-1"})
        self.assertEqual(query, "jobUUID=op-1&q=a%20b%2B%2F")
        result = signed_headers("ak", "sk", query, "", "00000000-0000-0000-0000-000000000000", "20260101T010203Z")
        self.assertEqual(result["Eop-Authorization"],
                         "ak Headers=ctyun-eop-request-id;eop-date Signature=pmveuEm2VRg6b+fTcQo8wgtlRxZIkTz9O2dy5bBhovw=")


if __name__ == "__main__":
    unittest.main()
