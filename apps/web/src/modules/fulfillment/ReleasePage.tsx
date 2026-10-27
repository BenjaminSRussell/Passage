import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, newKey, runRelease, type Problem, type ReleaseInput, type ReleaseResult, type ReleaseSkip, type Shipment } from "./api/client";
import { ShipmentStatusBadge } from "./badges";
import { DECIMAL, formatDate, SKIP_LABELS, today, UUID } from "./format";
import styles from "./pages.module.css";

interface LineValues {
  orderId: string;
  orderLineId: string;
  customerId: string;
  legalEntityId: string;
  productId: string;
  unit: string;
  qty: string;
  confirmedShipDate: string;
  atpOk: boolean;
  held: boolean;
}

interface Values {
  warehouseId: string;
  horizonDays: string;
  requireFullAtp: boolean;
  lines: LineValues[];
}

type Errors = Record<string, string>;

const UNIT = /^[A-Za-z0-9]{1,8}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const ID_FIELDS = [
  ["orderId", "Order id"],
  ["orderLineId", "Order line id"],
  ["customerId", "Customer id"],
  ["legalEntityId", "Legal entity id"],
  ["productId", "Product id"],
] as const;

export function emptyLine(): LineValues {
  return { orderId: "", orderLineId: "", customerId: "", legalEntityId: "", productId: "", unit: "EA", qty: "", confirmedShipDate: today(), atpOk: true, held: false };
}

/** Checks that mirror the service (FUL-0050), keyed like its field names, and the request to send. */
export function validateRelease(values: Values): { errors: Errors; input?: ReleaseInput } {
  const errors: Errors = {};
  if (!UUID.test(values.warehouseId.trim())) errors.warehouseId = "Enter the warehouse id (a UUID).";
  const horizon = values.horizonDays.trim();
  if (horizon !== "" && (!/^\d{1,2}$/.test(horizon) || Number(horizon) > 30)) errors.horizonDays = "Enter 0 to 30 days, or leave it empty for the warehouse default.";
  const seen = new Set<string>();
  values.lines.forEach((l, i) => {
    for (const [name, label] of ID_FIELDS) {
      if (!UUID.test(l[name].trim())) errors[`lines[${i}].${name}`] = `Enter the ${label.toLowerCase()} (a UUID).`;
    }
    if (!UNIT.test(l.unit.trim())) errors[`lines[${i}].unit`] = "Enter 1 to 8 letters or digits, such as EA.";
    if (!DECIMAL.test(l.qty.trim()) || /^0+(\.0+)?$/.test(l.qty.trim())) errors[`lines[${i}].qty`] = "Enter a quantity above 0 with at most 6 decimals.";
    if (!DATE.test(l.confirmedShipDate)) errors[`lines[${i}].confirmedShipDate`] = "Enter the confirmed ship date.";
    const lineId = l.orderLineId.trim().toLowerCase();
    if (lineId && seen.has(lineId)) errors[`lines[${i}].orderLineId`] = "This order line is already listed.";
    seen.add(lineId);
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      warehouseId: values.warehouseId.trim(),
      ...(horizon !== "" ? { horizonDays: Number(horizon) } : {}),
      requireFullAtp: values.requireFullAtp,
      lines: values.lines.map((l) => ({
        orderId: l.orderId.trim(),
        orderLineId: l.orderLineId.trim(),
        customerId: l.customerId.trim(),
        legalEntityId: l.legalEntityId.trim(),
        productId: l.productId.trim(),
        unit: l.unit.trim(),
        qty: l.qty.trim(),
        confirmedShipDate: l.confirmedShipDate,
        atpOk: l.atpOk,
        held: l.held,
      })),
    },
  };
}

const shipmentColumns: DataTableColumn<Shipment>[] = [
  { key: "shipmentNumber", header: "Shipment", mono: true, render: (row) => <Link to={`/fulfillment/shipments/${row.id}`}>{row.shipmentNumber}</Link> },
  { key: "status", header: "Status", render: (row) => <ShipmentStatusBadge status={row.status} /> },
  { key: "plannedShipDate", header: "Planned ship", render: (row) => formatDate(row.plannedShipDate) },
  { key: "lines", header: "Lines", align: "end", render: (row) => String(row.lines.length) },
];

