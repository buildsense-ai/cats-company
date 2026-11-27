"""CTYun EOP transport and explicit capacity API bindings."""

import base64
import contextlib
import errno
import hashlib
import hmac
import http.client
import json
import os
import re
import subprocess
import sys
import tempfile
import threading
import time
import uuid
from datetime import datetime, timezone
from urllib.parse import quote, urlsplit


try:
    import fcntl
except ImportError:
    fcntl = None

try:
    import msvcrt
except ImportError:
    msvcrt = None


_LOCAL_MUTATION_LOCKS = {}
_LOCAL_MUTATION_LOCKS_GUARD = threading.Lock()


def _local_mutation_lock(path):
    with _LOCAL_MUTATION_LOCKS_GUARD:
        return _LOCAL_MUTATION_LOCKS.setdefault(path, threading.Lock())


class MutationGate:
    def __init__(self, path, timeout_seconds=5.0, interval_seconds=0.2):
        if not isinstance(path, str) or not os.path.isabs(path):
            raise ValueError("mutation_lock_file must be an absolute path")
        if type(timeout_seconds) not in (int, float) or timeout_seconds <= 0:
            raise ValueError("mutation_lock_timeout_seconds must be positive")
        if type(interval_seconds) not in (int, float) or interval_seconds < 0:
            raise ValueError("mutation_cooldown_seconds must be non-negative")
        if fcntl is None and msvcrt is None:
            raise WorkerError("cloud mutation locking is unavailable on this platform")
        self.path = path
        self.lock_path = path + ".lock"
        self.timeout_seconds = float(timeout_seconds)
        self.interval_seconds = float(interval_seconds)
        self.local = _local_mutation_lock(path)
        self.handle = None
        self.lock_handle = None
        self.file_locked = False

    def __enter__(self):
        deadline = time.monotonic() + self.timeout_seconds
        if not self.local.acquire(timeout=self.timeout_seconds):
            raise MutationLockTimeout("cloud mutation lock timed out")
        try:
            os.makedirs(os.path.dirname(self.path) or ".", exist_ok=True)
            self.lock_handle = open(self.lock_path, "a+b")
            while True:
                try:
                    if fcntl is not None:
                        fcntl.flock(self.lock_handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                    else:
                        self.lock_handle.seek(0)
                        msvcrt.locking(self.lock_handle.fileno(), msvcrt.LK_NBLCK, 1)
                    self.file_locked = True
                    break
                except OSError as error:
                    if error.errno not in (
                        errno.EACCES, errno.EAGAIN, errno.EDEADLK,
                    ):
                        raise
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise MutationLockTimeout("cloud mutation lock timed out")
                    time.sleep(min(0.05, remaining))
            self.handle = open(self.path, "a+b")
            self.handle.seek(0)
            raw = self.handle.read().decode("ascii", errors="ignore").strip()
            if raw:
                try:
                    last = float(raw)
                except ValueError:
                    last = 0.0
                wait = self.interval_seconds - (time.time() - last)
                if wait > 0:
                    time.sleep(wait)
            return self
        except Exception:
            self._close()
            raise

    def __exit__(self, exc_type, exc, traceback):
        try:
            if self.handle is not None:
                self.handle.seek(0)
                self.handle.truncate()
                self.handle.write(str(time.time()).encode("ascii"))
                self.handle.flush()
        finally:
            self._close()

    def _close(self):
        try:
            if self.lock_handle is not None:
                try:
                    if self.file_locked:
                        if fcntl is not None:
                            fcntl.flock(self.lock_handle.fileno(), fcntl.LOCK_UN)
                        else:
                            self.lock_handle.seek(0)
                            msvcrt.locking(self.lock_handle.fileno(), msvcrt.LK_UNLCK, 1)
                finally:
                    self.file_locked = False
                    self.lock_handle.close()
                    self.lock_handle = None
            if self.handle is not None:
                self.handle.close()
                self.handle = None
        finally:
            if self.local.locked():
                self.local.release()

IO_TIMEOUT = 8
CTYUN_MUTATION_TIMEOUT = 30  # The acknowledged intent holds a 45-second lease.
MAX_RESPONSE = 1024 * 1024


class WorkerError(Exception):
    pass


class MutationLockTimeout(WorkerError):
    pass


class Rejected(WorkerError):
    """A configured provider code explicitly guarantees no operation was accepted."""


def http_child():
    request = json.load(sys.stdin)
    url = urlsplit(request["url"])
    target = url.path + ("?" + url.query if url.query else "")
    connection = http.client.HTTPSConnection(url.hostname, url.port, timeout=request["timeout"])
    try:
        connection.request(request["method"], target, request["body"], request["headers"])
        with connection.getresponse() as response:
            body = response.read(MAX_RESPONSE + 1)
            if len(body) > MAX_RESPONSE:
                return 1
            json.dump([response.status, body.decode("utf-8")], sys.stdout)
    except (OSError, http.client.HTTPException, UnicodeError):
        return 1
    finally:
        connection.close()
    return 0


def http_request(method, url, headers, body=None, *, timeout=IO_TIMEOUT):
    # Bound the entire HTTP exchange, including DNS, TLS and slow response bodies.
    request = json.dumps(dict(method=method, url=url, headers=headers, body=body, timeout=timeout))
    try:
        result = subprocess.run(
            [sys.executable, "-I", "-B", __file__, "--http"], input=request,
            capture_output=True, text=True, encoding="utf-8", timeout=timeout, check=True,
            env={},
        )
        return json.loads(result.stdout)
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        raise WorkerError("HTTP exchange failed or timed out") from exc


def https_base(value):
    url = urlsplit(value)
    if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment:
        raise ValueError("expected an HTTPS base URL without credentials, query or fragment")
    return value.rstrip("/")


def query_string(params):
    return "&".join(f"{quote(k, safe='')}={quote(str(v), safe='')}" for k, v in sorted(params.items()))


def signed_headers(ak, sk, query, body, request_id, date):
    header_text = f"ctyun-eop-request-id:{request_id}\neop-date:{date}\n"
    body_hash = hashlib.sha256(body.encode()).hexdigest()
    message = f"{header_text}\n{query}\n{body_hash}".encode()
    key = sk.encode()
    for part in (date, ak, date.split("T", 1)[0]):
        key = hmac.digest(key, part.encode(), "sha256")
    signature = base64.b64encode(hmac.digest(key, message, "sha256")).decode()
    return {
        "ctyun-eop-request-id": request_id,
        "Eop-date": date,
        "Eop-Authorization": f"{ak} Headers=ctyun-eop-request-id;eop-date Signature={signature}",
        "Content-Type": "application/json",
    }


def select(value, path):
    try:
        for part in path.split(".") if path else ():
            value = value[int(part)] if isinstance(value, list) and part.isdecimal() else value[part]
    except (IndexError, KeyError, TypeError):
        raise WorkerError(f"cloud response is missing {path}") from None
    return value


def identifier(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", value):
        raise WorkerError("missing or unexpected resource identifier")
    return value


def project(context, operation):
    payload = {}
    for semantic, field in operation["params"].items():
        target = payload
        parts = field.split(".")
        for part in parts[:-1]:
            target = target.setdefault(part, {})
        target[parts[-1]] = context[semantic]
    return payload


# These two requests have a fixed contract. The remaining API bindings must be
# supplied by the operator for the capacity/ECS/EVS API versions in use.
CREATE = {"path": "/v4/cthpc/hpc/create-queue", "method": "POST", "params": {
    key: key for key in ("regionID", "azName", "clusterUUID", "queueName")
}}
JOB = {"path": "/v4/cthpc/query-job-result", "method": "GET", "params": {"jobUUID": "jobUUID"}}
SCOPE = ("regionID", "azName", "clusterUUID")
API_FIELDS = {
    "create_queue": (tuple(CREATE["params"]), ("jobUUID",)),
    "job": (tuple(JOB["params"]), ("status",)),
    "list_queues": (("regionID", "clusterUUID"), ("queueName", "queueUUID", "default")),
    "list_nodes": (("regionID", "clusterUUID"), (
        "queueName", "nodeUUID", "nodeName", "instanceID", "role", "kind", "billing", "creation", "specificationName",
    )),
    "expand": ((), ("jobUUID",)),
    "shrink": (("regionID", "clusterUUID", "nodeUUIDList"), ("jobUUID",)),
    "delete_queue": (("regionID", "clusterUUID", "queueUUID"), ("jobUUID",)),
    "ecs": (("regionID", "instanceID"), ("id", "volumeID")),
    "evs": (("regionID", "volumeID"), ("id", "instanceID", "system")),
}


class Cloud:
    def __init__(self, config, ak, sk, transport=None):
        self.transport = transport
        self.ak, self.sk = ak, sk
        lock_file = os.environ.get("CATS_HPC_MUTATION_LOCK_FILE") or config.get(
            "mutation_lock_file", os.path.join(tempfile.gettempdir(), "catsco-hpc-task-worker.mutation"),
        )
        self.mutation_gate = MutationGate(
            lock_file,
            config.get("mutation_lock_timeout_seconds", 5),
            config.get("mutation_cooldown_seconds", 0.2),
        )
        self.provision_lease_file = os.environ.get("CATS_HPC_PROVISION_LEASE_FILE") or config.get(
            "provision_lease_file", lock_file + ".provision",
        )
        self.provision_lease_ttl = config.get("provision_lease_ttl_seconds", 900)
        self.hosts = {service: https_base(config["hosts"][service]) for service in ("hpc", "ecs", "evs")}
        self.scope = {key: identifier(config[key]) for key in SCOPE}
        self.role = identifier(config["compute_role_uuid"])
        self.node_values = dict(role=1, kind="VM", billing=True, creation="manual")
        self.disk_type, self.disk_size = (config[key] for key in ("system_disk_type", "system_disk_size_gb"))
        self.subnet = identifier(config["subnet"])
        if self.disk_type not in ("SATA", "SAS", "SSD", "SSD-genric") or type(self.disk_size) is not int or not 100 <= self.disk_size <= 2048:
            raise ValueError("invalid CTYun system disk configuration")
        self.default_value = config["default_queue_value"]
        self.nondefault_value = config["nondefault_queue_value"]
        if self.default_value == self.nondefault_value:
            raise ValueError("default and nondefault queue values must be distinct")
        self.skus = config["skus"]
        if not self.skus:
            raise ValueError("at least one SKU is required")
        for sku in self.skus.values():
            identifier(sku["imageUUID"])
            identifier(sku["specificationUUID"])
            identifier(sku["specificationName"])
        self.probe_job_id = identifier(config["probe_job_uuid"])
        self.pending = set(config["pending_job_statuses"])
        self.api = {}
        for name, (params, fields) in API_FIELDS.items():
            operation = dict(config["api"][name])
            if name in ("create_queue", "job"):
                operation.update(CREATE if name == "create_queue" else JOB)
            elif name == "expand":
                operation.update(path="/v4/cthpc/hpc/expand-cluster", method="POST", params={})
            self.validate_binding(name, operation, params, fields)
            self.api[name] = operation

    @staticmethod
    def validate_binding(name, operation, params, fields):
        if not re.fullmatch(r"/[A-Za-z0-9/_-]+", operation["path"]) or operation["method"] not in ("GET", "POST"):
            raise ValueError(f"invalid API path or method for {name}")
        if name in ("expand", "shrink", "delete_queue") and operation["method"] != "POST":
            raise ValueError(f"{name} must use POST")
        mapped = operation["params"]
        collection = name.startswith("list_") or (name in ("ecs", "evs") and "items" in operation)
        if collection and "items" not in operation:
            raise ValueError(f"missing collection selector for {name}")
        paged = collection and not operation.get("unpaged", False)
        optional = set(SCOPE) | ({"queueUUID"} if name == "shrink" else set())
        required = set(params) | ({"page", "page_size"} if paged else set())
        if set(mapped) - (required | optional) or required - set(mapped) or set(fields) - set(operation["fields"]):
            raise ValueError(f"incomplete API binding for {name}")
        paths = list(mapped.values())
        for path in paths:
            if not re.fullmatch(r"[A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*", path):
                raise ValueError(f"invalid request field for {name}")
            if paths.count(path) != 1 or any(other.startswith(path + ".") for other in paths):
                raise ValueError(f"overlapping request fields for {name}")
            if operation["method"] == "GET" and "." in path:
                raise ValueError(f"nested query field for {name}")
        if paged and ("items" not in operation or "total" not in operation or operation.get("first_page") not in (0, 1)):
            raise ValueError(f"missing pagination for {name}")
        if name in ("ecs", "evs") and not collection and not operation.get("absent_codes"):
            raise ValueError(f"missing authoritative absence codes for {name}")
        if (operation.get("absent_codes") or operation.get("rejected_codes")) and not operation.get("error_code"):
            raise ValueError(f"missing error code selector for {name}")

    def request(self, name, context, request_id=None, before_send=None):
        operation = self.api[name]
        payload = project(self.scope | context, operation)
        if name == "expand":
            payload = dict(self.scope, clusterTypeRoleUUID=self.role, nodeCreateMethod="AutoCreate", onDemand=True,
                           ecsComputeNodeOrders=[dict(orderCount=context["count"], queueUUID=context["queueUUID"],
                               imageUUID=context["imageUUID"], specificationUUID=context["specificationUUID"],
                               subnetID=self.subnet, systemDiskType=self.disk_type, systemDiskSize=self.disk_size)])
        method = operation["method"]
        service = name if name in ("ecs", "evs") else "hpc"
        body = json.dumps(payload, separators=(",", ":")) if method == "POST" else ""
        query = query_string(payload) if method == "GET" else ""
        url = self.hosts[service] + operation["path"] + ("?" + query if query else "")
        gate = self.mutation_gate if request_id is not None else contextlib.nullcontext()
        with gate:
            if before_send is not None:
                before_send()
            date = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
            headers = signed_headers(self.ak, self.sk, query, body, request_id or str(uuid.uuid4()), date)
            if self.transport is None:
                timeout = CTYUN_MUTATION_TIMEOUT if request_id is not None else IO_TIMEOUT
                status, raw = http_request(method, url, headers, body or None, timeout=timeout)
            else:
                status, raw = self.transport(method, url, headers, body or None)
        if status != 200:
            raise WorkerError(f"{name} returned HTTP {status}")
        try:
            reply = json.loads(raw)
        except ValueError:
            raise WorkerError(f"{name} returned invalid JSON") from None
        if not isinstance(reply, dict):
            raise WorkerError(f"{name} returned an unexpected response")
        if reply.get("statusCode") == 800 or reply.get("statusCode") == "800":
            return reply
        if operation.get("error_code"):
            code = str(select(reply, operation["error_code"]))
            if code in operation.get("absent_codes", ()):
                return None
            if code in operation.get("rejected_codes", ()):
                raise Rejected(f"{name} explicitly rejected the request")
        raise WorkerError(f"{name} provider response did not confirm success")

    def record(self, name, value):
        record = {}
        for key, path in self.api[name]["fields"].items():
            try:
                record[key] = select(value, path)
            except WorkerError:
                if key != "masterOrderID" and (name != "list_nodes" or key in ("nodeUUID", "queueName")):
                    raise
                record[key] = None
        return record

    def pages(self, name, context, call):
        operation = self.api[name]
        if operation.get("unpaged", False):
            reply = call(self.request, name, context)
            items = select(reply, operation["items"])
            if not isinstance(items, list):
                raise WorkerError(f"{name} returned an unverified list")
            return [self.record(name, row) for row in items]
        page_size = 20
        rows = []
        total = None
        for page in range(operation["first_page"], operation["first_page"] + 20):
            reply = call(self.request, name, context | {"page": page, "page_size": page_size})
            if reply is None:
                raise WorkerError(f"{name} returned an unverified list")
            count = select(reply, operation["total"])
            items = select(reply, operation["items"])
            if type(count) is not int or not 0 <= count <= 400 or not isinstance(items, list):
                raise WorkerError(f"{name} returned invalid pagination")
            if total is None:
                total = count
            if count != total or len(items) != min(page_size, total - len(rows)):
                raise WorkerError(f"{name} changed or returned an incomplete page")
            rows.extend(self.record(name, row) for row in items)
            if len(rows) == total:
                return rows
        raise WorkerError(f"{name} exceeded the pagination limit")

    def outcome(self, name, call, request_id, *, before_send=None, **context):
        reply = call(self.request, name, context, request_id, before_send)
        result = self.record(name, reply)
        identifier(result["jobUUID"])
        if result.get("masterOrderID"):
            try:
                identifier(result["masterOrderID"])
            except WorkerError:
                result.pop("masterOrderID")
        return result

    def status(self, job_uuid, call):
        reply = call(self.request, "job", {"jobUUID": identifier(job_uuid)})
        record = self.record("job", reply)
        if "jobUUID" in record and record["jobUUID"] != job_uuid:
            raise WorkerError("cloud job response does not match the queried UUID")
        status = record["status"]
        if not isinstance(status, str) or not status.strip():
            raise WorkerError("cloud job response is missing its status")
        return status.strip().lower()

    def by_id(self, name, identity, call):
        identity = identifier(identity)
        operation = self.api[name]
        context = {"instanceID" if name == "ecs" else "volumeID": identity}
        if "items" in operation:
            rows = self.pages(name, context, call)
            if not rows:
                return None
            if len(rows) == 1 and rows[0]["id"] == identity:
                return rows[0]
        else:
            reply = call(self.request, name, context)
            if reply is None:
                return None
            row = self.record(name, reply)
            if row["id"] == identity:
                return row
        raise WorkerError(f"{name} returned an unexpected resource identity")


if __name__ == "__main__" and sys.argv[1:] == ["--http"]:
    sys.exit(http_child())
