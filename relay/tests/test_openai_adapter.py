from __future__ import annotations

import importlib.util
import io
import json
import sys
import threading
import time
import types
import unittest
from http import HTTPStatus
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.request import urlopen


MODULE_PATH = Path(__file__).resolve().parents[1] / "openai_adapter.py"
SPEC = importlib.util.spec_from_file_location("catsco_openai_adapter", MODULE_PATH)
assert SPEC and SPEC.loader
adapter = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = adapter
SPEC.loader.exec_module(adapter)


class HandlerHarness:
    def __init__(self, providers: list[str], responses: dict[str, tuple[int, dict[str, str], bytes]]):
        self.providers = providers
        self.responses = responses
        self.attempts: list[str] = []
        self.sent: list[tuple[int, bytes, dict[str, str]]] = []
        self.usage: list[dict] = []

        handler = object.__new__(adapter.Handler)
        handler.headers = {"Authorization": "Bearer test"}
        handler.path = "/v1/chat/completions"
        handler.routed_provider_candidates = lambda model, api_key: (list(self.providers), 1)
        handler.call_relay_admin = self.call_relay_admin
        handler.call_bifrost = self.call_bifrost
        handler.open_bifrost_stream = self.open_bifrost_stream
        handler.forward_responses_stream = self.forward_responses_stream
        handler.submit_relay_usage = self.submit_relay_usage
        handler.send_bytes = self.send_bytes
        handler.send_json = self.send_json
        self.handler = handler

    @staticmethod
    def call_relay_admin(path: str, body: dict, *, timeout: float | None = None):
        return HTTPStatus.OK, {"uid": 42}

    def call_bifrost(self, body: dict, *, path: str, extra_headers: dict[str, str]):
        provider = extra_headers["x-model-provider"]
        self.attempts.append(provider)
        return self.responses[provider]

    def open_bifrost_stream(self, body: dict, *, path: str, extra_headers: dict[str, str]):
        status, headers, payload = self.call_bifrost(
            body,
            path=path,
            extra_headers=extra_headers,
        )
        return status, headers, io.BytesIO(payload)

    def forward_responses_stream(self, status: int, headers: dict[str, str], upstream):
        payload = upstream.read()
        observer = adapter.ResponsesSSEObserver()
        observer.feed(payload)
        observer.finish()
        upstream.close()
        self.send_bytes(status, payload, {"Content-Type": "text/event-stream; charset=utf-8"})
        return observer.outcome(), False, None

    def submit_relay_usage(self, payload: dict, *, context: str):
        self.usage.append({**payload, "context": context})

    def send_bytes(self, status: int, payload: bytes, headers: dict[str, str]):
        self.sent.append((int(status), payload, dict(headers)))

    def send_json(self, status: int, payload: dict):
        self.send_bytes(status, adapter.json_bytes(payload), {"Content-Type": "application/json"})


def error_response(status: int, code: str) -> tuple[int, dict[str, str], bytes]:
    return (
        status,
        {"Content-Type": "application/json"},
        adapter.json_bytes(adapter.openai_error("upstream rejected request", "api_error", code)),
    )


def success_response() -> tuple[int, dict[str, str], bytes]:
    return (
        HTTPStatus.OK,
        {"Content-Type": "application/json"},
        adapter.json_bytes(
            {
                "id": "chatcmpl-test",
                "object": "chat.completion",
                "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 1},
            }
        ),
    )


def successful_responses_response() -> tuple[int, dict[str, str], bytes]:
    return (
        HTTPStatus.OK,
        {"Content-Type": "application/json"},
        adapter.json_bytes(
            {
                "id": "resp-test",
                "object": "response",
                "status": "completed",
                "output": [],
                "usage": {"input_tokens": 10, "output_tokens": 1},
            }
        ),
    )


def responses_sse(event_type: str, body: dict) -> tuple[int, dict[str, str], bytes]:
    payload = f"event: {event_type}\ndata: {json.dumps(body)}\n\n".encode()
    return HTTPStatus.OK, {"Content-Type": "text/event-stream"}, payload


