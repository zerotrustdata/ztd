"""Business invariants and real HTTP/client integration; no platform mocks."""

from concurrent.futures import ThreadPoolExecutor
from datetime import date
from http.client import HTTPConnection
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from urllib.error import HTTPError
from urllib.request import Request as HTTPRequest, build_opener, ProxyHandler

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from api import Application, Request, parse_json
from core import APIError
from database import connect, initialize, reset
from server import make_server

AS_OF = date(2030, 4, 15)


class CompanyFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix=".test-", dir=ROOT)
        self.addCleanup(self.temp.cleanup)
        self.state = Path(self.temp.name)
        initialize(self.state, AS_OF)
        self.app = Application(self.state)
        self.sequence = 0

    def call(self, method, path, role="admin", body=None, key=None):
        self.sequence += 1
        auth = "" if role == "public" else "Bearer demo-" + role
        result = self.app.handle(Request(method, path, auth, body, key or f"test-{self.sequence}"))
        return result.data

    def get(self, path, role="admin"):
        return self.call("GET", path, role)

    def create_order(self, quantity=3, role="sales", key=None):
        return self.call("POST", "/storefront/orders", role, {"customer_id": "CUS-001", "items": [{"sku": "COPY-A4", "quantity": quantity}]}, key)

    def assert_error(self, status, function, *args, **kwargs):
        with self.assertRaises(APIError) as error:
            function(*args, **kwargs)
        self.assertEqual(error.exception.status, status)


