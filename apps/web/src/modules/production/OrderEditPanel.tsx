import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  editOrderBOM,
  listOrderEdits,
  type Flushing,
  type LineEdit,
  type Operation,
  type OperationEdit,
  type OrderEditEntry,
  type OrderEdits,
  type OrderLine,
  type Problem,
  type ProductionOrder,
} from "./api/client";
import { dash, editOpLabel, flushingLabel } from "./labels";
import styles from "./pages.module.css";

/** What one submission of the editor changes. */
export type EditKind = "add-line" | "change-line" | "remove-line" | "add-operation" | "change-operation" | "remove-operation";

export const editKindLabel: Record<EditKind, string> = {
  "add-line": "Add a component line",
  "change-line": "Change a component line",
  "remove-line": "Remove a component line",
  "add-operation": "Add an operation",
  "change-operation": "Change an operation",
  "remove-operation": "Remove an operation",
};

export interface EditValues {
  lineNo: string;
  itemId: string;
  qtyPer: string;
  seriesSize: string;
  scrapPct: string;
  unitCost: string;
  flushing: Flushing | "";
  roundUp: "" | "yes" | "no";
  operNumber: string;
  resourceId: string;
  setupTimeH: string;
  runTimeHPer: string;
  laborRate: string;
  overheadRate: string;
}

export type EditErrors = Partial<Record<keyof EditValues | "form", string>>;

export const emptyEdit: EditValues = {
  lineNo: "",
  itemId: "",
  qtyPer: "",
  seriesSize: "",
  scrapPct: "",
  unitCost: "",
  flushing: "",
  roundUp: "",
  operNumber: "",
  resourceId: "",
  setupTimeH: "",
  runTimeHPer: "",
  laborRate: "",
  overheadRate: "",
};

const decimalPattern = /^[0-9]{1,12}(\.[0-9]{1,6})?$/;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const lineDecimals = ["qtyPer", "seriesSize", "scrapPct", "unitCost"] as const;
const operationDecimals = ["setupTimeH", "runTimeHPer", "scrapPct", "laborRate", "overheadRate"] as const;

/**
 * Turns the form into the edit request (FR-21.18), or the field errors. A change sends only
 * the fields that were filled in; the service applies the rules that need the order.
 */
export function toEdits(kind: EditKind, values: EditValues): { edits?: OrderEdits; errors: EditErrors } {
  const errors: EditErrors = {};
  const isLine = kind.endsWith("-line");
  const op = kind.startsWith("add") ? "add" : kind.startsWith("remove") ? "remove" : "change";
  // A removal sends only the line or operation number, so the other fields are not checked.
  const decimals = op === "remove" ? [] : isLine ? lineDecimals : operationDecimals;
  for (const name of decimals) {
    if (values[name] !== "" && !decimalPattern.test(values[name])) {
      errors[name] = "Enter a number with up to 6 decimals.";
    }
  }
  if (op !== "remove" && values.scrapPct !== "" && !errors.scrapPct && Number(values.scrapPct.split(".")[0]) >= 100) {
    errors.scrapPct = "Scrap must be below 100%.";
  }
  if (isLine) {
    if (values.lineNo.trim() === "" || values.lineNo.length > 20) {
      errors.lineNo = "Enter a line number of up to 20 characters.";
    }
    if (op !== "remove" && values.itemId !== "" && !uuidPattern.test(values.itemId)) {
      errors.itemId = "Enter the item's id (a UUID).";
    }
    if (op === "add") {
      if (values.itemId === "") {
        errors.itemId = "Enter the item to add.";
      }
      if (values.qtyPer === "") {
        errors.qtyPer = "Enter the quantity per unit.";
      }
    }
    if (op !== "remove" && values.seriesSize !== "" && !errors.seriesSize && !/[1-9]/.test(values.seriesSize)) {
      errors.seriesSize = "The series size must be positive.";
    }
    const edit: LineEdit = { op, lineNo: values.lineNo.trim() };
    if (op !== "remove") {
      if (values.itemId !== "") edit.itemId = values.itemId;
      for (const name of lineDecimals) {
        if (values[name] !== "") edit[name] = values[name];
      }
      if (values.flushing !== "") edit.flushing = values.flushing;
      if (values.roundUp !== "") edit.roundUp = values.roundUp === "yes";
      if (op === "change" && Object.keys(edit).length === 2) {
        errors.form = "Fill in at least one value to change.";
      }
    }
    return Object.keys(errors).length > 0 ? { errors } : { edits: { lines: [edit] }, errors };
  }
  if (!/^[1-9][0-9]{0,8}$/.test(values.operNumber)) {
    errors.operNumber = "Enter a positive whole operation number.";
  }
  if (op !== "remove" && values.resourceId !== "" && !uuidPattern.test(values.resourceId)) {
    errors.resourceId = "Enter the resource's id (a UUID).";
  }
  if (op === "add" && values.resourceId === "") {
    errors.resourceId = "Enter the resource of the operation.";
  }
  const edit: OperationEdit = { op, operNumber: Number(values.operNumber) };
  if (op !== "remove") {
    if (values.resourceId !== "") edit.resourceId = values.resourceId;
    for (const name of operationDecimals) {
      if (values[name] !== "") edit[name] = values[name];
    }
    if (op === "change" && Object.keys(edit).length === 2) {
      errors.form = "Fill in at least one value to change.";
    }
  }
  return Object.keys(errors).length > 0 ? { errors } : { edits: { operations: [edit] }, errors };
}

