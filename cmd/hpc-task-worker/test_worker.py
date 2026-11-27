import atexit
import copy
import json
import os
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit
from slurm import Slurm

_TEST_LOCK_PATHS = set()


def _cleanup_test_lock_files():
    for path in _TEST_LOCK_PATHS:
        try:
            os.unlink(path)
        except OSError:
            pass


atexit.register(_cleanup_test_lock_files)

from cloud import API_FIELDS, Cloud, MutationGate, WorkerError, query_string, signed_headers
from worker import Cats, LeaseError, Task, main, preflight, resource_budget_seconds


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
                        max_node_seconds=60,
                        task=dict(nodes=1, logical_cpus=2, memory_mib=512, wall_seconds=60,
                                   max_resource_seconds_per_node=60))
        self.calls = []
        self.mutations = []
        self.queues = []
        self.nodes = []
        self.instances = {}
        self.volumes = {}
        self.statuses = {"read-probe": "success"}
        self.finished = None
        self.finish_status = None
        self.slurm_job = False
        self.slurm_state = "COMPLETED"
        self.exit_code = "0:0"
        self.busy = False
        self.lose_ack = None
        self.reject = None
        self.force = None
        self.fail = None
        self.save_conflict = None
        self.keep_volume = False
        self.node_names = ["wn01"]
        self.missing_master = False
        self.cats = Cats("https://cats.invalid", "fixture-token", self.http)
        fd, self.lock_path = tempfile.mkstemp(prefix="catsco-worker-test-", suffix=".lock")
        _TEST_LOCK_PATHS.update((self.lock_path, self.lock_path + ".lock"))
        os.close(fd)
        os.unlink(self.lock_path)
        cloud_config = config()
        cloud_config["mutation_lock_file"] = self.lock_path
        cloud_config["mutation_cooldown_seconds"] = 0
        self.cloud = Cloud(cloud_config, "fixture-ak", "fixture-sk", self.http)
        self.slurm = Slurm({"account": "fixed"}, self.command)

    def __del__(self):
        for path in (getattr(self, "lock_path", ""), getattr(self, "lock_path", "") + ".lock"):
            try:
                os.unlink(path)
            except OSError:
                pass

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
                if self.finish_status is not None:
                    return self.finish_status, ""
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
            if self.fail == name:
                self.statuses[name] = "failed"
                return 200, json.dumps(dict(statusCode=800, returnObj=dict(jobUUID=name)))
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
            minutes = self.run["task"]["wall_seconds"] // 60
            assert set(argv) >= {f"--partition={QUEUE}", f"--nodelist={','.join(self.node_names)}", "--account=fixed",
                                 "--export=NIL", "--no-requeue", f"--nodes={count}", f"--ntasks={count * 2}",
                                 "--mem=512M", f"--time={minutes}"}
            assert self.slurm_job is False and self.run["progress"]["volume_ids"] == list(self.volumes)
            assert script.startswith("#!/bin/sh\nset -eu\nexec /usr/bin/srun ") and "--account=fixed" in script and "/usr/bin/awk" in script
            self.slurm_job = True
            self.mutations.append("sbatch")
            if self.lose_ack == "sbatch":
                raise WorkerError("fixture sbatch timeout after acceptance")
            return "123;local\n"
        if name == "squeue":
            if f"--name={JOB}" in argv:
                return f"123|{JOB}|{QUEUE}|fixed\n" if self.slurm_job else ""
            return "999|RUNNING\n" if self.busy else ""
        if name == "sacct":
            count = self.run["task"]["nodes"]
            nodes = ",".join(self.node_names)
            return f"123|{JOB}|{QUEUE}|fixed|{self.slurm_state}|{self.exit_code}|4|{count * 2}|512Mn|{nodes}\n" if self.slurm_job else ""
        if name == "scontrol":
            assert argv[1:] == ["show", "hostnames", ",".join(self.node_names)]
            return "\n".join(self.node_names) + "\n"
        raise AssertionError(name)


