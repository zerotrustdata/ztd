# Zero Trust Paper Company

A fictional regional paper distributor with ten employees, six business customers, two suppliers, two trucks, six sellable products, and two warehouse supplies. You are Alex Chen, the operations and systems lead. Acorn Design Studio wants its paper, Beacon Community School needs delivery before reception closes, recycled paper is below its reorder point, and finance has an overdue invoice to investigate.

This is the first organization in the [demos collection](../README.md). The infrastructure uses Python 3.10+ and the standard library, including SQLite with FTS5. No pip packages, network downloads, cloud accounts, real credentials or containers are required. Services expose JSON APIs for CLI and agent use; there are no browser application frontends in this foundation.

## Start the company

From the repository root:

```bash
python3 demos/zero-trust-paper-company/paper.py init
python3 demos/zero-trust-paper-company/paper.py serve
```

Initialization creates `demos/zero-trust-paper-company/.demo-state/company.sqlite3`, ignored by Git. It refuses to overwrite an existing database. Serving never initializes data implicitly and binds only to `127.0.0.1:9100`. Keep that terminal running; Ctrl+C stops it. On Windows, use the installed Python 3 launcher in place of `python3` if needed.

In another terminal:

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /health
python3 demos/zero-trust-paper-company/paper.py call GET /storefront/products
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/stock --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /documents/search?q=delivery --role support
```

Use `paper.py --help` and each subcommand's `--help` for options. `call GET /` discovers the methods and paths exposed by the running service. Request bodies use named dataclasses beside each handler under [systems](systems/); the server rejects extra fields and type coercion. See the [scenarios](SCENARIOS.md) for complete requests and expected results.

## The nine systems

| System | Base path | What you can do |
| --- | --- | --- |
| Storefront / order portal | `/storefront` | Browse products; place, inspect and cancel orders; track shipments |
| CRM | `/crm` | Onboard finance-approved customers; read account history; add confidential notes; create and advance deals |
| Accounting | `/accounting` | Inspect invoices, received payments and past-due balances; record payments and expenses |
| Inventory / warehouse | `/inventory` | Inspect availability, reservations, orders, shipments and movements; record stock counts |
| Support desk | `/support` | Open tickets; reply; assign staff; resolve and reopen cases |
| Document library / intranet | `/documents` | List and retrieve authorized documents; search public and private content with citations |
| HR portal | `/hr` | Browse the work directory; request and review time off; access restricted payroll |
| Dispatch board | `/dispatch` | View trucks and routes; plan routes; load orders; mark shipments delivered |
| Purchasing | `/purchasing` | Inspect suppliers and negotiated costs; create and receive purchase orders |

All nine run together by default. To model separate applications, stop the combined server and start selected systems in separate terminals against the same state directory:

```bash
python3 demos/zero-trust-paper-company/paper.py serve --service storefront --port 9101
python3 demos/zero-trust-paper-company/paper.py serve --service inventory --port 9102
```

Use `call --url http://127.0.0.1:9101` for the first server. Repeat with other `--service` values and distinct ports as needed. Each process uses its own database connections; SQLite WAL and immediate write transactions coordinate their shared company state. These HTTP listeners belong to the fictional applications. An outbound ztd Operator still exposes no inbound port.

## Identities and sensitive data

The client defaults to `public`. Use `--role` to choose a demo identity. For another HTTP client, the equivalent header is `Authorization: Bearer demo-ops`, replacing `ops` with the identity name below.

| Identity | Person or scope | Main access |
| --- | --- | --- |
| `public` | Anonymous | Product catalog and public documents |
| `customer-acorn` | Acorn, `CUS-001` | Its own orders, tickets and public documents |
| `customer-beacon` | Beacon, `CUS-002` | Its own orders, tickets and public documents |
| `ops` | Alex Chen, `EMP-002` | Warehouse, dispatch and operations runbooks |
| `sales` | Jordan Ellis, `EMP-003` | CRM, order entry and collections documents |
| `finance` | Priya Shah, `EMP-004` | Invoices, payments, expenses and financial documents |
| `support` | Sam Rivera, `EMP-005` | Tickets, customer order investigations and fulfillment documents |
| `driver` | Casey Brooks, `EMP-006` | Own routes and delivery confirmation |
| `driver-drew` | Drew Lane, `EMP-009` | Drew's own routes and delivery confirmation |
| `hr` | Taylor Quinn, `EMP-007` | Leave decisions, payroll and restricted HR documents |
| `procurement` | Jamie Park, `EMP-008` | Suppliers, purchase orders and supplier contracts |
| `admin` | Morgan Reed, `EMP-001` | All application endpoints and business events |

Staff can read the work directory and request their own leave. Non-HR staff see only their own leave requests. The exact endpoint role lists are in each system's `ROUTES`; customer and driver ownership checks also apply inside handlers.

The dataset includes salary, emergency contacts, payroll account references, supplier settlement references, negotiated prices, confidential CRM notes and a synthetic recovery code. Everything is invented. Email uses reserved `.example` domains; account references explicitly say `FICTIONAL` or `SYNTHETIC` and cannot move money.

These published demo tokens simulate application roles. Any local user who knows them can select another identity. The database and fixture checkout are also directly readable by the host user. This is an application access-control exercise, not production authentication, storage encryption or host isolation. Keep real data out of this sandbox.

## Business consistency

