"""Customer contacts, confidential account notes, and a small sales pipeline."""

from dataclasses import dataclass
import re

from core import Route, new_id, now, one, positive, require, rows, text


@dataclass
class Note:
    text: str


@dataclass
class Customer:
    id: str
    name: str
    contact: str
    email: str
    address: str
    credit_limit_cents: int
    tax_reference: str


@dataclass
class Deal:
    customer_id: str
    title: str
    value_cents: int


@dataclass
class DealStage:
    stage: str


def customers(db, actor, params, query, body):
    return rows(db, "SELECT * FROM customers ORDER BY id")


def add_customer(db, actor, params, query, body):
    customer_id = text(body.id)
    require(bool(re.fullmatch(r"CUS-[A-Z0-9_-]{3,24}", customer_id)),
            "Customer ID must use CUS- followed by 3 to 24 uppercase letters, digits, underscores or hyphens",
            400, "invalid_request")
    email = text(body.email)
    require("@" in email and not any(character.isspace() for character in email),
            "Customer email is invalid", 400, "invalid_request")
    require(db.execute("SELECT 1 FROM customers WHERE id=? OR email=? OR tax_reference=?",
                       (customer_id, email, text(body.tax_reference))).fetchone() is None,
            "Customer ID, email or tax reference already exists")
    db.execute("INSERT INTO customers VALUES (?,?,?,?,?,?,?)", (
        customer_id, text(body.name), text(body.contact), email, text(body.address),
        positive(body.credit_limit_cents), text(body.tax_reference),
    ))
    return {"id": customer_id, "name": text(body.name), "credit_limit_cents": body.credit_limit_cents}


def customer(db, actor, params, query, body):
    result = one(db, "SELECT * FROM customers WHERE id=?", (params["id"],))
    result["notes"] = rows(db, "SELECT * FROM notes WHERE customer_id=? ORDER BY created_at,id", (params["id"],))
    result["orders"] = rows(db, "SELECT * FROM orders WHERE customer_id=? ORDER BY created_at,id", (params["id"],))
    return result


def add_note(db, actor, params, query, body):
    one(db, "SELECT id FROM customers WHERE id=?", (params["id"],))
    note_id = new_id("NOTE")
    db.execute("INSERT INTO notes VALUES (?,?,?,?,?)", (note_id, params["id"], actor.role, text(body.text), now()))
    return {"id": note_id, "customer_id": params["id"]}


def deals(db, actor, params, query, body):
    return rows(db, "SELECT * FROM deals ORDER BY id")


def add_deal(db, actor, params, query, body):
    one(db, "SELECT id FROM customers WHERE id=?", (body.customer_id,))
    deal_id = new_id("DEAL")
    db.execute("INSERT INTO deals VALUES (?,?,?,?,'qualified',?)", (
        deal_id, body.customer_id, text(body.title), positive(body.value_cents), actor.employee_id,
    ))
    return {"id": deal_id, "customer_id": body.customer_id, "stage": "qualified"}


def stage(db, actor, params, query, body):
    deal = one(db, "SELECT * FROM deals WHERE id=?", (params["id"],))
    require(body.stage in ("qualified", "proposal", "won", "lost"), "Unknown deal stage", 400, "invalid_request")
    allowed = {"qualified": ("proposal", "lost"), "proposal": ("won", "lost"), "won": (), "lost": ()}
    require(body.stage in allowed[deal["stage"]], "Deal cannot make that stage transition")
    db.execute("UPDATE deals SET stage=? WHERE id=?", (body.stage, params["id"]))
    return {"id": params["id"], "stage": body.stage}


ROUTES = [
    Route("GET", "/crm/customers", ("sales", "finance", "support"), customers),
    Route("POST", "/crm/customers", ("finance",), add_customer, Customer),
    Route("GET", "/crm/customers/{id}", ("sales", "finance", "support"), customer),
    Route("POST", "/crm/customers/{id}/notes", ("sales", "support"), add_note, Note),
    Route("GET", "/crm/deals", ("sales", "finance"), deals),
    Route("POST", "/crm/deals", ("sales",), add_deal, Deal),
    Route("PATCH", "/crm/deals/{id}", ("sales",), stage, DealStage),
]
