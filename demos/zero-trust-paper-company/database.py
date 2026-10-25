"""Explicit initialization/reset and connections to demo-owned SQLite state."""

from contextlib import contextmanager
from datetime import date
from pathlib import Path
import sqlite3

from core import APIError, require
from seed import populate

ROOT = Path(__file__).resolve().parent
DEFAULT_STATE = ROOT / ".demo-state"
SCHEMA_VERSION = "1"


def database_path(state_dir):
    root = Path(state_dir).resolve()
    require(not any(part.startswith(".ztd") for part in root.parts),
            "Demo state must be outside ztd runtime directories", 400, "invalid_state_dir")
    return root / "company.sqlite3"


@contextmanager
def connect(state_dir):
    path = database_path(state_dir)
    if not path.is_file():
        raise APIError(503, "not_initialized", "Run paper.py init first")
    db = sqlite3.connect(path.as_uri() + "?mode=rw", uri=True, timeout=5)
    db.row_factory = sqlite3.Row
    db.execute("PRAGMA foreign_keys = ON")
    try:
        metadata = dict(db.execute("SELECT key, value FROM metadata"))
        require(metadata.get("company") == "zero-trust-paper-company" and metadata.get("schema_version") == SCHEMA_VERSION,
                "Database is not this demo's supported schema", 503, "invalid_database")
        yield db
    finally:
        db.close()


def initialize(state_dir, as_of=None):
    path = database_path(state_dir)
    path.parent.mkdir(parents=True, exist_ok=True)
    try:
        with path.open("xb"):
            pass
    except FileExistsError as exc:
        raise APIError(409, "already_initialized", "Database exists; use reset explicitly") from exc
    db = sqlite3.connect(path)
    try:
        db.execute("PRAGMA journal_mode = WAL")
        db.executescript((ROOT / "schema.sql").read_text(encoding="utf-8"))
        with db:
            populate(db, ROOT, as_of or date.today())
    except Exception:
        db.close()
        path.unlink()
        raise
    finally:
        db.close()
    return path


def reset(state_dir, as_of=None):
    with connect(state_dir) as db:
        db.execute("BEGIN IMMEDIATE")
        # Fixed dependency order, never arbitrary user-supplied table names.
        for table in ("idempotency", "business_events", "payments", "replies", "tickets", "notes",
                      "deals", "expenses", "time_off", "shipments", "routes", "trucks",
                      "stock_movements", "purchase_orders", "supplier_products", "suppliers",
                      "invoices", "order_lines", "orders", "products", "customers",
                      "employees", "document_search", "documents", "metadata"):
            db.execute(f"DELETE FROM {table}")
        populate(db, ROOT, as_of or date.today())
        db.commit()
