import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createTransfer, describeProblem, type Problem } from "./api/client";
import { DECIMAL, sentence, today, UUID } from "./format";
import styles from "./pages.module.css";

/** Source dimensions a transfer line can name; the warehouse comes from the header. */
const LINE_DIMS = ["site", "location", "batch", "serial"] as const;
type LineDim = (typeof LINE_DIMS)[number];

export interface TransferLineValues {
  productId: string;
  qty: string;
  unit: string;
  underdeliveryPct: string;
  overdeliveryPct: string;
  dimensions: Record<LineDim, string>;
}

export interface TransferValues {
  legalEntityId: string;
  fromWarehouseId: string;
  toWarehouseId: string;
  toSiteId: string;
  useTransit: boolean;
  transitWarehouseId: string;
  shipDate: string;
  plannedReceiptDate: string;
  lines: TransferLineValues[];
}

type HeaderField = "legalEntityId" | "fromWarehouseId" | "toWarehouseId" | "toSiteId" | "transitWarehouseId" | "shipDate" | "plannedReceiptDate" | "lines";
type LineField = "productId" | "qty" | "unit" | "underdeliveryPct" | "overdeliveryPct" | "site";

export interface TransferErrors {
  header: Partial<Record<HeaderField, string>>;
  lines: Partial<Record<LineField, string>>[];
}

const emptyLine = (site = ""): TransferLineValues => ({
  productId: "",
  qty: "",
  unit: "EA",
  underdeliveryPct: "",
  overdeliveryPct: "",
  dimensions: { site, location: "", batch: "", serial: "" },
});

const code = /^\S{1,60}$/;

function percent(value: string): boolean {
  const v = value.trim();
  return v === "" || (DECIMAL.test(v) && Number(v) >= 0 && Number(v) <= 100);
}

/** Client checks that mirror the service (INV-TRF-001, site mandatory, serial quantity 1). */
export function validateTransfer(values: TransferValues): TransferErrors {
  const header: TransferErrors["header"] = {};
  if (!UUID.test(values.legalEntityId.trim())) header.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!code.test(values.fromWarehouseId.trim())) header.fromWarehouseId = "Enter the shipping warehouse.";
  if (!code.test(values.toWarehouseId.trim())) header.toWarehouseId = "Enter the receiving warehouse.";
  else if (values.toWarehouseId.trim() === values.fromWarehouseId.trim()) header.toWarehouseId = "The receiving warehouse must differ from the shipping warehouse.";
  if (values.toSiteId.trim() !== "" && !code.test(values.toSiteId.trim())) header.toSiteId = "A site code has no spaces and at most 60 characters.";
  if (values.useTransit) {
    const transit = values.transitWarehouseId.trim();
    if (!code.test(transit)) header.transitWarehouseId = "Enter the transit warehouse, or ship in one step.";
    else if (transit === values.fromWarehouseId.trim() || transit === values.toWarehouseId.trim())
      header.transitWarehouseId = "The transit warehouse must differ from the shipping and receiving warehouses.";
  }
  if (!/^\d{4}-\d{2}-\d{2}$/.test(values.shipDate)) header.shipDate = "Enter the planned ship date.";
  if (values.plannedReceiptDate !== "") {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(values.plannedReceiptDate)) header.plannedReceiptDate = "Enter the planned receipt date.";
    else if (values.plannedReceiptDate < values.shipDate) header.plannedReceiptDate = "The planned receipt date must not be before the ship date.";
  }
  if (values.lines.length === 0) header.lines = "Add at least one line.";
  const lines = values.lines.map((line) => {
    const errors: Partial<Record<LineField, string>> = {};
    if (!UUID.test(line.productId.trim())) errors.productId = "Enter the product id (a UUID).";
    const qty = line.qty.trim();
    if (!DECIMAL.test(qty) || Number(qty) <= 0) errors.qty = "Enter a quantity above zero.";
    else if (line.dimensions.serial.trim() !== "" && Number(qty) !== 1) errors.qty = "A serial number moves one unit at a time.";
    if (!/^[A-Z0-9_-]{1,12}$/.test(line.unit.trim())) errors.unit = "Enter a unit code such as EA.";
    if (line.dimensions.site.trim() === "") errors.site = "Site is required.";
    if (!percent(line.underdeliveryPct)) errors.underdeliveryPct = "Enter a percentage from 0 to 100.";
    if (!percent(line.overdeliveryPct)) errors.overdeliveryPct = "Enter a percentage from 0 to 100.";
    return errors;
  });
  return { header, lines };
}

function hasErrors(errors: TransferErrors): boolean {
  return Object.keys(errors.header).length > 0 || errors.lines.some((l) => Object.keys(l).length > 0);
}