class CompanyCase(CompanyFixture):
    def test_seed_is_relationally_consistent_and_reset_is_repeatable(self):
        def snapshot():
            with connect(self.state) as db:
                self.assertEqual(list(db.execute("PRAGMA foreign_key_check")), [])
                self.assertEqual(db.execute("PRAGMA integrity_check").fetchone()[0], "ok")
                self.assertEqual(db.execute("SELECT sum(reserved) FROM products").fetchone()[0], 10)
                self.assertEqual(db.execute("SELECT sum(paid_cents) FROM invoices").fetchone()[0], 20192)
                return {table: [tuple(row) for row in db.execute(f"SELECT * FROM {table} ORDER BY 1")]
                        for table in ("employees", "products", "orders", "invoices", "payments", "documents", "routes")}
        original = snapshot()
        self.create_order()
        self.call("POST", "/purchasing/orders/PO-1001/receive", "ops", {})
        reset(self.state, AS_OF)
        self.assertEqual(snapshot(), original)
        self.assertEqual(self.get("/events"), [])
        self.assertEqual(len(self.get("/documents/search?q=paper")["results"]), 5)

    def test_initialization_refuses_to_overwrite_and_reads_never_create_state(self):
        self.assert_error(409, initialize, self.state, AS_OF)
        self.assert_error(503, Application(self.state / "missing").handle, Request("GET", "/health"))
        self.assertFalse((self.state / "missing").exists())
        self.assert_error(400, initialize, self.state / ".ztd", AS_OF)

    def test_customer_order_reserves_stock_and_issues_exact_invoice(self):
        order = self.create_order(role="customer-acorn")
        self.assertEqual(order["total_cents"], 7497)
        stock = next(p for p in self.get("/inventory/stock", "ops") if p["sku"] == "COPY-A4")
        self.assertEqual((stock["on_hand"], stock["reserved"], stock["available"]), (240, 13, 227))
        invoice = next(i for i in self.get("/accounting/invoices", "finance") if i["id"] == order["invoice_id"])
        self.assertEqual(invoice["total_cents"], 7497)
        self.assertEqual(invoice["order_id"], order["id"])

    def test_invalid_multiline_order_leaves_no_partial_writes(self):
        before = self.get("/inventory/stock")
        body = {"customer_id": "CUS-001", "items": [{"sku": "COPY-A4", "quantity": 2}, {"sku": "REC-A4", "quantity": 999}]}
        self.assert_error(409, self.call, "POST", "/storefront/orders", "sales", body)
        self.assertEqual(self.get("/inventory/stock"), before)
        self.assertEqual(len(self.get("/storefront/orders")), 3)
        self.assertEqual(self.get("/events"), [])

    def test_stock_and_credit_limits_reject_orders(self):
        self.assert_error(409, self.create_order, 231)
        with connect(self.state) as db:
            db.execute("UPDATE customers SET credit_limit_cents=20000 WHERE id='CUS-001'")
            db.commit()
        self.assert_error(409, self.create_order, 1)
        self.assertEqual(len(self.get("/accounting/invoices")), 3)

    def test_typed_payloads_reject_coercion_unknown_fields_and_duplicate_lines(self):
        for quantity in (True, "2", 2.5, 0, -1):
            with self.subTest(quantity=quantity):
                self.assert_error(400, self.create_order, quantity)
        self.assert_error(400, self.call, "POST", "/inventory/adjustments", "ops", {"sku": "COPY-A4", "quantity": 1, "reason": "count", "admin": True})
        self.assert_error(400, self.call, "POST", "/storefront/orders", "sales", {"customer_id": "CUS-001", "items": [{"sku": "COPY-A4", "quantity": 1}] * 2})
        self.assert_error(400, self.call, "POST", "/storefront/orders", "sales", {"customer_id": "CUS-001"})
        self.assert_error(400, parse_json, '{"quantity":1,"quantity":2}')
        self.assert_error(400, parse_json, '{"quantity":NaN}')

    def test_idempotent_replay_does_not_duplicate_effects_and_key_reuse_fails(self):
        order = self.create_order(key="same-order")
        self.assertEqual(self.create_order(key="same-order"), order)
        self.assertEqual(len(self.get("/events")), 1)
        self.assert_error(409, self.create_order, 4, key="same-order")
        self.assertEqual(len(self.get("/accounting/invoices")), 4)

    def test_concurrent_orders_cannot_oversell_shared_stock(self):
        def buy(index):
            try:
                result = self.app.handle(Request("POST", "/storefront/orders", "Bearer demo-sales",
                    {"customer_id": "CUS-002", "items": [{"sku": "CARD-A3", "quantity": 30}]}, f"concurrent-{index}"))
                return result.status
            except APIError as error:
                return error.status
        with ThreadPoolExecutor(max_workers=2) as pool:
            self.assertEqual(sorted(pool.map(buy, range(2))), [201, 409])
        stock = next(p for p in self.get("/inventory/stock") if p["sku"] == "CARD-A3")
        self.assertEqual(stock["reserved"], 30)

    def test_concurrent_idempotent_requests_have_one_effect(self):
        def buy(_):
            return self.app.handle(Request("POST", "/storefront/orders", "Bearer demo-sales",
                {"customer_id": "CUS-001", "items": [{"sku": "COPY-A4", "quantity": 2}]}, "concurrent-replay")).data
        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(buy, range(2)))
        self.assertEqual(results[0], results[1])
        self.assertEqual(len(self.get("/events")), 1)

    def test_order_cancellation_releases_stock_and_voids_invoice(self):
        order = self.create_order()
        self.call("POST", f'/storefront/orders/{order["id"]}/cancel', "customer-acorn", {})
        self.assertEqual(self.get(f'/storefront/orders/{order["id"]}')["status"], "cancelled")
        self.assertEqual(next(i for i in self.get("/accounting/invoices") if i["id"] == order["invoice_id"])["status"], "void")
        self.assertEqual(next(p for p in self.get("/inventory/stock") if p["sku"] == "COPY-A4")["reserved"], 10)
        self.assert_error(409, self.call, "POST", "/storefront/orders/ORD-1001/cancel", "sales", {})

    def test_payments_reconcile_and_overpayments_and_duplicate_references_fail(self):
        due = self.get("/accounting/past-due?as_of=2030-04-15", "finance")
        self.assertEqual([(i["id"], i["balance_cents"]) for i in due], [("INV-1001", 19990)])
        paid = self.call("POST", "/accounting/invoices/INV-1001/payments", "finance", {"amount_cents": 19990, "reference": "DEMO-FINAL"})
        self.assertEqual(paid["balance_cents"], 0)
        self.assertEqual(self.get("/accounting/past-due?as_of=2030-04-15"), [])
        self.assert_error(409, self.call, "POST", "/accounting/invoices/INV-1002/payments", "finance", {"amount_cents": 14996, "reference": "DEMO-OVER"})
        self.assert_error(409, self.call, "POST", "/accounting/invoices/INV-1002/payments", "finance", {"amount_cents": 1, "reference": "DEMO-FINAL"})

    def test_load_and_delivery_update_warehouse_and_customer_status_once(self):
        order = self.create_order()
        shipment = self.call("POST", "/dispatch/routes/RTE-1001/load", "ops", {"order_id": order["id"]})
        stock = next(p for p in self.get("/inventory/stock") if p["sku"] == "COPY-A4")
        self.assertEqual((stock["on_hand"], stock["reserved"]), (237, 10))
        self.assertEqual(self.get(f'/storefront/orders/{order["id"]}', "customer-acorn")["status"], "shipped")
        self.assert_error(409, self.call, "POST", "/dispatch/routes/RTE-1001/load", "ops", {"order_id": order["id"]})
        self.call("POST", f'/dispatch/shipments/{shipment["id"]}/deliver', "driver", {})
        self.assertEqual(self.get(f'/storefront/orders/{order["id"]}')["status"], "delivered")
        self.assertEqual(self.get("/dispatch/routes/RTE-1001")["status"], "loaded")
        self.call("POST", "/dispatch/shipments/SHP-1002/deliver", "driver", {})
        self.assertEqual(self.get("/dispatch/routes/RTE-1001")["status"], "delivered")
        self.assertEqual(len(self.get("/inventory/movements")), 1)

    def test_capacity_and_driver_scope_reject_delivery_changes(self):
        order = self.create_order(116)
        self.assert_error(409, self.call, "POST", "/dispatch/routes/RTE-1001/load", "ops", {"order_id": order["id"]})
        self.assertEqual(self.get(f'/storefront/orders/{order["id"]}')["status"], "confirmed")
        self.assert_error(404, self.get, "/dispatch/routes/RTE-1003", "driver")
        self.assert_error(404, self.call, "POST", "/dispatch/shipments/SHP-1003/deliver", "driver", {})

    def test_receiving_po_adds_stock_once_and_stock_count_preserves_reservations(self):
        self.call("POST", "/purchasing/orders/PO-1001/receive", "ops", {})
        self.assertEqual(next(p for p in self.get("/inventory/stock") if p["sku"] == "REC-A4")["on_hand"], 165)
        self.assert_error(409, self.call, "POST", "/purchasing/orders/PO-1001/receive", "ops", {})
        self.assert_error(409, self.call, "POST", "/inventory/adjustments", "ops", {"sku": "COPY-A4", "quantity": -231, "reason": "count discrepancy"})
        self.assertEqual(len(self.get("/inventory/movements")), 1)

    def test_role_boundaries_and_customer_isolation(self):
        for role in ("public", "ops", "customer-acorn", "finance"):
            self.assert_error(403, self.get, "/hr/payroll", role)
        self.assertEqual(len(self.get("/hr/payroll", "hr")), 10)
        self.assertNotIn("salary_cents", self.get("/hr/employees", "ops")[0])
        self.assertNotIn("bank_account", self.get("/purchasing/suppliers", "ops")[0])
        self.assertEqual([o["id"] for o in self.get("/storefront/orders", "customer-acorn")], ["ORD-1001"])
        self.assert_error(404, self.get, "/storefront/orders/ORD-1002", "customer-acorn")
        self.assert_error(404, self.get, "/support/tickets/TKT-1002", "customer-acorn")
        self.assert_error(404, self.create_order, role="customer-beacon")
        self.assert_error(403, self.get, "/events", "ops")
        self.assert_error(401, self.app.handle, Request("GET", "/storefront/products", "Bearer wrong"))

    def test_retrieval_never_returns_inaccessible_titles_content_or_direct_documents(self):
        public = self.get("/documents/library", "public")
        self.assertEqual({d["id"] for d in public}, {"DOC-001", "DOC-002", "DOC-003"})
        for role in ("public", "ops", "customer-acorn", "finance"):
            with self.subTest(role=role):
                self.assertEqual(self.get("/documents/search?q=Morgan", role)["results"], [])
                self.assert_error(404, self.get, "/documents/library/DOC-301", role)
        self.assertIn("DOC-301", {hit["id"] for hit in self.get("/documents/search?q=payroll", "hr")["results"]})
        contract = self.get("/documents/search?q=Northstar", "procurement")["results"]
        self.assertEqual(contract[0]["id"], "DOC-201")
        self.assertIn("#p", contract[0]["citation"])
        self.assertEqual(self.get("/documents/search?q=Northstar", "public")["results"], [])
        self.assert_error(400, self.get, "/documents/search?q=paper&limit=zero")

    def test_support_crm_and_purchasing_actions_persist(self):
        ticket = self.call("POST", "/support/tickets", "customer-acorn", {"customer_id": "CUS-001", "order_id": "ORD-1001", "subject": "Delivery", "message": "Please check status"})
        self.call("POST", f'/support/tickets/{ticket["id"]}/replies', "support", {"text": "Checking with dispatch"})
        self.call("PATCH", f'/support/tickets/{ticket["id"]}', "support", {"status": "resolved", "assigned_to": "EMP-005"})
        self.assertEqual(len(self.get(f'/support/tickets/{ticket["id"]}', "customer-acorn")["replies"]), 2)
        self.assert_error(409, self.call, "POST", f'/support/tickets/{ticket["id"]}/replies', "customer-acorn", {"text": "Thanks"})
        self.call("POST", "/crm/customers/CUS-001/notes", "sales", {"text": "Synthetic account review"})
        self.assertEqual(len(self.get("/crm/customers/CUS-001", "sales")["notes"]), 2)
        self.call("PATCH", "/crm/deals/DEAL-001", "sales", {"stage": "won"})
        self.assert_error(409, self.call, "PATCH", "/crm/deals/DEAL-001", "sales", {"stage": "proposal"})
        purchase = self.call("POST", "/purchasing/orders", "procurement", {"supplier_id": "SUP-001", "sku": "RAW-PULP", "quantity": 200, "expected_date": "2030-04-20"})
        self.assertEqual(purchase["total_cents"], 19000)
        self.call("POST", "/accounting/expenses", "finance", {"category": "fuel", "description": "Fictional receipt", "amount_cents": 4200, "incurred_on": "2030-04-15"})
        self.assertEqual(len(self.get("/accounting/expenses")), 3)

    def test_leave_approval_and_route_planning_respect_each_other(self):
        leave = self.call("POST", "/hr/time-off", "driver", {"employee_id": "EMP-006", "start_date": "2030-05-01", "end_date": "2030-05-03", "reason": "Annual leave"})
        self.call("PATCH", f'/hr/time-off/{leave["id"]}', "hr", {"status": "approved"})
        self.assert_error(409, self.call, "POST", "/dispatch/routes", "ops", {"truck_id": "TRK-01", "driver_id": "EMP-006", "delivery_date": "2030-05-02", "name": "Blocked route"})
        conflict = self.call("POST", "/hr/time-off", "driver", {"employee_id": "EMP-006", "start_date": "2030-04-15", "end_date": "2030-04-15", "reason": "Appointment"})
        self.assert_error(409, self.call, "PATCH", f'/hr/time-off/{conflict["id"]}', "hr", {"status": "approved"})
        self.assertTrue(all(row["employee_id"] == "EMP-006" for row in self.get("/hr/time-off", "driver")))
        own = self.call("POST", "/hr/time-off", "hr", {"employee_id": "EMP-007", "start_date": "2030-06-01", "end_date": "2030-06-02", "reason": "Leave"})
        self.assert_error(403, self.call, "PATCH", f'/hr/time-off/{own["id"]}', "hr", {"status": "approved"})

    def test_independently_selected_services_share_company_state(self):
        store = Application(self.state, "storefront")
        warehouse = Application(self.state, "inventory")
        result = store.handle(Request("POST", "/storefront/orders", "Bearer demo-sales", {"customer_id": "CUS-001", "items": [{"sku": "COPY-A4", "quantity": 2}]}, "separate-service"))
        stock = warehouse.handle(Request("GET", "/inventory/stock", "Bearer demo-ops")).data
        self.assertEqual(next(p for p in stock if p["sku"] == "COPY-A4")["reserved"], 12)
        self.assertEqual(result.status, 201)
        self.assert_error(404, warehouse.handle, Request("GET", "/crm/customers", "Bearer demo-admin"))


