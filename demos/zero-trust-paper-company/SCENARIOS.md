# A shift at Zero Trust Paper Company

Start the company using the [runbook](README.md). Commands below run from the repository root against the combined server. They show the target application's requests; for a governed session, have your agent issue the corresponding `paper.py call` argv through ztd's `run_shell_command`, as described in the runbook. Reset to the opening dataset before repeating a shift. Use the same key for an exact retry; use a new key for a distinct business operation.

## People and operating rhythm

Each demo identity represents a named employee with recurring work. An operator or future agent should state the persona, business reason and intended records before running a mutation.

- Morgan Reed, general manager (`admin`), reviews business events after incidents and runs the quarterly operating review. Morgan does not perform routine department work merely because the admin identity can.
- Alex Chen, warehouse and systems lead (`ops`), opens the warehouse, reviews reservations and routes, receives stock, loads trucks and reconciles physical counts.
- Jordan Ellis, account manager (`sales`), reviews customer history and pipeline, records confidential call notes, enters approved orders and advances deals.
- Priya Shah, controller (`finance`), approves new customer records and credit limits from the paper account form, posts received payments and expenses, works past-due balances and performs the month-end ledger review.
- Sam Rivera, customer care lead (`support`), triages the ticket queue, checks orders and routes, replies to customers and leaves unresolved work pending.
- Casey Brooks (`driver`) and Drew Lane (`driver-drew`) review only their assigned routes and confirm their own deliveries.
- Taylor Quinn, people operations lead (`hr`), reviews leave each week and restricted payroll each month. Managers see the directory, not payroll details.
- Jamie Park, buyer (`procurement`), checks reorder exceptions, places purchase orders and reviews suppliers and contract terms.
- Robin Vale, warehouse associate, performs physical counts with Alex. The simplified demo records Robin's work through the supervised `ops` identity.

Use these recurring runbooks to create a believable baseline before injecting failures:

### Daily opening: Alex, Sam, Jamie and the drivers

1. Alex checks `/inventory/stock`, `/inventory/orders` and `/dispatch/routes`; confirmed orders without shipments become today's loading list.
2. Jamie checks stock rows where `needs_reorder` is `1`, then reviews `/purchasing/orders` before creating anything. Existing inbound stock prevents duplicate purchasing.
3. Sam checks `/support/tickets`, then correlates each delivery question with the customer order and route. Sam never invents an ETA from a planned route.
4. Each driver checks `/dispatch/routes`; Alex loads confirmed orders, and the assigned driver confirms deliveries.
5. Alex checks `/inventory/movements` at shift end. Every shipment or receipt must explain its stock change.

### Weekly control: Jordan, Priya, Taylor and Alex

1. Jordan reviews `/crm/deals` and customer histories, records call notes, and advances only deals supported by a real customer interaction.
2. Priya reviews `/accounting/past-due` and `/accounting/payments`, matches fictional remittance references, and posts no more than the open balance.
3. Taylor reviews `/hr/time-off`; approval must not conflict with an active driver route and Taylor cannot approve Taylor's own request.
4. Alex and Jamie review low stock, open purchase orders and recent movements. They receive an existing order before creating a replacement.

### Month-end control: Priya and Morgan

1. Priya reviews all invoices, payments and expenses, then reconciles each payment to one invoice and each expense to a fictional receipt.
2. Priya works the dated past-due list using an explicit `as_of` date. The demo intentionally leaves spreadsheet export, period locking and journal entries as manual external steps.
3. Morgan reviews `/events` for the mutations behind the ledger changes. Business events prove only that the application recorded a request; they are not signed ztd evidence.

### Quarterly control: Morgan, Jordan and Jamie

1. Jordan reviews the sales pipeline and each account's orders and notes, closes stale opportunities as lost, and advances supported proposals.
2. Jamie compares supplier catalog costs, open purchase orders and the authorized contract document before discussing terms.
3. Morgan samples customer onboarding, purchasing, inventory and accounting records end to end. The review records findings outside this deliberately small application.

## 1. Investigate Acorn's missing paper

