import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  addReportLine,
  createReport,
  deleteReportLine,
  describeProblem,
  getReport,
  listReports,
  newIdempotencyKey,
  submitReport,
  type ExpenseReport,
  type NewLineInput,
  type NewReportInput,
  type Outcome,
  type PaidBy,
  type Problem,
  type ReportLine,
  type ReportStatus,
} from "./api/client";
import { CURRENCY, DAY, DECIMAL, splitProblem, today } from "./format";
import styles from "./pages.module.css";

type Errors = Record<string, string>;
type Failure = Pick<Problem, "code" | "message">;

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export const CATEGORIES = ["MEALS", "TRAVEL", "AIRFARE", "LODGING", "MILEAGE", "ALCOHOL"] as const;

const STATUS: Record<ReportStatus, { label: string; tone: BadgeTone }> = {
  DRAFT: { label: "Draft", tone: "neutral" },
  SUBMITTED: { label: "Submitted", tone: "info" },
  APPROVED: { label: "Approved", tone: "success" },
  REJECTED: { label: "Rejected", tone: "danger" },
  RETURNED: { label: "Returned", tone: "warning" },
  POSTED: { label: "Posted", tone: "success" },
  PAID: { label: "Paid", tone: "success" },
};

const OUTCOME_LABEL: Record<Outcome, string> = { BLOCK: "Blocked", REQUIRE_JUSTIFICATION: "Justification required", REQUIRE_RECEIPT: "Receipt required", WARN: "Warning" };