class HTTPCase(CompanyFixture):
    # Keep real socket checks separate from the business cases above.
    def setUp(self):
        super().setUp()
        self.server = make_server(self.app, 0)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()
        self.addCleanup(self.stop_server)
        self.url = f"http://127.0.0.1:{self.server.server_port}"

    def stop_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        self.assertFalse(self.thread.is_alive())

    def test_http_and_cli_run_the_real_order_workflow(self):
        args = [sys.executable, str(ROOT / "paper.py"), "call", "POST", "/storefront/orders", "--url", self.url,
                "--role", "customer-acorn", "--key", "http-order", "--data", json.dumps({"customer_id": "CUS-001", "items": [{"sku": "COPY-A4", "quantity": 2}]})]
        result = subprocess.run(args, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        order = json.loads(result.stdout)
        self.assertTrue(order["fictional"])
        replay = subprocess.run(args, capture_output=True, text=True, timeout=15)
        self.assertEqual(json.loads(replay.stdout), order)
        self.assertEqual(len(self.get("/events")), 1)
        result = subprocess.run([sys.executable, str(ROOT / "paper.py"), "call", "GET", "/hr/payroll", "--role", "ops", "--url", self.url], capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(json.loads(result.stderr)["error"]["code"], "forbidden")

    def test_checked_in_shift_requests_execute_across_the_company(self):
        def request(method, path, role, fixture=None):
            body = (ROOT / "requests" / fixture).read_bytes() if fixture else b"{}"
            headers = {"Authorization": "Bearer demo-" + role}
            if method != "GET":
                headers.update({"Content-Type": "application/json", "Idempotency-Key": "shift-" + (fixture or path.rsplit("/", 1)[-1])})
            opener = build_opener(ProxyHandler({}))
            with opener.open(HTTPRequest(self.url + path, data=body if method != "GET" else None, headers=headers, method=method), timeout=5) as response:
                return json.load(response)["data"]

        shipment = request("POST", "/dispatch/routes/RTE-1003/load", "ops", "load-acorn.json")
        request("POST", f'/dispatch/shipments/{shipment["id"]}/deliver', "ops")
        self.assertEqual(request("GET", "/storefront/orders/ORD-1001", "customer-acorn")["status"], "delivered")
        request("POST", "/support/tickets/TKT-1002/replies", "support", "beacon-reply.json")
        request("PATCH", "/support/tickets/TKT-1002", "support", "ticket-pending.json")
        request("POST", "/purchasing/orders/PO-1001/receive", "ops")
        request("POST", "/purchasing/orders", "procurement", "pulp-order.json")
        request("POST", "/accounting/invoices/INV-1001/payments", "finance", "acorn-payment.json")
        request("POST", "/crm/customers/CUS-001/notes", "sales", "acorn-note.json")
        order = request("POST", "/storefront/orders", "customer-acorn", "new-order.json")
        self.assertEqual(order["total_cents"], 7497)
        request("PATCH", "/hr/time-off/PTO-001", "hr", "approve-leave.json")
        self.assertEqual(request("GET", "/documents/search?q=Northstar", "procurement")["results"][0]["id"], "DOC-201")
        stock = request("GET", "/inventory/stock", "ops")
        self.assertEqual(next(p for p in stock if p["sku"] == "COPY-A4")["on_hand"], 230)
        self.assertEqual(next(p for p in stock if p["sku"] == "COPY-A4")["reserved"], 3)
        self.assertEqual(next(p for p in stock if p["sku"] == "REC-A4")["on_hand"], 165)
        self.assertEqual(len(request("GET", "/events", "admin")), 10)

    def test_http_errors_have_json_and_do_not_write(self):
        opener = build_opener(ProxyHandler({}))
        cases = [
            ("POST", "/inventory/adjustments", b'{"sku":"COPY-A4"}', {"Content-Type": "application/json", "Authorization": "Bearer demo-ops"}, 400),
            ("POST", "/inventory/adjustments", b"{}", {"Authorization": "Bearer demo-ops"}, 415),
            ("GET", "/hr/payroll", None, {}, 403),
            ("DELETE", "/storefront/products", None, {}, 405),
        ]
        for method, path, data, headers, status in cases:
            with self.subTest(method=method, path=path):
                with self.assertRaises(HTTPError) as error:
                    opener.open(HTTPRequest(self.url + path, data=data, headers=headers, method=method), timeout=5)
                with error.exception as response:
                    self.assertEqual(response.code, status)
                    self.assertEqual(response.headers["Cache-Control"], "no-store")
                    self.assertIn("error", json.load(response))
        self.assertEqual(self.get("/events"), [])

    def test_http_rejects_oversize_body_before_reading(self):
        connection = HTTPConnection("127.0.0.1", self.server.server_port, timeout=5)
        self.addCleanup(connection.close)
        connection.request("POST", "/storefront/orders", headers={"Content-Length": "65537", "Content-Type": "application/json"})
        response = connection.getresponse()
        self.assertEqual(response.status, 413)
        self.assertEqual(json.loads(response.read())["error"]["code"], "body_too_large")


if __name__ == "__main__":
    unittest.main()