function isLine(image: OrderLine | Operation): image is OrderLine {
  return (image as OrderLine).lineNo !== undefined;
}

/** A short description of a line or operation image of the audit trail. */
export function describeImage(image: OrderLine | Operation | null): string {
  if (!image) {
    return dash;
  }
  if (isLine(image)) {
    return `${image.itemId} · ${image.qtyPer} per unit · requires ${image.qtyRequired}`;
  }
  return `${image.resourceId} · run ${image.runTimeHPer} h per unit · starts ${image.processQty}`;
}

const trailColumns: DataTableColumn<OrderEditEntry>[] = [
  { key: "occurredAt", header: "When", mono: true, render: (row) => <time dateTime={row.occurredAt}>{row.occurredAt.slice(0, 16).replace("T", " ")}</time> },
  {
    key: "op",
    header: "Edit",
    render: (row) => `${editOpLabel[row.op] ?? row.op} ${row.target === "line" ? "line" : "operation"} ${row.key}`,
  },
  { key: "before", header: "Before", render: (row) => describeImage(row.before) },
  { key: "after", header: "After", render: (row) => describeImage(row.after) },
  { key: "actor", header: "By" },
  { key: "orderVersion", header: "Version", align: "end" },
];

export interface OrderEditPanelProps {
  order: ProductionOrder;
  /** Called with the order after an edit; the page shows its lines, route, costs and version. */
  onEdited(order: ProductionOrder): void;
}

interface Trail {
  rows: OrderEditEntry[];
  loading: boolean;
  error: Problem | null;
}

/**
 * Order-specific BOM and route editor (FR-21.18): add, change and remove component lines and
 * operations of an order before it is started, with the audit trail of every edit.
 */
