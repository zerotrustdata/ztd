# A shift at Zero Trust Paper Company

Start the company using the [runbook](README.md). Commands below run from the repository root against the combined server. They show the target application's requests; for a governed session, have your agent issue the corresponding `paper.py call` argv through ztd's `run_shell_command`, as described in the runbook. Reset to the opening dataset before repeating a shift. Use the same key for an exact retry; use a new key for a distinct business operation.

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

To complete delivery, use `POST /dispatch/shipments/{returned-shipment-id}/deliver` with an empty body, a fresh stable key, and `ops`. The `driver` identity is Casey (`EMP-006`) and cannot deliver this route assigned to Drew (`EMP-009`); that denial is intentional. Acorn then sees delivered in its portal. Completed routes reject further loads.

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
```

Expected: `REC-A4` starts with 65 cartons, below its reorder point of 70. The existing purchase order already covers 100 cartons. Receiving it increases stock to 165 and records one movement; it does not place another order or pay a supplier. A second receipt with a different key is rejected; an exact retry with the original key replays safely.

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