Order placement reserves stock and creates a net-30 invoice in one transaction. Available stock is on-hand minus reserved. Orders cannot exceed stock or the customer's remaining credit limit. Loading a truck consumes the reservation and physical stock, creates a shipment, and marks the order shipped. Delivery updates that same order. A truck's capacity counts selling units, and each truck and driver has one route per day.

Cancellation releases reservations and voids the invoice only for confirmed, unpaid orders. Payments cannot exceed the balance or reuse a payment reference. Receiving a purchase order adds stock once. Manual stock deductions cannot consume existing reservations. Leave decisions and route planning check each other for driver scheduling conflicts.

Every mutation requires `Idempotency-Key`; `call` supplies it through `--key`. Retrying an identical request with the same identity and key returns the original result without repeating effects. Reusing the key for a different request returns a conflict. Successful mutations append a business event in the same SQLite transaction; failed operations roll back. `GET /events --role admin` returns the most recent 100 business events. These events are ordinary application records, not signed ztd evidence.

## Document retrieval

[documents/manifest.json](documents/manifest.json) owns document identity, classification, audience and source file. The `.txt` files are fictional business fixture content. Initialization copies them into SQLite and builds the FTS5 index; reset rebuilds it from those files. Editing a source file does not change an already initialized company until reset.

`GET /documents/library` returns authorized metadata. `GET /documents/library/DOC-001` returns a full authorized document. `GET /documents/search?q=delivery&limit=5` searches literal terms joined with AND, filters candidates by audience in SQL, and returns the most relevant paragraph of each matching document with its source, classification, chunk identifier and document citation. `limit` ranges from 1 to 20. Private documents return 404 to unauthorized direct requests and are absent from their search results.

This is actual local text retrieval over the corpus, suitable as RAG input. It does not call an LLM, generate an answer, compute embeddings, or download a vector model. Preserve citations when an agent uses the retrieved text.

## Use the company through ztd

Follow the platform [getting-started guide](../../docs/guides/getting_started.md) and [Operator enrollment guide](../../docs/guides/connect_operator_to_gateway.md). Run this company on the same host as the executing Operator, with Python and this directory available to that host. The Operator's loopback address reaches these application listeners; your workstation's loopback does not reach a different host.

For an agent connected through `ztd mcp stdio`, request the registered `run_shell_command` tool using one executable and an argv array. From a repository-root working directory, the following is the tool's arguments object for a warehouse read:

```json
{
  "command": "python3",
  "args": ["demos/zero-trust-paper-company/paper.py", "call", "GET", "/inventory/stock", "--role", "ops"]
}
```

To receive the opening recycled-paper purchase order, submit this arguments object through the same governed tool:

```json
{
  "command": "python3",
  "args": ["demos/zero-trust-paper-company/paper.py", "call", "POST", "/purchasing/orders/PO-1001/receive", "--role", "ops", "--key", "demo-receive-po-1001"]
}
```

Set the tool's `working_dir` to the absolute repository checkout path on the executing host if its current directory differs. [protocol/docs/mcp.md](../../protocol/docs/mcp.md) describes that ingress and approval lifecycle. Use the same idempotency key when retrying an approved application operation. The application key does not replace a ztd envelope nonce, proof or approval.

An enrolled human CLI can select an owned active session from `./ztd operator list` and dispatch a command with `./ztd operator run`. The following Bash example prompts for the intended session rather than targeting every Operator:

```bash
read -r -p 'Operator session ID: ' PAPER_OPERATOR_SESSION
./ztd operator run "$PAPER_OPERATOR_SESSION" --cmd 'python3 demos/zero-trust-paper-company/paper.py call GET /inventory/stock --role ops'
```

Confirm current options with `./ztd operator run --help`. Admission and approval behavior comes from the Gateway's active posture and actual submitted shell intent; the demo does not grant or fabricate proofs. Inspect the executing Operator's local ztd evidence to verify governed execution. The `make demos-test` suite checks the simulated applications and client, and does not qualify a live Gateway/Operator governance run.

Direct `paper.py call`, HTTP, Python, SQLite or fixture-file access stays outside ztd governance. For a governed demonstration, submit actions through ztd and restrict the agent's other access using the platform's agent-launch and host controls. Merely starting these services does not mediate other tools or isolate local users.

## Reset and validation

Stop all service processes before resetting. This explicitly replaces the demo company, business events and saved idempotency results; it leaves ztd state untouched:

```bash
python3 demos/zero-trust-paper-company/paper.py reset --confirm fictional-data
python3 demos/zero-trust-paper-company/paper.py serve
```

Use `init --as-of 2030-04-15` on a fresh state directory, or `reset --confirm fictional-data --as-of 2030-04-15`, for repeatable seed dates. Past-due queries accept `as_of=2030-04-15`; newly placed orders and live events use the current host date/time. All stored money is USD cents. Initial quantities are opening balances; stock movements describe changes made after initialization.

```bash
make demos-test
```

Tests exercise actual SQLite transactions, concurrent requests, access boundaries, reset reproducibility, retrieval, loopback HTTP and the subprocess client. CI runs the same target. The simulation omits refunds, taxes, real payments, accounts-payable settlement, partial shipments/receipts, manufacturing, GPS, weight-based routing, mail delivery and real SaaS integrations. Add those as explicit workflows with invariants and tests when expanding the company.