function StatusBadge({ status }: { status: ReportStatus }) {
  const s = STATUS[status] ?? { label: status, tone: "neutral" as BadgeTone };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

function money(currency: string, amount: string): string {
  return `${amount} ${currency}`;
}

// ---- List ----

const listColumns: DataTableColumn<ExpenseReport>[] = [
  { key: "purpose", header: "Purpose" },
  { key: "status", header: "Status", render: (r) => <StatusBadge status={r.status} /> },
  { key: "createdAt", header: "Created", mono: true, render: (r) => r.createdAt.slice(0, 10) },
  { key: "total", header: "Total", align: "end", mono: true, render: (r) => money(r.currency, r.total) },
];

/** FR-43.11 expense reports visible to the caller, filtered by status, in creation order. */
export function ReportsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ReportStatus | "">("");
  const [items, setItems] = useState<ExpenseReport[]>([]);
  const [next, setNext] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setFailure(null);
    listReports({ status: status || undefined })
      .then((page) => {
        if (!alive) return;
        setItems(page.items);
        setNext(page.nextCursor);
      })
      .catch((err: unknown) => {
        if (!alive) return;
        setItems([]);
        setNext("");
        setFailure(describeProblem(err));
      })
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [status, attempt]);

  async function loadMore() {
    setLoadingMore(true);
    try {
      const page = await listReports({ status: status || undefined, cursor: next });
      setItems((prev) => [...prev, ...page.items]);
      setNext(page.nextCursor);
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Expense reports"
        description="Draft reports collect lines and their policy results; a report is submitted once no line is blocked and every receipt and justification is in."
        actions={
          <Button variant="primary" icon="receipt" onClick={() => navigate("/expense/reports/new")}>
            New report
          </Button>
        }
      />
      <Card>
        <div className={styles.form}>
          <FormField id="reports-status" label="Status">
            {(c) => (
              <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as ReportStatus | "")}>
                <option value="">All statuses</option>
                {(Object.keys(STATUS) as ReportStatus[]).map((s) => (
                  <option key={s} value={s}>
                    {STATUS[s].label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {failure ? (
        <Alert
          tone="danger"
          title="The reports could not be loaded"
          live="alert"
          action={
            <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>
              Try again
            </Button>
          }
        >
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <DataTable
        label="Expense reports"
        columns={listColumns}
        rows={items}
        loading={loading}
        getRowKey={(r) => r.id}
        empty={
          status
            ? { title: `No ${STATUS[status].label.toLowerCase()} reports`, description: "Choose another status to see other reports." }
            : { title: "No expense reports yet", description: "Create a report, then add its lines." }
        }
        onRowOpen={(r) => navigate(`/expense/reports/${r.id}`)}
      />
      {next && !loading ? (
        <div className={styles.formActions}>
          <Button loading={loadingMore} onClick={loadMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

// ---- Create ----

export interface NewReportValues {
  purpose: string;
  currency: string;
  legalEntityId: string;
  workerId: string;
}

export const emptyNewReport: NewReportValues = { purpose: "", currency: "USD", legalEntityId: "", workerId: "" };

export function validateNewReport(v: NewReportValues): { errors: Errors; input?: NewReportInput } {
  const errors: Errors = {};
  const purpose = v.purpose.trim();
  const currency = v.currency.trim().toUpperCase();
  if (purpose === "" || purpose.length > 500) errors.purpose = "Enter the purpose, up to 500 characters.";
  if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(v.workerId.trim())) errors.workerId = "Enter the worker id (a UUID).";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { purpose, currency, legalEntityId: v.legalEntityId.trim(), workerId: v.workerId.trim() } };
}

const REPORT_FIELDS = ["purpose", "currency", "legalEntityId", "workerId"];

/** Create a DRAFT report for a worker in a legal entity. */
export function NewReportPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<NewReportValues>(emptyNewReport);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per version of the form: a retry of the same values replays, an edit is a new request.
  const key = useRef(newIdempotencyKey());

  function update(patch: Partial<NewReportValues>) {
    key.current = newIdempotencyKey();
    setValues((prev) => ({ ...prev, ...patch }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateNewReport(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const report = await createReport(input, key.current);
      navigate(`/expense/reports/${report.id}`);
    } catch (err) {
      const split = splitProblem(describeProblem(err), REPORT_FIELDS);
      setErrors(split.fields);
      setFailure(split.failure);
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New expense report" description="A report starts as a draft in the report currency. Add its lines next, then submit it." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>
            <FormField id="report-purpose" label="Purpose" error={errors.purpose}>
              {(c) => <Input {...c} value={values.purpose} maxLength={500} onChange={(e) => update({ purpose: e.target.value })} invalid={Boolean(errors.purpose)} />}
            </FormField>
          </div>
          <FormField id="report-currency" label="Currency" hint="ISO code such as USD or EUR." error={errors.currency}>
            {(c) => <Input {...c} className={styles.mono} value={values.currency} maxLength={3} onChange={(e) => update({ currency: e.target.value })} invalid={Boolean(errors.currency)} />}
          </FormField>
          <FormField id="report-legal-entity" label="Legal entity" hint="The legal entity id; it must be in your scope." error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="report-worker" label="Worker" hint="The worker id the report reimburses." error={errors.workerId}>
            {(c) => <Input {...c} className={styles.mono} value={values.workerId} onChange={(e) => update({ workerId: e.target.value })} invalid={Boolean(errors.workerId)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The report was not created" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create report
            </Button>
            <Button variant="ghost" onClick={() => navigate("/expense/reports")}>
              Cancel
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

// ---- Detail ----

export interface LineValues {
  category: string;
  txnDate: string;
  amount: string;
  merchant: string;
  attendees: string;
  paidBy: PaidBy;
  justification: string;
  hasReceipt: boolean;
}

export function emptyLine(): LineValues {
  return { category: "MEALS", txnDate: today(), amount: "", merchant: "", attendees: "0", paidBy: "EMPLOYEE", justification: "", hasReceipt: false };
}

export function validateLine(v: LineValues): { errors: Errors; input?: NewLineInput } {
  const errors: Errors = {};
  if (!DAY.test(v.txnDate)) errors.txnDate = "Enter the date as YYYY-MM-DD.";
  if (!DECIMAL.test(v.amount.trim())) errors.amount = "Enter the amount as a number such as 42.50.";
  if (v.merchant.length > 200) errors.merchant = "Enter at most 200 characters.";
  const attendees = Number(v.attendees);
  if (!/^\d+$/.test(v.attendees.trim()) || attendees > 500) errors.attendees = "Enter 0 to 500 attendees.";
  if (v.justification.length > 500) errors.justification = "Enter at most 500 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      category: v.category,
      txnDate: v.txnDate,
      amount: v.amount.trim(),
      merchant: v.merchant.trim(),
      attendees,
      hasReceipt: v.hasReceipt,
      justification: v.justification.trim(),
      paidBy: v.paidBy,
    },
  };
}

const LINE_FIELDS = ["category", "txnDate", "amount", "merchant", "attendees", "justification", "paidBy"];

/** "<lineId>/<ruleId>" refusal errors as "Line n: message", in line order. */
export function refusalMessages(report: ExpenseReport, fields: Record<string, string>): string[] {
  const lineNo = new Map(report.lines.map((l) => [l.id, l.lineNo]));
  return Object.entries(fields)
    .map(([key, message]) => {
      const [lineId, ruleId] = key.split("/");
      const n = lineNo.get(lineId);
      return { order: n ?? Number.MAX_SAFE_INTEGER, text: `${n ? `Line ${n}` : "A line"}: ${message}${ruleId ? ` (${ruleId})` : ""}` };
    })
    .sort((a, b) => a.order - b.order)
    .map((m) => m.text);
}

function PolicyBadges({ line }: { line: ReportLine }) {
  if (line.policyResults.length === 0) return <Badge tone="success">No issues</Badge>;
  return (
    <span className={styles.badges}>
      {line.policyResults.map((r) => (
        <Badge key={r.ruleId} tone={r.satisfied ? "info" : r.outcome === "BLOCK" ? "danger" : "warning"}>
          {OUTCOME_LABEL[r.outcome]}
          {r.satisfied ? ", satisfied" : ""}
        </Badge>
      ))}
    </span>
  );
}

/** One report: its totals, lines with policy results, adding and removing lines, and submit. */
export function ReportDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [report, setReport] = useState<ExpenseReport | null>(null);
  const [loadFailure, setLoadFailure] = useState<Problem | null>(null);
  const [line, setLine] = useState<LineValues>(emptyLine);
  const [lineErrors, setLineErrors] = useState<Errors>({});
  const [lineFailure, setLineFailure] = useState<Failure | null>(null);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [actionFailure, setActionFailure] = useState<{ title: string; message: string; details: string[] } | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const lineKey = useRef(newIdempotencyKey());
  // One submit key per report version, so a retry replays and a changed report is a new submit.
  const submitKey = useRef<{ version: number; key: string } | null>(null);

  const load = useCallback(() => {
    let alive = true;
    setLoadFailure(null);
    getReport(id)
      .then((r) => alive && setReport(r))
      .catch((err: unknown) => alive && setLoadFailure(describeProblem(err)));
    return () => {
      alive = false;
    };
  }, [id]);

  useEffect(() => load(), [load]);

  function updateLine(patch: Partial<LineValues>) {
    lineKey.current = newIdempotencyKey();
    setLine((prev) => ({ ...prev, ...patch }));
  }

  async function add(event: FormEvent) {
    event.preventDefault();
    if (!report) return;
    const { errors, input } = validateLine(line);
    setLineErrors(errors);
    setLineFailure(null);
    setNotice(null);
    if (!input) return;
    setAdding(true);
    try {
      const updated = await addReportLine(report.id, input, lineKey.current);
      setReport(updated);
      lineKey.current = newIdempotencyKey();
      setLine(emptyLine());
      setNotice(`Line ${updated.lines[updated.lines.length - 1]?.lineNo ?? ""} added.`);
    } catch (err) {
      const split = splitProblem(describeProblem(err), LINE_FIELDS);
      setLineErrors(split.fields);
      setLineFailure(split.failure);
    } finally {
      setAdding(false);
    }
  }

  async function remove(target: ReportLine) {
    if (!report) return;
    setRemoving(target.id);
    setActionFailure(null);
    setNotice(null);
    try {
      setReport(await deleteReportLine(report.id, target.id, report.version));
      setNotice(`Line ${target.lineNo} removed.`);
    } catch (err) {
      const p = describeProblem(err);
      setActionFailure({ title: "The line was not removed", message: `${p.message} (${p.code})`, details: [] });
      if (p.code === "EXP-REP-004") load();
    } finally {
      setRemoving(null);
    }
  }

  async function submit() {
    if (!report) return;
    if (submitKey.current?.version !== report.version) submitKey.current = { version: report.version, key: newIdempotencyKey() };
    setSubmitting(true);
    setActionFailure(null);
    setNotice(null);
    try {
      setReport(await submitReport(report.id, report.version, submitKey.current.key));
      setNotice("Report submitted for approval.");
    } catch (err) {
      const p = describeProblem(err);
      const details = refusalMessages(report, p.fields);
      setActionFailure({
        title: "The report was not submitted",
        message: details.length > 0 ? "Fix these lines, for example by removing a line and adding it again with its receipt or justification." : `${p.message} (${p.code})`,
        details,
      });
      if (p.code === "EXP-REP-004") load();
      else getReport(report.id).then(setReport, () => undefined);
    } finally {
      setSubmitting(false);
    }
  }

  if (loadFailure) {
    if (loadFailure.code === "EXP-REP-001" || loadFailure.code === "COM-0001") {
      return (
        <EmptyState
          icon="receipt"
          title="Report not found"
          description="It does not exist or is outside your legal entities."
          action={<Button onClick={() => navigate("/expense/reports")}>Back to reports</Button>}
        />
      );
    }
    return (
      <Alert tone="danger" title="The report could not be loaded" live="alert" action={<Button size="sm" onClick={load}>Try again</Button>}>
        {loadFailure.message} ({loadFailure.code})
      </Alert>
    );
  }

  if (!report) {
    return (
      <div className={styles.stack} aria-busy="true" aria-label="Loading the report">
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const draft = report.status === "DRAFT";
  const lineColumns: DataTableColumn<ReportLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "txnDate", header: "Date", mono: true },
    { key: "category", header: "Category" },
    { key: "merchant", header: "Merchant" },
    { key: "paidBy", header: "Paid by", render: (l) => (l.paidBy === "COMPANY_CARD" ? "Company card" : "Employee") },
    { key: "amount", header: "Amount", align: "end", mono: true, render: (l) => money(report.currency, l.amount) },
    { key: "policy", header: "Policy", render: (l) => <PolicyBadges line={l} /> },
  ];
  if (draft) {
    lineColumns.push({
      key: "actions",
      header: "Actions",
      render: (l) => (
        <Button size="sm" variant="ghost" loading={removing === l.id} aria-label={`Remove line ${l.lineNo}`} onClick={() => remove(l)}>
          Remove
        </Button>
      ),
    });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={report.purpose}
        description={`Report in ${report.currency} for worker ${report.workerId}.`}
        meta={<StatusBadge status={report.status} />}
        actions={
          <>
            <Button variant="ghost" onClick={() => navigate("/expense/reports")}>
              All reports
            </Button>
            {draft ? (
              <Button variant="primary" loading={submitting} disabled={report.lines.length === 0} onClick={submit}>
                Submit report
              </Button>
            ) : null}
          </>
        }
      />
      <StatGroup label="Report totals">
        <StatTile label="Total" value={money(report.currency, report.total)} />
        <StatTile label="Reimbursable" value={money(report.currency, report.reimbursable)} hint="Paid by the employee" />
        <StatTile label="Company paid" value={money(report.currency, report.companyPaid)} hint="Company card" />
        <StatTile label="Lines" value={report.lines.length} />
      </StatGroup>
      {notice ? (
        <Alert tone="success" title={notice} live="status" />
      ) : draft && report.lines.length > 0 ? (
        report.submittable ? (
          <Alert tone="success" title="Ready to submit" live="off" />
        ) : (
          <Alert tone="warning" title="Cannot submit yet" live="off">
            A line is blocked or still needs a receipt or a justification.
          </Alert>
        )
      ) : !draft ? (
        <Alert tone="info" title={`Submitted ${report.submittedAt?.slice(0, 10) ?? ""}`} live="off">
          The report and its lines can no longer change.
        </Alert>
      ) : null}
      {actionFailure ? (
        <Alert tone="danger" title={actionFailure.title} live="alert">
          {actionFailure.message}
          {actionFailure.details.length > 0 ? (
            <ul className={styles.list}>
              {actionFailure.details.map((d) => (
                <li key={d}>{d}</li>
              ))}
            </ul>
          ) : null}
        </Alert>
      ) : null}
      <Card title="Lines">
        <DataTable
          label="Lines"
          columns={lineColumns}
          rows={report.lines}
          getRowKey={(l) => l.id}
          empty={{ title: "No lines yet", description: draft ? "Add the first expense below." : "This report has no lines." }}
        />
      </Card>
      {draft ? (
        <Card title="Add a line">
          <form className={styles.form} onSubmit={add} noValidate>
            <FormField id="add-category" label="Category" error={lineErrors.category}>
              {(c) => (
                <Select {...c} value={line.category} onChange={(e) => updateLine({ category: e.target.value })}>
                  {CATEGORIES.map((cat) => (
                    <option key={cat} value={cat}>
                      {cat.charAt(0) + cat.slice(1).toLowerCase()}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="add-date" label="Date" error={lineErrors.txnDate}>
              {(c) => <Input {...c} type="date" value={line.txnDate} onChange={(e) => updateLine({ txnDate: e.target.value })} invalid={Boolean(lineErrors.txnDate)} />}
            </FormField>
            <FormField id="add-amount" label="Amount" hint={`In ${report.currency}.`} error={lineErrors.amount}>
              {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={line.amount} onChange={(e) => updateLine({ amount: e.target.value })} invalid={Boolean(lineErrors.amount)} />}
            </FormField>
            <FormField id="add-merchant" label="Merchant" optional error={lineErrors.merchant}>
              {(c) => <Input {...c} value={line.merchant} maxLength={200} onChange={(e) => updateLine({ merchant: e.target.value })} invalid={Boolean(lineErrors.merchant)} />}
            </FormField>
            <FormField id="add-attendees" label="Attendees" error={lineErrors.attendees}>
              {(c) => <Input {...c} inputMode="numeric" value={line.attendees} onChange={(e) => updateLine({ attendees: e.target.value })} invalid={Boolean(lineErrors.attendees)} />}
            </FormField>
            <FormField id="add-paid-by" label="Paid by" error={lineErrors.paidBy}>
              {(c) => (
                <Select {...c} value={line.paidBy} onChange={(e) => updateLine({ paidBy: e.target.value as PaidBy })}>
                  <option value="EMPLOYEE">Employee</option>
                  <option value="COMPANY_CARD">Company card</option>
                </Select>
              )}
            </FormField>
            <div className={styles.wide}>
              <FormField id="add-justification" label="Justification" optional error={lineErrors.justification}>
                {(c) => <Input {...c} value={line.justification} maxLength={500} onChange={(e) => updateLine({ justification: e.target.value })} invalid={Boolean(lineErrors.justification)} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <span className={styles.check}>
                <Input id="add-receipt" type="checkbox" checked={line.hasReceipt} onChange={(e) => updateLine({ hasReceipt: e.target.checked })} />
                <label htmlFor="add-receipt">Receipt attached</label>
              </span>
            </div>
            {lineFailure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="The line was not added" live="alert">
                  {lineFailure.message} ({lineFailure.code})
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={adding}>
                Add line
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