class ProviderCircuitRecoveryTest(unittest.TestCase):
    model = "relay-test-model"

    def setUp(self):
        self.original_pools = adapter.MODEL_PROVIDER_POOLS
        adapter.MODEL_PROVIDER_POOLS = {self.model: ("provider-a", "provider-b", "provider-c", "provider-d")}
        adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL.clear()
        adapter.PROVIDER_POOL_FAILURE_GENERATION.clear()
        adapter.PROVIDER_POOL_CONSECUTIVE_FAILURES.clear()
        adapter.PROVIDER_POOL_HALF_OPEN_INFLIGHT.clear()
        adapter.PROVIDER_POOL_LAST_PROBE_AT.clear()
        adapter.PROVIDER_POOL_ACTIVE.clear()

    def tearDown(self):
        adapter.MODEL_PROVIDER_POOLS = self.original_pools

    def run_chat(self, harness: HandlerHarness):
        harness.handler.handle_chat_completion(
            {"model": self.model, "messages": [{"role": "user", "content": "hello"}]}
        )

    def run_responses(self, harness: HandlerHarness):
        harness.handler.path = "/v1/responses"
        harness.handler.handle_responses(
            {"model": self.model, "input": [{"role": "user", "content": "hello"}]}
        )

    def test_unknown_upstream_403_opens_circuit_and_fails_over(self):
        harness = HandlerHarness(
            ["provider-a", "provider-b"],
            {
                "provider-a": error_response(HTTPStatus.FORBIDDEN, "http_403"),
                "provider-b": success_response(),
            },
        )

        self.run_chat(harness)

        self.assertEqual(harness.attempts, ["provider-a", "provider-b"])
        self.assertEqual(harness.sent[-1][0], HTTPStatus.OK)
        self.assertGreater(adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL["provider-a"], time.monotonic())

    def test_all_provider_403_errors_become_retryable_503(self):
        providers = ["provider-a", "provider-b", "provider-c", "provider-d"]
        harness = HandlerHarness(
            providers,
            {provider: error_response(HTTPStatus.FORBIDDEN, "http_403") for provider in providers},
        )

        self.run_chat(harness)

        self.assertEqual(harness.attempts, providers)
        status, payload, headers = harness.sent[-1]
        self.assertEqual(status, HTTPStatus.SERVICE_UNAVAILABLE)
        self.assertIn("Retry-After", headers)
        self.assertLessEqual(int(headers["Retry-After"]), 4)
        parsed = json.loads(payload)
        self.assertEqual(parsed["error"]["code"], "provider_pool_unavailable")
        self.assertNotIn(b"http_403", payload)
        self.assertTrue(any(item["error_code"] == "provider_pool_unavailable" for item in harness.usage))

    def test_responses_403_fails_over_to_a_healthy_provider(self):
        harness = HandlerHarness(
            ["provider-a", "provider-b"],
            {
                "provider-a": error_response(HTTPStatus.FORBIDDEN, "http_403"),
                "provider-b": successful_responses_response(),
            },
        )

        self.run_responses(harness)

        self.assertEqual(harness.attempts, ["provider-a", "provider-b"])
        self.assertEqual(harness.sent[-1][0], HTTPStatus.OK)
        self.assertGreater(adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL["provider-a"], time.monotonic())

    def test_streaming_responses_403_fails_over_before_downstream_commit(self):
        harness = HandlerHarness(
            ["provider-a", "provider-b"],
            {
                "provider-a": error_response(HTTPStatus.FORBIDDEN, "http_403"),
                "provider-b": responses_sse(
                    "response.completed",
                    {"type": "response.completed", "response": {"status": "completed"}},
                ),
            },
        )
        harness.handler.path = "/v1/responses"

        harness.handler.handle_responses(
            {"model": self.model, "stream": True, "input": [{"role": "user", "content": "hello"}]}
        )

        self.assertEqual(harness.attempts, ["provider-a", "provider-b"])
        self.assertEqual(len(harness.sent), 1)
        self.assertEqual(harness.sent[0][0], HTTPStatus.OK)
        self.assertIn(b"response.completed", harness.sent[0][1])
        self.assertNotIn(b"http_403", harness.sent[0][1])

    def test_responses_exhausted_pool_hides_the_last_provider_403(self):
        providers = ["provider-a", "provider-b", "provider-c", "provider-d"]
        harness = HandlerHarness(
            providers,
            {provider: error_response(HTTPStatus.FORBIDDEN, "http_403") for provider in providers},
        )

        self.run_responses(harness)

        self.assertEqual(harness.attempts, providers)
        status, payload, headers = harness.sent[-1]
        self.assertEqual(status, HTTPStatus.SERVICE_UNAVAILABLE)
        self.assertIn("Retry-After", headers)
        self.assertEqual(json.loads(payload)["error"]["code"], "provider_pool_unavailable")

    def test_incomplete_responses_stream_is_forwarded_and_not_spliced_after_commit(self):
        harness = HandlerHarness(
            ["provider-a", "provider-b"],
            {
                "provider-a": responses_sse(
                    "response.incomplete",
                    {"type": "response.incomplete", "response": {"status": "incomplete"}},
                ),
                "provider-b": responses_sse(
                    "response.completed",
                    {"type": "response.completed", "response": {"status": "completed"}},
                ),
            },
        )
        harness.handler.path = "/v1/responses"

        harness.handler.handle_responses(
            {"model": self.model, "stream": True, "input": [{"role": "user", "content": "hello"}]}
        )

        self.assertEqual(harness.attempts, ["provider-a"])
        self.assertEqual(len(harness.sent), 1)
        self.assertEqual(harness.sent[0][0], HTTPStatus.OK)
        self.assertIn(b"response.incomplete", harness.sent[0][1])
        self.assertNotIn("provider-a", adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL)

    def test_empty_success_stream_is_committed_once_and_not_spliced(self):
        harness = HandlerHarness(
            ["provider-a", "provider-b"],
            {
                "provider-a": (HTTPStatus.OK, {"Content-Type": "text/event-stream"}, b""),
                "provider-b": responses_sse(
                    "response.completed",
                    {"type": "response.completed", "response": {"status": "completed"}},
                ),
            },
        )
        harness.handler.path = "/v1/responses"

        harness.handler.handle_responses(
            {"model": self.model, "stream": True, "input": [{"role": "user", "content": "hello"}]}
        )

        self.assertEqual(harness.attempts, ["provider-a"])
        self.assertEqual(len(harness.sent), 1)
        self.assertEqual(harness.sent[0][1], b"")

    def test_committed_upstream_stream_error_is_recorded_without_failover(self):
        harness = HandlerHarness(
            ["provider-a", "provider-b"],
            {
                "provider-a": responses_sse(
                    "response.created",
                    {"type": "response.created", "response": {"status": "in_progress"}},
                ),
                "provider-b": responses_sse(
                    "response.completed",
                    {"type": "response.completed", "response": {"status": "completed"}},
                ),
            },
        )
        harness.handler.path = "/v1/responses"
        harness.handler.forward_responses_stream = lambda status, headers, upstream: (
            {
                "valid_payload": False,
                "succeeded": False,
                "usage": {},
                "terminal": "",
                "error_code": "missing_terminal_event",
            },
            False,
            "TimeoutError",
        )

        harness.handler.handle_responses(
            {"model": self.model, "stream": True, "input": [{"role": "user", "content": "hello"}]}
        )

        self.assertEqual(harness.attempts, ["provider-a"])
        self.assertTrue(any(item["error_code"] == "upstream_stream_error" for item in harness.usage))
        self.assertEqual(adapter.PROVIDER_POOL_CONSECUTIVE_FAILURES["provider-a"], 1)
        self.assertNotIn("provider-a", adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL)

    def test_committed_downstream_disconnect_does_not_penalize_provider(self):
        harness = HandlerHarness(
            ["provider-a", "provider-b"],
            {
                "provider-a": responses_sse(
                    "response.created",
                    {"type": "response.created", "response": {"status": "in_progress"}},
                ),
                "provider-b": responses_sse(
                    "response.completed",
                    {"type": "response.completed", "response": {"status": "completed"}},
                ),
            },
        )
        harness.handler.path = "/v1/responses"
        harness.handler.forward_responses_stream = lambda status, headers, upstream: (
            {
                "valid_payload": False,
                "succeeded": False,
                "usage": {},
                "terminal": "",
                "error_code": "missing_terminal_event",
            },
            True,
            None,
        )

        harness.handler.handle_responses(
            {"model": self.model, "stream": True, "input": [{"role": "user", "content": "hello"}]}
        )

        self.assertEqual(harness.attempts, ["provider-a"])
        self.assertTrue(any(item["status"] == "client_disconnected" for item in harness.usage))
        self.assertNotIn("provider-a", adapter.PROVIDER_POOL_CONSECUTIVE_FAILURES)
        self.assertNotIn("provider-a", adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL)


    def test_explicit_content_policy_error_does_not_switch_or_open_circuit(self):
        harness = HandlerHarness(
            ["provider-a", "provider-b"],
            {
                "provider-a": error_response(HTTPStatus.FORBIDDEN, "content_policy_violation"),
                "provider-b": success_response(),
            },
        )

        self.run_chat(harness)

        self.assertEqual(harness.attempts, ["provider-a"])
        self.assertEqual(harness.sent[-1][0], HTTPStatus.FORBIDDEN)
        self.assertNotIn("provider-a", adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL)

    def test_preflight_user_error_is_terminal(self):
        harness = HandlerHarness(
            ["provider-a", "provider-b"],
            {"provider-a": success_response(), "provider-b": success_response()},
        )
        harness.handler.call_relay_admin = lambda path, body, *, timeout=None: (
            HTTPStatus.PAYMENT_REQUIRED,
            {"error": "relay budget exceeded"},
        )

        self.run_chat(harness)

        self.assertEqual(harness.attempts, [])
        self.assertEqual(harness.sent[-1][0], HTTPStatus.PAYMENT_REQUIRED)
        self.assertEqual(adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL, {})

    def test_transient_5xx_requires_two_failures_before_opening(self):
        for failure_number in (1, 2):
            adapter.record_provider_pool_result(
                self.model,
                "provider-a",
                available=False,
                now=100.0 + failure_number,
                cooldown_seconds=30,
                failure_threshold=2,
                error_fingerprint=adapter.provider_error_fingerprint(503, "upstream_error"),
            )
            if failure_number == 1:
                self.assertNotIn("provider-a", adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL)

        self.assertEqual(adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL["provider-a"], 132.0)

    def test_retry_after_and_recovery_wait_are_bounded(self):
        self.assertEqual(
            adapter.provider_cooldown_seconds(
                {"Retry-After": "17"}, status=HTTPStatus.TOO_MANY_REQUESTS, error_code="rate_limit"
            ),
            17,
        )
        self.assertEqual(
            adapter.provider_cooldown_seconds(
                {}, status=HTTPStatus.TOO_MANY_REQUESTS, error_code="rate_limit"
            ),
            adapter.PROVIDER_POOL_RATE_LIMIT_COOLDOWN_SECONDS,
        )
        self.assertTrue(
            adapter.provider_failure_opens_circuit(
                HTTPStatus.TOO_MANY_REQUESTS,
                valid_payload=False,
                payload=b"",
                error_code="rate_limit",
            )
        )
        self.assertLessEqual(adapter.PROVIDER_POOL_RECOVERY_WAIT_SECONDS, 5.0)

    def test_all_cooling_pool_allows_only_one_coordinated_probe(self):
        for provider in adapter.MODEL_PROVIDER_POOLS[self.model]:
            adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL[provider] = 100.0

        leases = [
            adapter.acquire_provider_pool_lease(self.model, provider, now=50.0, deadline=54.0)
            for provider in adapter.MODEL_PROVIDER_POOLS[self.model]
        ]

        acquired = [lease for lease in leases if lease is not None]
        self.assertEqual(len(acquired), 1)
        self.assertTrue(acquired[0].half_open_probe)
        adapter.abandon_provider_pool_lease(acquired[0])

    def test_affinity_stays_first_until_the_provider_is_cooling(self):
        candidates = adapter.provider_candidates_for_model(
            self.model,
            preferred_provider="provider-b",
            include_unavailable_fallbacks=True,
            now=50.0,
        )
        self.assertEqual(candidates[0], "provider-b")

        adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL["provider-b"] = 100.0
        candidates = adapter.provider_candidates_for_model(
            self.model,
            preferred_provider="provider-b",
            include_unavailable_fallbacks=True,
            now=50.0,
        )
        self.assertNotEqual(candidates[0], "provider-b")
        self.assertEqual(candidates[-1], "provider-b")

    def test_successful_half_open_probe_closes_the_circuit(self):
        adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL["provider-a"] = 100.0
        lease = adapter.acquire_provider_pool_lease(
            self.model,
            "provider-a",
            now=101.0,
            deadline=105.0,
        )
        self.assertIsNotNone(lease)
        assert lease is not None
        self.assertTrue(lease.half_open_probe)

        adapter.release_provider_pool_lease(lease)
        adapter.record_provider_pool_result(
            self.model,
            "provider-a",
            available=True,
            lease=lease,
        )

        self.assertNotIn("provider-a", adapter.PROVIDER_POOL_UNAVAILABLE_UNTIL)
        self.assertNotIn("provider-a", adapter.PROVIDER_POOL_HALF_OPEN_INFLIGHT)


