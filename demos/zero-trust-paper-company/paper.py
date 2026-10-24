#!/usr/bin/env python3
"""Run the fictional infrastructure or call it from a governed ztd command."""

import argparse
from datetime import date
import json
from pathlib import Path
import sqlite3
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import Request as HTTPRequest, build_opener, ProxyHandler, HTTPRedirectHandler

from api import Application, SYSTEMS, parse_json
from core import APIError, IDENTITIES, require
from database import DEFAULT_STATE, connect, initialize, reset
from server import make_server


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        return None


def call(args):
    endpoint = urlsplit(args.url)
    require(endpoint.scheme == "http" and endpoint.hostname == "127.0.0.1" and not endpoint.username
            and not endpoint.password and endpoint.path in ("", "/") and not endpoint.query and not endpoint.fragment,
            "Demo client requires http://127.0.0.1[:port]", 400, "invalid_endpoint")
    target = urlsplit(args.path)
    require(args.path.startswith("/") and not target.netloc and not target.scheme and not target.fragment,
            "Use an absolute API path such as /inventory/stock", 400, "invalid_request")
    headers = {}
    if args.role != "public":
        headers["Authorization"] = "Bearer demo-" + args.role
    data = None
    if args.method in ("POST", "PATCH"):
        require(bool(args.key), "Supply --key for a mutation", 400, "invalid_request")
        raw = Path(args.data_file).read_text(encoding="utf-8") if args.data_file else args.data
        body = parse_json(raw or "{}")
        data = json.dumps(body).encode("utf-8")
        headers.update({"Content-Type": "application/json", "Idempotency-Key": args.key})
    else:
        require(not args.data and not args.data_file and not args.key, "GET takes no body or idempotency key", 400, "invalid_request")
    request = HTTPRequest(args.url.rstrip("/") + args.path, data=data, headers=headers, method=args.method)
    # Local calls must not send demo data or tokens through ambient HTTP proxies/redirects.
    opener = build_opener(ProxyHandler({}), NoRedirect())
    try:
        with opener.open(request, timeout=10) as response:
            print(json.dumps(json.load(response), indent=2))
        return 0
    except HTTPError as exc:
        print(exc.read().decode("utf-8"), file=sys.stderr)
        return 1


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("init", "reset", "serve"):
        sub = commands.add_parser(name)
        sub.add_argument("--state-dir", type=Path, default=DEFAULT_STATE)
        if name in ("init", "reset"):
            sub.add_argument("--as-of", type=date.fromisoformat, help="Seed day (default: today); fixtures use relative dates")
        if name == "reset":
            sub.add_argument("--confirm", required=True, choices=("fictional-data",))
        if name == "serve":
            sub.add_argument("--port", type=int, default=9100)
            sub.add_argument("--service", choices=("all", *SYSTEMS), default="all")
    sub = commands.add_parser("call", help="HTTP client suitable for ztd run_shell_command")
    sub.add_argument("method", choices=("GET", "POST", "PATCH"))
    sub.add_argument("path")
    sub.add_argument("--url", default="http://127.0.0.1:9100")
    sub.add_argument("--role", choices=("public", *IDENTITIES), default="public")
    sub.add_argument("--key", help="Stable idempotency key for this exact mutation")
    group = sub.add_mutually_exclusive_group()
    group.add_argument("--data", help="JSON request body")
    group.add_argument("--data-file", help="Read a JSON request body from a local file")
    args = parser.parse_args(argv)
    try:
        if args.command == "init":
            print(f"Initialized fictional company: {initialize(args.state_dir, args.as_of)}")
        elif args.command == "reset":
            reset(args.state_dir, args.as_of)
            print("Reset fictional company, including business events and idempotency keys. Restart any active walkthrough.")
        elif args.command == "serve":
            require(0 <= args.port <= 65535, "Port must be 0 to 65535", 400, "invalid_port")
            with connect(args.state_dir):
                pass
            server = make_server(Application(args.state_dir, args.service), args.port)
            print(f"Zero Trust Paper Company [{args.service}] http://127.0.0.1:{server.server_port} (fictional data)", flush=True)
            try:
                server.serve_forever()
            except KeyboardInterrupt:
                pass
            finally:
                server.server_close()
        else:
            return call(args)
        return 0
    except (APIError, OSError, URLError, sqlite3.Error, ValueError) as exc:
        print(f"Demo error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
