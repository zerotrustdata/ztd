"""Supplier catalog, purchase orders, and single-receipt stock replenishment."""

from dataclasses import dataclass

from core import Empty, Route, day, new_id, now, one, positive, require, rows


@dataclass
class Purchase:
    supplier_id: str
    sku: str
    quantity: int
    expected_date: str


def suppliers(db, actor, params, query, body):
    if actor.role == "ops":
        return rows(db, "SELECT id,name,contact,email,terms FROM suppliers ORDER BY id")
    return rows(db, "SELECT * FROM suppliers ORDER BY id")


def catalog(db, actor, params, query, body):
    return rows(db, "SELECT * FROM supplier_products ORDER BY supplier_id,sku")


def orders(db, actor, params, query, body):
    return rows(db, "SELECT *,quantity*unit_cost_cents AS total_cents FROM purchase_orders ORDER BY expected_date,id")


def purchase(db, actor, params, query, body):
    offer = one(db, "SELECT * FROM supplier_products WHERE supplier_id=? AND sku=?", (body.supplier_id, body.sku))
    purchase_id = new_id("PO")
    db.execute("INSERT INTO purchase_orders VALUES (?,?,?,?,?,'ordered',?)", (purchase_id, body.supplier_id, body.sku, positive(body.quantity), offer["unit_cost_cents"], day(body.expected_date)))
    return {"id": purchase_id, "status": "ordered", "total_cents": body.quantity * offer["unit_cost_cents"]}


def receive(db, actor, params, query, body):
    purchase = one(db, "SELECT * FROM purchase_orders WHERE id=?", (params["id"],))
    require(purchase["status"] == "ordered", "Purchase order already received")
    db.execute("UPDATE products SET on_hand=on_hand+? WHERE sku=?", (purchase["quantity"], purchase["sku"]))
    db.execute("UPDATE purchase_orders SET status='received' WHERE id=?", (purchase["id"],))
    db.execute("INSERT INTO stock_movements VALUES (?,?,?,?,?,?)", (new_id("MOV"), purchase["sku"], purchase["quantity"], "purchase-receipt", purchase["id"], now()))
    return {"id": purchase["id"], "sku": purchase["sku"], "received_quantity": purchase["quantity"], "status": "received"}


ROUTES = [
    Route("GET", "/purchasing/suppliers", ("procurement", "finance", "ops"), suppliers),
    Route("GET", "/purchasing/catalog", ("procurement", "finance"), catalog),
    Route("GET", "/purchasing/orders", ("procurement", "finance", "ops"), orders),
    Route("POST", "/purchasing/orders", ("procurement",), purchase, Purchase),
    Route("POST", "/purchasing/orders/{id}/receive", ("procurement", "ops"), receive, Empty),
]
