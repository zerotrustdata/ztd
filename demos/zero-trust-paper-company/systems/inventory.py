"""Warehouse stock, counted adjustments, and the order fulfillment queue."""

from dataclasses import dataclass

from core import Route, new_id, now, one, require, rows, text


@dataclass
class Adjustment:
    sku: str
    quantity: int
    reason: str


def stock(db, actor, params, query, body):
    return rows(db, "SELECT *,on_hand-reserved AS available,on_hand-reserved<reorder_point AS needs_reorder FROM products ORDER BY sku")


def orders(db, actor, params, query, body):
    return rows(db, "SELECT o.id,o.customer_id,o.status,l.sku,l.quantity,p.location FROM orders o JOIN order_lines l ON l.order_id=o.id JOIN products p ON p.sku=l.sku ORDER BY o.id,l.sku")


def shipments(db, actor, params, query, body):
    return rows(db, "SELECT * FROM shipments ORDER BY shipped_at,id")


def movements(db, actor, params, query, body):
    return rows(db, "SELECT * FROM stock_movements ORDER BY created_at,id")


def adjust(db, actor, params, query, body):
    product = one(db, "SELECT * FROM products WHERE sku=?", (body.sku,))
    require(body.quantity != 0, "Adjustment must change stock", 400, "invalid_request")
    require(product["on_hand"] + body.quantity >= product["reserved"], "Adjustment would consume reserved stock")
    reason = text(body.reason)
    movement_id = new_id("MOV")
    db.execute("UPDATE products SET on_hand=on_hand+? WHERE sku=?", (body.quantity, body.sku))
    db.execute("INSERT INTO stock_movements VALUES (?,?,?,?,?,?)", (movement_id, body.sku, body.quantity, reason, "manual-count", now()))
    return {"id": movement_id, "sku": body.sku, "on_hand": product["on_hand"] + body.quantity}


ROUTES = [
    Route("GET", "/inventory/stock", ("ops", "procurement", "sales"), stock),
    Route("GET", "/inventory/orders", ("ops",), orders),
    Route("GET", "/inventory/shipments", ("ops", "support", "driver"), shipments),
    Route("GET", "/inventory/movements", ("ops", "procurement"), movements),
    Route("POST", "/inventory/adjustments", ("ops",), adjust, Adjustment),
]
