"""Customer order portal: reserve stock and issue an invoice atomically."""

from dataclasses import dataclass
from datetime import date, timedelta

from core import Empty, Route, STAFF, customer_scope, new_id, now, one, positive, require, rows


@dataclass
class Line:
    sku: str
    quantity: int


@dataclass
class PlaceOrder:
    customer_id: str
    items: list[Line]


def products(db, actor, params, query, body):
    return rows(db, "SELECT sku,name,unit,price_cents,on_hand-reserved AS available FROM products WHERE sku NOT LIKE 'RAW-%' ORDER BY sku")


def orders(db, actor, params, query, body):
    if actor.role == "customer":
        return rows(db, "SELECT * FROM orders WHERE customer_id=? ORDER BY created_at,id", (actor.customer_id,))
    return rows(db, "SELECT * FROM orders ORDER BY created_at,id")


def detail(db, actor, params, query, body):
    order = one(db, "SELECT * FROM orders WHERE id=?", (params["id"],))
    customer_scope(actor, order["customer_id"])
    order["items"] = rows(db, "SELECT sku,quantity,unit_price_cents FROM order_lines WHERE order_id=? ORDER BY sku", (order["id"],))
    order["shipments"] = rows(db, "SELECT * FROM shipments WHERE order_id=?", (order["id"],))
    return order


def place(db, actor, params, query, body):
    customer_scope(actor, body.customer_id)
    customer = one(db, "SELECT * FROM customers WHERE id=?", (body.customer_id,))
    require(bool(body.items), "Order must contain at least one line", 400, "invalid_request")
    require(len({line.sku for line in body.items}) == len(body.items), "Duplicate SKU; combine quantities", 400, "invalid_request")
    total, lines = 0, []
    for line in body.items:
        positive(line.quantity)
        product = one(db, "SELECT * FROM products WHERE sku=? AND sku NOT LIKE 'RAW-%'", (line.sku,))
        require(product["on_hand"] - product["reserved"] >= line.quantity, "Insufficient available stock")
        total += product["price_cents"] * line.quantity
        lines.append((line.sku, line.quantity, product["price_cents"]))
    outstanding = db.execute("SELECT coalesce(sum(total_cents-paid_cents),0) FROM invoices WHERE customer_id=? AND status='open'", (body.customer_id,)).fetchone()[0]
    require(outstanding + total <= customer["credit_limit_cents"], "Customer credit limit exceeded")
    order_id, invoice_id = new_id("ORD"), new_id("INV")
    db.execute("INSERT INTO orders VALUES (?,?,'confirmed',?,?)", (order_id, body.customer_id, now(), total))
    for sku, quantity, price in lines:
        db.execute("INSERT INTO order_lines VALUES (?,?,?,?)", (order_id, sku, quantity, price))
        db.execute("UPDATE products SET reserved=reserved+? WHERE sku=?", (quantity, sku))
    db.execute("INSERT INTO invoices VALUES (?,?,?,?,?,0,'open')", (
        invoice_id, order_id, body.customer_id, (date.today() + timedelta(days=30)).isoformat(), total,
    ))
    return {"id": order_id, "invoice_id": invoice_id, "status": "confirmed", "total_cents": total}


def cancel(db, actor, params, query, body):
    order = detail(db, actor, params, query, body)
    require(order["status"] == "confirmed", "Only confirmed, unshipped orders can be cancelled")
    invoice = one(db, "SELECT * FROM invoices WHERE order_id=?", (order["id"],))
    require(invoice["paid_cents"] == 0, "Paid orders need a finance refund workflow; cancellation is blocked")
    for line in order["items"]:
        db.execute("UPDATE products SET reserved=reserved-? WHERE sku=?", (line["quantity"], line["sku"]))
    db.execute("UPDATE orders SET status='cancelled' WHERE id=?", (order["id"],))
    db.execute("UPDATE invoices SET status='void' WHERE order_id=?", (order["id"],))
    return {"id": order["id"], "status": "cancelled"}


READERS = ("customer", "sales", "ops", "support", "finance")
ROUTES = [
    Route("GET", "/storefront/products", ("public", "customer") + STAFF, products),
    Route("GET", "/storefront/orders", READERS, orders),
    Route("GET", "/storefront/orders/{id}", READERS, detail),
    Route("POST", "/storefront/orders", ("customer", "sales"), place, PlaceOrder),
    Route("POST", "/storefront/orders/{id}/cancel", ("customer", "sales"), cancel, Empty),
]
