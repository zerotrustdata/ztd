"""Loopback-only stdlib HTTP host for one or all of the business systems."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import sqlite3
import sys

from api import Request, parse_json
from core import APIError, require

MAX_BODY = 65536


def make_server(application, port):
    class Handler(BaseHTTPRequestHandler):
        # HTTP/1.0 closes each request, including rejected/unread request bodies.
        server_version = "FictionalPaper/1"
        sys_version = ""

        def setup(self):
            super().setup()
            self.connection.settimeout(10)

        def respond(self, status, payload):
            data = json.dumps({"fictional": True, **payload}, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.end_headers()
            self.wfile.write(data)

        def dispatch(self):
            try:
                require(not self.headers.get("Transfer-Encoding"), "Transfer encoding is unsupported", 400, "invalid_request")
                require(len(self.headers.get_all("Content-Length", [])) <= 1, "Duplicate content length", 400, "invalid_request")
                try:
                    length = int(self.headers.get("Content-Length", "0"))
                except ValueError as exc:
                    raise APIError(400, "invalid_request", "Invalid content length") from exc
                require(0 <= length <= MAX_BODY, "Request body exceeds 64 KiB", 413, "body_too_large")
                body = None
                if self.command in ("POST", "PATCH"):
                    require(self.headers.get_content_type() == "application/json", "Use application/json", 415, "unsupported_media_type")
                    body = parse_json(self.rfile.read(length))
                else:
                    require(length == 0, "This method takes no request body", 400, "invalid_request")
                result = application.handle(Request(self.command, self.path, self.headers.get("Authorization", ""), body, self.headers.get("Idempotency-Key", "")))
                self.respond(result.status, {"data": result.data})
            except APIError as exc:
                self.respond(exc.status, {"error": {"code": exc.code, "message": exc.message}})
            except sqlite3.IntegrityError:
                self.respond(409, {"error": {"code": "constraint_conflict", "message": "Change conflicts with a business constraint"}})
            except sqlite3.OperationalError:
                self.respond(503, {"error": {"code": "database_unavailable", "message": "Database unavailable; check initialization, schema and write contention"}})
            except Exception:
                # Never put exception strings (possibly containing sensitive rows) into responses.
                print("Unhandled demo request failure; inspect the service and run make demos-test", file=sys.stderr)
                self.respond(500, {"error": {"code": "internal_error", "message": "Unexpected demo service error"}})

        do_GET = dispatch
        do_POST = dispatch
        do_PATCH = dispatch
        do_DELETE = dispatch
        do_PUT = dispatch

        def log_message(self, format, *args):
            # Paths/queries and auth headers can contain confidential demo material.
            pass

    return ThreadingHTTPServer(("127.0.0.1", port), Handler)
