"""Deterministic fictional fixtures, with dates relative to an explicit seed day."""

from datetime import timedelta
import json


def populate(db, root, as_of):
    def day(offset):
        return (as_of + timedelta(days=offset)).isoformat()

    def insert(table, columns, records):
        # Table/column names are source-owned constants; values are always bound.
        placeholders = ",".join("?" for _ in columns.split(","))
        db.executemany(f"INSERT INTO {table} ({columns}) VALUES ({placeholders})", records)

    insert("metadata", "key,value", [
        ("company", "zero-trust-paper-company"), ("schema_version", "1"),
        ("as_of", as_of.isoformat()), ("currency", "USD"),
        ("fictional", "All people, organizations, account references and transactions are synthetic"),
    ])
    staff = [
        ("EMP-001", "Morgan Reed", "Leadership", "General Manager", None, 12400000),
        ("EMP-002", "Alex Chen", "Operations", "Warehouse and Systems Lead", "EMP-001", 8850000),
        ("EMP-003", "Jordan Ellis", "Sales", "Account Manager", "EMP-001", 7800000),
        ("EMP-004", "Priya Shah", "Finance", "Controller", "EMP-001", 9600000),
        ("EMP-005", "Sam Rivera", "Support", "Customer Care Lead", "EMP-003", 6450000),
        ("EMP-006", "Casey Brooks", "Delivery", "Delivery Driver", "EMP-002", 6100000),
        ("EMP-007", "Taylor Quinn", "People", "People Operations Lead", "EMP-001", 8200000),
        ("EMP-008", "Jamie Park", "Purchasing", "Buyer", "EMP-002", 7150000),
        ("EMP-009", "Drew Lane", "Delivery", "Delivery Driver", "EMP-002", 6000000),
        ("EMP-010", "Robin Vale", "Operations", "Warehouse Associate", "EMP-002", 5250000),
    ]
    insert("employees", "id,name,department,title,email,manager_id,salary_cents,bank_account,emergency_contact", [
        (eid, name, dept, title, name.lower().replace(" ", ".") + "@zerotrustpaper.example",
         manager, salary, "FICTIONAL-PAYROLL-" + eid, "Demo contact +1-202-555-0109")
        for eid, name, dept, title, manager, salary in staff
    ])
    insert("customers", "id,name,contact,email,address,credit_limit_cents,tax_reference", [
        ("CUS-001", "Acorn Design Studio", "Avery Stone", "avery@acorn.example", "10 Sample Lane, Example City", 500000, "SYNTHETIC-TAX-001"),
        ("CUS-002", "Beacon Community School", "Riley West", "riley@beacon.example", "20 Sample Lane, Example City", 1200000, "SYNTHETIC-TAX-002"),
        ("CUS-003", "Cedar Legal Group", "Finley North", "finley@cedar.example", "30 Sample Lane, Example City", 800000, "SYNTHETIC-TAX-003"),
        ("CUS-004", "Juniper Printworks", "Hayden Fox", "hayden@juniper.example", "40 Sample Lane, Example City", 2000000, "SYNTHETIC-TAX-004"),
        ("CUS-005", "Orchard Market", "Emery Lake", "emery@orchard.example", "50 Sample Lane, Example City", 300000, "SYNTHETIC-TAX-005"),
        ("CUS-006", "Willow Health Supply", "Rowan Dale", "rowan@willow.example", "60 Sample Lane, Example City", 900000, "SYNTHETIC-TAX-006"),
    ])
    insert("products", "sku,name,unit,price_cents,on_hand,reserved,reorder_point,location", [
        ("COPY-A4", "Everyday A4 Copy Paper 80 gsm", "carton of 5 reams", 2499, 240, 10, 80, "A-01"),
        ("REC-A4", "Recycled A4 Copy Paper 80 gsm", "carton of 5 reams", 2999, 65, 0, 70, "A-02"),
        ("PREM-A4", "Premium A4 Presentation Paper 100 gsm", "carton of 5 reams", 3999, 90, 0, 30, "A-03"),
        ("KRAFT-ROLL", "Kraft Packing Paper 50 m", "roll", 1899, 112, 0, 30, "B-01"),
        ("ENV-DL", "Recycled DL Envelopes", "box of 500", 2199, 75, 0, 25, "B-02"),
        ("CARD-A3", "A3 White Cardstock 200 gsm", "pack of 250", 3499, 40, 0, 15, "B-03"),
        ("RAW-PULP", "Recycled Pulp Feedstock", "kg", 150, 1400, 0, 500, "RAW-01"),
        ("RAW-WRAP", "Pallet Stretch Wrap", "roll", 999, 18, 0, 20, "RAW-02"),
    ])
    insert("orders", "id,customer_id,status,created_at,total_cents", [
        ("ORD-1001", "CUS-001", "confirmed", day(-15) + "T09:00:00+00:00", 24990),
        ("ORD-1002", "CUS-002", "shipped", day(-2) + "T10:00:00+00:00", 14995),
        ("ORD-1003", "CUS-004", "delivered", day(-7) + "T11:00:00+00:00", 15192),
    ])
    insert("order_lines", "order_id,sku,quantity,unit_price_cents", [
        ("ORD-1001", "COPY-A4", 10, 2499), ("ORD-1002", "REC-A4", 5, 2999),
        ("ORD-1003", "KRAFT-ROLL", 8, 1899),
    ])
    insert("invoices", "id,order_id,customer_id,due_date,total_cents,paid_cents,status", [
        ("INV-1001", "ORD-1001", "CUS-001", day(-10), 24990, 5000, "open"),
        ("INV-1002", "ORD-1002", "CUS-002", day(10), 14995, 0, "open"),
        ("INV-1003", "ORD-1003", "CUS-004", day(-1), 15192, 15192, "paid"),
    ])
    insert("payments", "id,invoice_id,amount_cents,reference,received_at", [
        ("PAY-1001", "INV-1001", 5000, "DEMO-ACORN-DEPOSIT", day(-12)),
        ("PAY-1003", "INV-1003", 15192, "DEMO-JUNIPER-PAID", day(-3)),
    ])
    insert("notes", "id,customer_id,author,text,created_at", [
        ("NOTE-001", "CUS-001", "sales", "Confidential: Acorn requested a payment plan. Do not include in public replies.", day(-5)),
        ("NOTE-002", "CUS-002", "sales", "Term-time deliveries only; school office closes at 15:00.", day(-3)),
    ])
    insert("deals", "id,customer_id,title,value_cents,stage,owner_id", [
        ("DEAL-001", "CUS-002", "School-year recycled paper agreement", 1800000, "proposal", "EMP-003"),
        ("DEAL-002", "CUS-006", "Quarterly packaging pilot", 450000, "qualified", "EMP-003"),
    ])
    insert("expenses", "id,category,description,amount_cents,incurred_on", [
        ("EXP-001", "fuel", "Truck 1 demo fuel receipt", 12450, day(-2)),
        ("EXP-002", "facilities", "Warehouse fictional monthly rent", 450000, day(-5)),
    ])
    insert("tickets", "id,customer_id,order_id,subject,status,assigned_to,created_at", [
        ("TKT-1001", "CUS-001", "ORD-1001", "Where is our paper? Please confirm delivery.", "open", "EMP-005", day(-1)),
        ("TKT-1002", "CUS-002", "ORD-1002", "Please deliver before the school office closes", "pending", "EMP-005", day(-1)),
    ])
    insert("replies", "id,ticket_id,author,text,created_at", [
        ("REPLY-001", "TKT-1002", "support", "We have passed the 15:00 delivery window to dispatch.", day(-1)),
    ])
    insert("time_off", "id,employee_id,start_date,end_date,reason,status", [
        ("PTO-001", "EMP-006", day(7), day(9), "Fictional family appointment", "pending"),
        ("PTO-002", "EMP-010", day(14), day(16), "Annual leave", "approved"),
    ])
    insert("trucks", "id,name,capacity_units", [("TRK-01", "Paper Plane", 120), ("TRK-02", "Ream Machine", 80)])
    insert("routes", "id,truck_id,driver_id,delivery_date,name,status", [
        ("RTE-1001", "TRK-01", "EMP-006", day(0), "Eastside school run", "loaded"),
        ("RTE-1002", "TRK-02", "EMP-009", day(-4), "Print district", "delivered"),
        ("RTE-1003", "TRK-02", "EMP-009", day(0), "Downtown studios", "planned"),
    ])
    insert("shipments", "id,order_id,route_id,status,shipped_at,delivered_at", [
        ("SHP-1002", "ORD-1002", "RTE-1001", "loaded", day(-1), None),
        ("SHP-1003", "ORD-1003", "RTE-1002", "delivered", day(-4), day(-4)),
    ])
    insert("suppliers", "id,name,contact,email,bank_account,terms", [
        ("SUP-001", "Northstar Fiber Cooperative", "Parker Grey", "orders@northstar.example", "FICTIONAL-SUPPLIER-001", "Net 30; USD"),
        ("SUP-002", "Evergreen Mill Supply", "Skyler Moss", "orders@evergreen.example", "FICTIONAL-SUPPLIER-002", "Net 15; USD"),
    ])
    insert("supplier_products", "supplier_id,sku,unit_cost_cents", [
        ("SUP-001", "RAW-PULP", 95), ("SUP-001", "REC-A4", 1800),
        ("SUP-002", "RAW-WRAP", 600), ("SUP-002", "COPY-A4", 1450),
        ("SUP-002", "PREM-A4", 2500), ("SUP-002", "KRAFT-ROLL", 1100),
    ])
    insert("purchase_orders", "id,supplier_id,sku,quantity,unit_cost_cents,status,expected_date", [
        ("PO-1001", "SUP-001", "REC-A4", 100, 1800, "ordered", day(2)),
        ("PO-1002", "SUP-002", "RAW-WRAP", 30, 600, "ordered", day(1)),
    ])
    manifest = json.loads((root / "documents" / "manifest.json").read_text(encoding="utf-8"))
    for document in manifest:
        body = (root / "documents" / document["source"]).read_text(encoding="utf-8")
        db.execute("INSERT INTO documents VALUES (?,?,?,?,?,?)", (
            document["id"], document["title"], document["classification"],
            document["audience"], document["source"], body,
        ))
    db.execute("INSERT INTO document_search(document_search) VALUES ('rebuild')")
