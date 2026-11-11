import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, entityText, FormField, Input, ListPage, PageHeader, StatusPill, type ListColumn, type ListData, type ListFilterDef, type ListQuery, useFormBaseline, useUnsavedChanges } from "../../shell/ui";
import {
  createQuotation,
  describeProblem,
  listQuotations,
  newKey,
  QUOTATION_STATUSES,
  SalesApiError,
  type NewQuotation,
  type Problem,
  type Quotation,
  type QuotationStatus,
} from "./api/client";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { formatDateTime, money, sentence, today, UUID } from "./format";
import { emptyLine, validateLines, type LineValues } from "./lines";
import { LinesEditor } from "./LinesEditor";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

type Errors = Record<string, string>;

/** Whether a DRAFT or SENT quotation's validity has passed on day `on` (BR-27.5). */
export function isExpired(q: Pick<Quotation, "status" | "validUntil">, on: string = today()): boolean {
  return (q.status === "DRAFT" || q.status === "SENT") && q.validUntil < on;
}

/** The status pill, with an expiry badge while the hourly expiry job is not built. */
export function QuotationStatusBadge({ quotation }: { quotation: Pick<Quotation, "status" | "validUntil"> }) {
  return (
    <span className={styles.actions}>
      <StatusPill status={quotation.status} />
      {isExpired(quotation) ? <Badge tone="warning">Validity passed</Badge> : null}
    </span>
  );
}

export interface QuotationValues {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  currency: string;
  validUntil: string;
  probabilityPct: string;
  listingOverrideReason: string;
  lines: LineValues[];
}

/** Checks that mirror the service (api/quotations.go buildQuotation). Keys are the service's field paths. */
export function validateQuotation(values: QuotationValues, on: string = today()): { errors: Errors; input?: NewQuotation } {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Choose the selling legal entity.";
  if (!UUID.test(values.customerId.trim())) errors.customerId = "Choose the customer.";
  const invoice = values.invoiceAccountId.trim();
  if (invoice && !UUID.test(invoice)) errors.invoiceAccountId = "Choose the invoice account, or leave it empty to invoice the customer.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (values.validUntil && !DATE.test(values.validUntil)) errors.validUntil = "Choose a date.";
  else if (values.validUntil && values.validUntil < on) errors.validUntil = "The quotation must be valid at least until today.";
  const probability = values.probabilityPct.trim();
  if (probability && (!/^\d{1,3}$/.test(probability) || Number(probability) > 100)) errors.probabilityPct = "Enter a whole percentage from 0 to 100.";
  const override = values.listingOverrideReason.trim();
  if (override.length > 500) errors.listingOverrideReason = "Use at most 500 characters.";
  const lines = validateLines(values.lines, errors);
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      customerId: values.customerId.trim(),
      ...(invoice ? { invoiceAccountId: invoice } : {}),
      currency: values.currency,
      ...(values.validUntil ? { validUntil: values.validUntil } : {}),
      ...(probability ? { probabilityPct: Number(probability) } : {}),
      ...(override ? { listingOverrideReason: override } : {}),
      lines,
    },
  };
}

const columns: ListColumn<Quotation>[] = [
  { key: "quotationNumber", header: "Quotation", mono: true },
  { key: "customerId", header: "Customer", sortable: false, render: (q) => <EntityName entity="customer" id={q.customerId} fallback="Customer" />, csv: (q) => entityText("customer", q.customerId) },
  { key: "status", header: "Status", render: (q) => <QuotationStatusBadge quotation={q} />, sortValue: (q) => q.status, csv: (q) => sentence(q.status) },
  { key: "validUntil", header: "Valid until" },
  { key: "probabilityPct", header: "Probability", align: "end", render: (q) => `${q.probabilityPct}%`, sortValue: (q) => q.probabilityPct, csv: (q) => q.probabilityPct },
  { key: "netTotal", header: "Net total", align: "end", render: (q) => money(q.netTotal, q.currency), sortValue: (q) => q.netTotal, csv: (q) => q.netTotal, csvWith: "currency" },
  { key: "lines", header: "Open lines", align: "end", sortable: false, render: (q) => `${q.openLines} of ${q.lineCount}`, csv: (q) => `${q.openLines} of ${q.lineCount}` },
  { key: "createdAt", header: "Created", render: (q) => formatDateTime(q.createdAt), csv: (q) => q.createdAt },
  { key: "currency", header: "Currency", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  { kind: "entity", key: "legalEntityId", label: "Legal entity", entity: "legalEntity" },
  { kind: "entity", key: "customerId", label: "Customer", entity: "customer" },
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: QUOTATION_STATUSES.map((s) => ({ value: s, label: sentence(s) })) },
];

