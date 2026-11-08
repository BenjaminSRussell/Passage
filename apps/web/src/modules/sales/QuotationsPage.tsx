import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  createQuotation,
  describeProblem,
  listQuotations,
  newKey,
  QUOTATION_STATUSES,
  type NewQuotation,
  type Problem,
  type Quotation,
  type QuotationFilter,
  type QuotationStatus,
} from "./api/client";
import { formatDateTime, money, sentence, shortId, today, UUID } from "./format";
import { emptyLine, validateLines, type LineValues } from "./lines";
import { LinesEditor } from "./LinesEditor";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

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
  customerGroup: string;
  listingOverrideReason: string;
  lines: LineValues[];
}

/** Checks that mirror the service (api/quotations.go buildQuotation). Keys are the service's field paths. */
export function validateQuotation(values: QuotationValues, on: string = today()): { errors: Errors; input?: NewQuotation } {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(values.customerId.trim())) errors.customerId = "Enter the customer's UUID.";
  const invoice = values.invoiceAccountId.trim();
  if (invoice && !UUID.test(invoice)) errors.invoiceAccountId = "Enter the invoice account's UUID, or leave it empty to invoice the customer.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (values.validUntil && !DATE.test(values.validUntil)) errors.validUntil = "Choose a date.";
  else if (values.validUntil && values.validUntil < on) errors.validUntil = "The quotation must be valid at least until today.";
  const probability = values.probabilityPct.trim();
  if (probability && (!/^\d{1,3}$/.test(probability) || Number(probability) > 100)) errors.probabilityPct = "Enter a whole percentage from 0 to 100.";
  const group = values.customerGroup.trim();
  if (group.length > 80) errors.customerGroup = "Use at most 80 characters.";
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
      ...(group ? { customerGroup: group } : {}),
      ...(override ? { listingOverrideReason: override } : {}),
      lines,
    },
  };
}

const columns: DataTableColumn<Quotation>[] = [
  { key: "quotationNumber", header: "Quotation", mono: true },
  { key: "customerId", header: "Customer", mono: true, render: (q) => shortId(q.customerId) },
  { key: "status", header: "Status", render: (q) => <QuotationStatusBadge quotation={q} /> },
  { key: "validUntil", header: "Valid until" },
  { key: "probabilityPct", header: "Probability", align: "end", render: (q) => `${q.probabilityPct}%` },
  { key: "netTotal", header: "Net total", align: "end", render: (q) => money(q.netTotal, q.currency) },
  { key: "lines", header: "Open lines", align: "end", render: (q) => `${q.openLines} of ${q.lineCount}` },
  { key: "createdAt", header: "Created", render: (q) => formatDateTime(q.createdAt) },
];

/** Quotations pipeline (PRD §5.27.14): every quotation, newest first, with validity and probability (FR-27.1). */
export function QuotationsPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<{ legalEntityId: string; customerId: string; status: QuotationStatus | "" }>({ legalEntityId: "", customerId: "", status: "" });
  const [filter, setFilter] = useState<QuotationFilter>({});
  const invalid: Errors = {};
  if (draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.customerId.trim() && !UUID.test(draft.customerId.trim())) invalid.customerId = "Enter a customer UUID.";
  const state = useLoad((signal) => listQuotations(filter, signal), JSON.stringify(filter));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Sales quotations"
        description="Priced offers to customers. Send a quotation, then convert it, whole or by line, into a draft order while it is valid."
        actions={
          <Link className={buttonClassName("primary")} to="/sales/quotations/new">
            New quotation
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length === 0) setFilter({ legalEntityId: draft.legalEntityId.trim(), customerId: draft.customerId.trim(), status: draft.status });
        }}
      >
        <FormField id="quotations-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="quotations-customer" label="Customer" error={invalid.customerId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.customerId} onChange={(e) => setDraft({ ...draft, customerId: e.target.value })} invalid={Boolean(invalid.customerId)} />}
        </FormField>
        <FormField id="quotations-status" label="Status">
          {(control) => (
            <Select {...control} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as QuotationStatus | "" })}>
              <option value="">Any status</option>
              {QUOTATION_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load quotations" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Sales quotations"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(q) => q.id}
          loading={state.loading}
          empty={{ title: "No quotations yet", description: "Create a quotation to offer a customer prices before they order." }}
          onRowOpen={(q) => navigate(`/sales/quotations/${q.id}`)}
        />
      )}
    </div>
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
    customerGroup: "",
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

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateQuotation(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createQuotation(input, key.current);
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

  return (
    <div className={styles.stack}>
      <PageHeader title="New sales quotation" description="The quotation is saved as a draft. Send it from the quotation page, then convert it into an order." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New sales quotation">
          {header("legalEntityId", "Legal entity", "UUID of the selling legal entity.", { mono: true })}
          {header("customerId", "Customer", "UUID of the customer party.", { mono: true })}
          {header("invoiceAccountId", "Invoice account", "Leave empty to invoice the customer.", { mono: true, optional: true })}
          {header("currency", "Currency")}
          {header("validUntil", "Valid until", "Leave empty for the legal entity's quotation expiry (30 days unless changed in Sales parameters).", { type: "date", optional: true })}
          {header("probabilityPct", "Win probability %", "Leave empty for the legal entity's default (50% unless changed in Sales parameters).", { optional: true, numeric: true })}
          {header("customerGroup", "Customer group", "Applies group listing entries, for example EU-RETAIL; a converted order carries it.", { optional: true })}
          {header("listingOverrideReason", "Listing override reason", "Needs the sales.order.approve duty. Accepts lines the product listing denies and logs the reason.", { optional: true })}
          <LinesEditor lines={values.lines} errors={errors} newLineDate={start} onChange={(lines) => update({ lines })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the quotation" live="alert">
                {failure.message} ({failure.code})
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
      </Card>
    </div>
  );
}
