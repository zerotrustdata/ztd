"""Customer contacts, confidential account notes, and a small sales pipeline."""

from dataclasses import dataclass

from core import Route, new_id, now, one, require, rows, text


@dataclass
class Note:
    text: str


@dataclass
class DealStage:
    stage: str


def customers(db, actor, params, query, body):
    return rows(db, "SELECT * FROM customers ORDER BY id")


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


def stage(db, actor, params, query, body):
    deal = one(db, "SELECT * FROM deals WHERE id=?", (params["id"],))
    require(body.stage in ("qualified", "proposal", "won", "lost"), "Unknown deal stage", 400, "invalid_request")
    allowed = {"qualified": ("proposal", "lost"), "proposal": ("won", "lost"), "won": (), "lost": ()}
    require(body.stage in allowed[deal["stage"]], "Deal cannot make that stage transition")
    db.execute("UPDATE deals SET stage=? WHERE id=?", (body.stage, params["id"]))
    return {"id": params["id"], "stage": body.stage}


ROUTES = [
    Route("GET", "/crm/customers", ("sales", "finance", "support"), customers),
    Route("GET", "/crm/customers/{id}", ("sales", "finance", "support"), customer),
    Route("POST", "/crm/customers/{id}/notes", ("sales", "support"), add_note, Note),
    Route("GET", "/crm/deals", ("sales", "finance"), deals),
    Route("PATCH", "/crm/deals/{id}", ("sales",), stage, DealStage),
]