class ResponsesSSEObserverTest(unittest.TestCase):
    def test_tracks_terminal_event_and_usage_across_arbitrary_chunks(self):
        payload = (
            b'event: response.created\r\ndata: {"type":"response.created"}\r\n\r\n'
            b'event: response.output_text.delta\r\ndata: {"type":"response.output_text.delta","delta":"ok"}\r\n\r\n'
            b'event: response.completed\r\ndata: {"type":"response.completed","response":'
            b'{"status":"completed","usage":{"input_tokens":10,"output_tokens":2}}}\r\n\r\n'
        )
        observer = adapter.ResponsesSSEObserver()
        for index in range(0, len(payload), 7):
            observer.feed(payload[index : index + 7])
        observer.finish()

        outcome = observer.outcome()
        self.assertTrue(outcome["succeeded"])
        self.assertEqual(outcome["terminal"], "response.completed")
        self.assertEqual(outcome["usage"], {"input_tokens": 10, "output_tokens": 2})

    def test_reports_missing_terminal_without_inventing_an_event(self):
        observer = adapter.ResponsesSSEObserver()
        observer.feed(b'data: {"type":"response.output_text.delta","delta":"partial"}\n\n')
        observer.finish()

        outcome = observer.outcome()
        self.assertFalse(outcome["valid_payload"])
        self.assertEqual(outcome["error_code"], "missing_terminal_event")


