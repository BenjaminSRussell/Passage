import { Fragment, useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import { actOnEcr, ApiError, createEcr, getEcr, listEcrs, type Ecr, type EcrAction } from "./api/client";
import styles from "./pages.module.css";

const ecrStates = ["draft", "submitted", "under_review", "approved", "rejected", "cancelled"];
const priorities = ["low", "medium", "high", "critical"];
const notFoundCode = "ENG-ECR-NOTFOUND";
const forbiddenStatus = 403;

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

function stateLabel(state: string): string {
  return state.replace("_", " ");
}

const ecrColumns: DataTableColumn<Ecr>[] = [
  { key: "number", header: "ECR", mono: true },
  { key: "title", header: "Title" },
  { key: "priority", header: "Priority" },
  { key: "state", header: "State", render: (row) => <StatusPill status={row.state} /> },
  { key: "products", header: "Products", render: (row) => row.products.join(", ") },
  { key: "requestedBy", header: "Requested by", render: (row) => row.requestedBy || "" },
];

export function EcrsPage({ onOpen, onCreate }: { onOpen?: (id: string) => void; onCreate?: () => void }) {
  const [state, setState] = useState("");
  const [productInput, setProductInput] = useState("");
  const [productId, setProductId] = useState("");
  const [rows, setRows] = useState<Ecr[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const generation = useRef(0);

  useEffect(() => {
    let current = true;
    generation.current += 1;
    setLoadingMore(false);
    setLoading(true);
    setError("");
    listEcrs({ state, productId })
      .then((page) => {
        if (!current) return;
        setRows(page.items);
        setNextCursor(page.nextCursor ?? "");
      })
      .catch((e: unknown) => {
        if (!current) return;
        setRows([]);
        setNextCursor("");
        setError(errorText(e));
      })
      .finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
  }, [state, productId, attempt]);

  async function loadMore() {
    const token = generation.current;
    setLoadingMore(true);
    setError("");
    try {
      const page = await listEcrs({ state, productId }, nextCursor);
      if (token !== generation.current) return;
      setRows((existing) => [...existing, ...page.items]);
      setNextCursor(page.nextCursor ?? "");
    } catch (e) {
      if (token === generation.current) setError(errorText(e));
    } finally {
      if (token === generation.current) setLoadingMore(false);
    }
  }

  return (
    <>
      <PageHeader title="Engineering change requests" description="Why a product's engineering data should change, before a change order replaces its BOM versions." />
      <Card
        actions={
          onCreate ? (
            <Button size="sm" variant="primary" onClick={onCreate}>
              New ECR
            </Button>
          ) : undefined
        }
      >
        <form
          className={styles.form}
          onSubmit={(event) => {
            event.preventDefault();
            setProductId(productInput.trim());
          }}
        >
          <div className={styles.field}>
            <FormField id="ecr-state" label="State" optional>
              {(control) => (
                <Select {...control} value={state} onChange={(e) => setState(e.target.value)}>
                  <option value="">Any state</option>
                  {ecrStates.map((s) => (
                    <option key={s} value={s}>
                      {stateLabel(s)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="ecr-product" label="Affected product" optional>
              {(control) => <Input {...control} value={productInput} onChange={(e) => setProductInput(e.target.value)} maxLength={64} />}
            </FormField>
          </div>
          <div className={styles.toolbar}>
            <Button type="submit">Filter</Button>
          </div>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert" action={error ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}>
          {error || null}
        </Alert>
      </div>
      <div className={styles.results}>
        {error && rows.length === 0 ? null : (
          <DataTable
            label="Engineering change requests"
            columns={ecrColumns}
            rows={rows}
            loading={loading}
            getRowKey={(row) => row.id}
            empty={{ title: "No engineering change requests match" }}
            onRowOpen={(row) => onOpen?.(row.id)}
          />
        )}
      </div>
      {nextCursor !== "" && !loading ? (
        <div className={styles.toolbar}>
          <Button loading={loadingMore} onClick={loadMore}>Load more</Button>
        </div>
      ) : null}
    </>
  );
}

interface EcrPageProps {
  id: string;
  onBack?: () => void;
  onOpenEco?: (ecoId: string) => void;
  /** Raise an ECO from this approved request. */
  onCreateEco?: (ecrId: string) => void;
  /** UX only: submit and withdraw need engineering.bom.write; the service still enforces it. */
  canWrite?: boolean;
  /** UX only: review, approve and reject need engineering.ecr.review; the service still enforces it. */
  canReview?: boolean;
}

const actionLabels: Record<EcrAction, string> = {
  submit: "Submit for review",
  review: "Start review",
  approve: "Approve",
  reject: "Reject",
  cancel: "Withdraw",
};

/** The service keeps a note only on these moves; submit and review take none. */
const keepsNote = new Set<EcrAction>(["approve", "reject", "cancel"]);

const pastTense: Record<EcrAction, string> = { submit: "submitted", review: "under review", approve: "approved", reject: "rejected", cancel: "withdrawn" };

export function EcrPage({ id, onBack, onOpenEco, onCreateEco, canWrite = false, canReview = false }: EcrPageProps) {
  const [ecr, setEcr] = useState<Ecr | null>(null);
  const [loadError, setLoadError] = useState<Error | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState<EcrAction | null>(null);
  const [actionError, setActionError] = useState("");
  const [announcement, setAnnouncement] = useState("");

  useEffect(() => {
    let current = true;
    setEcr(null);
    setLoadError(null);
    setActionError("");
    setAnnouncement("");
    getEcr(id)
      .then((loaded) => current && setEcr(loaded))
      .catch((e: Error) => current && setLoadError(e));
    return () => {
      current = false;
    };
  }, [id, attempt]);

  async function act(action: EcrAction) {
    setActionError("");
    setAnnouncement("");
    setBusy(action);
    try {
      const updated = await actOnEcr(id, action, keepsNote.has(action) ? reason.trim() || undefined : undefined);
      setEcr(updated);
      if (keepsNote.has(action)) setReason("");
      setAnnouncement(`ECR ${updated.number} is ${pastTense[action]}`);
    } catch (e) {
      setActionError(e instanceof ApiError && e.status === forbiddenStatus && !e.code?.startsWith("ENG-") ? "You do not have permission to do that" : errorText(e));
      // A refused move can mean someone else moved the request first, so show its current state.
      await getEcr(id)
        .then((latest) => setEcr(latest))
        .catch(() => undefined);
    } finally {
      setBusy(null);
    }
  }

  const notFound = loadError instanceof ApiError && loadError.code === notFoundCode;
  const alertText = loadError ? (notFound ? "This ECR does not exist" : errorText(loadError)) : actionError;
  const facts: [string, React.ReactNode][] = ecr
    ? [
        ["Number", ecr.number],
        ["Title", ecr.title],
        ["State", <StatusPill status={ecr.state} />],
        ["Priority", ecr.priority],
        ["Requested by", ecr.requestedBy || "Unknown"],
        ["Decided by", ecr.decidedBy || "Pending"],
        ["Reason", ecr.reason || "None given"],
        ["Decision note", ecr.decisionReason || "None"],
        ["Change order", ecr.ecoId ? (onOpenEco ? <Button size="sm" onClick={() => onOpenEco(ecr.ecoId!)}>Open change order {ecr.ecoId}</Button> : ecr.ecoId) : "None yet"],
      ]
    : [];
  const allowed: EcrAction[] = [];
  if (ecr && canWrite) {
    if (ecr.state === "draft") allowed.push("submit");
    if (["draft", "submitted", "under_review"].includes(ecr.state)) allowed.push("cancel");
  }
  if (ecr && canReview) {
    if (ecr.state === "submitted") allowed.push("review");
    if (ecr.state === "under_review") allowed.push("approve", "reject");
  }

  return (
    <>
      <PageHeader title={ecr ? `ECR ${ecr.number}` : "Engineering change request"} description="State, affected products and the decision." />
      <div className={styles.toolbar}>
        {onBack ? <Button size="sm" onClick={onBack}>Back to change requests</Button> : null}
        {loadError && !notFound ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : null}
        {ecr && ecr.state === "approved" && !ecr.ecoId && onCreateEco && canWrite ? (
          <Button size="sm" variant="primary" onClick={() => onCreateEco(ecr.id)}>
            Create change order
          </Button>
        ) : null}
      </div>
      <p className="visually-hidden" aria-live="polite">
        {announcement}
      </p>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {alertText || null}
        </Alert>
      </div>
      {!ecr && !loadError ? (
        <Card>
          <Skeleton width="1/2" />
          <Skeleton />
        </Card>
      ) : null}
      {ecr ? (
        <>
          <Card title="Change request">
            <dl className={styles.facts}>
              {facts.map(([term, value]) => (
                <Fragment key={term}>
                  <dt>{term}</dt>
                  <dd>{value}</dd>
                </Fragment>
              ))}
            </dl>
          </Card>
          <div className={styles.results}>
            <DataTable
              label="Affected products"
              columns={[{ key: "product", header: "Product", mono: true, render: (row: { product: string }) => row.product }]}
              rows={ecr.products.map((product) => ({ product }))}
              getRowKey={(row) => row.product}
              empty={{ title: "No affected products" }}
            />
          </div>
          {allowed.length > 0 ? (
            <Card title="Actions">
              <div className={styles.form}>
                <div className={styles.field}>
                  <FormField id="ecr-reason" label="Note for the decision" optional hint="Kept when the request is approved, rejected or withdrawn.">
                    {(control) => <Textarea {...control} value={reason} onChange={(e) => setReason(e.target.value)} maxLength={200} rows={2} />}
                  </FormField>
                </div>
                <div className={styles.toolbar}>
                  {allowed.map((action) => (
                    <Button key={action} variant={action === "approve" || action === "submit" ? "primary" : undefined} loading={busy === action} disabled={busy !== null && busy !== action} onClick={() => act(action)}>
                      {actionLabels[action]}
                    </Button>
                  ))}
                </div>
              </div>
            </Card>
          ) : null}
        </>
      ) : null}
    </>
  );
}

const productSeparator = /[\s,]+/;

export function EcrCreatePage({ onCreated, onCancel }: { onCreated?: (id: string) => void; onCancel?: () => void }) {
  const [number, setNumber] = useState("");
  const [title, setTitle] = useState("");
  const [reason, setReason] = useState("");
  const [priority, setPriority] = useState("medium");
  const [products, setProducts] = useState("");
  const idempotency = useRef({ body: "", key: "" });
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [fields, setFields] = useState<Record<string, string>>({});

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setFields({});
    setSubmitting(true);
    const input = {
      number: number.trim(),
      title: title.trim(),
      ...(reason.trim() ? { reason: reason.trim() } : {}),
      priority,
      products: products.split(productSeparator).filter(Boolean),
    };
    const body = JSON.stringify(input);
    if (idempotency.current.body !== body) idempotency.current = { body, key: crypto.randomUUID() };
    try {
      const created = await createEcr(input, idempotency.current.key);
      onCreated?.(created.id);
    } catch (e) {
      if (e instanceof ApiError && e.status === forbiddenStatus) setError("You do not have permission to raise ECRs");
      else {
        setError(errorText(e));
        if (e instanceof ApiError) setFields(e.errors);
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <PageHeader title="New engineering change request" description="Describe the change and list the products it affects. It starts as a draft." />
      <Card>
        <form className={styles.form} onSubmit={submit}>
          <div className={styles.field}>
            <FormField id="new-ecr-number" label="ECR number" error={fields.number}>
              {(control) => <Input {...control} value={number} onChange={(e) => setNumber(e.target.value)} required maxLength={40} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-ecr-title" label="Title" error={fields.title}>
              {(control) => <Input {...control} value={title} onChange={(e) => setTitle(e.target.value)} required maxLength={200} />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-ecr-priority" label="Priority" error={fields.priority}>
              {(control) => (
                <Select {...control} value={priority} onChange={(e) => setPriority(e.target.value)}>
                  {priorities.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-ecr-products" label="Affected products" hint="Product ids separated by commas or spaces." error={fields.products}>
              {(control) => <Input {...control} value={products} onChange={(e) => setProducts(e.target.value)} required />}
            </FormField>
          </div>
          <div className={styles.field}>
            <FormField id="new-ecr-reason" label="Reason" optional error={fields.reason}>
              {(control) => <Textarea {...control} value={reason} onChange={(e) => setReason(e.target.value)} maxLength={2000} rows={4} />}
            </FormField>
          </div>
          <div className={styles.toolbar}>
            <Button type="submit" variant="primary" loading={submitting}>Create ECR</Button>
            {onCancel ? <Button onClick={onCancel}>Cancel</Button> : null}
          </div>
        </form>
      </Card>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
    </>
  );
}
