"""HTTP routing and transactional business events; no ztd receipt simulation."""

from dataclasses import dataclass
import hashlib
import json
import re
from urllib.parse import parse_qsl, urlsplit

from core import APIError, authenticate, decode, now, require, rows
from database import connect
from systems import accounting, crm, dispatch, documents, hr, inventory, purchasing, storefront, support

SYSTEMS = {
    "storefront": storefront, "crm": crm, "accounting": accounting,
    "inventory": inventory, "support": support, "documents": documents,
    "hr": hr, "dispatch": dispatch, "purchasing": purchasing,
}


@dataclass(frozen=True)
class Request:
    method: str
    target: str
    authorization: str = ""
    body: object = None
    idempotency_key: str = ""


@dataclass(frozen=True)
class Result:
    status: int
    data: object


class Application:
    def __init__(self, state_dir, service="all"):
        require(service == "all" or service in SYSTEMS, "Unknown service", 400, "invalid_service")
        self.state_dir, self.service = state_dir, service
        selected = SYSTEMS.values() if service == "all" else (SYSTEMS[service],)
        self.routes = []
        for module in selected:
            for route in module.ROUTES:
                pattern = re.sub(r"\{(\w+)\}", r"(?P<\1>[A-Za-z0-9_-]+)", route.path)
                self.routes.append((route, re.compile(pattern)))

    def handle(self, request):
        url = urlsplit(request.target)
        require(not url.scheme and not url.netloc and not url.fragment, "Use a relative API path", 400, "invalid_request")
        actor = authenticate(request.authorization)
        if request.method == "GET" and url.path == "/health":
            with connect(self.state_dir) as db:
                db.execute("SELECT 1").fetchone()
            return Result(200, {"status": "ok", "service": self.service})
        if request.method == "GET" and url.path == "/":
            return Result(200, {"company": "Zero Trust Paper Company", "service": self.service,
                                "endpoints": [{"method": r.method, "path": r.path} for r, _ in self.routes]})
        if request.method == "GET" and url.path == "/events":
            require(actor.role == "admin", "Business events require admin", 403, "forbidden")
            with connect(self.state_dir) as db:
                return Result(200, rows(db, "SELECT * FROM business_events ORDER BY sequence DESC LIMIT 100"))
        candidates = [(route, pattern.fullmatch(url.path)) for route, pattern in self.routes]
        candidates = [(route, match) for route, match in candidates if match]
        require(bool(candidates), "Endpoint not found", 404, "not_found")
        chosen = [(route, match) for route, match in candidates if route.method == request.method]
        require(bool(chosen), "Method not allowed", 405, "method_not_allowed")
        route, match = chosen[0]
        require(actor.role == "admin" or actor.role in route.roles, "Role cannot access this endpoint", 403, "forbidden")
        pairs = parse_qsl(url.query, keep_blank_values=True)
        require(len(dict(pairs)) == len(pairs), "Duplicate query parameter", 400, "invalid_request")
        require(len(url.query) <= 4000, "Query too long", 400, "invalid_request")
        body = decode(route.body, request.body) if route.body else None
        mutation = request.method != "GET"
        key = request.idempotency_key
        if mutation:
            require(bool(re.fullmatch(r"[A-Za-z0-9._-]{1,128}", key)), "Mutation requires Idempotency-Key (1 to 128 letters, digits, dots, underscores or hyphens)", 400, "invalid_request")
        identity = ":".join((actor.role, actor.employee_id, actor.customer_id))
        fingerprint = hashlib.sha256(json.dumps([request.method, request.target, request.body], sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        with connect(self.state_dir) as db:
            # Serialize writes across every service process; a failed handler rolls back all effects.
            db.execute("BEGIN IMMEDIATE" if mutation else "BEGIN")
            if mutation:
                replay = db.execute("SELECT * FROM idempotency WHERE actor=? AND key=?", (identity, key)).fetchone()
                if replay:
                    require(replay["fingerprint"] == fingerprint, "Idempotency key was used for a different request")
                    return Result(replay["status"], json.loads(replay["response"]))
            result = route.handler(db, actor, match.groupdict(), dict(pairs), body)
            status = 201 if request.method == "POST" else 200
            if mutation:
                db.execute("INSERT INTO business_events(actor,method,path,entity_id,created_at) VALUES (?,?,?,?,?)", (
                    identity, request.method, url.path, result.get("id", ""), now(),
                ))
                db.execute("INSERT INTO idempotency VALUES (?,?,?,?,?)", (identity, key, fingerprint, status, json.dumps(result)))
            db.commit()
            return Result(status, result)


def parse_json(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "Duplicate JSON field", 400, "invalid_request")
            result[key] = value
        return result

    def invalid_constant(value):
        raise APIError(400, "invalid_request", "Non-finite JSON numbers are not allowed")

    try:
        return json.loads(raw, object_pairs_hook=unique, parse_constant=invalid_constant)
    except (ValueError, UnicodeError, RecursionError) as exc:
        raise APIError(400, "invalid_request", "Invalid JSON") from exc
