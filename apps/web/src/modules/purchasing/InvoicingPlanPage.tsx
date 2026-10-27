import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  buttonClassName,
  type DataTableColumn,
} from "../../shell/ui";
import {
  ProcurementApiError,
  deleteInvoicingPlan,
  describeProblem,
  getInvoicingPlan,
  getPurchaseOrder,
  putInvoicingPlan,
  type InvoicingPlan,
  type InvoicingPlanInput,
  type InvoicingPlanRow,
  type PlanFrequency,
  type PlanKind,
  type PurchaseOrderLine,
} from "./api/client";
import { Details, LoadError, ShortId, StatusBadge, datePattern, dash, decimalPattern, money, useResource, type Failure } from "./parts";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

export interface MilestoneDraft {
  key: string;
  date: string;
  value: string;
}

export interface PlanDraft {
  kind: PlanKind;
  start: string;
  frequency: PlanFrequency;
  periods: string;
  billingDay: string;
  amountPerPeriod: string;
  /** PARTIAL milestones are all percentages or all amounts. */
  by: "percent" | "amount";
  milestones: MilestoneDraft[];
  settleWithoutReceipt: boolean;
}

let milestoneSeq = 0;
const emptyMilestone = (): MilestoneDraft => ({ key: `m${++milestoneSeq}`, date: "", value: "" });

export function emptyPlanDraft(): PlanDraft {
  return {
    kind: "PERIODIC",
    start: "",
    frequency: "MONTHLY",
    periods: "12",
    billingDay: "",
    amountPerPeriod: "",
    by: "percent",
    milestones: [emptyMilestone(), emptyMilestone()],
    settleWithoutReceipt: true,
  };
}

/** A draft that shows an existing plan, so replacing it starts from what is there. */
export function draftFromPlan(plan: InvoicingPlan): PlanDraft {
  const draft = emptyPlanDraft();
  draft.settleWithoutReceipt = plan.settleWithoutReceipt;
  if (plan.kind === "PERIODIC") {
    return {
      ...draft,
      start: plan.rows[0]?.dueDate.slice(0, 8).concat("01") ?? "",
      frequency: plan.frequency ?? "MONTHLY",
      periods: String(plan.rows.length),
      billingDay: plan.billingDay ? String(plan.billingDay) : "",
      amountPerPeriod: plan.rows[0]?.amount ?? "",
    };
  }
  const byPercent = plan.rows.every((r) => r.percent !== undefined);
  return {
    ...draft,
    kind: "PARTIAL",
    by: byPercent ? "percent" : "amount",
    milestones: plan.rows.map((r) => ({ key: `m${++milestoneSeq}`, date: r.dueDate, value: (byPercent ? r.percent : r.amount) ?? "" })),
  };
}

/**
 * Checks the form's shape and builds the PUT body. The amounts are decimal
 * strings end to end: whether they add up to the line amount is the
 * service's check (PRC-IP-001), never float arithmetic here.
 */
export function toPlanInput(d: PlanDraft): { input?: InvoicingPlanInput; errors: Errors } {
  const errors: Errors = {};
  if (d.kind === "PERIODIC") {
    if (!datePattern.test(d.start)) errors.start = "Enter the date of the first billing period.";
    const periods = Number(d.periods);
    if (!/^\d+$/.test(d.periods.trim()) || periods < 1 || periods > 120) errors.periods = "Enter a whole number of periods from 1 to 120.";
    const day = d.billingDay.trim();
    if (day !== "" && (!/^\d+$/.test(day) || Number(day) < 1 || Number(day) > 31)) errors.billingDay = "Enter a day from 1 to 31, or leave it blank.";
    if (!decimalPattern.test(d.amountPerPeriod.trim()) || /^0+(\.0+)?$/.test(d.amountPerPeriod.trim())) {
      errors.amountPerPeriod = "Enter a positive amount.";
    }
    if (Object.keys(errors).length > 0) return { errors };
    return {
      errors,
      input: {
        kind: "PERIODIC",
        start: d.start,
        frequency: d.frequency,
        periods,
        billingDay: day === "" ? undefined : Number(day),
        amountPerPeriod: d.amountPerPeriod.trim(),
        settleWithoutReceipt: d.settleWithoutReceipt,
      },
    };
  }
  if (d.milestones.length === 0) errors.milestones = "Add at least one milestone.";
  d.milestones.forEach((m, i) => {
    if (!datePattern.test(m.date)) errors[`milestones[${i}].date`] = "Enter a date.";
    if (!decimalPattern.test(m.value.trim()) || /^0+(\.0+)?$/.test(m.value.trim())) {
      errors[`milestones[${i}].${d.by}`] = d.by === "percent" ? "Enter a percent above 0." : "Enter a positive amount.";
    }
    if (i > 0 && datePattern.test(m.date) && datePattern.test(d.milestones[i - 1].date) && m.date < d.milestones[i - 1].date) {
      errors[`milestones[${i}].date`] = "Dates must not go back in time.";
    }
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      kind: "PARTIAL",
      milestones: d.milestones.map((m) => (d.by === "percent" ? { date: m.date, percent: m.value.trim() } : { date: m.date, amount: m.value.trim() })),
      settleWithoutReceipt: d.settleWithoutReceipt,
    },
  };
}

