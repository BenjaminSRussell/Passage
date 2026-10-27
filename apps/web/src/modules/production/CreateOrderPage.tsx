import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  createOrder,
  describeProblem,
  newIdempotencyKey,
  type CreateOrderInput,
  type Flushing,
  type Problem,
  type ProductionOrder,
} from "./api/client";
import { flushingLabel } from "./labels";
import styles from "./pages.module.css";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const decimalPattern = /^[0-9]{1,12}(\.[0-9]{1,6})?$/;
const currencyPattern = /^[A-Z]{3}$/;
const datePattern = /^\d{4}-\d{2}-\d{2}$/;

export interface LineValues {
  lineNo: string;
  itemId: string;
  qtyPer: string;
  scrapPct: string;
  unitCost: string;
  /** Empty means manual (FR-21.5). */
  flushing?: Flushing | "";
}

export interface OperationValues {
  operNumber: string;
  resourceId: string;
  setupTimeH: string;
  runTimeHPer: string;
  laborRate: string;
  overheadRate: string;
}

export interface OrderValues {
  itemGroup: string;
  productId: string;
  siteId: string;
  warehouseId: string;
  currency: string;
  quantity: string;
  deliveryDate: string;
  lines: LineValues[];
  operations: OperationValues[];
}

export type OrderErrors = Record<string, string>;

const emptyLine = (n: number): LineValues => ({ lineNo: String(n * 10), itemId: "", qtyPer: "", scrapPct: "", unitCost: "" });
const emptyOperation = (n: number): OperationValues => ({
  operNumber: String(n * 10),
  resourceId: "",
  setupTimeH: "",
  runTimeHPer: "",
  laborRate: "",
  overheadRate: "",
});

export const initialValues = (): OrderValues => ({
  itemGroup: "",
  productId: "",
  siteId: "",
  warehouseId: "",
  currency: "USD",
  quantity: "",
  deliveryDate: "",
  lines: [emptyLine(1)],
  operations: [emptyOperation(1)],
});

const positive = (v: string) => decimalPattern.test(v) && /[1-9]/.test(v);
const optionalDecimal = (v: string) => v === "" || decimalPattern.test(v);

/** Client-side checks that mirror the service's validation, keyed like the form fields. */
export function validateOrder(values: OrderValues): OrderErrors {
  const errors: OrderErrors = {};
  for (const key of ["productId", "siteId", "warehouseId"] as const) {
    if (!uuidPattern.test(values[key].trim())) {
      errors[key] = "Enter the record's id (a UUID).";
    }
  }
  if (!currencyPattern.test(values.currency)) {
    errors.currency = "Enter a three-letter currency code such as USD.";
  }
  if (!positive(values.quantity)) {
    errors.quantity = "Enter a positive quantity with up to 6 decimals.";
  }
  if (values.deliveryDate !== "" && !datePattern.test(values.deliveryDate)) {
    errors.deliveryDate = "Enter a date (YYYY-MM-DD).";
  }
  if (values.itemGroup.length > 40 || values.itemGroup.trim() === "All") {
    errors.itemGroup = "Enter up to 40 characters; All is reserved for posting profiles.";
  }
  const seen = new Set<string>();
  values.lines.forEach((line, i) => {
    if (line.lineNo.trim() === "" || line.lineNo.length > 20) {
      errors[`lines.${i}.lineNo`] = "Enter a line number.";
    } else if (seen.has(line.lineNo.trim())) {
      errors[`lines.${i}.lineNo`] = "Line numbers must be unique.";
    }
    seen.add(line.lineNo.trim());
    if (!uuidPattern.test(line.itemId.trim())) {
      errors[`lines.${i}.itemId`] = "Enter the item id (a UUID).";
    }
    if (!positive(line.qtyPer)) {
      errors[`lines.${i}.qtyPer`] = "Enter a positive quantity.";
    }
    if (!optionalDecimal(line.scrapPct) || (line.scrapPct !== "" && Number.parseInt(line.scrapPct, 10) >= 100)) {
      errors[`lines.${i}.scrapPct`] = "Enter a percentage below 100.";
    }
    if (!optionalDecimal(line.unitCost)) {
      errors[`lines.${i}.unitCost`] = "Enter a cost with up to 6 decimals.";
    }
  });
  if (values.operations.length === 0) {
    errors.operations = "Add at least one operation.";
  }
  let previous = 0;
  values.operations.forEach((op, i) => {
    const n = Number.parseInt(op.operNumber, 10);
    if (!/^[0-9]{1,6}$/.test(op.operNumber) || n < 1) {
      errors[`operations.${i}.operNumber`] = "Enter a positive whole number.";
    } else if (n <= previous) {
      errors[`operations.${i}.operNumber`] = "Operation numbers must increase along the route.";
    }
    previous = Number.isNaN(n) ? previous : n;
    if (!uuidPattern.test(op.resourceId.trim())) {
      errors[`operations.${i}.resourceId`] = "Enter the resource id (a UUID).";
    }
    for (const key of ["setupTimeH", "runTimeHPer", "laborRate", "overheadRate"] as const) {
      if (!optionalDecimal(op[key])) {
        errors[`operations.${i}.${key}`] = "Enter a number with up to 6 decimals.";
      }
    }
  });
  return errors;
}

