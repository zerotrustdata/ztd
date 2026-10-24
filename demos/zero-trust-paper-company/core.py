"""Small shared contracts for the fictional business services (stdlib only)."""

from dataclasses import dataclass, fields, is_dataclass, MISSING
from datetime import date, datetime, timezone
from typing import get_args, get_origin, get_type_hints
from uuid import uuid4


class APIError(Exception):
    def __init__(self, status, code, message):
        super().__init__(message)
        self.status, self.code, self.message = status, code, message


def require(condition, message, status=409, code="business_rule"):
    if not condition:
        raise APIError(status, code, message)


@dataclass(frozen=True)
class Principal:
    role: str
    employee_id: str = ""
    customer_id: str = ""


# Deliberately public demo identities, never credentials for a real service.
IDENTITIES = {
    "admin": Principal("admin", "EMP-001"),
    "ops": Principal("ops", "EMP-002"),
    "sales": Principal("sales", "EMP-003"),
    "finance": Principal("finance", "EMP-004"),
    "support": Principal("support", "EMP-005"),
    "driver": Principal("driver", "EMP-006"),
    "hr": Principal("hr", "EMP-007"),
    "procurement": Principal("procurement", "EMP-008"),
    "customer-acorn": Principal("customer", customer_id="CUS-001"),
    "customer-beacon": Principal("customer", customer_id="CUS-002"),
}
STAFF = ("admin", "ops", "sales", "finance", "support", "driver", "hr", "procurement")


def authenticate(authorization):
    if not authorization:
        return Principal("public")
    prefix = "Bearer demo-"
    identity = IDENTITIES.get(authorization[len(prefix):]) if authorization.startswith(prefix) else None
    if identity is None:
        raise APIError(401, "unauthorized", "Use a documented fictional demo identity")
    return identity


def decode(model, value):
    """Reject unknown fields, missing fields, booleans as integers, and coercion."""
    if is_dataclass(model):
        require(type(value) is dict, "Request body must be an object", 400, "invalid_request")
        names = {field.name for field in fields(model)}
        require(not (value.keys() - names), "Unknown request field", 400, "invalid_request")
        hints = get_type_hints(model)
        result = {}
        for field in fields(model):
            if field.name in value:
                result[field.name] = decode(hints[field.name], value[field.name])
            elif field.default is MISSING and field.default_factory is MISSING:
                raise APIError(400, "invalid_request", f"Missing field: {field.name}")
        return model(**result)
    if get_origin(model) is list:
        require(type(value) is list and len(value) <= 100, "Expected a list of at most 100 items", 400, "invalid_request")
        return [decode(get_args(model)[0], item) for item in value]
    require(type(value) is model, f"Expected {model.__name__}", 400, "invalid_request")
    if model is str:
        require(len(value) <= 4000 and "\x00" not in value, "Text is too long or contains NUL", 400, "invalid_request")
    if model is int:
        require(abs(value) <= 1_000_000_000, "Integer exceeds demo limit", 400, "invalid_request")
    return value


def text(value):
    require(bool(value.strip()), "Text cannot be blank", 400, "invalid_request")
    return value.strip()


def positive(value):
    require(value > 0, "Value must be positive", 400, "invalid_request")
    return value


def day(value):
    try:
        parsed = date.fromisoformat(value)
    except ValueError as exc:
        raise APIError(400, "invalid_request", "Date must be YYYY-MM-DD") from exc
    require(parsed.isoformat() == value, "Date must be YYYY-MM-DD", 400, "invalid_request")
    return value


def now():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def new_id(prefix):
    return f"{prefix}-{uuid4().hex[:12]}"


def rows(db, sql, args=()):
    return [dict(row) for row in db.execute(sql, args)]


def one(db, sql, args=()):
    row = db.execute(sql, args).fetchone()
    if row is None:
        raise APIError(404, "not_found", "Record not found")
    return dict(row)


def customer_scope(actor, customer_id):
    # Hide other customers' records, including their existence.
    if actor.role == "customer" and actor.customer_id != customer_id:
        raise APIError(404, "not_found", "Record not found")


@dataclass(frozen=True)
class Route:
    method: str
    path: str
    roles: tuple
    handler: object
    body: object = None


@dataclass(frozen=True)
class Empty:
    pass
