import { useMemo, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  buttonClassName,
  ListPage,
  type ListColumn,
  type ListData,
  type ListFilterDef,
  type ListQuery,
  FormErrorSummary,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
} from "../../shell/ui";
import {
  createSigningLimitRequest,
  decideSigningLimitRequest,
  describeProblem,
  getSigningLimit,
  getSigningLimitRequest,
  listSigningLimitRequests,
  listSigningLimits,
  newIdempotencyKey,
  recertifySigningLimit,
  type SigningDocumentType,
  type SigningLimit,
  type SigningLimitRequest,
  type SigningRequestStatus,
} from "./api/client";
import {
  Details,
  LoadError,
  StatusBadge,
  currencyPattern,
  decimalPattern,
  statusLabel,
  todayIso,
  useResource,
  uuidPattern,
  type Failure,
  Named,
  useProcurementPages,
} from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const DOC_TYPES: SigningDocumentType[] = ["REQUISITION", "PURCHASE_ORDER", "INVOICE", "EXPENSE"];
const REQUEST_STATUSES: SigningRequestStatus[] = ["PENDING", "APPROVED", "REJECTED"];

type Errors = Record<string, string>;

const amount = (value: string, currency: string) => `${Number(value).toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 6 })} ${currency}`;
const expired = (l: SigningLimit) => l.recertifyBy < todayIso();

const limitColumns: ListColumn<SigningLimit>[] = [
  { key: "workerId", header: "Worker", render: (l) => <Named entity="worker" id={l.workerId} /> },
  { key: "documentType", header: "Document", render: (l) => statusLabel(l.documentType) },
  { key: "spendingLimit", header: "Spending limit", mono: true, render: (l) => amount(l.spendingLimit, l.currency) },
  { key: "approvalLimit", header: "Approval limit", mono: true, render: (l) => amount(l.approvalLimit, l.currency) },
  {
    key: "recertifyBy",
    header: "Recertify by",
    mono: true,
    render: (l) => (expired(l) ? <Badge tone="warning">{`Expired ${l.recertifyBy}`}</Badge> : l.recertifyBy),
  },
];

const limitFilters: ListFilterDef[] = [
  { kind: "select", key: "documentType", label: "Document", anyLabel: "All documents", options: DOC_TYPES.map((d) => ({ value: d, label: statusLabel(d) })) },
  { kind: "select", key: "expired", label: "Recertification", anyLabel: "All limits", options: [{ value: "yes", label: "Expired only" }] },
  { kind: "entity", key: "workerId", label: "Worker", entity: "worker" },
];

function useLimitRows(query: ListQuery): ListData<SigningLimit> {
  const documentType = (query.filters.documentType ?? "") as SigningDocumentType | "";
  const onlyExpired = query.filters.expired === "yes";
  const workerId = query.filters.workerId ?? "";
  return useProcurementPages(`${documentType}|${onlyExpired}|${workerId}`, (cursor, signal) =>
    listSigningLimits({ documentType, expired: onlyExpired, workerId: workerId || undefined }, { cursor, signal }),
  );
}

/** Signing limits with document type and expired filters and load-more paging (FR-17.17). */
export function SigningLimitsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="purchasing-signing-limits"
      title="Signing limits"
      description="What each worker may request and approve. A requester above their spending limit gets one more approval level. An approver above their approval limit, or past recertification, leaves the approval to the next approver."
      actions={
        <>
          <Link className={buttonClassName("secondary")} to="/purchasing/signing-limit-requests">
            Limit requests
          </Link>
          <Link className={buttonClassName("primary")} to="/purchasing/signing-limit-requests/new">
            Request a limit
          </Link>
        </>
      }
      noun="signing limits"
      singular="signing limit"
      columns={limitColumns}
      filters={limitFilters}
      useRows={useLimitRows}
      getRowKey={(l) => l.id}
      onRowOpen={(l) => navigate(`/purchasing/signing-limits/${l.id}`)}
      empty={{ title: "No signing limits yet", description: "Request a limit for a worker; a limit exists once its request is approved." }}
    />
  );
}