/** The quotations of the filter; the service answers one page of at most 200, so there is nothing more to load. */
function useQuotationRows(query: ListQuery): ListData<Quotation> {
  const { legalEntityId = "", customerId = "", status = "" } = query.filters;
  const [forbidden, setForbidden] = useState(false);
  const state = useLoad(async (signal) => {
    try {
      const rows = await listQuotations({ legalEntityId, customerId, status: status as QuotationStatus | "" }, signal);
      if (!signal.aborted) setForbidden(false);
      return rows;
    } catch (error) {
      if (!signal.aborted) setForbidden(error instanceof SalesApiError && error.status === 403);
      throw error;
    }
  }, `${legalEntityId}|${customerId}|${status}`);
  return {
    rows: state.error ? [] : (state.data ?? []),
    loading: state.loading,
    loadingMore: false,
    error: state.error ?? undefined,
    forbidden,
    hasMore: false,
    loadMore: () => undefined,
    retry: state.reload,
  };
}

/** Quotations pipeline (PRD §5.27.14): every quotation, newest first, with validity and probability (FR-27.1). */
export function QuotationsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="sales-quotations"
      title="Sales quotations"
      description="Priced offers to customers. Send a quotation, then convert it, whole or by line, into a draft order while it is valid."
      noun="quotations"
      singular="quotation"
      actions={
        <Link className={buttonClassName("primary")} to="/sales/quotations/new">
          New quotation
        </Link>
      }
      columns={columns}
      filters={filters}
      useRows={useQuotationRows}
      search={(q) => ({ number: q.quotationNumber })}
      getRowKey={(q) => q.id}
      onRowOpen={(q) => navigate(`/sales/quotations/${q.id}`)}
      empty={{
        title: "No quotations yet",
        description: "Create a quotation to offer a customer prices before they order.",
        action: (
          <Link className={buttonClassName("secondary")} to="/sales/quotations/new">
            New quotation
          </Link>
        ),
      }}
    />
  );
}

/** Creates a DRAFT quotation numbered SQ- (duty sales.quotation.maintain). */
export function NewQuotationPage() {
  const navigate = useNavigate();
  const start = today();
  const [values, setValues] = useState<QuotationValues>({
    legalEntityId: "",
    customerId: "",
    invoiceAccountId: "",
    currency: "USD",
    validUntil: "",
    probabilityPct: "",
    listingOverrideReason: "",
    lines: [emptyLine(start)],
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<QuotationValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  const { prompt, release } = useUnsavedChanges(useFormBaseline(values));
  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateQuotation(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createQuotation(input, key.current);
      release();
      navigate(`/sales/quotations/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const header = (name: keyof Omit<QuotationValues, "lines">, label: string, hint?: string, extra: { type?: string; mono?: boolean; optional?: boolean; numeric?: boolean } = {}) => (
    <FormField id={`quotation-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          inputMode={extra.numeric ? "numeric" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: name === "currency" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  const lookup = (name: "legalEntityId" | "customerId" | "invoiceAccountId", entity: "legalEntity" | "customer", label: string, hint: string, optional = false) => (
    <FormField id={`quotation-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <EntityLookup
          entity={entity}
          {...control}
          scope={{ legalEntityId: values.legalEntityId || undefined }}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New sales quotation" description="The quotation is saved as a draft. Send it from the quotation page, then convert it into an order." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New sales quotation">
          {lookup("legalEntityId", "legalEntity", "Legal entity", "The selling legal entity.")}
          {lookup("customerId", "customer", "Customer", "Search by name or account number.")}
          {lookup("invoiceAccountId", "customer", "Invoice account", "Leave empty to invoice the customer.", true)}
          {header("currency", "Currency")}
          {header("validUntil", "Valid until", "Leave empty for the legal entity's quotation expiry (30 days unless changed in Sales parameters).", { type: "date", optional: true })}
          {header("probabilityPct", "Win probability %", "Leave empty for the legal entity's default (50% unless changed in Sales parameters).", { optional: true, numeric: true })}
          {header("listingOverrideReason", "Listing override reason", "Needs the sales.order.approve duty. Accepts lines the product listing denies and logs the reason.", { optional: true })}
          <LinesEditor lines={values.lines} errors={errors} legalEntityId={values.legalEntityId} newLineDate={start} onChange={(lines) => update({ lines })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the quotation" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save draft quotation
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/quotations">
              Cancel
            </Link>
          </div>
        </form>
      {prompt}
      </Card>
    </div>
  );
}
