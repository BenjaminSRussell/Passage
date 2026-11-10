import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  CAPEX_STATUSES,
  createCapex,
  decideCapex,
  describeProblem,
  getCapex,
  listCapex,
  newKey,
  submitCapex,
  type CapexDecision,
  type CapexDecisionRecord,
  type CapexRequest,
  type CapexStatus,
  type CapexVariant,
  type NewCapexRequest,
  type Problem,
  type Versioned,
} from "./api/client";
import { StatusBadge } from "./badges";
import { amount, DATE, dash, parseYearLines, sentence, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const level: Record<string, string> = { PlantManager: "Plant manager", CFO: "CFO", Board: "Board" };

const listColumns: DataTableColumn<CapexRequest>[] = [
  { key: "number", header: "Request", mono: true },
  { key: "title", header: "Title" },
  { key: "status", header: "Status", render: (c) => <StatusBadge status={c.status} /> },
  { key: "approvalLevel", header: "Approval level", render: (c) => (c.approvalLevel ? level[c.approvalLevel] : dash) },
  { key: "requestedAmount", header: "Requested", align: "end", render: (c) => amount(c.requestedAmount, c.requestedAmount ? c.currency : null) },
];

/** Capital appropriation requests (FR-37.17), filterable by status; the approvals entry opens it on InApproval. */
export function CapexListPage({ initialStatus = "" }: { initialStatus?: CapexStatus | "" }) {
  const navigate = useNavigate();
  const [status, setStatus] = useState<CapexStatus | "">(initialStatus);
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
          <ProblemMessage problem={state.error} />
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
          <ProblemMessage problem={moreError} />
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
                <ProblemMessage problem={failure} />
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

interface DecisionAction {
  decision: CapexDecision;
  label: string;
  variant: "primary" | "secondary" | "danger";
  /** Reject and postpone tell the requester why (the service requires it). */
  needsComment: boolean;
}

const decisionLabel: Record<CapexDecision, string> = {
  approve: "Approved",
  reject: "Rejected",
  postpone: "Postponed",
  reopen: "Returned to draft",
  release: "Released",
  close: "Closed",
};

/**
 * The decisions offered in each status (BR-37.12). The service re-checks the
 * move, the duty (assets.capex-request.approve; reopen is the requester's
 * assets.capex-request.create) and that the approver did not create or request it.
 */
export function decisionsFor(status: CapexStatus): DecisionAction[] {
  switch (status) {
    case "InApproval":
      return [
        { decision: "approve", label: "Approve", variant: "primary", needsComment: false },
        { decision: "postpone", label: "Postpone", variant: "secondary", needsComment: true },
        { decision: "reject", label: "Reject", variant: "danger", needsComment: true },
      ];
    case "Postponed":
      return [{ decision: "reopen", label: "Return to draft", variant: "secondary", needsComment: false }];
    case "Approved":
      return [{ decision: "release", label: "Release", variant: "primary", needsComment: false }];
    case "Released":
      return [{ decision: "close", label: "Close", variant: "secondary", needsComment: false }];
    default:
      return [];
  }
}

const trailColumns: DataTableColumn<CapexDecisionRecord>[] = [
  { key: "decidedAt", header: "When", mono: true, render: (d) => d.decidedAt.replace("T", " ").slice(0, 16) },
  { key: "decision", header: "Decision", render: (d) => decisionLabel[d.decision] ?? sentence(d.decision) },
  { key: "fromStatus", header: "From", render: (d) => <StatusBadge status={d.fromStatus} /> },
  { key: "toStatus", header: "To", render: (d) => <StatusBadge status={d.toStatus} /> },
  { key: "decidedBy", header: "By", mono: true },
  { key: "comment", header: "Comment", render: (d) => d.comment ?? dash },
];

function failureTitle(problem: Problem, action: string): string {
  if (problem.code === "AST-0006") return "The request changed since you opened it";
  if (problem.code === "AST-0028") return "You cannot decide your own request";
  return `Could not ${action} the request`;
}

/** A capex request with its variants: a Draft is submitted here, and the approver decides it here. */
export function CapexDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCapex(id, signal), id);
  const [variantId, setVariantId] = useState("");
  const [current, setCurrent] = useState<Versioned<CapexRequest> | null>(null);
  const [submitted, setSubmitted] = useState(false);
  const [decided, setDecided] = useState<CapexDecision | null>(null);
  const [comment, setComment] = useState("");
  const [commentError, setCommentError] = useState<string | undefined>(undefined);
  const [failure, setFailure] = useState<{ problem: Problem; action: string } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  useEffect(() => {
    setCurrent(null);
    setSubmitted(false);
    setDecided(null);
    setFailure(null);
  }, [state.data]);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Capex request" />
        <Alert tone="danger" title="Could not load the request" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
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
  const latest = current ?? state.data;
  const request = latest.value;
  const variants = request.variants ?? [];
  const trail = request.decisions ?? [];
  const chosen = variantId || variants[0]?.id || "";
  const actions = decisionsFor(request.status);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (chosen === "") return;
    setBusy("submit");
    setFailure(null);
    setDecided(null);
    try {
      setCurrent(await submitCapex(request.id, latest.etag, chosen));
      setSubmitted(true);
    } catch (error) {
      setFailure({ problem: describeProblem(error), action: "submit" });
    } finally {
      setBusy(null);
    }
  }

  async function decide(action: DecisionAction) {
    const text = comment.trim();
    if (action.needsComment && text === "") {
      setCommentError(`Say why you ${action.decision} the request.`);
      return;
    }
    if (text.length > 1000) {
      setCommentError("Keep the comment to 1,000 characters.");
      return;
    }
    setCommentError(undefined);
    setBusy(action.decision);
    setFailure(null);
    setSubmitted(false);
    try {
      setCurrent(await decideCapex(request.id, latest.etag, action.decision, text));
      setDecided(action.decision);
      setComment("");
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.fields.comment) setCommentError(problem.fields.comment);
      setFailure({ problem, action: action.label.toLowerCase() });
    } finally {
      setBusy(null);
    }
  }

  const failureAlert = failure ? (
    <div className={styles.formActions}>
      <Alert
        tone="danger"
        title={failureTitle(failure.problem, failure.action)}
        live="alert"
        action={failure.problem.code === "AST-0006" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}
      >
        <ProblemMessage problem={failure.problem} />
      </Alert>
    </div>
  ) : null;

  return (
    <div className={styles.stack}>
      <PageHeader title={`${request.number} ${request.title}`} meta={<StatusBadge status={request.status} />} />
      {submitted ? (
        <Alert tone="success" title="Submitted for approval" live="status">
          {`Routed to ${request.approvalLevel ? level[request.approvalLevel] : "approval"} for ${amount(request.requestedAmount, request.currency)}.`}
        </Alert>
      ) : null}
      {decided ? (
        <Alert tone="success" title={decisionLabel[decided]} live="status">
          {decided === "approve"
            ? `Approved ${amount(request.approvedAmount, request.currency)}. The planned acquisition is published to budgeting and the depreciation forecast.`
            : `The request is now ${sentence(request.status).toLowerCase()}.`}
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
            {failureAlert}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "submit"} disabled={chosen === ""}>
                Submit request
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {actions.length > 0 ? (
        <Card title="Decision">
          <form className={styles.form} onSubmit={(e) => e.preventDefault()} noValidate aria-label="Decide the request">
            {request.status === "InApproval" ? (
              <p className={styles.note}>
                {`Waiting for the ${request.approvalLevel ? level[request.approvalLevel] : "approver"}. Approving commits ${amount(request.requestedAmount, request.currency)}; whoever created or requested it cannot decide it.`}
              </p>
            ) : null}
            <FormField
              id="capex-comment"
              label="Comment"
              optional={!actions.some((a) => a.needsComment)}
              hint={actions.some((a) => a.needsComment) ? "Required to postpone or reject; the requester sees it." : undefined}
              error={commentError}
            >
              {(control) => <Textarea {...control} rows={3} value={comment} onChange={(e) => setComment(e.target.value)} invalid={Boolean(commentError)} />}
            </FormField>
            {failureAlert}
            <div className={styles.formActions}>
              {actions.map((a) => (
                <Button key={a.decision} type="button" variant={a.variant} loading={busy === a.decision} disabled={busy !== null && busy !== a.decision} onClick={() => decide(a)}>
                  {a.label}
                </Button>
              ))}
            </div>
          </form>
        </Card>
      ) : null}
      <Card title="Decision trail">
        <DataTable
          label="Decision trail"
          columns={trailColumns}
          rows={trail}
          getRowKey={(d) => d.id}
          empty={{ title: "No decisions yet", description: request.status === "Draft" ? "Submit the request to start the approval." : "The approver has not decided this request." }}
        />
      </Card>
      <div>
        <Link className={buttonClassName("ghost")} to="/assets/capex">
          Back to requests
        </Link>
      </div>
    </div>
  );
}