class ResponsesHTTPStreamingTest(unittest.TestCase):
    def test_sends_headers_before_the_first_event_and_preserves_sse_bytes(self):
        release_event = threading.Event()
        payload = responses_sse(
            "response.completed",
            {"type": "response.completed", "response": {"status": "completed"}},
        )[2]

        class DelayedStream(io.BytesIO):
            def read1(self, size: int = -1) -> bytes:
                release_event.wait(timeout=2)
                return self.read(size)

        class StreamingHandler(adapter.Handler):
            def do_GET(self):
                self.forward_responses_stream(
                    HTTPStatus.OK,
                    {"Content-Type": "text/event-stream", "x-request-id": "req-test"},
                    DelayedStream(payload),
                )

            def log_message(self, format: str, *args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), StreamingHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            started = time.monotonic()
            response = urlopen(f"http://127.0.0.1:{server.server_port}/", timeout=2)
            self.assertLess(time.monotonic() - started, 1)
            self.assertEqual(response.headers.get_content_type(), "text/event-stream")
            self.assertEqual(response.headers.get("X-Accel-Buffering"), "no")
            self.assertEqual(response.headers.get("x-request-id"), "req-test")
            release_event.set()
            self.assertEqual(response.read(), payload)
            response.close()
        finally:
            release_event.set()
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_downstream_abort_closes_upstream_without_reporting_provider_failure(self):
        class TrackingStream(io.BytesIO):
            closed_by_relay = False

            def close(self):
                self.closed_by_relay = True
                super().close()

        class AbortedWriter:
            def flush(self):
                return

            def write(self, chunk: bytes):
                raise ConnectionAbortedError("downstream closed")

        upstream = TrackingStream(b'data: {"type":"response.created"}\n\n')
        handler = object.__new__(adapter.Handler)
        handler.wfile = AbortedWriter()
        handler.send_response = lambda status: None
        handler.send_header = lambda name, value: None
        handler.end_headers = lambda: None

        outcome, disconnected, upstream_error = handler.forward_responses_stream(
            HTTPStatus.OK,
            {"Content-Type": "text/event-stream"},
            upstream,
        )

        self.assertTrue(disconnected)
        self.assertIsNone(upstream_error)
        self.assertTrue(upstream.closed_by_relay)
        self.assertEqual(outcome["error_code"], "missing_terminal_event")

    def test_upstream_read_error_is_distinct_from_downstream_disconnect(self):
        class BrokenStream(io.BytesIO):
            closed_by_relay = False

            def read1(self, size: int = -1) -> bytes:
                raise TimeoutError("upstream stalled")

            def close(self):
                self.closed_by_relay = True
                super().close()

        class Writer(io.BytesIO):
            def flush(self):
                return

        upstream = BrokenStream()
        handler = object.__new__(adapter.Handler)
        handler.wfile = Writer()
        handler.send_response = lambda status: None
        handler.send_header = lambda name, value: None
        handler.end_headers = lambda: None

        outcome, disconnected, upstream_error = handler.forward_responses_stream(
            HTTPStatus.OK,
            {"Content-Type": "text/event-stream"},
            upstream,
        )

        self.assertFalse(disconnected)
        self.assertEqual(upstream_error, "TimeoutError")
        self.assertTrue(upstream.closed_by_relay)
        self.assertEqual(outcome["error_code"], "missing_terminal_event")


if __name__ == "__main__":
    unittest.main()