/** One limit with its recertification action (duty procurement.signing-limit.approve in the service). */
export function SigningLimitDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getSigningLimit(id, signal)).body, [id]);
  const [current, setCurrent] = useState<SigningLimit | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const limit = current && current.id === id ? current : resource.data;

  async function onRecertify() {
    if (!limit) return;
    setBusy(true);
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await recertifySigningLimit(limit.id, limit.version);
      setCurrent(body);
      setNotice(`Recertified until ${body.recertifyBy}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "PRC-SL-005";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Signing limit"
        actions={
          <Link className={buttonClassName("secondary")} to="/purchasing/signing-limits">
            All signing limits
          </Link>
        }
      />
      {resource.error ? <LoadError what="the signing limit" error={resource.error} onRetry={resource.reload} /> : null}
      {resource.loading && !limit ? <Skeleton /> : null}
      {limit ? (
        <>
          <Card>
            <Details
              items={[
                { label: "Worker", value: <Named entity="worker" id={limit.workerId} /> },
                { label: "Legal entity", value: <Named entity="legalEntity" id={limit.legalEntityId} /> },
                { label: "Document", value: statusLabel(limit.documentType) },
                { label: "Spending limit", value: amount(limit.spendingLimit, limit.currency), mono: true },
                { label: "Approval limit", value: amount(limit.approvalLimit, limit.currency), mono: true },
                { label: "Recertify by", value: expired(limit) ? <Badge tone="warning">{`Expired ${limit.recertifyBy}`}</Badge> : limit.recertifyBy, mono: true },
                { label: "Last recertified", value: limit.lastRecertifiedOn ?? "Never", mono: true },
                { label: "Version", value: String(limit.version), mono: true },
              ]}
            />
          </Card>
          <Alert
            tone="danger"
            live="alert"
            action={
              stale ? (
                <Button type="button" variant="secondary" onClick={() => { setCurrent(null); setFailure(null); resource.reload(); }}>
                  Reload limit
                </Button>
              ) : undefined
            }
          >
            {failure ? <ProblemMessage problem={failure} /> : null}
          </Alert>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          <div className={styles.actions}>
            <Button variant="primary" loading={busy} onClick={onRecertify}>
              Recertify for 12 months
            </Button>
          </div>
        </>
      ) : null}
    </div>
  );
}

const requestColumns: ListColumn<SigningLimitRequest>[] = [
  { key: "workerId", header: "Worker", render: (r) => <Named entity="worker" id={r.workerId} /> },
  { key: "documentType", header: "Document", render: (r) => statusLabel(r.documentType) },
  { key: "spendingLimit", header: "Spending limit", mono: true, render: (r) => amount(r.spendingLimit, r.currency) },
  { key: "approvalLimit", header: "Approval limit", mono: true, render: (r) => amount(r.approvalLimit, r.currency) },
  { key: "status", header: "Status", render: (r) => <StatusBadge status={r.status} /> },
];

const requestFilters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: REQUEST_STATUSES.map((s) => ({ value: s, label: statusLabel(s) })) },
  { kind: "entity", key: "workerId", label: "Worker", entity: "worker" },
];

/** Requests waiting for a decision are what the page is for, so it opens on them. */
const PENDING_FIRST = { status: "PENDING" };

function useRequestRows(query: ListQuery): ListData<SigningLimitRequest> {
  const status = (query.filters.status ?? "") as SigningRequestStatus | "";
  const workerId = query.filters.workerId ?? "";
  return useProcurementPages(`${status}|${workerId}`, (cursor, signal) => listSigningLimitRequests({ status, workerId: workerId || undefined }, { cursor, signal }));
}

/** Signing limit requests with a status filter and load-more paging. */
export function SigningLimitRequestsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="purchasing-signing-limit-requests"
      title="Signing limit requests"
      description="A limit is set only by approving its request. The person who made a request, and the worker it is for, cannot decide it."
      actions={
        <>
          <Link className={buttonClassName("secondary")} to="/purchasing/signing-limits">
            Signing limits
          </Link>
          <Link className={buttonClassName("primary")} to="/purchasing/signing-limit-requests/new">
            Request a limit
          </Link>
        </>
      }
      noun="signing limit requests"
      singular="signing limit request"
      columns={requestColumns}
      filters={requestFilters}
      defaultFilters={PENDING_FIRST}
      useRows={useRequestRows}
      getRowKey={(r) => r.id}
      onRowOpen={(r) => navigate(`/purchasing/signing-limit-requests/${r.id}`)}
      empty={{ title: "No requests", description: "Requests for a worker's signing limit appear here until they are decided." }}
    />
  );
}

const SIGNING_LIMIT_REQUEST_CREATE_FIELDS: Record<string, FormFieldTarget> = {
  legalEntityId: { id: "slr-legal-entity", label: "Legal entity" },
  workerId: { id: "slr-worker", label: "Worker" },
  documentType: { id: "slr-doc", label: "Document" },
  currency: { id: "slr-currency", label: "Currency" },
  spendingLimit: { id: "slr-spending", label: "Spending limit" },
  approvalLimit: { id: "slr-approval", label: "Approval limit" },
};

/** Create form: request a worker's limit for a document type and currency. */
export function SigningLimitRequestCreatePage() {
  const navigate = useNavigate();
  const [form, setForm] = useState({
    legalEntityId: "",
    workerId: "",
    documentType: "PURCHASE_ORDER" as SigningDocumentType,
    currency: "USD",
    spendingLimit: "",
    approvalLimit: "",
    reason: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [form]);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([form]));
  const set = (patch: Partial<typeof form>) => setForm((f) => ({ ...f, ...patch }));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!uuidPattern.test(form.legalEntityId.trim())) found.legalEntityId = "Enter the legal entity id (a UUID).";
    if (!uuidPattern.test(form.workerId.trim())) found.workerId = "Enter the worker id (a UUID).";
    if (!currencyPattern.test(form.currency.trim())) found.currency = "Enter a three-letter currency code, such as USD.";
    if (!decimalPattern.test(form.spendingLimit.trim())) found.spendingLimit = "Enter an amount of zero or more.";
    if (!decimalPattern.test(form.approvalLimit.trim())) found.approvalLimit = "Enter an amount of zero or more.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createSigningLimitRequest(
        {
          legalEntityId: form.legalEntityId.trim(),
          workerId: form.workerId.trim(),
          documentType: form.documentType,
          currency: form.currency.trim(),
          spendingLimit: form.spendingLimit.trim(),
          approvalLimit: form.approvalLimit.trim(),
          reason: form.reason.trim() || undefined,
        },
        key,
      );
      release();
      navigate(`/purchasing/signing-limit-requests/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Request a signing limit"
        description="The spending limit is what the worker may request before one more approval level is added. The approval limit is what the worker may approve."
      />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <FormErrorSummary items={summaryItems(errors, SIGNING_LIMIT_REQUEST_CREATE_FIELDS)} attempt={attempt} />
        <Card title="Limit">
          <div className={styles.grid}>
            <FormField id="slr-legal-entity" label="Legal entity" error={errors.legalEntityId}>
              {(c) => <EntityLookup entity="legalEntity" {...c} value={form.legalEntityId} onChange={(e) => set({ legalEntityId: e.target.value })} />}
            </FormField>
            <FormField id="slr-worker" label="Worker" error={errors.workerId}>
              {(c) => <EntityLookup entity="worker" {...c} value={form.workerId} onChange={(e) => set({ workerId: e.target.value })} />}
            </FormField>
            <FormField id="slr-doc" label="Document" error={errors.documentType}>
              {(c) => (
                <Select {...c} value={form.documentType} onChange={(e) => set({ documentType: e.target.value as SigningDocumentType })}>
                  {DOC_TYPES.map((d) => (
                    <option key={d} value={d}>
                      {statusLabel(d)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="slr-currency" label="Currency" error={errors.currency}>
              {(c) => <Input {...c} value={form.currency} onChange={(e) => set({ currency: e.target.value.toUpperCase() })} />}
            </FormField>
            <FormField id="slr-spending" label="Spending limit" error={errors.spendingLimit}>
              {(c) => <Input {...c} inputMode="decimal" value={form.spendingLimit} onChange={(e) => set({ spendingLimit: e.target.value })} />}
            </FormField>
            <FormField id="slr-approval" label="Approval limit" error={errors.approvalLimit}>
              {(c) => <Input {...c} inputMode="decimal" value={form.approvalLimit} onChange={(e) => set({ approvalLimit: e.target.value })} />}
            </FormField>
            <FormField id="slr-reason" label="Reason" optional>
              {(c) => <Input {...c} value={form.reason} onChange={(e) => set({ reason: e.target.value })} />}
            </FormField>
          </div>
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Send request
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/signing-limit-requests">
            Cancel
          </Link>
        </div>
      </form>
      {prompt}
    </div>
  );
}

/** One request with approve and reject while it is PENDING (duty procurement.signing-limit.approve in the service). */
export function SigningLimitRequestDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getSigningLimitRequest(id, signal)).body, [id]);
  const [decided, setDecided] = useState<SigningLimitRequest | null>(null);
  const [note, setNote] = useState("");
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<"approve" | "reject" | null>(null);
  const request = decided && decided.id === id ? decided : resource.data;

  async function decide(action: "approve" | "reject") {
    if (!request) return;
    setBusy(action);
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await decideSigningLimitRequest(request.id, action, request.version, note.trim());
      setDecided(body);
      setNotice(action === "approve" ? "Request approved; the limit is in force." : "Request rejected.");
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const stale = failure?.code === "PRC-SL-005" || failure?.code === "PRC-SL-004";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Signing limit request"
        actions={
          <Link className={buttonClassName("secondary")} to="/purchasing/signing-limit-requests">
            All requests
          </Link>
        }
      />
      {resource.error ? <LoadError what="the request" error={resource.error} onRetry={resource.reload} /> : null}
      {resource.loading && !request ? <Skeleton /> : null}
      {request ? (
        <>
          <Card>
            <Details
              items={[
                { label: "Status", value: <StatusBadge status={request.status} /> },
                { label: "Worker", value: <Named entity="worker" id={request.workerId} /> },
                { label: "Legal entity", value: <Named entity="legalEntity" id={request.legalEntityId} /> },
                { label: "Document", value: statusLabel(request.documentType) },
                { label: "Spending limit", value: amount(request.spendingLimit, request.currency), mono: true },
                { label: "Approval limit", value: amount(request.approvalLimit, request.currency), mono: true },
                { label: "Requested by", value: request.requestedBy, mono: true },
                { label: "Reason", value: request.reason || "None given" },
                ...(request.decidedBy
                  ? [
                      { label: "Decided by", value: request.decidedBy, mono: true },
                      { label: "Decision note", value: request.decisionNote || "None given" },
                    ]
                  : []),
              ]}
            />
            {request.signingLimitId ? (
              <p>
                <Link to={`/purchasing/signing-limits/${request.signingLimitId}`}>Open the limit</Link>
              </p>
            ) : null}
          </Card>
          <Alert
            tone="danger"
            live="alert"
            action={
              stale ? (
                <Button type="button" variant="secondary" onClick={() => { setDecided(null); setFailure(null); resource.reload(); }}>
                  Reload request
                </Button>
              ) : undefined
            }
          >
            {failure ? <ProblemMessage problem={failure} /> : null}
          </Alert>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          {request.status === "PENDING" ? (
            <Card title="Decision">
              <FormField id="slrd-note" label="Note" optional>
                {(c) => <Input {...c} value={note} onChange={(e) => setNote(e.target.value)} />}
              </FormField>
              <div className={styles.actions}>
                <Button variant="primary" loading={busy === "approve"} onClick={() => decide("approve")}>
                  Approve request
                </Button>
                <Button variant="secondary" loading={busy === "reject"} onClick={() => decide("reject")}>
                  Reject request
                </Button>
              </div>
            </Card>
          ) : null}
        </>
      ) : null}
    </div>
  );
}
