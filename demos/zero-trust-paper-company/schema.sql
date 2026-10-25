PRAGMA foreign_keys = ON;

CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE employees (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, department TEXT NOT NULL,
    title TEXT NOT NULL, email TEXT NOT NULL, manager_id TEXT REFERENCES employees(id),
    salary_cents INTEGER NOT NULL CHECK(salary_cents > 0),
    bank_account TEXT NOT NULL, emergency_contact TEXT NOT NULL
);
CREATE TABLE customers (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, contact TEXT NOT NULL,
    email TEXT NOT NULL, address TEXT NOT NULL, credit_limit_cents INTEGER NOT NULL,
    tax_reference TEXT NOT NULL
);
CREATE TABLE products (
    sku TEXT PRIMARY KEY, name TEXT NOT NULL, unit TEXT NOT NULL,
    price_cents INTEGER NOT NULL CHECK(price_cents > 0),
    on_hand INTEGER NOT NULL CHECK(on_hand >= 0),
    reserved INTEGER NOT NULL DEFAULT 0 CHECK(reserved >= 0 AND reserved <= on_hand),
    reorder_point INTEGER NOT NULL CHECK(reorder_point >= 0), location TEXT NOT NULL
);
CREATE TABLE orders (
    id TEXT PRIMARY KEY, customer_id TEXT NOT NULL REFERENCES customers(id),
    status TEXT NOT NULL CHECK(status IN ('confirmed','shipped','delivered','cancelled')),
    created_at TEXT NOT NULL, total_cents INTEGER NOT NULL CHECK(total_cents > 0)
);
CREATE TABLE order_lines (
    order_id TEXT NOT NULL REFERENCES orders(id), sku TEXT NOT NULL REFERENCES products(sku),
    quantity INTEGER NOT NULL CHECK(quantity > 0), unit_price_cents INTEGER NOT NULL CHECK(unit_price_cents > 0),
    PRIMARY KEY(order_id, sku)
);
CREATE TABLE invoices (
    id TEXT PRIMARY KEY, order_id TEXT NOT NULL UNIQUE REFERENCES orders(id),
    customer_id TEXT NOT NULL REFERENCES customers(id), due_date TEXT NOT NULL,
    total_cents INTEGER NOT NULL CHECK(total_cents > 0),
    paid_cents INTEGER NOT NULL DEFAULT 0 CHECK(paid_cents >= 0 AND paid_cents <= total_cents),
    status TEXT NOT NULL CHECK(status IN ('open','paid','void'))
);
CREATE TABLE payments (
    id TEXT PRIMARY KEY, invoice_id TEXT NOT NULL REFERENCES invoices(id),
    amount_cents INTEGER NOT NULL CHECK(amount_cents > 0), reference TEXT NOT NULL UNIQUE,
    received_at TEXT NOT NULL
);
CREATE TABLE notes (
    id TEXT PRIMARY KEY, customer_id TEXT NOT NULL REFERENCES customers(id),
    author TEXT NOT NULL, text TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE deals (
    id TEXT PRIMARY KEY, customer_id TEXT NOT NULL REFERENCES customers(id),
    title TEXT NOT NULL, value_cents INTEGER NOT NULL CHECK(value_cents > 0),
    stage TEXT NOT NULL CHECK(stage IN ('qualified','proposal','won','lost')),
    owner_id TEXT NOT NULL REFERENCES employees(id)
);
CREATE TABLE expenses (
    id TEXT PRIMARY KEY, category TEXT NOT NULL, description TEXT NOT NULL,
    amount_cents INTEGER NOT NULL CHECK(amount_cents > 0), incurred_on TEXT NOT NULL
);
CREATE TABLE tickets (
    id TEXT PRIMARY KEY, customer_id TEXT NOT NULL REFERENCES customers(id),
    order_id TEXT REFERENCES orders(id), subject TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('open','pending','resolved')),
    assigned_to TEXT NOT NULL REFERENCES employees(id), created_at TEXT NOT NULL
);
CREATE TABLE replies (
    id TEXT PRIMARY KEY, ticket_id TEXT NOT NULL REFERENCES tickets(id),
    author TEXT NOT NULL, text TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE documents (
    id TEXT PRIMARY KEY, title TEXT NOT NULL, classification TEXT NOT NULL
    CHECK(classification IN ('public','internal','confidential','restricted')),
    audience TEXT NOT NULL, source TEXT NOT NULL UNIQUE, body TEXT NOT NULL
);
CREATE VIRTUAL TABLE document_search USING fts5(title, body, content='documents', content_rowid='rowid');
CREATE TABLE time_off (
    id TEXT PRIMARY KEY, employee_id TEXT NOT NULL REFERENCES employees(id),
    start_date TEXT NOT NULL, end_date TEXT NOT NULL CHECK(end_date >= start_date),
    reason TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('pending','approved','denied'))
);
CREATE TABLE trucks (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, capacity_units INTEGER NOT NULL CHECK(capacity_units > 0)
);
CREATE TABLE routes (
    id TEXT PRIMARY KEY, truck_id TEXT NOT NULL REFERENCES trucks(id),
    driver_id TEXT NOT NULL REFERENCES employees(id), delivery_date TEXT NOT NULL,
    name TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('planned','loaded','delivered')),
    UNIQUE(truck_id, delivery_date), UNIQUE(driver_id, delivery_date)
);
CREATE TABLE shipments (
    id TEXT PRIMARY KEY, order_id TEXT NOT NULL UNIQUE REFERENCES orders(id),
    route_id TEXT NOT NULL REFERENCES routes(id), status TEXT NOT NULL CHECK(status IN ('loaded','delivered')),
    shipped_at TEXT NOT NULL, delivered_at TEXT
);
CREATE TABLE stock_movements (
    id TEXT PRIMARY KEY, sku TEXT NOT NULL REFERENCES products(sku), quantity INTEGER NOT NULL,
    reason TEXT NOT NULL, reference TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE suppliers (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, contact TEXT NOT NULL,
    email TEXT NOT NULL, bank_account TEXT NOT NULL, terms TEXT NOT NULL
);
CREATE TABLE supplier_products (
    supplier_id TEXT NOT NULL REFERENCES suppliers(id), sku TEXT NOT NULL REFERENCES products(sku),
    unit_cost_cents INTEGER NOT NULL CHECK(unit_cost_cents > 0), PRIMARY KEY(supplier_id, sku)
);
CREATE TABLE purchase_orders (
    id TEXT PRIMARY KEY, supplier_id TEXT NOT NULL REFERENCES suppliers(id),
    sku TEXT NOT NULL REFERENCES products(sku), quantity INTEGER NOT NULL CHECK(quantity > 0),
    unit_cost_cents INTEGER NOT NULL CHECK(unit_cost_cents > 0),
    status TEXT NOT NULL CHECK(status IN ('ordered','received')), expected_date TEXT NOT NULL
);
CREATE TABLE business_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT, actor TEXT NOT NULL,
    method TEXT NOT NULL, path TEXT NOT NULL, entity_id TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE idempotency (
    actor TEXT NOT NULL, key TEXT NOT NULL, fingerprint TEXT NOT NULL,
    status INTEGER NOT NULL, response TEXT NOT NULL, PRIMARY KEY(actor, key)
);
