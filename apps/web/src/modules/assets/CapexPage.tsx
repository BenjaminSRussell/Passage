import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  CAPEX_STATUSES,
  createCapex,
  describeProblem,
  getCapex,
  listCapex,
  newKey,
  submitCapex,
  type CapexRequest,
  type CapexStatus,
  type CapexVariant,
  type NewCapexRequest,
  type Problem,
} from "./api/client";
import { StatusBadge } from "./badges";
import { amount, DATE, dash, parseYearLines, sentence, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const level: Record<string, string> = { PlantManager: "Plant manager", CFO: "CFO", Board: "Board" };

const listColumns: DataTableColumn<CapexRequest>[] = [
  { key: "number", header: "Request", mono: true },
  { key: "title", header: "Title" },
  { key: "status", header: "Status", render: (c) => <StatusBadge status={c.status} /> },
  { key: "approvalLevel", header: "Approval level", render: (c) => (c.approvalLevel ? level[c.approvalLevel] : dash) },
  { key: "requestedAmount", header: "Requested", align: "end", render: (c) => amount(c.requestedAmount, c.requestedAmount ? c.currency : null) },
];

/** Capital appropriation requests (FR-37.17), filterable by status. */
export function CapexListPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<CapexStatus | "">("");
  const state = useLoad((signal) => listCapex({ status }, signal), `capex|${status}`);
  const [more, setMore] = useState<CapexRequest[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listCapex({ status, after: cursor });
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Capex requests"
        description="Capital appropriation requests with their variants. Submitting a request routes the chosen variant to an approval level by amount."
        actions={
          <Link className={buttonClassName("primary")} to="/assets/capex/new">
            New request
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="capex-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as CapexStatus | "")}>
              <option value="">All statuses</option>
              {CAPEX_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load capex requests" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Capex requests"
          columns={listColumns}
          rows={[...(state.data?.items ?? []), ...more]}
          getRowKey={(c) => c.id}
          loading={state.loading}
          empty={{ title: "No capex requests", description: status ? "No request has this status." : "Create a request to plan a capital investment." }}
          onRowOpen={(c) => navigate(`/assets/capex/${c.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more requests" live="alert">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
      {cursor && !state.error ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

export interface VariantValues {
  description: string;
  amounts: string;
  inServiceDate: string;
  profileAssumption: string;
}

export interface CapexValues {
  legalEntityId: string;
  number: string;
  title: string;
  requesterId: string;
  currency: string;
  variants: VariantValues[];
}

export type CapexErrors = Partial<Record<Exclude<keyof CapexValues, "variants"> | "variants", string>> & { variantFields?: Partial<Record<keyof VariantValues, string>>[] };

/** Checks that mirror the service (internal/api createCapexRequest). Amounts are "2026=200000.00" lines. */
export function validateCapex(values: CapexValues): { errors: CapexErrors; input?: NewCapexRequest } {
  const errors: CapexErrors = {};
  const number = values.number.trim();
  const title = values.title.trim();
  const requester = values.requesterId.trim();
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (number.length < 1 || number.length > 40) errors.number = "Enter a request number of up to 40 characters.";
  if (title.length < 1 || title.length > 200) errors.title = "Enter a title of up to 200 characters.";
  if (requester.length < 1 || requester.length > 200) errors.requesterId = "Enter who requests the investment.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (values.variants.length < 1 || values.variants.length > 10) errors.variants = "Add one to ten variants.";
  const variantFields: Partial<Record<keyof VariantValues, string>>[] = [];
  const variants = values.variants.map((v, i) => {
    const f: Partial<Record<keyof VariantValues, string>> = {};
    const description = v.description.trim();
    const profile = v.profileAssumption.trim();
    if (description.length < 1 || description.length > 200) f.description = "Describe the variant in up to 200 characters.";
    if (profile.length < 1 || profile.length > 60) f.profileAssumption = "Name the depreciation profile assumed, such as SL-120.";
    if (!DATE.test(v.inServiceDate)) f.inServiceDate = "Choose the expected in-service date.";
    const parsed = parseYearLines(v.amounts);
    if (parsed.error) f.amounts = parsed.error;
    else if (Object.keys(parsed.values).length === 0) f.amounts = "Enter at least one year, such as 2026=200000.00.";
    else if (Object.values(parsed.values).some((a) => /^0+(\.0+)?$/.test(a))) f.amounts = "Every amount must be above 0.";
    if (Object.keys(f).length > 0) variantFields[i] = f;
    return { description, amountsByYear: parsed.values, inServiceDate: v.inServiceDate, profileAssumption: profile };
  });
  if (variantFields.length > 0) errors.variantFields = variantFields;
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { legalEntityId: values.legalEntityId.trim(), number, title, requesterId: requester, currency: values.currency, variants } };
}

function blankVariant(): VariantValues {
  return { description: "", amounts: "", inServiceDate: today(), profileAssumption: "" };
}

/** Create a capex request in Draft with one or more variants. */
export function NewCapexPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CapexValues>(() => ({ legalEntityId: "", number: "", title: "", requesterId: "", currency: "USD", variants: [blankVariant()] }));
  const [errors, setErrors] = useState<CapexErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CapexValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  function updateVariant(index: number, patch: Partial<VariantValues>) {
    update({ variants: values.variants.map((v, i) => (i === index ? { ...v, ...patch } : v)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCapex(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createCapex(input, key.current);
      navigate(`/assets/capex/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as CapexErrors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "legalEntityId" | "number" | "title" | "requesterId" | "currency", label: string, hint?: string, mono = false) => (
    <FormField id={`capex-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          className={mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: name === "currency" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New capex request" description="Describe the investment and one or more variants with the amount per year. You choose the variant to approve when you submit." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New capex request">
          {text("legalEntityId", "Legal entity", "UUID of the legal entity.", true)}
          {text("number", "Request number", undefined, true)}
          {text("title", "Title")}
          {text("requesterId", "Requester")}
          {text("currency", "Currency")}
          {values.variants.map((v, i) => {
            const f = errors.variantFields?.[i] ?? {};
            return (
              <fieldset key={i} className={styles.variant}>
                <legend className={styles.term}>{`Variant ${i + 1}`}</legend>
                <div className={styles.form}>
                  <FormField id={`variant-${i}-description`} label="Description" error={f.description}>
                    {(control) => <Input {...control} value={v.description} onChange={(e) => updateVariant(i, { description: e.target.value })} invalid={Boolean(f.description)} />}
                  </FormField>
                  <FormField id={`variant-${i}-inServiceDate`} label="Expected in service" error={f.inServiceDate}>
                    {(control) => <Input {...control} type="date" value={v.inServiceDate} onChange={(e) => updateVariant(i, { inServiceDate: e.target.value })} invalid={Boolean(f.inServiceDate)} />}
                  </FormField>
                  <FormField id={`variant-${i}-profileAssumption`} label="Depreciation profile" hint="The profile assumed for forecasting, such as SL-120." error={f.profileAssumption}>
                    {(control) => <Input {...control} value={v.profileAssumption} onChange={(e) => updateVariant(i, { profileAssumption: e.target.value })} invalid={Boolean(f.profileAssumption)} />}
                  </FormField>
                  <FormField id={`variant-${i}-amounts`} label="Amounts by year" hint="One year per line, such as 2026=200000.00." error={f.amounts}>
                    {(control) => <Textarea {...control} mono rows={3} value={v.amounts} onChange={(e) => updateVariant(i, { amounts: e.target.value })} invalid={Boolean(f.amounts)} />}
                  </FormField>
                </div>
                {values.variants.length > 1 ? (
                  <div>
                    <Button size="sm" variant="ghost" onClick={() => update({ variants: values.variants.filter((_, j) => j !== i) })}>
                      {`Remove variant ${i + 1}`}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            );
          })}
          <div className={styles.formActions}>
            <Button size="sm" onClick={() => update({ variants: [...values.variants, blankVariant()] })} disabled={values.variants.length >= 10}>
              Add variant
            </Button>
            {errors.variants ? <span className={styles.error}>{errors.variants}</span> : null}
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "AST-0023" ? "That request number is taken" : "Could not create the request"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create request
            </Button>
            <Link className={buttonClassName("ghost")} to="/assets/capex">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

function variantColumns(currency: string, selected: string | null): DataTableColumn<CapexVariant>[] {
  return [
    { key: "position", header: "Variant", align: "end" },
    { key: "description", header: "Description", render: (v) => (v.id === selected ? `${v.description} (selected)` : v.description) },
    { key: "amountsByYear", header: "Per year", render: (v) => Object.entries(v.amountsByYear).sort(([a], [b]) => a.localeCompare(b)).map(([y, a]) => `${y}: ${a}`).join(", ") },
    { key: "totalAmount", header: "Total", align: "end", render: (v) => amount(v.totalAmount, currency) },
    { key: "inServiceDate", header: "In service", mono: true },
    { key: "profileAssumption", header: "Profile", mono: true },
  ];
}

/** A capex request with its variants; a Draft is submitted here with the variant to approve. */
export function CapexDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCapex(id, signal), id);
  const [variantId, setVariantId] = useState("");
  const [submitted, setSubmitted] = useState<CapexRequest | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Capex request" />
        <Alert tone="danger" title="Could not load the request" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/assets/capex">
          Back to requests
        </Link>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Capex request" />
        <span className="visually-hidden">Loading the request…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const request = submitted ?? state.data.value;
  const variants = request.variants ?? [];
  const chosen = variantId || variants[0]?.id || "";

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!state.data || chosen === "") return;
    setBusy(true);
    setFailure(null);
    try {
      setSubmitted((await submitCapex(request.id, state.data.etag, chosen)).value);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={`${request.number} ${request.title}`} meta={<StatusBadge status={request.status} />} />
      {submitted ? (
        <Alert tone="success" title="Submitted for approval" live="status">
          {`Routed to ${submitted.approvalLevel ? level[submitted.approvalLevel] : "approval"} for ${amount(submitted.requestedAmount, submitted.currency)}.`}
        </Alert>
      ) : null}
      <Card title="Request">
        <dl className={styles.details}>
          <Detail term="Requester">{request.requesterId}</Detail>
          <Detail term="Currency">{request.currency}</Detail>
          <Detail term="Approval level">{request.approvalLevel ? level[request.approvalLevel] : dash}</Detail>
          <Detail term="Requested">{amount(request.requestedAmount, request.requestedAmount ? request.currency : null)}</Detail>
          <Detail term="Approved">{amount(request.approvedAmount, request.approvedAmount ? request.currency : null)}</Detail>
          <Detail term="Legal entity">
            <span className={styles.mono}>{request.legalEntityId}</span>
          </Detail>
        </dl>
      </Card>
      <Card title="Variants">
        <DataTable label="Variants" columns={variantColumns(request.currency, request.selectedVariantId)} rows={variants} getRowKey={(v) => v.id} empty={{ title: "No variants" }} />
      </Card>
      {request.status === "Draft" ? (
        <Card title="Submit for approval">
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Submit for approval">
            <FormField id="capex-variant" label="Variant to approve" hint="Up to 50,000 goes to the plant manager, up to 500,000 to the CFO, above that to the board.">
              {(control) => (
                <Select {...control} value={chosen} onChange={(e) => setVariantId(e.target.value)}>
                  {variants.map((v) => (
                    <option key={v.id} value={v.id}>
                      {`${v.position}. ${v.description} (${amount(v.totalAmount, request.currency)})`}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Alert
                  tone="danger"
                  title={failure.code === "AST-0006" ? "The request changed since you opened it" : "Could not submit the request"}
                  live="alert"
                  action={failure.code === "AST-0006" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}
                >
                  {failure.message} ({failure.code})
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy} disabled={chosen === ""}>
                Submit request
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <div>
        <Link className={buttonClassName("ghost")} to="/assets/capex">
          Back to requests
        </Link>
      </div>
    </div>
  );
}
