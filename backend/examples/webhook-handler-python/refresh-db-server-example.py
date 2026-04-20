#!/usr/bin/env python3
"""
Minimal Python reference for a k8s-stack-manager webhook subscriber.

Uses Python stdlib only — no Flask, FastAPI, or external deps — so it
runs in any Python 3.9+ environment without `pip install`. Swap in your
favourite framework when you scale beyond a proof-of-concept.

This covers:
  - HMAC-SHA256 signature verification (constant-time compare)
  - EventEnvelope / ActionRequest parsing
  - Proper response shape (Allowed/Message for events; arbitrary JSON for actions)
  - /healthz endpoint (unauthenticated)

Environment variables:
  WEBHOOK_SECRET  — HMAC shared secret. If unset, signature verification is DISABLED
                    (safe only for internal localhost testing).
  LISTEN_ADDR     — host:port to bind (default 0.0.0.0:8080)

Run locally:
  export WEBHOOK_SECRET=$(openssl rand -hex 32)
  python3 refresh-db-server-example.py

Register it in HOOKS_CONFIG_FILE on the k8s-stack-manager side:
  {
    "subscriptions": [
      { "name": "audit-log", "events": ["post-instance-create"],
        "url": "http://<this-host>:8080/events",
        "failure_policy": "ignore",
        "secret_env": "WEBHOOK_SECRET" }
    ],
    "actions": [
      { "name": "my-action",
        "url": "http://<this-host>:8080/actions/my-action",
        "secret_env": "WEBHOOK_SECRET" }
    ]
  }
"""

from __future__ import annotations

import hashlib
import hmac
import json
import logging
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

SECRET = os.environ.get("WEBHOOK_SECRET", "")
LISTEN_ADDR = os.environ.get("LISTEN_ADDR", "0.0.0.0:8080")

log = logging.getLogger("example-webhook")


def verify_signature(body: bytes, header: str) -> bool:
    """Constant-time HMAC-SHA256 verification. When SECRET is empty, signature
    checks are disabled — use only on trusted networks."""
    if not SECRET:
        return True
    expected = "sha256=" + hmac.new(SECRET.encode(), body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(header, expected)


def handle_event(envelope: dict[str, Any]) -> tuple[int, dict[str, Any]]:
    """Decide what to do with an event.

    Replace this stub with your actual policy. For pre-* events with
    failure_policy=fail, return {"allowed": False, "message": "..."} to block.
    """
    event = envelope.get("event", "")
    inst = envelope.get("instance") or {}
    log.info("received event=%s instance=%s namespace=%s request_id=%s",
             event, inst.get("id"), inst.get("namespace"), envelope.get("request_id"))

    # Example: block pre-instance-delete if the instance has a "critical" label.
    # This is a stub — real policy would look up metadata from your CMDB.
    if event == "pre-instance-delete" and "critical" in (inst.get("name") or ""):
        return 200, {"allowed": False, "message": "instance name contains 'critical' — delete blocked by policy"}

    return 200, {"allowed": True}


def handle_action(name: str, request_obj: dict[str, Any]) -> tuple[int, dict[str, Any]]:
    """Execute a user-invoked action and return arbitrary JSON.

    The response is forwarded verbatim to the stackctl caller, wrapped in
    {"action", "instance_id", "status_code", "result"}.
    """
    inst = request_obj.get("instance") or {}
    params = request_obj.get("parameters") or {}
    log.info("received action=%s instance=%s params=%s request_id=%s",
             name, inst.get("id"), params, request_obj.get("request_id"))

    # Replace this with real work. For long-running operations (>30s),
    # return 202 + a job_id and run the work on a background thread.
    return 200, {
        "ok": True,
        "instance_namespace": inst.get("namespace"),
        "echo_parameters": params,
        "note": "This is a stub — replace with real work.",
    }


class Handler(BaseHTTPRequestHandler):
    # Silence the default stderr access log (use the logger instead).
    def log_message(self, fmt: str, *args: Any) -> None:  # noqa: N802
        log.debug("http %s %s", self.address_string(), fmt % args)

    def _write_json(self, status: int, body: dict[str, Any]) -> None:
        payload = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/healthz":
            self._write_json(200, {"ok": True, "signature_verification": bool(SECRET)})
            return
        self._write_json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        if length <= 0 or length > 1 << 20:  # 1 MiB cap
            self._write_json(400, {"error": "missing or too-large body"})
            return
        body = self.rfile.read(length)

        sig = self.headers.get("X-StackManager-Signature", "")
        if not verify_signature(body, sig):
            log.warning("rejected request: bad signature event=%s",
                        self.headers.get("X-StackManager-Event", ""))
            self._write_json(401, {"error": "bad signature"})
            return

        try:
            payload = json.loads(body)
        except json.JSONDecodeError as e:
            self._write_json(400, {"error": f"invalid json: {e}"})
            return

        # Dispatch by path
        if self.path == "/events":
            status, resp = handle_event(payload)
            self._write_json(status, resp)
        elif self.path.startswith("/actions/"):
            action = self.path[len("/actions/"):]
            if not action:
                self._write_json(400, {"error": "missing action name in path"})
                return
            status, resp = handle_action(action, payload)
            self._write_json(status, resp)
        else:
            self._write_json(404, {"error": f"unknown path {self.path}"})


def main() -> None:
    logging.basicConfig(
        level=os.environ.get("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
        stream=sys.stdout,
    )
    if ":" not in LISTEN_ADDR:
        sys.exit(f"invalid LISTEN_ADDR {LISTEN_ADDR!r}; expected host:port")
    host, port = LISTEN_ADDR.rsplit(":", 1)
    server = ThreadingHTTPServer((host or "0.0.0.0", int(port)), Handler)
    log.info("listening on %s signature_verification=%s",
             LISTEN_ADDR, "on" if SECRET else "off")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log.info("shutting down")
        server.server_close()


if __name__ == "__main__":
    main()