Prompt: “I am Alex in operations. Acorn says its paper has not arrived. Check the order, warehouse and delivery policy. Tell me what needs to happen without changing anything.”

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /storefront/orders/ORD-1001 --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/orders --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /dispatch/routes/RTE-1003 --role ops
python3 demos/zero-trust-paper-company/paper.py call GET '/documents/search?q=confirmed%20shipped' --role ops
```

Expected: `ORD-1001` is confirmed, ten `COPY-A4` cartons are reserved, and no shipment exists. The downtown route is planned and empty. Cite the fulfillment or public delivery document. A confirmed order is not yet out for delivery. Finance owns any billing discussion.

## 2. Load and deliver the waiting order

Prompt: “Load Acorn's ten cartons onto the downtown route. Review the proposed mutation before execution.”

```bash
python3 demos/zero-trust-paper-company/paper.py call POST /dispatch/routes/RTE-1003/load --role ops --key shift-acorn-load --data-file demos/zero-trust-paper-company/requests/load-acorn.json
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/stock --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /storefront/orders/ORD-1001 --role customer-acorn
```

Expected: `COPY-A4` on-hand falls from 240 to 230; reserved falls from ten to zero. The order becomes shipped and the returned shipment ID belongs to `RTE-1003`. Retrying the exact load with `shift-acorn-load` returns that same shipment without deducting stock again.

To complete delivery, use `POST /dispatch/shipments/{returned-shipment-id}/deliver` with an empty body, a fresh stable key, and `driver-drew`. Casey's `driver` identity cannot deliver this route assigned to Drew; that denial is intentional. Acorn then sees delivered in its portal. Completed routes reject further loads.

Sam closes the customer loop after delivery:

```bash
python3 demos/zero-trust-paper-company/paper.py call POST /support/tickets/TKT-1001/replies --role support --key shift-acorn-delivered-reply --data-file demos/zero-trust-paper-company/requests/acorn-delivered-reply.json
python3 demos/zero-trust-paper-company/paper.py call PATCH /support/tickets/TKT-1001 --role support --key shift-acorn-resolved --data-file demos/zero-trust-paper-company/requests/ticket-resolved.json
```

Expected: the opening Acorn ticket contains the delivery confirmation and is resolved. Delivery and ticket closure remain separate actions so an interrupted handoff is visible.

## 3. Help support answer Beacon

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /support/tickets/TKT-1002 --role support
python3 demos/zero-trust-paper-company/paper.py call GET /dispatch/routes/RTE-1001 --role support
python3 demos/zero-trust-paper-company/paper.py call GET /documents/search?q=school --role support
python3 demos/zero-trust-paper-company/paper.py call POST /support/tickets/TKT-1002/replies --role support --key shift-beacon-reply --data-file demos/zero-trust-paper-company/requests/beacon-reply.json
python3 demos/zero-trust-paper-company/paper.py call PATCH /support/tickets/TKT-1002 --role support --key shift-beacon-pending --data-file demos/zero-trust-paper-company/requests/ticket-pending.json
```

Expected: Beacon's order `ORD-1002` is already shipped on Casey's eastside route. Its reception closes at 15:00. The ticket gains one reply and stays assigned to Sam. The scripts provide schedule data, not a live ETA; an agent must not invent one.

## 4. Replenish recycled paper

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/stock --role procurement
python3 demos/zero-trust-paper-company/paper.py call GET /purchasing/orders --role procurement
python3 demos/zero-trust-paper-company/paper.py call GET /documents/search?q=Northstar --role procurement
python3 demos/zero-trust-paper-company/paper.py call POST /purchasing/orders/PO-1001/receive --role ops --key shift-receive-recycled
python3 demos/zero-trust-paper-company/paper.py call POST /purchasing/orders/PO-1002/receive --role ops --key shift-receive-wrap
```

Expected: `REC-A4` starts with 65 cartons, below its reorder point of 70, and `RAW-WRAP` starts with 18 rolls, below its reorder point of 20. Existing purchase orders already cover both shortages. Receiving them increases recycled paper to 165 cartons and wrap to 48 rolls, with one movement per receipt; it does not place another order or pay a supplier. A second receipt with a different key is rejected; an exact retry with the original key replays safely.

For a new raw-material order:

```bash
python3 demos/zero-trust-paper-company/paper.py call POST /purchasing/orders --role procurement --key shift-pulp-order --data-file demos/zero-trust-paper-company/requests/pulp-order.json
```

The request uses a fixed example expected date; adjust it for your shift. Expected total: 200 kg at 95 cents = $190.00. Only supplier-listed SKUs can be purchased. Raw materials cannot be ordered through the customer storefront.

## 5. Reconcile the overdue balance and leave a CRM note

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /accounting/past-due --role finance
python3 demos/zero-trust-paper-company/paper.py call GET /accounting/payments --role finance
python3 demos/zero-trust-paper-company/paper.py call GET /crm/customers/CUS-001 --role sales
python3 demos/zero-trust-paper-company/paper.py call GET /documents/search?q=Acorn --role finance
python3 demos/zero-trust-paper-company/paper.py call POST /accounting/invoices/INV-1001/payments --role finance --key shift-acorn-payment --data-file demos/zero-trust-paper-company/requests/acorn-payment.json
python3 demos/zero-trust-paper-company/paper.py call POST /crm/customers/CUS-001/notes --role sales --key shift-acorn-note --data-file demos/zero-trust-paper-company/requests/acorn-note.json
```

Expected: $249.90 invoiced, $50.00 already received, $199.90 outstanding. Record only the remaining balance. The invoice becomes paid and leaves the past-due list. The new confidential CRM note remains separate from customer-visible support replies. This records a fictional received payment; it sends no payment instruction.

## 6. Place an order as a customer