class SlurmTest(unittest.TestCase):
    def test_memory_satisfies_accepts_slurm_rounded_units(self):
        task = dict(nodes=1, logical_cpus=2, memory_mib=3584)
        self.assertTrue(Slurm.memory_satisfies("3584M", task))
        self.assertTrue(Slurm.memory_satisfies("3.50G", task))
        self.assertTrue(Slurm.memory_satisfies("1792Mc", task))
        self.assertFalse(Slurm.memory_satisfies("3.4G", task))
        self.assertFalse(Slurm.memory_satisfies("garbage", task))

    def test_submit_passes_long_wall_time_to_slurm(self):
        commands = []

        def submit(argv, script):
            commands.extend(argv)
            return "123"

        slurm = Slurm({"account": "fixed"}, submit)
        task = dict(nodes=2, logical_cpus=2, memory_mib=512, wall_seconds=1800)
        job_id = slurm.submit(QUEUE, ["wn01", "wn02"], JOB, task, lambda func, *args: func(*args))
        self.assertEqual(job_id, "123")
        self.assertIn("--time=30", commands)
        self.assertIn("--deadline=now+32minutes", commands)
        self.assertIn("--nodes=2", commands)
        self.assertIn("--ntasks-per-node=2", commands)

    def test_submit_runs_caller_workload_in_container(self):
        commands, scripts = [], []

        def submit(argv, script):
            commands.extend(argv)
            scripts.append(script)
            return "123"

        slurm = Slurm({"account": "fixed"}, submit)
        task = dict(nodes=1, logical_cpus=2, memory_mib=512, wall_seconds=600,
                    workload=dict(command="foamRun -solver incompressibleFluid",
                                  image="docker://openfoam/openfoam12-paraview512:latest"))
        self.assertEqual(slurm.submit(QUEUE, ["wn01"], JOB, task, lambda func, *args: func(*args)), "123")
        self.assertIn(f"--chdir=/home/galaxy/catsco-runs/{JOB}", commands)
        self.assertIn(f"--output=/home/galaxy/catsco-runs/{JOB}/slurm-%j.out", commands)
        self.assertIn("apptainer", scripts[0])
        self.assertIn("--cleanenv", scripts[0])
        self.assertIn("docker://openfoam/openfoam12-paraview512:latest", scripts[0])
        self.assertIn("foamRun -solver incompressibleFluid", scripts[0])

    def test_submit_falls_back_to_builtin_job_without_workload(self):
        commands = []

        def submit(argv, script):
            commands.extend(argv)
            commands.append(script)
            return "123"

        slurm = Slurm({"account": "fixed"}, submit)
        task = dict(nodes=1, logical_cpus=1, memory_mib=64, wall_seconds=60)
        slurm.submit(QUEUE, ["wn01"], JOB, task, lambda func, *args: func(*args))
        self.assertIn("--chdir=/tmp", commands)
        self.assertTrue(any("/usr/bin/awk" in str(item) for item in commands))

    def test_terminal_waits_for_missing_job_metadata(self):
        placeholder = "123|allocation||fixed|PENDING|0:0|1|0|0|wn01\n"
        completed = f"123|{JOB}|{QUEUE}|fixed|COMPLETED|0:0|1|1|64M|wn01\n"
        completed_placeholder = "123|allocation||fixed|COMPLETED|0:0|1|1|0|wn01\n"
        outputs = iter(["", placeholder, completed_placeholder, completed, "wn01\n", placeholder + completed,
                        placeholder.replace("|fixed|", "|other|"), completed.replace(JOB, "other-job")])
        slurm = Slurm({"account": "fixed"}, lambda argv: next(outputs))
        task = dict(nodes=1, logical_cpus=1, memory_mib=64, wall_seconds=60)
        args = (QUEUE, ["wn01"], JOB, "123", "2026-01-01T00:00:00Z", task, lambda func, *args: func(*args))
        self.assertIsNone(slurm.terminal(*args))
        self.assertIsNone(slurm.terminal(*args))
        self.assertIsNone(slurm.terminal(*args))
        self.assertEqual(slurm.terminal(*args)["slurm_state"], "COMPLETED")
        for _ in range(3):
            with self.assertRaisesRegex(WorkerError, "does not uniquely identify"):
                slurm.terminal(*args)

    def test_busy_ignores_terminal_history_but_checks_both_scopes(self):
        for state, expected in [("COMPLETED", False), ("PENDING", True), ("COMPLETING", True), ("UNKNOWN", True)]:
            with self.subTest(state=state):
                outputs = iter(["123|COMPLETED\n", f"123|COMPLETED\n124|{state}\n"])
                slurm = Slurm({"account": "fixed"}, lambda argv: next(outputs))
                self.assertEqual(slurm.busy(QUEUE, ["wn01"], lambda func, *args: func(*args)), expected)