/** Loads the plan of one line; a line without a plan is null, not an error. */
async function loadPlan(poId: string, lineNo: number, signal: AbortSignal) {
  try {
    return (await getInvoicingPlan(poId, lineNo, signal)).body;
  } catch (error) {
    if (error instanceof ProcurementApiError && error.problem.code === "PRC-IP-NOT-FOUND") return null;
    throw error;
  }
}

const rowColumns = (currency: string): DataTableColumn<InvoicingPlanRow>[] => [
  { key: "seq", header: "Row", align: "end" },
  { key: "dueDate", header: "Due", mono: true },
  { key: "percent", header: "Percent", align: "end", render: (r) => (r.percent ? `${r.percent} %` : dash) },
  { key: "amount", header: "Amount", align: "end", render: (r) => money(r.amount, currency) },
  { key: "status", header: "Status", render: (r) => <StatusBadge status={r.status} /> },
];

const kindLabels: Record<PlanKind, string> = { PERIODIC: "Periodic", PARTIAL: "Milestones" };
const frequencyLabels: Record<PlanFrequency, string> = { MONTHLY: "Monthly", QUARTERLY: "Quarterly" };

/**
 * The invoicing plan of one PO line (FR-17.21): its schedule, a form that
 * replaces it, and removal. Changing the line amount on the order reshares the
 * open rows (BR-17.17), so the page only ever writes a whole plan.
 */