const optional = (v: string) => (v === "" ? undefined : v);

/** Maps the form to the CreateOrder body of api/openapi.yaml. */
export function toInput(values: OrderValues): CreateOrderInput {
  return {
    itemGroup: optional(values.itemGroup.trim()),
    productId: values.productId.trim(),
    siteId: values.siteId.trim(),
    warehouseId: values.warehouseId.trim(),
    currency: values.currency,
    quantity: values.quantity,
    deliveryDate: optional(values.deliveryDate),
    bom: values.lines.map((line) => ({
      lineNo: line.lineNo.trim(),
      itemId: line.itemId.trim(),
      qtyPer: line.qtyPer,
      scrapPct: optional(line.scrapPct),
      unitCost: optional(line.unitCost),
      flushing: line.flushing || undefined,
    })),
    operations: values.operations.map((op) => ({
      operNumber: Number.parseInt(op.operNumber, 10),
      resourceId: op.resourceId.trim(),
      setupTimeH: optional(op.setupTimeH),
      runTimeHPer: optional(op.runTimeHPer),
      laborRate: optional(op.laborRate),
      overheadRate: optional(op.overheadRate),
    })),
  };
}

/** Maps a server field path (bom[0].itemId, operations[1].resourceId) to the form's key. */
export function formKey(serverField: string): string {
  return serverField.replace(/^bom\[(\d+)\]/, "lines.$1").replace(/^operations\[(\d+)\]/, "operations.$1");
}

export interface CreateProductionOrderPageProps {
  onCreated?(order: ProductionOrder): void;
  onCancel?(): void;
}

