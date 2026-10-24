"""Employee directory, separately authorized payroll, and time-off approvals."""

from dataclasses import dataclass

from core import Route, STAFF, day, new_id, one, require, rows, text


@dataclass
class TimeOff:
    employee_id: str
    start_date: str
    end_date: str
    reason: str


@dataclass
class Decision:
    status: str


def directory(db, actor, params, query, body):
    return rows(db, "SELECT id,name,department,title,email,manager_id FROM employees ORDER BY id")


def payroll(db, actor, params, query, body):
    return rows(db, "SELECT id,name,salary_cents,bank_account,emergency_contact FROM employees ORDER BY id")


def time_off(db, actor, params, query, body):
    if actor.role in ("admin", "hr"):
        return rows(db, "SELECT * FROM time_off ORDER BY start_date,id")
    return rows(db, "SELECT * FROM time_off WHERE employee_id=? ORDER BY start_date,id", (actor.employee_id,))


def request(db, actor, params, query, body):
    require(body.employee_id == actor.employee_id or actor.role in ("hr", "admin"), "May only request your own leave", 403, "forbidden")
    one(db, "SELECT id FROM employees WHERE id=?", (body.employee_id,))
    start, end = day(body.start_date), day(body.end_date)
    require(start <= end, "Leave end precedes start", 400, "invalid_request")
    require(not db.execute("SELECT 1 FROM time_off WHERE employee_id=? AND status!='denied' AND start_date<=? AND end_date>=?", (body.employee_id, end, start)).fetchone(), "Leave overlaps an existing request")
    leave_id = new_id("PTO")
    db.execute("INSERT INTO time_off VALUES (?,?,?,?,?,'pending')", (leave_id, body.employee_id, start, end, text(body.reason)))
    return {"id": leave_id, "status": "pending"}


def decide(db, actor, params, query, body):
    leave = one(db, "SELECT * FROM time_off WHERE id=?", (params["id"],))
    require(leave["status"] == "pending", "Leave has already been decided")
    require(actor.employee_id != leave["employee_id"], "Cannot approve your own leave", 403, "forbidden")
    require(body.status in ("approved", "denied"), "Choose approved or denied", 400, "invalid_request")
    if body.status == "approved":
        require(not db.execute("SELECT 1 FROM routes WHERE driver_id=? AND status!='delivered' AND delivery_date BETWEEN ? AND ?", (leave["employee_id"], leave["start_date"], leave["end_date"])).fetchone(), "Driver has a scheduled route; re-plan before approving leave")
    db.execute("UPDATE time_off SET status=? WHERE id=?", (body.status, leave["id"]))
    return {"id": leave["id"], "status": body.status}


ROUTES = [
    Route("GET", "/hr/employees", STAFF, directory),
    Route("GET", "/hr/payroll", ("hr",), payroll),
    Route("GET", "/hr/time-off", STAFF, time_off),
    Route("POST", "/hr/time-off", STAFF, request, TimeOff),
    Route("PATCH", "/hr/time-off/{id}", ("hr",), decide, Decision),
]
