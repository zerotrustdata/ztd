"""Truck routes and guarded loading/delivery across warehouse and order state."""

from dataclasses import dataclass

from core import Empty, Route, day, new_id, now, one, require, rows, text


@dataclass
class Plan:
    truck_id: str
    driver_id: str
    delivery_date: str
    name: str


@dataclass
class Load:
    order_id: str


def trucks(db, actor, params, query, body):
    return rows(db, "SELECT * FROM trucks ORDER BY id")


def routes(db, actor, params, query, body):
    if actor.role == "driver":
        return rows(db, "SELECT * FROM routes WHERE driver_id=? ORDER BY delivery_date,id", (actor.employee_id,))
    return rows(db, "SELECT * FROM routes ORDER BY delivery_date,id")


def detail(db, actor, params, query, body):
    route = one(db, "SELECT * FROM routes WHERE id=?", (params["id"],))
    require(actor.role != "driver" or route["driver_id"] == actor.employee_id, "Route not found", 404, "not_found")
    route["shipments"] = rows(db, "SELECT s.*,c.name AS customer,c.address FROM shipments s JOIN orders o ON o.id=s.order_id JOIN customers c ON c.id=o.customer_id WHERE s.route_id=? ORDER BY s.id", (route["id"],))
    return route


def available_driver(db, driver_id, delivery_date):
    employee = one(db, "SELECT department FROM employees WHERE id=?", (driver_id,))
    require(employee["department"] == "Delivery", "Route driver must be delivery staff")
    require(not db.execute("SELECT 1 FROM time_off WHERE employee_id=? AND status='approved' AND ? BETWEEN start_date AND end_date", (driver_id, delivery_date)).fetchone(), "Driver is on approved leave")


def plan(db, actor, params, query, body):
    one(db, "SELECT id FROM trucks WHERE id=?", (body.truck_id,))
    delivery_date = day(body.delivery_date)
    available_driver(db, body.driver_id, delivery_date)
    require(not db.execute("SELECT 1 FROM routes WHERE delivery_date=? AND (truck_id=? OR driver_id=?)", (delivery_date, body.truck_id, body.driver_id)).fetchone(), "Truck or driver already scheduled that day")
    route_id = new_id("RTE")
    db.execute("INSERT INTO routes VALUES (?,?,?,?,?,'planned')", (route_id, body.truck_id, body.driver_id, delivery_date, text(body.name)))
    return {"id": route_id, "status": "planned"}


def load(db, actor, params, query, body):
    route = detail(db, actor, params, query, body)
    require(route["status"] in ("planned", "loaded"), "Cannot load a completed route")
    available_driver(db, route["driver_id"], route["delivery_date"])
    order = one(db, "SELECT * FROM orders WHERE id=?", (body.order_id,))
    require(order["status"] == "confirmed", "Only confirmed orders can be loaded")
    lines = rows(db, "SELECT * FROM order_lines WHERE order_id=?", (body.order_id,))
    capacity = one(db, "SELECT capacity_units FROM trucks WHERE id=?", (route["truck_id"],))["capacity_units"]
    loaded = db.execute("SELECT coalesce(sum(l.quantity),0) FROM shipments s JOIN order_lines l ON l.order_id=s.order_id WHERE s.route_id=?", (route["id"],)).fetchone()[0]
    require(loaded + sum(line["quantity"] for line in lines) <= capacity, "Truck capacity exceeded")
    stamp, shipment_id = now(), new_id("SHP")
    for line in lines:
        product = one(db, "SELECT * FROM products WHERE sku=?", (line["sku"],))
        require(product["reserved"] >= line["quantity"] and product["on_hand"] >= line["quantity"], "Reserved stock is unavailable")
        db.execute("UPDATE products SET reserved=reserved-?,on_hand=on_hand-? WHERE sku=?", (line["quantity"], line["quantity"], line["sku"]))
        db.execute("INSERT INTO stock_movements VALUES (?,?,?,?,?,?)", (new_id("MOV"), line["sku"], -line["quantity"], "shipment", shipment_id, stamp))
    db.execute("INSERT INTO shipments VALUES (?,?,?,'loaded',?,NULL)", (shipment_id, body.order_id, route["id"], stamp))
    db.execute("UPDATE orders SET status='shipped' WHERE id=?", (body.order_id,))
    db.execute("UPDATE routes SET status='loaded' WHERE id=?", (route["id"],))
    return {"id": shipment_id, "order_id": body.order_id, "route_id": route["id"], "status": "loaded"}


def deliver(db, actor, params, query, body):
    shipment = one(db, "SELECT * FROM shipments WHERE id=?", (params["id"],))
    route = one(db, "SELECT * FROM routes WHERE id=?", (shipment["route_id"],))
    require(actor.role != "driver" or route["driver_id"] == actor.employee_id, "Shipment not found", 404, "not_found")
    require(shipment["status"] == "loaded", "Shipment is already delivered")
    db.execute("UPDATE shipments SET status='delivered',delivered_at=? WHERE id=?", (now(), shipment["id"]))
    db.execute("UPDATE orders SET status='delivered' WHERE id=?", (shipment["order_id"],))
    if not db.execute("SELECT 1 FROM shipments WHERE route_id=? AND status='loaded'", (route["id"],)).fetchone():
        db.execute("UPDATE routes SET status='delivered' WHERE id=?", (route["id"],))
    return {"id": shipment["id"], "order_id": shipment["order_id"], "status": "delivered"}


ROUTES = [
    Route("GET", "/dispatch/trucks", ("ops", "driver"), trucks),
    Route("GET", "/dispatch/routes", ("ops", "driver", "support"), routes),
    Route("GET", "/dispatch/routes/{id}", ("ops", "driver", "support"), detail),
    Route("POST", "/dispatch/routes", ("ops",), plan, Plan),
    Route("POST", "/dispatch/routes/{id}/load", ("ops",), load, Load),
    Route("POST", "/dispatch/shipments/{id}/deliver", ("ops", "driver"), deliver, Empty),
]
