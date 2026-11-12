import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Badge, Button, Card, FormField, Input, ListPage, loadedRows, type BadgeTone, type DataTableColumn, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { createDropShip, DROP_SHIP_STATUSES, describeProblem, listDropShips, type DropShip, type DropShipInput, type DropShipStatus, type Problem } from "./api/client";
import { compareDecimal, CURRENCY, dash, DECIMAL, formatDate, formatMoney, shortId, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";
import { OrderNumber } from "../sales/OrderNumber";

const DUTY_CREATE = "fulfillment.warehouse.release";

const TONES: Record<DropShipStatus, BadgeTone> = { OPEN: "info", PARTIALLY_CONFIRMED: "warning", CONFIRMED: "success", CANCELLED: "neutral" };
const LABELS: Record<DropShipStatus, string> = { OPEN: "Open", PARTIALLY_CONFIRMED: "Partly confirmed", CONFIRMED: "Confirmed", CANCELLED: "Cancelled" };

export function DropShipStatusBadge({ status }: { status: DropShipStatus }) {
  return <Badge tone={TONES[status] ?? "neutral"}>{LABELS[status] ?? status}</Badge>;
}

const columns: DataTableColumn<DropShip>[] = [
  { key: "dropShipNumber", header: "Drop-ship", mono: true },
  { key: "status", header: "Status", render: (row) => <DropShipStatusBadge status={row.status} /> },
  { key: "orderId", header: "Order", render: (row) => <OrderNumber id={row.orderId} /> },
  { key: "vendorId", header: "Vendor", render: (row) => <EntityName entity="vendor" id={row.vendorId} /> },
  { key: "purchaseOrderId", header: "Purchase order", mono: true, render: (row) => shortId(row.purchaseOrderId) },
  { key: "qty", header: "Quantity", align: "end", render: (row) => `${row.qty} ${row.unit}` },
  { key: "qtyConfirmed", header: "Qty shipped", align: "end", render: (row) => `${row.qtyConfirmed} ${row.unit}` },
  { key: "unitCost", header: "Cost", align: "end", render: (row) => formatMoney(row.unitCost, row.currency) },
  { key: "shippedOn", header: "Shipped", render: (row) => (row.shippedOn ? formatDate(row.shippedOn) : dash) },
];

type Values = Record<keyof DropShipInput, string>;
const EMPTY: Values = {
  legalEntityId: "", orderId: "", orderLineId: "", customerId: "", productId: "", unit: "EA", qty: "",
  vendorId: "", purchaseOrderId: "", purchaseOrderLineId: "", unitCost: "", currency: "USD",
};

const ID_FIELDS: [keyof DropShipInput, string][] = [
  ["legalEntityId", "Legal entity id"], ["orderId", "Order id"], ["orderLineId", "Order line id"], ["customerId", "Customer id"], ["productId", "Product id"],
  ["vendorId", "Vendor id"], ["purchaseOrderId", "Purchase order id"], ["purchaseOrderLineId", "Purchase order line id"],
];

/** Validates the create form; returns the message per field. */
export function validateDropShip(v: Values): Partial<Record<keyof DropShipInput, string>> {
  const found: Partial<Record<keyof DropShipInput, string>> = {};
  for (const [field, label] of ID_FIELDS) if (!UUID.test(v[field].trim())) found[field] = `Enter the ${label.toLowerCase()} as a UUID.`;
  if (!/^[A-Za-z0-9]{1,8}$/.test(v.unit.trim())) found.unit = "Use 1 to 8 letters or digits.";
  if (!DECIMAL.test(v.qty.trim()) || compareDecimal(v.qty.trim(), "0") <= 0) found.qty = "Enter a quantity above 0, with at most 6 decimals.";
  if (!DECIMAL.test(v.unitCost.trim()) || compareDecimal(v.unitCost.trim(), "0") <= 0) found.unitCost = "Enter the purchase order cost, above 0, with at most 6 decimals.";
  if (!CURRENCY.test(v.currency.trim())) found.currency = "Use a 3-letter currency code in capitals.";
  return found;
}

/** Drop-ships (FR-28.7): order lines a vendor ships straight to the customer, with the form that creates one. */
const filters: ListFilterDef[] = [{ kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: DROP_SHIP_STATUSES.map((s) => ({ value: s, label: LABELS[s] })) }];

export function DropShipsPage() {
  const navigate = useNavigate();
  const canCreate = holdsDuty(usePrincipalDuties(), DUTY_CREATE);
  // Bumped after a create, so the list reads the new drop-ship.
  const [reloads, setReloads] = useState(0);
  const [values, setValues] = useState<Values>(EMPTY);
  const [errors, setErrors] = useState<Partial<Record<keyof DropShipInput, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [created, setCreated] = useState<DropShip | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useCommandKey();

  function set(field: keyof DropShipInput, value: string) {
    setValues((prev) => ({ ...prev, [field]: value }));
    key.renew();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setCreated(null);
    const found = validateDropShip(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const input = Object.fromEntries(Object.entries(values).map(([k, v]) => [k, v.trim()])) as unknown as DropShipInput;
      const made = await createDropShip(input, key.current());
      key.renew();
      setCreated(made);
      setValues(EMPTY);
      setReloads((n) => n + 1);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  const useDropShipRows = (query: ListQuery) => {
    const status = (query.filters.status ?? "") as DropShipStatus | "";
    const state = useLoad((signal) => listDropShips({ status }, signal), `drop-ships-${status}-${reloads}`);
    return loadedRows(state);
  };

  const createForm = (
    <>
      {canCreate ? (
        <Card title="New drop-ship">
          <form className={styles.form} onSubmit={submit} noValidate>
            {ID_FIELDS.map(([field, label]) => (
              <FormField key={field} id={`ds-${field}`} label={label} error={errors[field]}>
                {(control) => <Input {...control} value={values[field]} invalid={Boolean(errors[field])} onChange={(event) => set(field, event.target.value)} />}
              </FormField>
            ))}
            <FormField id="ds-qty" label="Quantity" error={errors.qty}>
              {(control) => <Input {...control} inputMode="decimal" value={values.qty} invalid={Boolean(errors.qty)} onChange={(event) => set("qty", event.target.value)} />}
            </FormField>
            <FormField id="ds-unit" label="Unit" error={errors.unit}>
              {(control) => <Input {...control} value={values.unit} maxLength={8} invalid={Boolean(errors.unit)} onChange={(event) => set("unit", event.target.value)} />}
            </FormField>
            <FormField id="ds-unitCost" label="Purchase order cost per unit" error={errors.unitCost}>
              {(control) => <Input {...control} inputMode="decimal" value={values.unitCost} invalid={Boolean(errors.unitCost)} onChange={(event) => set("unitCost", event.target.value)} />}
            </FormField>
            <FormField id="ds-currency" label="Currency" error={errors.currency}>
              {(control) => <Input {...control} value={values.currency} maxLength={3} invalid={Boolean(errors.currency)} onChange={(event) => set("currency", event.target.value)} />}
            </FormField>
            <ProblemAlert title="Could not create the drop-ship" problem={failure} />
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Create drop-ship
              </Button>
              {created ? (
                <p role="status" className={styles.note}>
                  Created {created.dropShipNumber}. It stays open until the vendor confirms.
                </p>
              ) : null}
            </div>
          </form>
        </Card>
      ) : null}
    </>
  );

  return (
    <ListPage
      id="fulfillment-drop-ships"
      title="Drop-ships"
      description="Order lines a vendor ships straight to the customer, linked to their purchase order line. The vendor's confirmation books the cost against goods received not invoiced; no stock moves."
      noun="drop-ships"
      singular="drop-ship"
      columns={columns}
      filters={filters}
      useRows={useDropShipRows}
      search={(row) => ({ number: row.dropShipNumber })}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/fulfillment/drop-ships/${row.id}`)}
      empty={
        canCreate
          ? { title: "No drop-ships yet", description: "Create one below for an order line that a vendor ships to the customer." }
          : { title: "No drop-ships yet", description: "Drop-ships appear here once an order line is routed to a vendor." }
      }
      footer={createForm}
    />
  );
}