export function OrderEditPanel({ order, onEdited }: OrderEditPanelProps) {
  const [kind, setKind] = useState<EditKind>("change-line");
  const [values, setValues] = useState<EditValues>(emptyEdit);
  const [errors, setErrors] = useState<EditErrors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [trail, setTrail] = useState<Trail>({ rows: [], loading: true, error: null });

  const loadTrail = useCallback(
    (signal?: AbortSignal) => {
      listOrderEdits(order.id, signal).then(
        (rows) => setTrail({ rows, loading: false, error: null }),
        (error) => !signal?.aborted && setTrail({ rows: [], loading: false, error: describeProblem(error) }),
      );
    },
    [order.id],
  );

  useEffect(() => {
    const controller = new AbortController();
    loadTrail(controller.signal);
    return () => controller.abort();
  }, [loadTrail]);

  const lineKind = kind.endsWith("-line");
  const adding = kind.startsWith("add");
  const removing = kind.startsWith("remove");

  const choose = (next: EditKind) => {
    setKind(next);
    setValues(emptyEdit);
    setErrors({});
    setFailure(null);
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) {
      return;
    }
    const { edits, errors: found } = toEdits(kind, values);
    setErrors(found);
    if (!edits) {
      return;
    }
    setBusy(true);
    setFailure(null);
    setNotice("");
    try {
      const updated = await editOrderBOM(order.id, order.version, edits);
      setNotice(`${editKindLabel[kind]}: saved. The order is now version ${updated.version}.`);
      setValues(emptyEdit);
      onEdited(updated);
      loadTrail();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  };

  const text = (name: keyof EditValues, label: string, optional = true, decimal = true) => (
    <FormField id={`edit-${name}`} label={label} optional={optional} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          inputMode={decimal ? "decimal" : undefined}
          value={values[name]}
          onChange={(event) => setValues({ ...values, [name]: event.target.value })}
        />
      )}
    </FormField>
  );

  const keep = adding ? undefined : "Keep the current value";

  return (
    <Card title="BOM and route of this order">
      <div className={styles.stack}>
        <p className={styles.hint}>
          Edits apply to this order only, until it is started. Required quantities, the route's process quantities and, once
          estimated, the planned cost are recalculated, and every edit is kept below.
          {order.status === "Released" ? " A released order also needs the Maintain order BOM after release duty." : ""}
        </p>
        <Alert tone="success" live="status">
          {notice}
        </Alert>
        {failure ? (
          <Alert tone="danger" live="alert" title="The edit was refused">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Edit the BOM or route">
          <div className={styles.fields}>
            <FormField id="edit-kind" label="Change">
              {(control) => (
                <Select {...control} value={kind} onChange={(event) => choose(event.target.value as EditKind)}>
                  {(Object.keys(editKindLabel) as EditKind[]).map((k) => (
                    <option key={k} value={k}>
                      {editKindLabel[k]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {lineKind ? (
              adding ? (
                text("lineNo", "Line number", false, false)
              ) : (
                <FormField id="edit-lineNo" label="Line" error={errors.lineNo}>
                  {(control) => (
                    <Select {...control} value={values.lineNo} onChange={(event) => setValues({ ...values, lineNo: event.target.value })}>
                      <option value="">Choose a line</option>
                      {order.lines.map((l) => (
                        <option key={l.lineNo} value={l.lineNo}>
                          {`${l.lineNo} · ${l.itemId}`}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
              )
            ) : adding ? (
              text("operNumber", "Operation number", false)
            ) : (
              <FormField id="edit-operNumber" label="Operation" error={errors.operNumber}>
                {(control) => (
                  <Select {...control} value={values.operNumber} onChange={(event) => setValues({ ...values, operNumber: event.target.value })}>
                    <option value="">Choose an operation</option>
                    {order.operations.map((op) => (
                      <option key={op.operNumber} value={String(op.operNumber)}>
                        {`${op.operNumber} · ${op.resourceId}`}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            )}
          </div>
          {!removing && lineKind ? (
            <div className={styles.fields}>
              {text("itemId", "Item", !adding, false)}
              {text("qtyPer", "Quantity per unit", !adding)}
              {text("scrapPct", "Scrap %")}
              {text("seriesSize", "Series size")}
              {text("unitCost", "Unit cost")}
              <FormField id="edit-flushing" label="Flushing" optional>
                {(control) => (
                  <Select {...control} value={values.flushing} onChange={(event) => setValues({ ...values, flushing: event.target.value as Flushing | "" })}>
                    <option value="">{keep ?? "Manual"}</option>
                    {(Object.keys(flushingLabel) as Flushing[]).map((f) => (
                      <option key={f} value={f}>
                        {flushingLabel[f]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="edit-roundUp" label="Round up to whole units" optional>
                {(control) => (
                  <Select
                    {...control}
                    value={values.roundUp}
                    onChange={(event) => setValues({ ...values, roundUp: event.target.value as EditValues["roundUp"] })}
                  >
                    <option value="">{keep ?? "No"}</option>
                    <option value="yes">Yes</option>
                    <option value="no">No</option>
                  </Select>
                )}
              </FormField>
            </div>
          ) : null}
          {!removing && !lineKind ? (
            <div className={styles.fields}>
              {text("resourceId", "Resource", !adding, false)}
              {text("setupTimeH", "Setup hours")}
              {text("runTimeHPer", "Run hours per unit")}
              {text("scrapPct", "Scrap %")}
              {text("laborRate", "Labor rate")}
              {text("overheadRate", "Overhead rate")}
            </div>
          ) : null}
          {errors.form ? (
            <Alert tone="warning" live="alert">
              {errors.form}
            </Alert>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant={removing ? "danger" : "primary"} loading={busy}>
              {removing ? (lineKind ? "Remove the line" : "Remove the operation") : "Save the edit"}
            </Button>
          </div>
        </form>
        {trail.error ? (
          <Alert tone="danger" title="Could not load the edit history">
            {trail.error.message} ({trail.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Edit history"
            columns={trailColumns}
            rows={trail.rows}
            loading={trail.loading}
            getRowKey={(row) => `${row.orderVersion}-${row.seq}`}
            empty={{ title: "No edits yet", description: "The order still has the BOM and route it was exploded with." }}
          />
        )}
      </div>
    </Card>
  );
}