/** Create a production order (FR-21.1): header, BOM lines and route; the service explodes and plans them. */
export function CreateProductionOrderPage({ onCreated, onCancel }: CreateProductionOrderPageProps) {
  const [values, setValues] = useState<OrderValues>(initialValues);
  const [errors, setErrors] = useState<OrderErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per order being entered: a retry after a lost response replays instead of creating twice.
  const key = useRef(newIdempotencyKey());

  const set = (patch: Partial<OrderValues>) => {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newIdempotencyKey();
  };
  const setLine = (i: number, patch: Partial<LineValues>) =>
    set({ lines: values.lines.map((line, j) => (j === i ? { ...line, ...patch } : line)) });
  const setOperation = (i: number, patch: Partial<OperationValues>) =>
    set({ operations: values.operations.map((op, j) => (j === i ? { ...op, ...patch } : op)) });

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateOrder(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      const order = await createOrder(toInput(values), key.current);
      key.current = newIdempotencyKey();
      onCreated?.(order);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) {
        setErrors(Object.fromEntries(Object.entries(problem.errors).map(([field, message]) => [formKey(field), message])));
      }
    } finally {
      setBusy(false);
    }
  };

  const text = (id: string, label: string, value: string, onChange: (v: string) => void, extra: { optional?: boolean; hint?: string; inputMode?: "decimal" | "numeric"; type?: string } = {}) => (
    <FormField id={id} label={label} error={errors[id.replace(/^po-/, "").replace(/-/g, ".")]} optional={extra.optional} hint={extra.hint}>
      {(control) => (
        <Input {...control} type={extra.type} inputMode={extra.inputMode} value={value} onChange={(event) => onChange(event.target.value)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New production order"
        description="Enter the product, quantity, the BOM lines and the route. Passage explodes phantoms, applies scrap and rounding, and numbers the order."
        actions={onCancel ? <Button variant="ghost" onClick={onCancel}>Back to production orders</Button> : undefined}
      />
      <Alert tone="danger" live="alert" title={failure ? "The order was not created" : undefined}>
        {failure ? (
          <>
            {failure.message} ({failure.code})
            {failure.errors ? (
              <ul>
                {Object.entries(failure.errors).map(([field, message]) => (
                  <li key={field}>
                    <code>{field}</code> {message}
                  </li>
                ))}
              </ul>
            ) : null}
          </>
        ) : null}
      </Alert>
      <form className={styles.form} onSubmit={submit} noValidate aria-label="New production order">
        <Card title="Order">
          <div className={styles.fields}>
            {text("po-productId", "Product", values.productId, (v) => set({ productId: v }), { hint: "Released product id (wave 8)." })}
            {text("po-quantity", "Quantity", values.quantity, (v) => set({ quantity: v }), { inputMode: "decimal" })}
            {text("po-currency", "Currency", values.currency, (v) => set({ currency: v.toUpperCase() }))}
            {text("po-deliveryDate", "Delivery date", values.deliveryDate, (v) => set({ deliveryDate: v }), { optional: true, type: "date" })}
            {text("po-siteId", "Site", values.siteId, (v) => set({ siteId: v }))}
            {text("po-warehouseId", "Warehouse", values.warehouseId, (v) => set({ warehouseId: v }))}
            {text("po-itemGroup", "Item group", values.itemGroup, (v) => set({ itemGroup: v }), { optional: true, hint: "Selects the posting profile with the site." })}
          </div>
        </Card>

        <Card
          title="Components"
          actions={
            <Button size="sm" onClick={() => set({ lines: [...values.lines, emptyLine(values.lines.length + 1)] })}>
              Add component
            </Button>
          }
        >
          <div className={styles.rows}>
            {values.lines.length === 0 ? <p className={styles.hint}>No components: the order consumes no material.</p> : null}
            {values.lines.map((line, i) => (
              <fieldset key={i} className={styles.row}>
                <legend className={styles.legend}>Component {i + 1}</legend>
                {text(`po-lines-${i}-lineNo`, "Line", line.lineNo, (v) => setLine(i, { lineNo: v }))}
                {text(`po-lines-${i}-itemId`, "Item", line.itemId, (v) => setLine(i, { itemId: v }))}
                {text(`po-lines-${i}-qtyPer`, "Quantity per unit", line.qtyPer, (v) => setLine(i, { qtyPer: v }), { inputMode: "decimal" })}
                {text(`po-lines-${i}-scrapPct`, "Scrap %", line.scrapPct, (v) => setLine(i, { scrapPct: v }), { optional: true, inputMode: "decimal" })}
                {text(`po-lines-${i}-unitCost`, "Unit cost", line.unitCost, (v) => setLine(i, { unitCost: v }), { optional: true, inputMode: "decimal" })}
                <FormField id={`po-lines-${i}-flushing`} label="Flushing" error={errors[`lines.${i}.flushing`]}>
                  {(control) => (
                    <Select {...control} value={line.flushing ?? ""} onChange={(event) => setLine(i, { flushing: event.target.value as Flushing | "" })}>
                      <option value="">{flushingLabel.manual} (picking list)</option>
                      <option value="start">{flushingLabel.start}</option>
                      <option value="finish">{flushingLabel.finish}</option>
                      <option value="available-at-location">{flushingLabel["available-at-location"]}</option>
                    </Select>
                  )}
                </FormField>
                <Button variant="ghost" size="sm" onClick={() => set({ lines: values.lines.filter((_, j) => j !== i) })}>
                  Remove component {i + 1}
                </Button>
              </fieldset>
            ))}
          </div>
        </Card>

        <Card
          title="Route"
          actions={
            <Button size="sm" onClick={() => set({ operations: [...values.operations, emptyOperation(values.operations.length + 1)] })}>
              Add operation
            </Button>
          }
        >
          <div className={styles.rows}>
            {errors.operations ? (
              <Alert tone="danger" live="off">
                {errors.operations}
              </Alert>
            ) : null}
            {values.operations.map((op, i) => (
              <fieldset key={i} className={styles.row}>
                <legend className={styles.legend}>Operation {i + 1}</legend>
                {text(`po-operations-${i}-operNumber`, "Operation", op.operNumber, (v) => setOperation(i, { operNumber: v }), { inputMode: "numeric" })}
                {text(`po-operations-${i}-resourceId`, "Resource", op.resourceId, (v) => setOperation(i, { resourceId: v }))}
                {text(`po-operations-${i}-setupTimeH`, "Setup hours", op.setupTimeH, (v) => setOperation(i, { setupTimeH: v }), { optional: true, inputMode: "decimal" })}
                {text(`po-operations-${i}-runTimeHPer`, "Run hours per unit", op.runTimeHPer, (v) => setOperation(i, { runTimeHPer: v }), { optional: true, inputMode: "decimal" })}
                {text(`po-operations-${i}-laborRate`, "Labor rate", op.laborRate, (v) => setOperation(i, { laborRate: v }), { optional: true, inputMode: "decimal" })}
                {text(`po-operations-${i}-overheadRate`, "Overhead rate", op.overheadRate, (v) => setOperation(i, { overheadRate: v }), { optional: true, inputMode: "decimal" })}
                <Button variant="ghost" size="sm" onClick={() => set({ operations: values.operations.filter((_, j) => j !== i) })}>
                  Remove operation {i + 1}
                </Button>
              </fieldset>
            ))}
          </div>
        </Card>

        <div className={styles.formActions}>
          {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
          <Button type="submit" variant="primary" loading={busy}>
            Create production order
          </Button>
        </div>
      </form>
    </div>
  );
}
