"""CTYun EOP transport and explicit capacity API bindings."""

import base64
import hashlib
import hmac
import http.client
import json
import re
import subprocess
import sys
import uuid
from datetime import datetime, timezone
from urllib.parse import quote, urlsplit


IO_TIMEOUT = 8
MAX_RESPONSE = 1024 * 1024


class WorkerError(Exception):
    pass


class Rejected(WorkerError):
    """A configured provider code explicitly guarantees no operation was accepted."""


def http_child():
    request = json.load(sys.stdin)
    url = urlsplit(request["url"])
    target = url.path + ("?" + url.query if url.query else "")
    connection = http.client.HTTPSConnection(url.hostname, url.port, timeout=IO_TIMEOUT)
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


def http_request(method, url, headers, body=None):
    # Bound the entire HTTP exchange, including DNS, TLS and slow response bodies.
    request = json.dumps(dict(method=method, url=url, headers=headers, body=body))
    try:
        result = subprocess.run(
            [sys.executable, "-I", "-B", __file__, "--http"], input=request,
            capture_output=True, text=True, encoding="utf-8", timeout=IO_TIMEOUT, check=True,
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
    def __init__(self, config, ak, sk, transport=http_request):
        self.transport = transport
        self.ak, self.sk = ak, sk
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

    def request(self, name, context, request_id=None):
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
        date = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        headers = signed_headers(self.ak, self.sk, query, body, request_id or str(uuid.uuid4()), date)
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

    def outcome(self, name, call, request_id, **context):
        reply = call(self.request, name, context, request_id)
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