class MutationGateTest(unittest.TestCase):
    def _start_child(self, path, timeout, hold, interval=0, mode="ok"):
        code = (
            "import sys,time\n"
            "sys.path.insert(0, sys.argv[5])\n"
            "from cloud import MutationGate\n"
            "with MutationGate(sys.argv[1], timeout_seconds=float(sys.argv[2]), "
            "interval_seconds=float(sys.argv[3])):\n"
            " print('locked', flush=True)\n"
            " time.sleep(float(sys.argv[4]))\n"
            " if sys.argv[6] == 'fail':\n"
            "  raise RuntimeError('child failure')\n"
        )
        return subprocess.Popen(
            [sys.executable, "-c", code, path, str(timeout), str(interval), str(hold),
             os.path.dirname(__file__), mode],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )

    def test_serializes_mutations_across_cloud_instances(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "mutation.lock")
            gates = [MutationGate(path, timeout_seconds=2, interval_seconds=0) for _ in range(2)]
            events = []
            barrier = threading.Barrier(2)

            def run(index):
                barrier.wait()
                with gates[index]:
                    events.append(("start", index))
                    time.sleep(0.03)
                    events.append(("end", index))

            threads = [threading.Thread(target=run, args=(index,)) for index in range(2)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()
            self.assertEqual(len(events), 4)
            self.assertEqual([event[0] for event in events], ["start", "end", "start", "end"])
            self.assertEqual(events[0][1], events[1][1])
            self.assertEqual(events[2][1], events[3][1])
            self.assertNotEqual(events[0][1], events[2][1])

    def test_serializes_mutations_across_processes(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "mutation.lock")
            first = self._start_child(path, 2, 0.25)
            second = None
            try:
                self.assertEqual(first.stdout.readline().strip(), "locked")
                second = self._start_child(path, 2, 0)
                second_output, second_error = second.communicate(timeout=3)
                self.assertEqual(second.returncode, 0, second_error)
                self.assertEqual(second_output.strip(), "locked")
            finally:
                if second is not None and second.poll() is None:
                    second.terminate()
                    second.communicate(timeout=3)
                if first.poll() is None:
                    first.terminate()
                first.communicate(timeout=3)

    def test_process_timeout_does_not_enter(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "mutation.lock")
            first = self._start_child(path, 2, 0.3)
            second = None
            try:
                self.assertEqual(first.stdout.readline().strip(), "locked")
                second = self._start_child(path, 0.05, 0)
                second_output, second_error = second.communicate(timeout=2)
                self.assertNotEqual(second.returncode, 0, second_error)
                self.assertNotIn("locked", second_output)
                self.assertIn("cloud mutation lock timed out", second_error)
            finally:
                if second is not None and second.poll() is None:
                    second.terminate()
                    second.communicate(timeout=3)
                if first.poll() is None:
                    first.terminate()
                first.communicate(timeout=3)

    def test_process_exception_releases_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "mutation.lock")
            failed = self._start_child(path, 2, 0, mode="fail")
            output, error = failed.communicate(timeout=2)
            self.assertNotEqual(failed.returncode, 0, output)
            self.assertIn("child failure", error)
            recovered = self._start_child(path, 0.5, 0)
            recovered_output, recovered_error = recovered.communicate(timeout=2)
            self.assertEqual(recovered.returncode, 0, recovered_error)
            self.assertEqual(recovered_output.strip(), "locked")

    def test_cooldown_is_shared_across_processes(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "mutation.lock")
            first = self._start_child(path, 2, 0, interval=0.08)
            self.assertEqual(first.stdout.readline().strip(), "locked")
            first_output, first_error = first.communicate(timeout=2)
            self.assertEqual(first.returncode, 0, first_error)
            started = time.monotonic()
            second = self._start_child(path, 2, 0, interval=0.08)
            second_output, second_error = second.communicate(timeout=2)
            self.assertEqual(second.returncode, 0, second_error)
            self.assertEqual(second_output.strip(), "locked")
            self.assertGreaterEqual(time.monotonic() - started, 0.07)

    def test_cooldown_is_shared_through_lock_file(self):
        with tempfile.TemporaryDirectory() as directory:
            gate = MutationGate(os.path.join(directory, "mutation.lock"), interval_seconds=0.05)
            with gate:
                pass
            started = time.monotonic()
            with gate:
                elapsed = time.monotonic() - started
            self.assertGreaterEqual(elapsed, 0.04)

    def test_failed_mutation_releases_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "mutation.lock")
            with self.assertRaises(RuntimeError):
                with MutationGate(path, interval_seconds=0):
                    raise RuntimeError("fixture mutation failure")
            with MutationGate(path, timeout_seconds=0.1, interval_seconds=0):
                pass