export function InvoicingPlanPage() {
  const { id = "", lineNo: rawLineNo = "" } = useParams();
  const lineNo = Number(rawLineNo);
  const po = useResource((signal) => getPurchaseOrder(id, signal), [id]);
  const loaded = useResource((signal) => loadPlan(id, lineNo, signal), [id, lineNo]);
  const [plan, setPlan] = useState<InvoicingPlan | null | undefined>(undefined);
  const [rowVersion, setRowVersion] = useState<number | null>(null);
  const [draft, setDraft] = useState<PlanDraft>(emptyPlanDraft);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<"save" | "delete" | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

  useEffect(() => {
    if (loaded.data !== null && loaded.data !== undefined) {
      setPlan(loaded.data);
      setRowVersion(loaded.data.poRowVersion);
      setDraft(draftFromPlan(loaded.data));
    } else if (!loaded.loading && !loaded.error) {
      setPlan(null);
    }
  }, [loaded.data, loaded.loading, loaded.error]);
  useEffect(() => {
    const poRowVersion = po.data?.body.rowVersion;
    if (poRowVersion !== undefined) setRowVersion((current) => current ?? poRowVersion);
  }, [po.data]);

  const order = po.data?.body ?? null;
  const line: PurchaseOrderLine | undefined = order?.lines.find((l) => l.lineNo === lineNo);
  const set = (patch: Partial<PlanDraft>) => setDraft((d) => ({ ...d, ...patch }));
  const setMilestone = (i: number, patch: Partial<MilestoneDraft>) =>
    setDraft((d) => ({ ...d, milestones: d.milestones.map((m, j) => (j === i ? { ...m, ...patch } : m)) }));

  async function onSave(event: FormEvent) {
    event.preventDefault();
    const built = toPlanInput(draft);
    setErrors(built.errors);
    setFailure(null);
    setNotice(null);
    if (!built.input || rowVersion === null) return;
    setBusy("save");
    try {
      const { body } = await putInvoicingPlan(id, lineNo, rowVersion, built.input);
      setPlan(body);
      setRowVersion(body.poRowVersion);
      setNotice(`Invoicing plan saved: ${body.rows.length} row${body.rows.length === 1 ? "" : "s"} adding up to ${money(body.lineAmount, body.currency)}.`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(null);
    }
  }

  async function onDelete() {
    if (rowVersion === null) return;
    setBusy("delete");
    setFailure(null);
    setNotice(null);
    try {
      setRowVersion(await deleteInvoicingPlan(id, lineNo, rowVersion));
      setPlan(null);
      setDraft(emptyPlanDraft());
      setConfirmDelete(false);
      setNotice("Invoicing plan removed.");
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const title = order ? `Invoicing plan: ${order.number} line ${lineNo}` : "Invoicing plan";
  const back = (
    <Link className={buttonClassName("secondary")} to={`/purchasing/purchase-orders/${id}`}>
      Back to the purchase order
    </Link>
  );
  if (po.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Invoicing plan" />
        {po.error.code === "PRC-PO-NOT-FOUND" ? (
          <Card>
            <EmptyState icon="receipt" title="Purchase order not found" description="It may belong to another legal entity, or the link is wrong." />
          </Card>
        ) : (
          <LoadError what="the purchase order" error={po.error} onRetry={po.reload} />
        )}
      </div>
    );
  }
  if (!order || plan === undefined) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Invoicing plan" />
        {loaded.error ? <LoadError what="the invoicing plan" error={loaded.error} onRetry={loaded.reload} /> : null}
        <p className={styles.muted} role="status">
          Loading invoicing plan…
        </p>
      </div>
    );
  }
  if (!line) {
    return (
      <div className={styles.stack}>
        <PageHeader title={title} actions={back} />
        <Card>
          <EmptyState icon="receipt" title={`Line ${rawLineNo} not found`} description={`${order.number} has no line with that number.`} />
        </Card>
      </div>
    );
  }

  const closed = order.approvalStatus === "CANCELLED" || order.approvalStatus === "FINALIZED" || line.status === "CANCELLED";
  const invoiced = plan?.rows.some((r) => r.status === "INVOICED") ?? false;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description="Schedule the invoices of this line. Payables (wave 18) invoices each open row on its due date; the rows always add up to the line amount, and a change to the line amount reshares the open rows."
        actions={back}
      />
      <Alert tone="danger" live="alert">
        {failure
          ? failure.code === "PRC-PO-009"
            ? `Someone else changed this order. Reload the page and try again. (${failure.code})`
            : `${failure.message} (${failure.code})`
          : null}
      </Alert>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Line">
        <Details
          items={[
            { label: "Item", value: line.description || <ShortId id={line.productId} /> },
            { label: "Quantity", value: `${line.qty} ${line.unit}`, mono: true },
            { label: "Net amount", value: money(line.lineAmount, order.currency), mono: true },
            { label: "Line status", value: <StatusBadge status={line.status} /> },
            { label: "Order status", value: <StatusBadge status={order.approvalStatus} /> },
          ]}
        />
      </Card>
      <Card
        title="Schedule"
        actions={
          plan && !closed && !invoiced ? (
            <Button size="sm" variant="ghost" onClick={() => setConfirmDelete(true)}>
              Remove plan
            </Button>
          ) : undefined
        }
      >
        {confirmDelete ? (
          <Alert
            tone="warning"
            title="Remove this invoicing plan?"
            action={
              <div className={styles.actions}>
                <Button size="sm" variant="danger" loading={busy === "delete"} onClick={() => void onDelete()}>
                  Yes, remove it
                </Button>
                <Button size="sm" variant="ghost" disabled={busy === "delete"} onClick={() => setConfirmDelete(false)}>
                  Keep it
                </Button>
              </div>
            }
          >
            The line is then invoiced against receipts as usual.
          </Alert>
        ) : null}
        {plan ? (
          <div className={styles.stack}>
            <Details
              items={[
                { label: "Plan type", value: kindLabels[plan.kind] },
                { label: "Frequency", value: plan.frequency ? frequencyLabels[plan.frequency] : dash },
                { label: "Billing day", value: plan.billingDay ? String(plan.billingDay) : dash, mono: true },
                { label: "Invoice without receipt", value: plan.settleWithoutReceipt ? "Yes" : "No" },
                { label: "Plan total", value: money(plan.lineAmount, plan.currency), mono: true },
              ]}
            />
            <DataTable label="Plan rows" columns={rowColumns(plan.currency)} rows={plan.rows} getRowKey={(r) => String(r.seq)} />
          </div>
        ) : (
          <EmptyState
            icon="receipt"
            title="No invoicing plan"
            description="Without a plan the line is invoiced against receipts. Add a periodic or milestone plan below to bill it on dates instead."
            compact
          />
        )}
      </Card>
      {closed ? (
        <Alert tone="info" title="This line cannot take a plan">
          Cancelled and finalized orders and cancelled lines keep no invoicing plan.
        </Alert>
      ) : invoiced ? (
        <Alert tone="info" title="Part of this plan is invoiced">
          A plan with invoiced rows cannot be replaced or removed. Changing the line amount on the order still reshares its open rows.
        </Alert>
      ) : (
        <form className={styles.form} onSubmit={onSave} noValidate>
          <Card title={plan ? "Replace the plan" : "Set up a plan"}>
            <div className={styles.form}>
              <div className={styles.grid}>
                <FormField id="plan-kind" label="Plan type">
                  {(c) => (
                    <Select {...c} value={draft.kind} onChange={(e) => set({ kind: e.target.value as PlanKind })}>
                      <option value="PERIODIC">Periodic: the same amount every month or quarter</option>
                      <option value="PARTIAL">Milestones: percentages or amounts on dates</option>
                    </Select>
                  )}
                </FormField>
              </div>
              {draft.kind === "PERIODIC" ? (
                <div className={styles.grid}>
                  <FormField id="plan-start" label="First period" hint="The first row falls in this month" error={errors.start}>
                    {(c) => <Input {...c} type="date" value={draft.start} onChange={(e) => set({ start: e.target.value })} />}
                  </FormField>
                  <FormField id="plan-frequency" label="Frequency" error={errors.frequency}>
                    {(c) => (
                      <Select {...c} value={draft.frequency} onChange={(e) => set({ frequency: e.target.value as PlanFrequency })}>
                        <option value="MONTHLY">Monthly</option>
                        <option value="QUARTERLY">Quarterly</option>
                      </Select>
                    )}
                  </FormField>
                  <FormField id="plan-periods" label="Periods" error={errors.periods}>
                    {(c) => <Input {...c} inputMode="numeric" value={draft.periods} onChange={(e) => set({ periods: e.target.value })} />}
                  </FormField>
                  <FormField id="plan-billing-day" label="Billing day" optional hint="Defaults to the first period's day" error={errors.billingDay}>
                    {(c) => <Input {...c} inputMode="numeric" value={draft.billingDay} onChange={(e) => set({ billingDay: e.target.value })} />}
                  </FormField>
                  <FormField
                    id="plan-amount"
                    label="Amount per period"
                    hint={`Periods × amount must equal ${money(line.lineAmount, order.currency)}`}
                    error={errors.amountPerPeriod}
                  >
                    {(c) => <Input {...c} inputMode="decimal" value={draft.amountPerPeriod} onChange={(e) => set({ amountPerPeriod: e.target.value })} />}
                  </FormField>
                </div>
              ) : (
                <div className={styles.form}>
                  <div className={styles.grid}>
                    <FormField id="plan-by" label="Milestones by" hint={draft.by === "percent" ? "Percents must add up to 100" : `Amounts must add up to ${money(line.lineAmount, order.currency)}`}>
                      {(c) => (
                        <Select {...c} value={draft.by} onChange={(e) => set({ by: e.target.value as PlanDraft["by"] })}>
                          <option value="percent">Percent of the line amount</option>
                          <option value="amount">Fixed amount</option>
                        </Select>
                      )}
                    </FormField>
                  </div>
                  {draft.milestones.map((m, i) => (
                    <fieldset key={m.key} className={styles.lineCard} aria-label={`Milestone ${i + 1}`}>
                      <div className={styles.lineHead}>
                        <p className={styles.lineTitle}>Milestone {i + 1}</p>
                        {draft.milestones.length > 1 ? (
                          <Button size="sm" variant="ghost" onClick={() => set({ milestones: draft.milestones.filter((_, j) => j !== i) })}>
                            Remove milestone {i + 1}
                          </Button>
                        ) : null}
                      </div>
                      <div className={styles.grid}>
                        <FormField id={`milestone-${i}-date`} label="Date" error={errors[`milestones[${i}].date`]}>
                          {(c) => <Input {...c} type="date" value={m.date} onChange={(e) => setMilestone(i, { date: e.target.value })} />}
                        </FormField>
                        <FormField
                          id={`milestone-${i}-value`}
                          label={draft.by === "percent" ? "Percent" : `Amount (${order.currency})`}
                          error={errors[`milestones[${i}].${draft.by}`]}
                        >
                          {(c) => <Input {...c} inputMode="decimal" value={m.value} onChange={(e) => setMilestone(i, { value: e.target.value })} />}
                        </FormField>
                      </div>
                    </fieldset>
                  ))}
                  <div className={styles.actions}>
                    <Button size="sm" disabled={draft.milestones.length >= 120} onClick={() => set({ milestones: [...draft.milestones, emptyMilestone()] })}>
                      Add milestone
                    </Button>
                  </div>
                  {errors.milestones ? <p className={styles.muted}>{errors.milestones}</p> : null}
                </div>
              )}
              <div className={styles.checkboxRow}>
                <Input
                  id="plan-settle"
                  type="checkbox"
                  checked={draft.settleWithoutReceipt}
                  onChange={(e) => set({ settleWithoutReceipt: e.target.checked })}
                />
                <label htmlFor="plan-settle">Invoice each row on its due date without waiting for a receipt</label>
              </div>
            </div>
          </Card>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy === "save"}>
              {plan ? "Replace plan" : "Save plan"}
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