/** Maps service field errors (fromWarehouseId, lines[0].qty, ...) onto the form. */
function mapProblem(problem: Problem, lineCount: number): TransferErrors | null {
  const out: TransferErrors = { header: {}, lines: Array.from({ length: lineCount }, () => ({})) };
  let found = false;
  for (const [name, message] of Object.entries(problem.fields)) {
    const line = /^lines\[(\d+)\]\.(\w+)/.exec(name);
    if (line) {
      const i = Number(line[1]);
      const field = (line[2] === "dimensions" ? "site" : line[2]) as LineField;
      if (out.lines[i]) {
        out.lines[i][field] = message;
        found = true;
      }
    } else if (["legalEntityId", "fromWarehouseId", "toWarehouseId", "toSiteId", "transitWarehouseId", "shipDate", "plannedReceiptDate", "lines"].includes(name)) {
      out.header[name as HeaderField] = message;
      found = true;
    }
  }
  return found ? out : null;
}

/** New transfer order (FR-16.15): header, route and lines. Creating it posts nothing. */
export function NewTransferPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<TransferValues>({
    legalEntityId: "",
    fromWarehouseId: "",
    toWarehouseId: "",
    toSiteId: "",
    useTransit: true,
    transitWarehouseId: "",
    shipDate: today(),
    plannedReceiptDate: "",
    lines: [emptyLine()],
  });
  const [errors, setErrors] = useState<TransferErrors>({ header: {}, lines: [] });
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(crypto.randomUUID());

  const change = (next: TransferValues) => {
    setValues(next);
    key.current = crypto.randomUUID();
  };
  const set = (name: Exclude<keyof TransferValues, "lines" | "useTransit">) => (event: { target: { value: string } }) =>
    change({ ...values, [name]: event.target.value });
  const setLine = (index: number, patch: Partial<TransferLineValues>) =>
    change({ ...values, lines: values.lines.map((line, i) => (i === index ? { ...line, ...patch } : line)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateTransfer(values);
    setErrors(found);
    setFailure(null);
    if (hasErrors(found)) return;
    setBusy(true);
    try {
      const created = await createTransfer(
        {
          legalEntityId: values.legalEntityId.trim(),
          fromWarehouseId: values.fromWarehouseId.trim(),
          toWarehouseId: values.toWarehouseId.trim(),
          toSiteId: values.toSiteId.trim(),
          useTransit: values.useTransit,
          transitWarehouseId: values.transitWarehouseId.trim(),
          shipDate: values.shipDate,
          plannedReceiptDate: values.plannedReceiptDate,
          lines: values.lines.map((line) => ({
            productId: line.productId.trim(),
            qty: line.qty.trim(),
            unit: line.unit.trim(),
            underdeliveryPct: line.underdeliveryPct,
            overdeliveryPct: line.overdeliveryPct,
            dimensions: line.dimensions,
          })),
        },
        key.current,
      );
      navigate(`/inventory/transfer-orders/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = mapProblem(problem, values.lines.length);
      if (mapped) setErrors(mapped);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = crypto.randomUUID();
    } finally {
      setBusy(false);
    }
  }

  const he = errors.header;
  return (
    <div className={styles.stack}>
      <PageHeader title="New transfer order" description="Plan a move of stock between two warehouses. Nothing moves until the order is shipped." />
      <form onSubmit={submit} noValidate className={styles.stack}>
        <Card title="Route">
          <div className={styles.form}>
            <FormField id="to-legal-entity" label="Legal entity" error={he.legalEntityId}>
              {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(he.legalEntityId)} />}
            </FormField>
            <FormField id="to-from" label="Shipping warehouse" error={he.fromWarehouseId}>
              {(control) => <Input {...control} value={values.fromWarehouseId} maxLength={60} onChange={set("fromWarehouseId")} invalid={Boolean(he.fromWarehouseId)} />}
            </FormField>
            <FormField id="to-to" label="Receiving warehouse" error={he.toWarehouseId}>
              {(control) => <Input {...control} value={values.toWarehouseId} maxLength={60} onChange={set("toWarehouseId")} invalid={Boolean(he.toWarehouseId)} />}
            </FormField>
            <FormField id="to-site" label="Receiving site" optional hint="Only when the receiving warehouse is in another site than the lines." error={he.toSiteId}>
              {(control) => <Input {...control} value={values.toSiteId} maxLength={60} onChange={set("toSiteId")} invalid={Boolean(he.toSiteId)} />}
            </FormField>
            <FormField id="to-ship-date" label="Planned ship date" error={he.shipDate}>
              {(control) => <Input {...control} type="date" value={values.shipDate} onChange={set("shipDate")} invalid={Boolean(he.shipDate)} />}
            </FormField>
            <FormField id="to-planned-receipt" label="Planned receipt date" optional hint="An open shipment is late once this date has passed." error={he.plannedReceiptDate}>
              {(control) => (
                <Input {...control} type="date" value={values.plannedReceiptDate} onChange={set("plannedReceiptDate")} invalid={Boolean(he.plannedReceiptDate)} />
              )}
            </FormField>
            {values.useTransit ? (
              <FormField id="to-transit" label="Transit warehouse" hint="Shipped stock waits here until it is received." error={he.transitWarehouseId}>
                {(control) => (
                  <Input {...control} value={values.transitWarehouseId} maxLength={60} onChange={set("transitWarehouseId")} invalid={Boolean(he.transitWarehouseId)} />
                )}
              </FormField>
            ) : null}
            <div className={styles.formActions}>
              <span className={styles.check}>
                <Input type="checkbox" id="to-use-transit" checked={values.useTransit} onChange={(event) => change({ ...values, useTransit: event.target.checked })} />
                <label htmlFor="to-use-transit">Ship through a transit warehouse</label>
              </span>
              <span className={styles.muted}>Without transit, shipping also receives the stock at the destination in one step.</span>
            </div>
          </div>
        </Card>
        {values.lines.map((line, index) => {
          const le = errors.lines[index] ?? {};
          const n = index + 1;
          return (
            <Card key={index} title={`Line ${n}`}>
              <div className={styles.form}>
                <FormField id={`to-line-${n}-product`} label="Product" error={le.productId}>
                  {(control) => <Input {...control} value={line.productId} onChange={(e) => setLine(index, { productId: e.target.value })} invalid={Boolean(le.productId)} />}
                </FormField>
                <FormField id={`to-line-${n}-qty`} label="Quantity" error={le.qty}>
                  {(control) => <Input {...control} inputMode="decimal" value={line.qty} onChange={(e) => setLine(index, { qty: e.target.value })} invalid={Boolean(le.qty)} />}
                </FormField>
                <FormField id={`to-line-${n}-unit`} label="Unit" error={le.unit}>
                  {(control) => (
                    <Input {...control} value={line.unit} maxLength={12} onChange={(e) => setLine(index, { unit: e.target.value.toUpperCase() })} invalid={Boolean(le.unit)} />
                  )}
                </FormField>
                {LINE_DIMS.map((name) => (
                  <FormField key={name} id={`to-line-${n}-${name}`} label={sentence(name)} optional={name !== "site"} error={name === "site" ? le.site : undefined}>
                    {(control) => (
                      <Input
                        {...control}
                        value={line.dimensions[name]}
                        maxLength={60}
                        onChange={(e) => setLine(index, { dimensions: { ...line.dimensions, [name]: e.target.value } })}
                        invalid={name === "site" && Boolean(le.site)}
                      />
                    )}
                  </FormField>
                ))}
                <FormField id={`to-line-${n}-under`} label="Underdelivery %" optional hint="Shortfall allowed when closing the line short." error={le.underdeliveryPct}>
                  {(control) => (
                    <Input {...control} inputMode="decimal" value={line.underdeliveryPct} onChange={(e) => setLine(index, { underdeliveryPct: e.target.value })} invalid={Boolean(le.underdeliveryPct)} />
                  )}
                </FormField>
                <FormField id={`to-line-${n}-over`} label="Overdelivery %" optional hint="Excess allowed over the shipped quantity." error={le.overdeliveryPct}>
                  {(control) => (
                    <Input {...control} inputMode="decimal" value={line.overdeliveryPct} onChange={(e) => setLine(index, { overdeliveryPct: e.target.value })} invalid={Boolean(le.overdeliveryPct)} />
                  )}
                </FormField>
                {values.lines.length > 1 ? (
                  <div className={styles.formActions}>
                    <Button size="sm" onClick={() => change({ ...values, lines: values.lines.filter((_, i) => i !== index) })}>
                      Remove line {n}
                    </Button>
                  </div>
                ) : null}
              </div>
            </Card>
          );
        })}
        {he.lines ? (
          <Alert tone="danger" title="No lines" live="alert">
            {he.lines}
          </Alert>
        ) : null}
        {failure ? (
          <Alert tone="danger" title="Could not create the transfer order" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button onClick={() => change({ ...values, lines: [...values.lines, emptyLine(values.lines.at(-1)?.dimensions.site ?? "")] })}>Add line</Button>
          <Button type="submit" variant="primary" loading={busy}>
            Create transfer order
          </Button>
          <Link className={buttonClassName("ghost")} to="/inventory/transfer-orders">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}