const skipColumns: DataTableColumn<ReleaseSkip>[] = [
  { key: "orderLineId", header: "Order line", mono: true },
  { key: "reason", header: "Why it was not released", render: (row) => SKIP_LABELS[row.reason] ?? row.reason },
];

/** Release board (FR-28.1): run the release rules over confirmed order lines for one warehouse. */
export function ReleasePage() {
  const [values, setValues] = useState<Values>({ warehouseId: "", horizonDays: "", requireFullAtp: true, lines: [emptyLine()] });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<ReleaseResult | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function updateLine(i: number, patch: Partial<LineValues>) {
    update({ lines: values.lines.map((l, j) => (j === i ? { ...l, ...patch } : l)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRelease(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await runRelease(input, key.current));
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: string, label: string, value: string, onChange: (v: string) => void, hint?: string, extra?: Record<string, unknown>) => (
    <FormField key={id} id={`release-${id.replace(/[^\w-]/g, "-")}`} label={label} hint={hint} error={errors[id]}>
      {(control) => <Input {...control} {...extra} value={value} onChange={(event) => onChange(event.target.value)} invalid={Boolean(errors[id])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Release lines"
        description="Lines that are confirmed, not held, fully available and due within the horizon are released into one shipment per order, warehouse and ship date."
        actions={
          <Link className={buttonClassName("secondary")} to="/fulfillment/shipments">
            Shipments
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("warehouseId", "Warehouse id", values.warehouseId, (v) => update({ warehouseId: v }), "The warehouse the shipments leave from.")}
          {text("horizonDays", "Horizon (days)", values.horizonDays, (v) => update({ horizonDays: v }), "Empty uses the warehouse default (2 days).", { inputMode: "numeric" })}
          <FormField id="release-requireFullAtp" label="Require full availability">
            {(control) => <Input {...control} type="checkbox" checked={values.requireFullAtp} onChange={(event) => update({ requireFullAtp: event.target.checked })} />}
          </FormField>
          {values.lines.map((line, i) => (
            <fieldset key={i} className={styles.valueGroup}>
              <legend className={styles.legend}>Line {i + 1}</legend>
              {ID_FIELDS.map(([name, label]) => text(`lines[${i}].${name}`, label, line[name], (v) => updateLine(i, { [name]: v } as Partial<LineValues>)))}
              {text(`lines[${i}].unit`, "Unit", line.unit, (v) => updateLine(i, { unit: v }), undefined, { maxLength: 8 })}
              {text(`lines[${i}].qty`, "Quantity", line.qty, (v) => updateLine(i, { qty: v }), undefined, { inputMode: "decimal" })}
              {text(`lines[${i}].confirmedShipDate`, "Confirmed ship date", line.confirmedShipDate, (v) => updateLine(i, { confirmedShipDate: v }), undefined, { type: "date" })}
              <FormField id={`release-lines-${i}-atpOk`} label="Available (ATP)">
                {(control) => <Input {...control} type="checkbox" checked={line.atpOk} onChange={(event) => updateLine(i, { atpOk: event.target.checked })} />}
              </FormField>
              <FormField id={`release-lines-${i}-held`} label="On hold">
                {(control) => <Input {...control} type="checkbox" checked={line.held} onChange={(event) => updateLine(i, { held: event.target.checked })} />}
              </FormField>
              {values.lines.length > 1 ? (
                <div className={styles.formActions}>
                  <Button variant="ghost" size="sm" onClick={() => update({ lines: values.lines.filter((_, j) => j !== i) })}>
                    Remove line {i + 1}
                  </Button>
                </div>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            <Button onClick={() => update({ lines: [...values.lines, emptyLine()] })}>Add line</Button>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not release the lines" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run release
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <Card title="Release result">
          <div className={styles.stack}>
            <p role="status" className={styles.note}>
              {`Created ${result.shipments.length} ${result.shipments.length === 1 ? "shipment" : "shipments"}; ${result.skipped.length} ${result.skipped.length === 1 ? "line" : "lines"} not released.`}
            </p>
            <DataTable
              label="Created shipments"
              columns={shipmentColumns}
              rows={result.shipments}
              getRowKey={(row) => row.id}
              empty={{ title: "No shipments created", description: "None of the lines met the release rules." }}
            />
            {result.skipped.length > 0 ? <DataTable label="Lines not released" columns={skipColumns} rows={result.skipped} getRowKey={(row) => row.orderLineId} /> : null}
          </div>
        </Card>
      ) : null}
    </div>
  );
}
