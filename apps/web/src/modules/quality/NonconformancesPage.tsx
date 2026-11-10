import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatGroup, StatTile, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  createNonconformance,
  describeProblem,
  getNonconformance,
  listNonconformances,
  newKey,
  type NcrSeverity,
  type NcrSource,
  type NcrStatus,
  type NewNonconformance,
  type Nonconformance,
  type Problem,
} from "./api/client";
import { CorrectionPanel, NcrActions } from "./CorrectionPanel";
import {
  CODE,
  DECIMAL,
  dash,
  formatDateTime,
  NCR_SEVERITIES,
  NCR_SEVERITY_LABELS,
  NCR_SOURCE_LABELS,
  NCR_SOURCES,
  NCR_STATUS_LABELS,
  NCR_STATUSES,
  CORRECTION_STATUS_LABELS,
  qty,
  shortId,
  UUID,
} from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export function NcrStatusBadge({ status }: { status: NcrStatus }) {
  const s = NCR_STATUS_LABELS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

export function NcrSeverityBadge({ severity }: { severity: NcrSeverity }) {
  const s = NCR_SEVERITY_LABELS[severity] ?? { label: severity, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

const columns: DataTableColumn<Nonconformance>[] = [
  { key: "id", header: "NCR", mono: true, render: (row) => <Link to={`/quality/nonconformances/${row.id}`}>{`Open NCR ${shortId(row.id)}`}</Link> },
  { key: "createdAt", header: "Raised", render: (row) => formatDateTime(row.createdAt) },
  { key: "source", header: "Source", render: (row) => NCR_SOURCE_LABELS[row.source] ?? row.source },
  { key: "problemType", header: "Problem", mono: true },
  { key: "severity", header: "Severity", render: (row) => <NcrSeverityBadge severity={row.severity} /> },
  { key: "itemId", header: "Item", mono: true, render: (row) => shortId(row.itemId) },
  { key: "qty", header: "Quantity", align: "end", mono: true, render: (row) => `${qty(row.qty)} ${row.uom}` },
  {
    key: "qualityOrderId",
    header: "Quality order",
    mono: true,
    render: (row) => (row.qualityOrderId ? <Link to={`/quality/orders/${row.qualityOrderId}`}>{shortId(row.qualityOrderId)}</Link> : dash),
  },
  { key: "status", header: "Status", render: (row) => <NcrStatusBadge status={row.status} /> },
];

type FilterName = "status" | "source" | "severity";

/** Nonconformances (FR-33.7), newest first, by status, source and severity (NCR and CAPA status inquiry). */
export function NonconformancesPage() {
  const [params, setParams] = useSearchParams();
  const status = (params.get("status") ?? "") as NcrStatus | "";
  const source = (params.get("source") ?? "") as NcrSource | "";
  const severity = (params.get("severity") ?? "") as NcrSeverity | "";
  const state = useLoad((signal) => listNonconformances({ status, source, severity }, signal), `ncrs|${status}|${source}|${severity}`);
  const filtered = Boolean(status || source || severity);

  function setFilter(name: FilterName, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(name, value);
    else next.delete(name);
    setParams(next);
  }

  const filter = (name: FilterName, label: string, all: string, options: readonly string[], labelOf: (v: string) => string, value: string) => (
    <FormField id={`ncrs-${name}`} label={label}>
      {(control) => (
        <Select {...control} value={value} onChange={(event) => setFilter(name, event.target.value)}>
          <option value="">{all}</option>
          {options.map((o) => (
            <option key={o} value={o}>
              {labelOf(o)}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Nonconformances"
        description="Nonconformance reports from receipts, production, customers and usage decisions, with their 8D correction status."
        actions={
          <Link className={buttonClassName("primary")} to="/quality/nonconformances/new">
            Raise a nonconformance
          </Link>
        }
      />
      <div className={styles.toolbar}>
        {filter("status", "Status", "All statuses", NCR_STATUSES, (v) => NCR_STATUS_LABELS[v as NcrStatus].label, status)}
        {filter("source", "Source", "All sources", NCR_SOURCES, (v) => NCR_SOURCE_LABELS[v as NcrSource], source)}
        {filter("severity", "Severity", "All severities", NCR_SEVERITIES, (v) => NCR_SEVERITY_LABELS[v as NcrSeverity].label, severity)}
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load nonconformances" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Nonconformances"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            filtered
              ? { title: "No nonconformances match", description: "Clear a filter to see more." }
              : { title: "No nonconformances yet", description: "A usage decision that rejects quantity raises one, or raise one here." }
          }
        />
      )}
    </div>
  );
}

export interface NcrValues {
  source: NcrSource;
  severity: NcrSeverity;
  problemType: string;
  qualityOrderId: string;
  itemId: string;
  qty: string;
  uom: string;
  vendorId: string;
  customerId: string;
  description: string;
}

type Errors = Partial<Record<keyof NcrValues, string>>;

const fieldNames: (keyof NcrValues)[] = ["source", "severity", "problemType", "qualityOrderId", "itemId", "qty", "uom", "vendorId", "customerId", "description"];

/** Checks that mirror the service's ValidateNCR, and the request the form sends. */
export function validateNcr(values: NcrValues): { errors: Errors; input?: NewNonconformance } {
  const errors: Errors = {};
  const order = values.qualityOrderId.trim();
  const item = values.itemId.trim();
  const uom = values.uom.trim();
  if (order && !UUID.test(order)) errors.qualityOrderId = "Enter the quality order id (a UUID), or leave it empty.";
  if (item ? !UUID.test(item) : !order) errors.itemId = "Enter the item id (a UUID); it can be left empty when a quality order is given.";
  if (!CODE.test(values.problemType.trim())) errors.problemType = "Enter a code of 2 to 32 capitals, digits, _ or -, such as DIMENSION.";
  const q = values.qty.trim();
  if (!DECIMAL.test(q) || /^0+(\.0+)?$/.test(q)) errors.qty = "Enter a quantity above 0 with up to 6 decimals.";
  if (uom.length > 10 || (!uom && !order)) errors.uom = "Enter the unit, up to 10 characters; it can be left empty when a quality order is given.";
  for (const name of ["vendorId", "customerId"] as const) {
    const v = values[name].trim();
    if (v && !UUID.test(v)) errors[name] = "Enter a UUID, or leave it empty.";
  }
  if (values.description.length > 2000) errors.description = "Keep the description to 2000 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewNonconformance = { source: values.source, severity: values.severity, problemType: values.problemType.trim(), qty: q };
  if (order) input.qualityOrderId = order;
  if (item) input.itemId = item;
  if (uom) input.uom = uom;
  if (values.vendorId.trim()) input.vendorId = values.vendorId.trim();
  if (values.customerId.trim()) input.customerId = values.customerId.trim();
  if (values.description.trim()) input.description = values.description.trim();
  return { errors, input };
}

/** Raise a nonconformance; ?qualityOrderId= links it to an order (BR-33.4 amendment). */
export function NewNonconformancePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<NcrValues>({
    source: "INTERNAL",
    severity: "MAJOR",
    problemType: "",
    qualityOrderId: params.get("qualityOrderId") ?? "",
    itemId: "",
    qty: "",
    uom: "",
    vendorId: "",
    customerId: "",
    description: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<NcrValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const checked = validateNcr(values);
    setErrors(checked.errors);
    setFailure(null);
    if (!checked.input) return;
    setBusy(true);
    try {
      const ncr = await createNonconformance(checked.input, key.current);
      navigate(`/quality/nonconformances/${ncr.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of fieldNames) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "QM-0141") fieldErrors.qualityOrderId = "This quality order does not exist.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: keyof NcrValues, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`ncr-${id}`} label={label} hint={hint} error={errors[id]}>
      {(control) => <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value })} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Raise a nonconformance"
        description="The report starts as New and is approved or refused by a quality engineer. Against a quality order, the item and unit default to the order's."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Raise a nonconformance">
          <FormField id="ncr-source" label="Source" error={errors.source}>
            {(control) => (
              <Select {...control} value={values.source} onChange={(event) => update({ source: event.target.value as NcrSource })}>
                {NCR_SOURCES.map((s) => (
                  <option key={s} value={s}>
                    {NCR_SOURCE_LABELS[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="ncr-severity" label="Severity" error={errors.severity}>
            {(control) => (
              <Select {...control} value={values.severity} onChange={(event) => update({ severity: event.target.value as NcrSeverity })}>
                {NCR_SEVERITIES.map((s) => (
                  <option key={s} value={s}>
                    {NCR_SEVERITY_LABELS[s].label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("problemType", "Problem type", "A code such as DIMENSION or CONTAMINATION.", { className: styles.mono, maxLength: 32 })}
          {text("qualityOrderId", "Quality order id", "Optional.", { className: styles.mono })}
          {text("itemId", "Item id", undefined, { className: styles.mono })}
          {text("qty", "Nonconforming quantity", undefined, { inputMode: "decimal" })}
          {text("uom", "Unit", undefined, { maxLength: 10 })}
          {text("vendorId", "Vendor id", "Optional; the supplier scorecard counts vendor NCRs.", { className: styles.mono })}
          {text("customerId", "Customer id", "Optional; for a customer complaint.", { className: styles.mono })}
          <div className={styles.wide}>
            <FormField id="ncr-description" label="Description" error={errors.description}>
              {(control) => (
                <Textarea {...control} rows={4} maxLength={2000} value={values.description} invalid={Boolean(errors.description)} onChange={(event) => update({ description: event.target.value })} />
              )}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not raise the nonconformance" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Raise nonconformance
            </Button>
            <Link className={buttonClassName("ghost")} to="/quality/nonconformances">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One nonconformance: its details, approve or refuse, and its 8D correction. */
export function NonconformanceDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getNonconformance(id, signal), `ncr|${id}`);
  const [latest, setLatest] = useState<Nonconformance | null>(null);
  const ncr = latest && latest.id === id && (!state.data || latest.version >= state.data.version) ? latest : state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Nonconformance" />
        <Alert tone="danger" title="Could not load the nonconformance" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!ncr) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Nonconformance" />
        <span className="visually-hidden">Loading the nonconformance…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const detail = (term: string, value: string, mono = false) => (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
    </div>
  );
  const correction = ncr.correction;
  const stepLabel = !correction ? dash : correction.status === "OPEN" ? `D${correction.currentStep}` : correction.status === "CLOSED" ? "Closed" : "Effectiveness check";

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Nonconformance ${shortId(ncr.id)}`}
        description={`${NCR_SOURCE_LABELS[ncr.source] ?? ncr.source}, ${ncr.problemType}`}
        meta={<NcrStatusBadge status={ncr.status} />}
        actions={
          <Link className={buttonClassName("secondary")} to="/quality/nonconformances">
            All nonconformances
          </Link>
        }
      />
      <StatGroup label="Nonconformance summary">
        <StatTile label="Quantity" value={`${qty(ncr.qty)} ${ncr.uom}`} />
        <StatTile label="Severity" value={NCR_SEVERITY_LABELS[ncr.severity]?.label ?? ncr.severity} tone={ncr.severity === "CRITICAL" ? "danger" : "neutral"} />
        <StatTile label="8D step" value={stepLabel} />
        <StatTile label="Overdue steps" value={correction ? correction.overdueSteps.length : 0} tone={correction && correction.overdueSteps.length > 0 ? "danger" : "neutral"} />
      </StatGroup>
      {ncr.status === "REFUSED" ? (
        <Alert tone="info" title="Refused">
          {ncr.statusReason || "No reason was recorded."}
        </Alert>
      ) : null}
      <Card title="Report">
        <dl className={styles.details}>
          {detail("Item", ncr.itemId, true)}
          {detail("Dimensions", Object.entries(ncr.dims ?? {}).map(([k, v]) => `${k} ${v}`).join(", ") || dash)}
          {detail("Vendor", ncr.vendorId ?? dash, Boolean(ncr.vendorId))}
          {detail("Customer", ncr.customerId ?? dash, Boolean(ncr.customerId))}
          {detail("Raised", `${ncr.createdBy || dash}, ${formatDateTime(ncr.createdAt)}`)}
          {detail("Correction", correction ? CORRECTION_STATUS_LABELS[correction.status].label : "Not started")}
          <div className={styles.entry}>
            <dt className={styles.term}>Quality order</dt>
            <dd className={`${styles.value} ${styles.mono}`}>
              {ncr.qualityOrderId ? <Link to={`/quality/orders/${ncr.qualityOrderId}`}>{ncr.qualityOrderId}</Link> : dash}
            </dd>
          </div>
        </dl>
        {ncr.description ? <p className={styles.note}>{ncr.description}</p> : null}
      </Card>
      <NcrActions ncr={ncr} onChanged={setLatest} />
      {correction ? <CorrectionPanel ncr={ncr} correction={correction} onChanged={setLatest} /> : null}
    </div>
  );
}
