"""Customer tickets, threaded replies, assignment and resolution."""

from dataclasses import dataclass

from core import Route, customer_scope, new_id, now, one, require, rows, text


@dataclass
class Ticket:
    customer_id: str
    subject: str
    message: str
    order_id: str = ""


@dataclass
class Reply:
    text: str


@dataclass
class Update:
    status: str
    assigned_to: str


def tickets(db, actor, params, query, body):
    if actor.role == "customer":
        return rows(db, "SELECT * FROM tickets WHERE customer_id=? ORDER BY created_at,id", (actor.customer_id,))
    return rows(db, "SELECT * FROM tickets ORDER BY created_at,id")


def detail(db, actor, params, query, body):
    ticket = one(db, "SELECT * FROM tickets WHERE id=?", (params["id"],))
    customer_scope(actor, ticket["customer_id"])
    ticket["replies"] = rows(db, "SELECT * FROM replies WHERE ticket_id=? ORDER BY created_at,id", (ticket["id"],))
    return ticket


def create(db, actor, params, query, body):
    customer_scope(actor, body.customer_id)
    one(db, "SELECT id FROM customers WHERE id=?", (body.customer_id,))
    if body.order_id:
        order = one(db, "SELECT * FROM orders WHERE id=?", (body.order_id,))
        customer_scope(actor, order["customer_id"])
        require(order["customer_id"] == body.customer_id, "Order does not belong to this customer")
    ticket_id = new_id("TKT")
    db.execute("INSERT INTO tickets VALUES (?,?,?,?,'open','EMP-005',?)", (ticket_id, body.customer_id, body.order_id or None, text(body.subject), now()))
    db.execute("INSERT INTO replies VALUES (?,?,?,?,?)", (new_id("REPLY"), ticket_id, actor.customer_id or actor.role, text(body.message), now()))
    return {"id": ticket_id, "status": "open"}


def reply(db, actor, params, query, body):
    ticket = detail(db, actor, params, query, body)
    require(ticket["status"] != "resolved", "Reopen ticket before replying")
    reply_id = new_id("REPLY")
    db.execute("INSERT INTO replies VALUES (?,?,?,?,?)", (reply_id, ticket["id"], actor.customer_id or actor.role, text(body.text), now()))
    return {"id": reply_id, "ticket_id": ticket["id"]}


def update(db, actor, params, query, body):
    ticket = detail(db, actor, params, query, body)
    allowed = {"open": ("open", "pending", "resolved"), "pending": ("pending", "open", "resolved"), "resolved": ("open",)}
    require(body.status in allowed[ticket["status"]], "Invalid ticket transition")
    assignee = one(db, "SELECT department FROM employees WHERE id=?", (body.assigned_to,))
    require(assignee["department"] == "Support", "Tickets must be assigned to support staff")
    db.execute("UPDATE tickets SET status=?,assigned_to=? WHERE id=?", (body.status, body.assigned_to, ticket["id"]))
    return {"id": ticket["id"], "status": body.status, "assigned_to": body.assigned_to}


ROUTES = [
    Route("GET", "/support/tickets", ("support", "customer"), tickets),
    Route("GET", "/support/tickets/{id}", ("support", "customer"), detail),
    Route("POST", "/support/tickets", ("support", "customer"), create, Ticket),
    Route("POST", "/support/tickets/{id}/replies", ("support", "customer"), reply, Reply),
    Route("PATCH", "/support/tickets/{id}", ("support",), update, Update),
]
