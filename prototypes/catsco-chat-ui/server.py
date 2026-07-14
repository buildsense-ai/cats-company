# -*- coding: utf-8 -*-
"""CatsCo local chat server.

Serves the static UI, persists local sessions, and proxies streaming chat
requests to an OpenAI-compatible upstream endpoint.
"""

import json
import mimetypes
import os
import time
import uuid
from collections import deque
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Lock
from urllib.error import HTTPError, URLError
from urllib.parse import unquote
from urllib.request import Request, urlopen


BASE_DIR = Path(__file__).resolve().parent
DATA_FILE = BASE_DIR / "sessions.json"


def load_env(path):
    if not path.exists():
        return
    for raw_line in path.read_text(encoding="utf-8-sig").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        os.environ[key.strip()] = value.strip()


load_env(BASE_DIR / ".env")

API_BASE = os.environ.get("API_BASE", "https://api.openai.com/v1").rstrip("/")
API_KEY = os.environ.get("API_KEY", "")
MODEL = os.environ.get("MODEL", "MiniMax-M3")
AVAILABLE_MODELS = [item.strip() for item in os.environ.get("AVAILABLE_MODELS", MODEL).split(",") if item.strip()]
COMPANY_API_BASE = os.environ.get("COMPANY_API_BASE", "https://app.catsco.cc").rstrip("/")
HOST = os.environ.get("CHAT_HOST", "127.0.0.1")
PORT = int(os.environ.get("CHAT_PORT", "5000"))
MAX_TOKENS = int(os.environ.get("MAX_TOKENS", "4096"))
HISTORY_LIMIT = max(10, int(os.environ.get("HISTORY_LIMIT", "200")))
SESSION_LIMIT = max(10, int(os.environ.get("SESSION_LIMIT", "100")))
SYSTEM_PROMPT = os.environ.get(
    "SYSTEM_PROMPT",
    "你是一个友好、专业的 AI 助手，名字叫 CatsCo。回答清晰、有条理，必要时使用代码块。",
).strip()

_sessions = {}
_session_order = deque()
_lock = Lock()


def _session_payload():
    return {
        "sessions": [
            {"id": sid, **_sessions[sid]}
            for sid in _session_order
            if sid in _sessions
        ]
    }