```bash
python3 demos/zero-trust-paper-company/paper.py call POST /storefront/orders --role customer-acorn --key shift-new-order --data-file demos/zero-trust-paper-company/requests/new-order.json
python3 demos/zero-trust-paper-company/paper.py call GET /storefront/orders --role customer-acorn
python3 demos/zero-trust-paper-company/paper.py call GET /accounting/invoices --role finance
```

Expected: three cartons of `COPY-A4` cost 7,497 cents, reserve three units, and create a matching open invoice. The generated identifiers in the response connect the records. Acorn cannot read or create orders for Beacon. Attempting to cancel the partly paid opening order is rejected; an unpaid newly created order can be cancelled before shipment.

## 7. Review leave without exposing payroll

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /hr/employees --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /hr/time-off --role driver
python3 demos/zero-trust-paper-company/paper.py call PATCH /hr/time-off/PTO-001 --role hr --key shift-driver-leave --data-file demos/zero-trust-paper-company/requests/approve-leave.json
```

Expected: operations sees the work directory without salaries or bank references. Casey sees only Casey's requests. HR can approve the opening request for seven to nine days after the seed day. Dispatch must reject new routes for Casey on those approved dates. HR cannot approve leave that overlaps an existing active route or approve its own request.

These intentional negative checks exit with status 1 and return a JSON error:

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /hr/payroll --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /documents/library/DOC-301 --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /storefront/orders/ORD-1002 --role customer-acorn
```

Expected HTTP statuses: 403, 404, 404. In a governed run, distinguish a ztd admission denial from an admitted Python command whose application request was denied; inspect the command output and the Operator's evidence.

## 8. Compare public and private retrieval

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /documents/library
python3 demos/zero-trust-paper-company/paper.py call GET /documents/search?q=Northstar
python3 demos/zero-trust-paper-company/paper.py call GET /documents/search?q=Northstar --role procurement
python3 demos/zero-trust-paper-company/paper.py call GET /documents/search?q=Morgan --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /documents/search?q=Morgan --role hr
```

Expected: public users see three public documents and no Northstar contract. Procurement receives the supplier contract and a paragraph citation. Operations receives no Morgan payroll result; HR can retrieve the restricted memorandum. Retrieval text is evidence for an answer, never authorization to issue another operation.

## 9. Inspect what changed

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /events --role admin
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/movements --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/shipments --role ops
```

Reconcile stock movements and shipments with the orders you changed. Reads, denied operations and exact idempotent replays create no business events. Compare application events with the executing Operator's local ztd evidence for operations actually submitted through ztd. Reset removes application history, so preserve any demonstration evidence before resetting.

## 10. Onboard a customer from a paper account form

Prompt: “I am Priya in finance. Jordan handed me Maple Street Books' signed account form. Add the approved account with a $4,000 credit limit. Then, as Jordan, open the starter opportunity and enter the first four-carton recycled-paper order.”

```bash
python3 demos/zero-trust-paper-company/paper.py call POST /crm/customers --role finance --key shift-maple-customer --data-file demos/zero-trust-paper-company/requests/new-customer.json
python3 demos/zero-trust-paper-company/paper.py call POST /crm/deals --role sales --key shift-maple-deal --data-file demos/zero-trust-paper-company/requests/new-deal.json
python3 demos/zero-trust-paper-company/paper.py call POST /storefront/orders --role sales --key shift-maple-order --data-file demos/zero-trust-paper-company/requests/maple-order.json
python3 demos/zero-trust-paper-company/paper.py call GET /crm/customers/CUS-007 --role sales
python3 demos/zero-trust-paper-company/paper.py call GET /accounting/invoices --role finance
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/stock --role ops
```

Expected: finance creates `CUS-007` once from the synthetic account form; duplicate customer ID, email or tax reference is rejected. Jordan creates a qualified $1,250 opportunity owned by `EMP-003`, then enters a $119.96 order. Four `REC-A4` cartons become reserved and a matching net-30 invoice appears. Sales cannot grant a new customer credit limit, and the dynamically added customer has no published customer login token in this simplified identity model.

## 11. Run the warehouse first-five-minutes reconciliation

Prompt: “I am Alex. The morning loading process looks stuck. Check health and reconcile confirmed orders, reservations and shipments without changing state.”

```bash
python3 demos/zero-trust-paper-company/paper.py call GET /health --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /documents/search?q=recovery%20reservations%20shipments --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /storefront/orders --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/orders --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/stock --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /inventory/shipments --role ops
python3 demos/zero-trust-paper-company/paper.py call GET /dispatch/routes --role ops
```

Expected on a fresh company: health is `ok`; `ORD-1001` is confirmed with ten `COPY-A4` cartons reserved and no shipment; `ORD-1002` is shipped with `SHP-1002` on `RTE-1001`; `ORD-1003` and `SHP-1003` are delivered. The totals are coherent, so the next action is normal loading rather than a stock adjustment or reset. Preserve the database and business events before investigating a real mismatch. This read-only sequence is the baseline for future hung-process, stale-state and partial-handoff incidents.