class WorkerTest(unittest.TestCase):
    def test_resource_budget_supports_explicit_and_legacy_quotes(self):
        f = Fixture()
        self.assertEqual(resource_budget_seconds(f.run), 60)
        f.run["task"].pop("max_resource_seconds_per_node")
        f.run["max_node_seconds"] = 1200
        self.assertEqual(resource_budget_seconds(f.run), 1200)
        f.claimed().validate_spec()

    def test_busy_mutation_lock_leaves_intent_unsent_for_retry(self):
        f = Fixture()
        task = f.advance_to("queue_created")
        gate = f.cloud.mutation_gate
        gate.timeout_seconds = 0.01
        with MutationGate(gate.path, interval_seconds=0):
            self.assertEqual(task.advance(), "wait")
            self.assertEqual(f.run["progress"]["phase"], "queue_created")
            self.assertNotIn("expand", f.mutations)
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "expand_intent")
        self.assertEqual(f.mutations.count("expand"), 1)

    def test_lifecycle_full_progress_and_finish(self):
        f = Fixture()
        f.run["task"].update(wall_seconds=1800, max_resource_seconds_per_node=2400)
        f.run["max_node_seconds"] = 2400
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

    def test_stalled_provider_expand_adopts_ready_nodes_after_grace(self):
        f = Fixture()
        task = f.advance_to("queue_created")
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "expand_intent")
        self.assertIn("expand_sent_at", f.run["progress"])
        # The provider accepted the expand but never reports success.
        f.statuses["expand"] = "running"
        self.assertEqual(f.claimed().advance(), "wait")
        # Once the grace period has passed and the quoted nodes are visible, adopt them.
        f.run["progress"]["expand_sent_at"] = "2026-01-01T00:00:00Z"
        self.assertEqual(f.claimed().advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "discover_disks")
        self.assertEqual(f.run["progress"]["node_names"], ["wn01"])
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
                if op == "shrink":
                    f.run["progress"]["provision_started_at"] = "2026-01-01T00:00:00Z"
                    task.progress["provision_started_at"] = "2026-01-01T00:00:00Z"
                f.lose_ack = op
                self.assertEqual(task.advance(), "reconcile")
                self.assertNotIn(field, f.run["progress"])
                self.assertEqual(f.claimed().advance(), "next")
                self.assertIn(field, f.run["progress"])
                self.assertEqual(f.run["progress"]["phase"], recovered)
                self.assertEqual(f.mutations.count(op), 1)
    def test_unconfirmed_shrink_is_resent_after_grace(self):
        f = Fixture()
        task = f.advance_to("slurm_done")
        f.run["progress"]["provision_started_at"] = "2026-01-01T00:00:00Z"
        task.progress["provision_started_at"] = "2026-01-01T00:00:00Z"
        f.lose_ack = "shrink"
        self.assertEqual(task.advance(), "reconcile")
        self.assertNotIn("shrink_operation", f.run["progress"])
        # The provider never applied the change, so the nodes are still ours and the
        # worker has to resend the shrink instead of waiting for an absence.
        f.lose_ack = None
        f.nodes = [dict(OWNED)]
        f.instances = {"instance-task": dict(id="instance-task", volumeID="volume-task")}
        f.volumes = {"volume-task": dict(id="volume-task", instanceID="instance-task", system=True)}
        self.assertEqual(f.claimed().advance(), "next")
        self.assertEqual(f.mutations.count("shrink"), 2)


    def test_recovery_handles_create_queue_appearing_or_disappearing(self):
        f = Fixture()
        task = f.claimed()
        f.lose_ack = "create-queue"
        self.assertEqual(task.advance(), "reconcile")
        self.assertEqual(f.claimed().advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "queue_created")
        self.assertEqual(f.run["progress"]["queue_id"], "queue-task")

        f = Fixture()
        task = f.claimed()
        f.lose_ack = "create-queue"
        self.assertEqual(task.advance(), "reconcile")
        f.queues.clear()
        recovered = f.claimed()
        self.assertEqual(recovered.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "rejected")
        self.assertTrue(f.run["progress"]["released_at"])
        self.assertEqual(recovered.advance(), "finish")

    def test_recovery_preserves_empty_queue_and_blocks_orphan_nodes(self):
        f = Fixture()
        task = f.advance_to("queue_created")
        f.lose_ack = "expand"
        self.assertEqual(task.advance(), "reconcile")
        f.nodes.clear()
        f.instances.clear()
        f.volumes.clear()
        f.run["progress"]["provision_started_at"] = "2026-01-01T00:00:00Z"
        recovered = f.claimed()
        self.assertEqual(recovered.advance(), "reconcile")
        self.assertIn("task queue remained empty", f.run["progress"]["problem"])
        self.assertTrue(f.queues)
        self.assertTrue(f.run["progress"].get("provision_started_at"))
        self.assertEqual(f.mutations, ["create-queue", "expand"])

        f = Fixture()
        task = f.advance_to("queue_created")
        f.lose_ack = "expand"
        self.assertEqual(task.advance(), "reconcile")
        f.queues.clear()
        self.assertEqual(f.claimed().advance(), "reconcile")
        self.assertIn("task nodes remain without a queue", f.run["progress"]["problem"])
        self.assertEqual(f.mutations, ["create-queue", "expand"])

    def test_recovery_accepts_missing_queue_delete_after_resources_are_gone(self):
        f = Fixture()
        task = f.advance_to("nodes_released")
        f.lose_ack = "delete-queue"
        self.assertEqual(task.advance(), "reconcile")
        f.run["progress"]["provision_started_at"] = "2026-01-01T00:00:00Z"
        recovered = f.claimed()
        self.assertEqual(recovered.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "delete_intent")
        self.assertEqual(recovered.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "released")
        self.assertEqual(recovered.advance(), "finish")
        self.assertEqual(f.mutations.count("delete-queue"), 1)

    def test_delete_empty_accepts_queue_already_gone(self):
        f = Fixture()
        task = f.advance_to("nodes_released")
        f.queues.clear()
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "delete_intent")
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "released")
        self.assertEqual(task.advance(), "finish")

    def test_provider_rejection_retries_shrink_and_delete(self):
        f = Fixture()
        task = f.advance_to("slurm_done")
        f.reject = "shrink"
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "slurm_done")
        f.reject = None
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "shrink_intent")

        f = Fixture()
        task = f.advance_to("nodes_released")
        f.reject = "delete-queue"
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "nodes_released")
        f.reject = None
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "delete_intent")

    def test_failed_shrink_and_delete_retry_after_state_checks(self):
        f = Fixture()
        task = f.advance_to("slurm_done")
        f.fail = "shrink"
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "shrink_intent")
        f.fail = None
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "shrink_intent")
        self.assertEqual(f.mutations.count("shrink"), 2)
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "nodes_released")

        f = Fixture()
        task = f.advance_to("nodes_released")
        f.fail = "delete-queue"
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "delete_intent")
        f.fail = None
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "delete_intent")
        self.assertEqual(f.mutations.count("delete-queue"), 2)
        self.assertEqual(task.advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "released")

    def test_finish_release_rejection_becomes_manual_reconcile(self):
        f = Fixture()
        task = f.advance_to("released")
        f.finish_status = 422
        self.assertEqual(task.advance(), "reconcile")
        self.assertIn("manual_finish", f.run["progress"]["problem"])
        self.assertIsNone(f.finished)
        f.finish_status = None
        self.assertEqual(task.advance(), "reconcile")
        self.assertIsNone(f.finished)

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
        self.assertEqual(f.claimed().advance(), "next")
        self.assertEqual(f.run["progress"]["phase"], "queue_created")
        self.assertEqual(f.mutations, ["create-queue"])

    def test_missing_optional_order_keeps_job_receipt(self):
        f = Fixture()
        f.missing_master = True
        f.advance_to("expand_intent")
        self.assertEqual(f.run["progress"]["expand_operation"], "expand")
        self.assertNotIn("provision_started_at", f.run["progress"])
        self.assertNotIn("master_order_id", f.run["progress"])

    def test_slow_cloud_mutation_receipt_does_not_extend_reads_or_cats(self):
        cloud = Cloud(config(), "fixture-ak", "fixture-sk")
        scope = dict(queueUUID="queue-1", count=2, imageUUID="image-1", specificationUUID="spec-cpu")

        def exchange(args, **kwargs):
            request = json.loads(kwargs["input"])
            timeout = kwargs["timeout"]
            self.assertEqual(request["timeout"], timeout)
            if request["url"].endswith("expand-cluster"):
                if timeout < 12:
                    raise subprocess.TimeoutExpired(args, timeout)
                self.assertEqual(timeout, 30)
                reply = dict(statusCode=800, returnObj=dict(jobUUID="slow-expansion"))
                return subprocess.CompletedProcess(args, 0, json.dumps([200, json.dumps(reply)]))
            self.assertEqual(timeout, 8)
            if request["url"].endswith("/api/compute/worker"):
                return subprocess.CompletedProcess(args, 0, json.dumps([204, ""]))
            reply = dict(statusCode=800, returnObj=dict(status="success"))
            return subprocess.CompletedProcess(args, 0, json.dumps([200, json.dumps(reply)]))

        with patch("cloud.subprocess.run", side_effect=exchange) as transport:
            call = lambda fn, *args: fn(*args)
            result = cloud.outcome("expand", call, "request-1", **scope)
            self.assertEqual(result["jobUUID"], "slow-expansion")
            self.assertEqual(cloud.status(result["jobUUID"], call), "success")
            self.assertIsNone(Cats("https://cats.invalid", "fixture-token").claim())
            self.assertEqual(transport.call_count, 3)

        with patch("cloud.subprocess.run", side_effect=subprocess.TimeoutExpired("expand", 30)) as transport:
            with self.assertRaisesRegex(WorkerError, "HTTP exchange failed or timed out"):
                cloud.outcome("expand", call, "request-2", **scope)
            transport.assert_called_once()

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