def _persist():
    try:
        with _lock:
            payload = _session_payload()
        temp_file = DATA_FILE.with_suffix(".json.tmp")
        temp_file.write_text(
            json.dumps(payload, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        temp_file.replace(DATA_FILE)
    except OSError as error:
        print(f"[保存会话失败] {error}")


def _load_persisted():
    if not DATA_FILE.exists():
        return
    try:
        data = json.loads(DATA_FILE.read_text(encoding="utf-8-sig"))
        items = data.get("sessions", [])[-SESSION_LIMIT:]
        with _lock:
            for item in items:
                sid = str(item.get("id") or "").strip()
                if not sid:
                    continue
                _sessions[sid] = {
                    "messages": item.get("messages", []),
                    "title": item.get("title") or "新任务",
                    "createdAt": item.get("createdAt", time.time()),
                }
                _session_order.append(sid)
    except (OSError, ValueError, TypeError) as error:
        print(f"[加载会话失败] {error}")


def _new_session():
    sid = uuid.uuid4().hex[:16]
    with _lock:
        while len(_session_order) >= SESSION_LIMIT:
            expired = _session_order.popleft()
            _sessions.pop(expired, None)
        _sessions[sid] = {
            "messages": [],
            "title": "新任务",
            "createdAt": time.time(),
        }
        _session_order.append(sid)
    _persist()
    return sid


def _get_session(sid):
    if not sid:
        return None
    with _lock:
        return _sessions.get(sid)


def _trim_history(messages):
    if len(messages) <= HISTORY_LIMIT:
        return messages
    return messages[-HISTORY_LIMIT:]


_load_persisted()


class CatsCoHTTPServer(ThreadingHTTPServer):
    allow_reuse_address = True
    daemon_threads = True


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, _format, *_args):
        return

    def _common_headers(self, content_type, length=None, cache="no-store"):
        self.send_header("Content-Type", content_type)
        if length is not None:
            self.send_header("Content-Length", str(length))
        self.send_header("Cache-Control", cache)
        self.send_header("Access-Control-Allow-Origin", "*")

    def _send_json(self, data, status=200):
        body = json.dumps(data, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self._common_headers("application/json; charset=utf-8", len(body))
        self.end_headers()
        try:
            self.wfile.write(body)
        except OSError:
            pass

    def _send_sse_start(self):
        self.send_response(200)
        self._common_headers("text/event-stream; charset=utf-8")
        self.send_header("Connection", "close")
        self.send_header("X-Accel-Buffering", "no")
        self.end_headers()

    def _sse_event(self, data):
        payload = json.dumps(data, ensure_ascii=False)
        try:
            self.wfile.write(f"data: {payload}\n\n".encode("utf-8"))
            self.wfile.flush()
            return True
        except OSError:
            return False

    def _serve_file(self, target):
        try:
            data = target.read_bytes()
        except OSError:
            self.send_error(404)
            return
        content_type = mimetypes.guess_type(str(target))[0] or "application/octet-stream"
        if target.suffix == ".js":
            content_type = "application/javascript; charset=utf-8"
        elif target.suffix == ".css":
            content_type = "text/css; charset=utf-8"
        self.send_response(200)
        self._common_headers(content_type, len(data), "no-cache")
        self.end_headers()
        try:
            self.wfile.write(data)
        except OSError:
            pass

    def _proxy_company(self, method):
        upstream_path = self.path.removeprefix("/company")
        if not upstream_path.startswith("/"):
            upstream_path = "/" + upstream_path
        upstream_url = COMPANY_API_BASE + upstream_path
        length = int(self.headers.get("Content-Length", "0") or 0)
        body = self.rfile.read(length) if length > 0 else None
        headers = {"Accept": self.headers.get("Accept", "application/json")}
        for name in ("Authorization", "Content-Type"):
            value = self.headers.get(name)
            if value:
                headers[name] = value
        request = Request(upstream_url, data=body, headers=headers, method=method)
        try:
            with urlopen(request, timeout=30) as response:
                payload = response.read()
                status = response.status
                content_type = response.headers.get("Content-Type", "application/json; charset=utf-8")
        except HTTPError as error:
            payload = error.read()
            status = error.code
            content_type = error.headers.get("Content-Type", "application/json; charset=utf-8")
        except (URLError, TimeoutError, OSError) as error:
            return self._send_json({
                "ok": False,
                "error": "CatsCo company service is unavailable",
                "detail": str(getattr(error, "reason", error)),
            }, 502)

        self.send_response(status)
        self._common_headers(content_type, len(payload))
        self.end_headers()
        try:
            self.wfile.write(payload)
        except OSError:
            pass

    def do_GET(self):
        clean_path = self.path.split("?", 1)[0]
        if clean_path.startswith("/company/"):
            return self._proxy_company("GET")

        if clean_path in ("/", "/index.html"):
            return self._serve_file(BASE_DIR / "index.html")

        if clean_path.startswith(("/app/", "/assets/")):
            relative_path = unquote(clean_path.lstrip("/"))
            try:
                target = (BASE_DIR / relative_path).resolve()
                if os.path.commonpath((str(BASE_DIR.resolve()), str(target))) != str(BASE_DIR.resolve()):
                    self.send_error(403)
                    return
            except (OSError, ValueError):
                self.send_error(404)
                return
            if not target.is_file():
                self.send_error(404)
                return
            return self._serve_file(target)

        if clean_path == "/api/health":
            return self._send_json({
                "ok": bool(API_KEY),
                "model": MODEL,
                "available_models": AVAILABLE_MODELS,
                "api_base": API_BASE,
                "has_key": bool(API_KEY),
                "sessions": len(_sessions),
            })

        if clean_path == "/api/sessions":
            with _lock:
                items = [
                    {
                        "id": sid,
                        "title": _sessions[sid]["title"],
                        "createdAt": _sessions[sid]["createdAt"],
                        "messageCount": len(_sessions[sid]["messages"]),
                    }
                    for sid in _session_order
                    if sid in _sessions
                ]
            return self._send_json({"ok": True, "sessions": items})

        if clean_path.startswith("/api/history/"):
            sid = clean_path.removeprefix("/api/history/")
            session = _get_session(sid)
            if not session:
                return self._send_json({"ok": False, "error": "任务不存在或已经过期"}, 404)
            return self._send_json({"ok": True, "sessionId": sid, **session})

        self.send_error(404)

    def _read_body(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length) if length > 0 else b"{}"
        return json.loads(raw.decode("utf-8"))

    def do_POST(self):
        clean_path = self.path.split("?", 1)[0]
        if clean_path.startswith("/company/"):
            return self._proxy_company("POST")

        try:
            data = self._read_body()
        except (ValueError, UnicodeError) as error:
            return self._send_json({"ok": False, "error": f"请求内容解析失败：{error}"}, 400)

        if clean_path == "/api/new":
            return self._send_json({"ok": True, "sessionId": _new_session()})
        if clean_path == "/api/chat":
            return self._handle_chat(data)

        if clean_path.startswith("/api/rename/"):
            sid = clean_path.removeprefix("/api/rename/")
            if not sid or "/" in sid or len(sid) > 64:
                return self._send_json({"ok": False, "error": "无效的任务编号"}, 400)
            session = _get_session(sid)
            if not session:
                return self._send_json({"ok": False, "error": "任务不存在"}, 404)
            title = str(data.get("title") or "").strip()[:60]
            if title:
                session["title"] = title
                _persist()
            return self._send_json({"ok": True, "title": session["title"]})

        if clean_path.startswith("/api/delete/"):
            sid = clean_path.removeprefix("/api/delete/")
            with _lock:
                _sessions.pop(sid, None)
                try:
                    _session_order.remove(sid)
                except ValueError:
                    pass
            _persist()
            return self._send_json({"ok": True})

        return self._send_json({"ok": False, "error": "未知接口"}, 404)

    def do_DELETE(self):
        clean_path = self.path.split("?", 1)[0]
        if clean_path.startswith("/company/"):
            return self._proxy_company("DELETE")
        return self._send_json({"ok": False, "error": "Not found"}, 404)

    def do_PATCH(self):
        clean_path = self.path.split("?", 1)[0]
        if clean_path.startswith("/company/"):
            return self._proxy_company("PATCH")
        return self._send_json({"ok": False, "error": "Not found"}, 404)

    def do_PUT(self):
        clean_path = self.path.split("?", 1)[0]
        if clean_path.startswith("/company/"):
            return self._proxy_company("PUT")
        return self._send_json({"ok": False, "error": "Not found"}, 404)

    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type, Authorization")
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _handle_chat(self, data):
        if not API_KEY:
            return self._send_json({"ok": False, "error": "服务端尚未配置 API_KEY"}, 500)

        incoming = data.get("messages") or []
        clean_messages = []
        for message in incoming:
            if not isinstance(message, dict) or message.get("role") not in ("user", "assistant", "system"):
                continue
            content = str(message.get("content") or "")
            if content.strip():
                clean_messages.append({"role": message["role"], "content": content})
        if not clean_messages:
            return self._send_json({"ok": False, "error": "消息不能为空"}, 400)

        session_id = str(data.get("sessionId") or "").strip()
        session = _get_session(session_id)
        if session is None:
            session_id = _new_session()
            session = _get_session(session_id)

        first_user = next((m["content"] for m in clean_messages if m["role"] == "user"), "")
        if first_user and session["title"] == "新任务":
            session["title"] = first_user[:24] + ("…" if len(first_user) > 24 else "")

        requested_model = str(data.get("model") or MODEL).strip()
        selected_model = requested_model if requested_model in AVAILABLE_MODELS else MODEL

        request_body = {
            "model": selected_model,
            "messages": [{"role": "system", "content": SYSTEM_PROMPT}] + _trim_history(clean_messages),
            "temperature": 0.7,
            "max_tokens": MAX_TOKENS,
            "stream": True,
        }
        request = Request(
            f"{API_BASE}/chat/completions",
            data=json.dumps(request_body, ensure_ascii=False).encode("utf-8"),
            headers={
                "Authorization": f"Bearer {API_KEY}",
                "Content-Type": "application/json",
                "Accept": "text/event-stream",
            },
            method="POST",
        )

        try:
            upstream = urlopen(request, timeout=180)
        except HTTPError as error:
            try:
                detail = error.read(1000).decode("utf-8", errors="replace")
            except OSError:
                detail = ""
            return self._send_json({"ok": False, "error": f"模型服务返回 {error.code}", "detail": detail}, error.code)
        except (URLError, TimeoutError, OSError) as error:
            return self._send_json({"ok": False, "error": f"连接模型服务失败：{error}"}, 502)

        self._send_sse_start()
        connected = self._sse_event({"sessionId": session_id, "model": selected_model})
        if connected:
            connected = self._sse_event({
                "type": "progress",
                "stage": "connected",
                "detail": "模型服务已连接，正在处理任务",
            })

        reply_parts = []
        first_content_sent = False
        try:
            for raw_line in upstream:
                if not connected:
                    break
                line = raw_line.decode("utf-8", errors="replace").strip()
                if not line.startswith("data:"):
                    continue
                data_text = line[5:].strip()
                if data_text == "[DONE]":
                    break
                try:
                    chunk = json.loads(data_text)
                except ValueError:
                    continue
                choices = chunk.get("choices") or []
                delta = choices[0].get("delta") if choices else None
                content = delta.get("content") if isinstance(delta, dict) else None
                if not content:
                    continue
                if not first_content_sent:
                    first_content_sent = True
                    connected = self._sse_event({
                        "type": "progress",
                        "stage": "first_content",
                        "detail": "已开始接收内容，正在整理回答",
                    })
                    if not connected:
                        break
                reply_parts.append(content)
                connected = self._sse_event({"content": content})
        except OSError:
            connected = False
        finally:
            upstream.close()

        reply = "".join(reply_parts)
        if reply and connected:
            session["messages"].append({"role": "user", "content": clean_messages[-1]["content"]})
            session["messages"].append({"role": "assistant", "content": reply})
            session["messages"] = session["messages"][-HISTORY_LIMIT * 2:]
            _persist()

        if connected:
            self._sse_event({
                "type": "progress",
                "stage": "finalizing",
                "detail": "内容接收完成，正在确认结果",
            })
            self._sse_event({"done": True, "aborted": False})
            self._sse_event({"[DONE]": True})


def main():
    print("=" * 52)
    print(" CatsCo 本地服务")
    print("=" * 52)
    print(f" 地址     : http://{HOST}:{PORT}")
    print(f" 模型     : {MODEL}")
    print(f" API 地址 : {API_BASE}")
    print(f" API Key  : {'已配置' if API_KEY else '未配置'}")
    print("=" * 52)
    server = CatsCoHTTPServer((HOST, PORT), Handler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\n服务已停止")
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
