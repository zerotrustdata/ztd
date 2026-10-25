"""Integer-cent accounting with partial payments and a dated collections view."""

from dataclasses import dataclass
from datetime import date

from core import Route, day, new_id, now, one, positive, require, rows, text


@dataclass
class Payment:
    amount_cents: int
    reference: str


@dataclass
class Expense:
    category: str
    description: str
    amount_cents: int
    incurred_on: str


def invoices(db, actor, params, query, body):
    return rows(db, "SELECT *,total_cents-paid_cents AS balance_cents FROM invoices ORDER BY due_date,id")


def past_due(db, actor, params, query, body):
    as_of = day(query.get("as_of", date.today().isoformat()))
    return rows(db, "SELECT *,total_cents-paid_cents AS balance_cents FROM invoices WHERE status='open' AND due_date<? ORDER BY due_date,id", (as_of,))


def payments(db, actor, params, query, body):
    return rows(db, "SELECT * FROM payments ORDER BY received_at,id")


def pay(db, actor, params, query, body):
    invoice = one(db, "SELECT * FROM invoices WHERE id=?", (params["id"],))
    positive(body.amount_cents)
    require(invoice["status"] == "open", "Invoice is not open")
    require(body.amount_cents <= invoice["total_cents"] - invoice["paid_cents"], "Payment exceeds balance")
    reference = text(body.reference)
    require(db.execute("SELECT 1 FROM payments WHERE reference=?", (reference,)).fetchone() is None, "Payment reference already recorded")
    payment_id = new_id("PAY")
    db.execute("INSERT INTO payments VALUES (?,?,?,?,?)", (payment_id, invoice["id"], body.amount_cents, reference, now()))
    balance = invoice["total_cents"] - invoice["paid_cents"] - body.amount_cents
    db.execute("UPDATE invoices SET paid_cents=paid_cents+?,status=? WHERE id=?", (body.amount_cents, "paid" if balance == 0 else "open", invoice["id"]))
    return {"id": payment_id, "invoice_id": invoice["id"], "balance_cents": balance}


def expenses(db, actor, params, query, body):
    return rows(db, "SELECT * FROM expenses ORDER BY incurred_on,id")


def expense(db, actor, params, query, body):
    expense_id = new_id("EXP")
    db.execute("INSERT INTO expenses VALUES (?,?,?,?,?)", (expense_id, text(body.category), text(body.description), positive(body.amount_cents), day(body.incurred_on)))
    return {"id": expense_id}


ROUTES = [
    Route("GET", "/accounting/invoices", ("finance",), invoices),
    Route("GET", "/accounting/past-due", ("finance",), past_due),
    Route("GET", "/accounting/payments", ("finance",), payments),
    Route("POST", "/accounting/invoices/{id}/payments", ("finance",), pay, Payment),
    Route("GET", "/accounting/expenses", ("finance",), expenses),
    Route("POST", "/accounting/expenses", ("finance",), expense, Expense),
]
