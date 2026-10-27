import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  cancelOrder,
  CHANGEABLE_LINE_STATUSES,
  CHANGEABLE_STATUSES,
  changeOrder,
  describeProblem,
  getOrder,
  type ChangeOrderInput,
  type FieldChange,
  type LineChangeInput,
  type Order,
  type Problem,
  type Versioned,
} from "./api/client";
import { DECIMAL, dash, money, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const REASON_MAX = 500;

export interface LineEdit {
  lineNo: number;
  quantity: string;
  unitPrice: string;
  discPct: string;
  requestedShipDate: string;
}

export interface ChangeValues {
  requestedShipDate: string;
  reason: string;
  lines: LineEdit[];
}

/** The editable values of an order as it is now: header date and its OPEN or RESERVED lines. */
export function editsOf(order: Order): ChangeValues {
  return {
    requestedShipDate: order.requestedShipDate,
    reason: "",
    lines: order.lines
      .filter((l) => CHANGEABLE_LINE_STATUSES.includes(l.lineStatus))
      .map((l) => ({ lineNo: l.lineNo, quantity: l.quantity, unitPrice: l.unitPrice, discPct: l.discPct, requestedShipDate: l.requestedShipDate })),
  };
}

/** "5.500000" → "5.5" so that a retyped equal value is not a change; never parsed to float. */
function canonical(value: string): string {
  const trimmed = value.trim();
  if (!trimmed.includes(".")) return trimmed.replace(/^0+(?=\d)/, "");
  return trimmed.replace(/^0+(?=\d)/, "").replace(/0+$/, "").replace(/\.$/, "");
}

const lineFields = [
  ["quantity", "Quantity"],
  ["unitPrice", "Unit price"],
  ["discPct", "Discount %"],
  ["requestedShipDate", "Requested ship date"],
] as const;

/**
 * Validates the edits like the service (api/change.go, store/change.go) and
 * builds the PATCH body with only the fields that changed, plus the diff to
 * show before saving. Error keys are "requestedShipDate", "reason" and
 * "line-<lineNo>.<field>".
 */
export function buildChange(order: Order, values: ChangeValues): { errors: Errors; diff: FieldChange[]; input?: ChangeOrderInput } {
  const errors: Errors = {};
  const diff: FieldChange[] = [];
  const input: ChangeOrderInput = {};
  if (values.reason.length > REASON_MAX) errors.reason = `Keep the reason to ${REASON_MAX} characters.`;
  if (!DATE.test(values.requestedShipDate)) errors.requestedShipDate = "Choose the requested ship date.";
  else if (values.requestedShipDate !== order.requestedShipDate) {
    input.requestedShipDate = values.requestedShipDate;
    diff.push({ lineNo: 0, field: "requestedShipDate", old: order.requestedShipDate, new: values.requestedShipDate });
  }
  const lines: LineChangeInput[] = [];
  for (const edit of values.lines) {
    const current = order.lines.find((l) => l.lineNo === edit.lineNo);
    if (!current) continue;
    const at = (field: string) => `line-${edit.lineNo}.${field}`;
    if (!DECIMAL.test(edit.quantity.trim()) || Number(edit.quantity) <= 0) errors[at("quantity")] = "Enter a quantity above 0 with up to 6 decimals.";
    if (!DECIMAL.test(edit.unitPrice.trim())) errors[at("unitPrice")] = "Enter a price of 0 or more.";
    if (!DECIMAL.test(edit.discPct.trim()) || Number(edit.discPct) > 100) errors[at("discPct")] = "Enter a discount from 0 to 100.";
    if (!DATE.test(edit.requestedShipDate)) errors[at("requestedShipDate")] = "Choose the requested ship date.";
    const change: LineChangeInput = { lineNo: edit.lineNo };
    for (const [field] of lineFields) {
      const before = current[field];
      const after = edit[field];
      const same = field === "requestedShipDate" ? before === after : canonical(before) === canonical(after);
      if (!same) {
        change[field] = after.trim();
        diff.push({ lineNo: edit.lineNo, field, old: before, new: after.trim() });
      }
    }
    if (Object.keys(change).length > 1) lines.push(change);
  }
  if (lines.length > 0) input.lines = lines;
  if (values.reason.trim() !== "") input.reason = values.reason.trim();
  if (Object.keys(errors).length > 0 || diff.length === 0) return { errors, diff };
  return { errors, diff, input };
}

const fieldNames: Record<string, string> = {
  quantity: "Quantity",
  unitPrice: "Unit price",
  discPct: "Discount %",
  requestedShipDate: "Requested ship date",
  confirmedShipDate: "Confirmed ship date",
  status: "Status",
};

export const diffColumns: DataTableColumn<FieldChange>[] = [
  { key: "lineNo", header: "Line", align: "end", render: (c) => (c.lineNo === 0 ? "Header" : String(c.lineNo)) },
  { key: "field", header: "Field", render: (c) => fieldNames[c.field] ?? c.field },
  { key: "old", header: "Was", mono: true, render: (c) => c.old || dash },
  { key: "new", header: "Becomes", mono: true, render: (c) => c.new || dash },
];

interface Outcome {
  tone: "success" | "warning";
  text: string;
}

/** Change an order's header date and open lines, or cancel it (FR-27.9, BR-27.5). */
export function ChangeOrderPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getOrder(id, signal), id);
  const [latest, setLatest] = useState<Versioned<Order> | null>(null);
  const [values, setValues] = useState<ChangeValues | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState<"save" | "cancel" | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [cancelReason, setCancelReason] = useState("");
  const [cancelSure, setCancelSure] = useState(false);
  const current = latest && latest.value.id === id ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Change sales order" />
        <Alert tone="danger" title="Could not load the order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Change sales order" />
        <span className="visually-hidden">Loading the order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: order, etag } = current;
  const form = values ?? editsOf(order);
  const { diff } = buildChange(order, form);
  const changeable = CHANGEABLE_STATUSES.includes(order.status);
  const stale = failure?.code === "SLS-0006";
  const lockedLines = order.lines.filter((l) => !CHANGEABLE_LINE_STATUSES.includes(l.lineStatus));

  function update(patch: Partial<ChangeValues>) {
    setValues({ ...form, ...patch });
  }
  function updateLine(lineNo: number, patch: Partial<LineEdit>) {
    setValues({ ...form, lines: form.lines.map((l) => (l.lineNo === lineNo ? { ...l, ...patch } : l)) });
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const built = buildChange(order, form);
    setErrors(built.errors);
    setFailure(null);
    setOutcome(null);
    if (!built.input) return;
    setBusy("save");
    try {
      const res = await changeOrder(order.id, etag, built.input);
      const newHold = res.value.holds.some((h) => h.holdType === "CREDIT" && h.status === "ACTIVE") && !order.holds.some((h) => h.holdType === "CREDIT" && h.status === "ACTIVE");
      setLatest(res);
      setValues(null);
      setOutcome(
        newHold
          ? { tone: "warning", text: `Saved as version ${res.value.version}. The higher total failed the credit check, so a credit hold was placed.` }
          : { tone: "success", text: `Saved as version ${res.value.version}.` },
      );
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  async function cancel() {
    setFailure(null);
    setOutcome(null);
    if (cancelReason.length > REASON_MAX) {
      setErrors({ cancelReason: `Keep the reason to ${REASON_MAX} characters.` });
      return;
    }
    setBusy("cancel");
    try {
      const res = await cancelOrder(order.id, etag, cancelReason.trim());
      setLatest(res);
      setValues(null);
      setCancelSure(false);
      setOutcome({ tone: "success", text: `Order ${res.value.orderNumber} is cancelled.` });
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Change sales order ${order.orderNumber}`}
        meta={<StatusPill status={order.status} />}
        description={`Version ${order.version} · net total ${money(order.netTotal, order.currency)}`}
        actions={
          <Link className={buttonClassName("ghost")} to={`/sales/orders/${order.id}`}>
            Back to the order
          </Link>
        }
      />
      <Alert tone={outcome?.tone ?? "success"} live="status">
        {outcome?.text}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={stale ? "Someone else changed this order" : "The change was not saved"}
          live="alert"
          action={
            stale ? (
              <Button
                size="sm"
                onClick={() => {
                  setLatest(null);
                  setValues(null);
                  setFailure(null);
                  state.reload();
                }}
              >
                Reload order
              </Button>
            ) : undefined
          }
        >
          {failure.message} ({failure.code})
          {Object.keys(failure.fields).length > 0 ? ` ${Object.entries(failure.fields).map(([k, v]) => `${k}: ${v}`).join("; ")}` : null}
        </Alert>
      ) : null}
      {!changeable ? (
        <Alert tone="info" title={`This order is ${sentence(order.status).toLowerCase()}`}>
          Only draft, open and confirmed orders can be changed or cancelled.
        </Alert>
      ) : (
        <>
          <Card title="Change">
            <form className={styles.form} onSubmit={save} noValidate aria-label="Change order">
              <FormField id="change-date" label="Requested ship date (order)" error={errors.requestedShipDate}>
                {(control) => (
                  <Input {...control} type="date" value={form.requestedShipDate} onChange={(e) => update({ requestedShipDate: e.target.value })} invalid={Boolean(errors.requestedShipDate)} />
                )}
              </FormField>
              <FormField id="change-reason" label="Reason" optional hint="Recorded with the new version." error={errors.reason}>
                {(control) => <Textarea {...control} rows={2} maxLength={REASON_MAX} value={form.reason} onChange={(e) => update({ reason: e.target.value })} invalid={Boolean(errors.reason)} />}
              </FormField>
              <div className={`${styles.formActions} ${styles.lines}`}>
                {form.lines.length === 0 ? <p className={styles.note}>No line can change: every line is past reservation or cancelled.</p> : null}
                {form.lines.map((l) => (
                  <fieldset key={l.lineNo} className={styles.lineCard}>
                    <legend className={styles.lineLegend}>Line {l.lineNo}</legend>
                    {lineFields.map(([field, label]) => {
                      const key = `line-${l.lineNo}.${field}`;
                      return (
                        <FormField key={field} id={`line-${l.lineNo}-${field}`} label={label} error={errors[key]}>
                          {(control) =>
                            field === "requestedShipDate" ? (
                              <Input {...control} type="date" value={l[field]} onChange={(e) => updateLine(l.lineNo, { [field]: e.target.value })} invalid={Boolean(errors[key])} />
                            ) : (
                              <Input {...control} inputMode="decimal" value={l[field]} onChange={(e) => updateLine(l.lineNo, { [field]: e.target.value })} invalid={Boolean(errors[key])} />
                            )
                          }
                        </FormField>
                      );
                    })}
                  </fieldset>
                ))}
                {lockedLines.length > 0 ? (
                  <p className={styles.note}>
                    {lockedLines.map((l) => `Line ${l.lineNo} is ${sentence(l.lineStatus).toLowerCase()}`).join("; ")} and cannot change.
                  </p>
                ) : null}
              </div>
              <div className={styles.formActions}>
                <DataTable
                  label="Changes to save"
                  columns={diffColumns}
                  rows={diff}
                  getRowKey={(c) => `${c.lineNo}-${c.field}`}
                  empty={{ title: "No changes yet", description: "Edit a value above to see what will change." }}
                />
              </div>
              {order.status === "CONFIRMED" ? (
                <p className={`${styles.note} ${styles.formActions}`}>
                  The order is confirmed: a new quantity or date re-promises that line, and a higher total runs the credit check again.
                </p>
              ) : null}
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy === "save"} disabled={diff.length === 0 || (busy !== null && busy !== "save")}>
                  Save changes
                </Button>
                <Button variant="ghost" disabled={values === null || busy !== null} onClick={() => { setValues(null); setErrors({}); }}>
                  Discard edits
                </Button>
              </div>
            </form>
          </Card>
          <Card title="Cancel order">
            <div className={styles.form}>
              <FormField id="cancel-reason" label="Reason" optional error={errors.cancelReason}>
                {(control) => <Textarea {...control} rows={2} maxLength={REASON_MAX} value={cancelReason} onChange={(e) => setCancelReason(e.target.value)} invalid={Boolean(errors.cancelReason)} />}
              </FormField>
              <div className={styles.formActions}>
                <span className={styles.check}>
                  <Input id="cancel-sure" type="checkbox" checked={cancelSure} onChange={(e) => setCancelSure(e.target.checked)} />
                  <label htmlFor="cancel-sure">Cancel every open line of this order and clear its holds</label>
                </span>
              </div>
              <div className={styles.formActions}>
                <Button variant="danger" loading={busy === "cancel"} disabled={!cancelSure || (busy !== null && busy !== "cancel")} onClick={cancel}>
                  Cancel order
                </Button>
              </div>
            </div>
          </Card>
        </>
      )}
    </div>
  );
}
