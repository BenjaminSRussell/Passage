import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  buildProposal,
  createTransaction,
  decideProposal,
  describeProblem,
  getBillingStatus,
  getProject,
  getProposal,
  LINE_PROPERTIES,
  listCategories,
  listProposals,
  listTransactions,
  newKey,
  PROPOSAL_STATUSES,
  proposalActions,
  putCategory,
  removeProposalLine,
  TXN_TYPES,
  type BillingStatus,
  type Category,
  type InvoiceProposal,
  type LineProperty,
  type NewTransaction,
  type Problem,
  type ProposalAction,
  type ProposalLine,
  type ProposalStatus,
  type Transaction,
  type TxnType,
} from "./api/client";
import { billingReasonLabel, dash, linePropertyLabel, proposalStatusLabel, proposalStatusTone, txnTypeLabel } from "./labels";
import styles from "./pages.module.css";
import { useLoad, useMore, type MoreState } from "./useLoad";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const CATEGORY_ID = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;
const QUANTITY = /^\d{1,11}(\.\d{1,3})?$/;
const PRICE = /^\d{1,14}(\.\d{1,6})?$/;
const SIGNED_PRICE = /^-?\d{1,14}(\.\d{1,6})?$/;

const projectPath = (id: string) => `/projects/${encodeURIComponent(id)}`;
const proposalsPath = (id: string) => `${projectPath(id)}/invoice-proposals`;

function NotFound({ title, text, back, backLabel }: { title: string; text: string; back: string; backLabel: string }) {
  return (
    <div className={styles.stack}>
      <PageHeader title={title} />
      <Card>
        <EmptyState
          icon="grid"
          title={`${title} not found`}
          description={text}
          action={
            <Link className={buttonClassName("secondary")} to={back}>
              {backLabel}
            </Link>
          }
        />
      </Card>
    </div>
  );
}

function LoadFailure({ what, problem, retry }: { what: string; problem: Problem; retry(): void }) {
  return (
    <Alert
      tone="danger"
      title={`Could not load ${what}`}
      live="alert"
      action={
        <Button size="sm" onClick={retry}>
          Try again
        </Button>
      }
    >
      {problem.message} ({problem.code})
    </Alert>
  );
}

/** The "Load more" button under a paged table, with the failure of the last page fetch. */
function LoadMore<T>({ what, more }: { what: string; more: MoreState<T> }) {
  return (
    <>
      {more.error ? (
        <Alert tone="danger" title={`Could not load more ${what}`} live="alert">
          {more.error.message} ({more.error.code})
        </Alert>
      ) : null}
      {more.hasMore ? (
        <div className={styles.more}>
          <Button onClick={() => void more.loadMore()} loading={more.loading}>
            Load more {what}
          </Button>
        </div>
      ) : null}
    </>
  );
}

export function ProposalBadge({ status }: { status: ProposalStatus }) {
  return <Badge tone={proposalStatusTone[status]}>{proposalStatusLabel[status]}</Badge>;
}

// ---- Project categories ----

/** Project categories (§8.41.4): the codes transactions are booked under, each bound to one transaction type. */
export function CategoriesPage() {
  const state = useLoad((signal) => listCategories(signal), "categories");
  const [editing, setEditing] = useState<Category | null>(null);
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [txnType, setTxnType] = useState<TxnType>("HOUR");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function startEdit(c: Category) {
    setEditing(c);
    setId(c.id);
    setName(c.name);
    setTxnType(c.txnType);
    setErrors({});
    setFailure(null);
    setSaved(null);
  }
  function reset() {
    setEditing(null);
    setId("");
    setName("");
    setTxnType("HOUR");
    setErrors({});
    setFailure(null);
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const code = id.trim().toUpperCase();
    if (!CATEGORY_ID.test(code)) found.id = "Use 1 to 20 capitals, digits, hyphens or underscores.";
    if (name.trim() === "" || name.trim().length > 120) found.name = "Enter a name of up to 120 characters.";
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await putCategory(code, { name: name.trim(), txnType }, editing?.version);
      setSaved(`Category ${out.id} is saved.`);
      reset();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<Category>[] = [
    {
      key: "id",
      header: "Category",
      mono: true,
      render: (c) => (
        <Button size="sm" variant="ghost" onClick={() => startEdit(c)}>
          {c.id}
        </Button>
      ),
    },
    { key: "name", header: "Name" },
    { key: "txnType", header: "Transaction type", render: (c) => txnTypeLabel[c.txnType] },
    { key: "version", header: "Version", align: "end" },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Project categories"
        description="Every transaction is booked under a category of its own type. A category's type cannot change once it is set."
      />
      <Alert tone="success" live="status">
        {saved}
      </Alert>
      {state.error ? (
        <LoadFailure what="the categories" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Project categories"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(c) => c.id}
          loading={state.loading}
          empty={{ title: "No categories yet", description: "Add a category such as LABOR for hours or TRAVEL for expenses." }}
        />
      )}
      <Card title={editing ? `Edit category ${editing.id}` : "Add a category"}>
        <form className={styles.form} onSubmit={save} noValidate aria-label={editing ? "Edit category" : "New category"}>
          <FormField id="cat-id" label="Category id" error={errors.id}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={id}
                readOnly={Boolean(editing)}
                maxLength={20}
                invalid={Boolean(errors.id)}
                onChange={(e) => setId(e.target.value)}
              />
            )}
          </FormField>
          <FormField id="cat-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={name} maxLength={120} invalid={Boolean(errors.name)} onChange={(e) => setName(e.target.value)} />}
          </FormField>
          <FormField id="cat-type" label="Transaction type" hint={editing ? "Fixed once the category exists." : undefined} error={errors.txnType}>
            {(control) => (
              <Select {...control} value={txnType} disabled={Boolean(editing)} onChange={(e) => setTxnType(e.target.value as TxnType)}>
                {TXN_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {txnTypeLabel[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "conflict_version" ? "Someone else changed this category" : "Could not save the category"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              {editing ? "Save category" : "Add category"}
            </Button>
            {editing ? (
              <Button variant="ghost" onClick={reset}>
                Cancel
              </Button>
            ) : null}
          </div>
        </form>
      </Card>
    </div>
  );
}

// ---- Transactions ----

export interface TransactionValues {
  txnType: TxnType;
  categoryId: string;
  lineProperty: LineProperty;
  txnDate: string;
  quantity: string;
  costPrice: string;
  salesPrice: string;
  workerId: string;
  itemId: string;
  wbsNodeId: string;
  description: string;
}

export const blankTransaction: TransactionValues = {
  txnType: "HOUR",
  categoryId: "",
  lineProperty: "CHARGEABLE",
  txnDate: "",
  quantity: "",
  costPrice: "",
  salesPrice: "",
  workerId: "",
  itemId: "",
  wbsNodeId: "",
  description: "",
};

/** Checks that mirror domain.ValidateTransaction; keys are the service's field names. */
export function transactionInput(projectId: string, currency: string, v: TransactionValues): { errors: Record<string, string>; input?: NewTransaction } {
  const errors: Record<string, string> = {};
  if (!CATEGORY_ID.test(v.categoryId)) errors.categoryId = "Choose a category.";
  if (!DATE.test(v.txnDate)) errors.txnDate = "Choose the transaction date.";
  const quantity = v.quantity.trim();
  if (!QUANTITY.test(quantity)) errors.quantity = "Enter a quantity of 0 or more with at most 3 decimals.";
  const cost = v.costPrice.trim();
  if (!PRICE.test(cost)) errors.costPrice = "Enter a unit cost of 0 or more with at most 6 decimals.";
  const sales = v.salesPrice.trim();
  if (!(v.txnType === "ON_ACCOUNT" ? SIGNED_PRICE : PRICE).test(sales)) {
    errors.salesPrice =
      v.txnType === "ON_ACCOUNT"
        ? "Enter a unit sales price with at most 6 decimals (negative applies a retainer)."
        : "Enter a unit sales price of 0 or more with at most 6 decimals.";
  }
  const worker = v.workerId.trim();
  if (v.txnType === "HOUR" && !UUID.test(worker)) errors.workerId = "Enter the worker's UUID; hours need a worker.";
  const item = v.itemId.trim();
  if (v.txnType === "ITEM" && !UUID.test(item)) errors.itemId = "Enter the item's UUID; item lines need an item.";
  const node = v.wbsNodeId.trim();
  if (node !== "" && !UUID.test(node)) errors.wbsNodeId = "Enter a WBS node UUID, or leave it empty.";
  if (v.description.length > 500) errors.description = "Use at most 500 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      projectId,
      txnType: v.txnType,
      categoryId: v.categoryId,
      lineProperty: v.lineProperty,
      txnDate: v.txnDate,
      quantity,
      costPrice: cost,
      salesPrice: sales,
      currency,
      ...(v.txnType === "HOUR" ? { workerId: worker } : {}),
      ...(v.txnType === "ITEM" ? { itemId: item } : {}),
      ...(node ? { wbsNodeId: node } : {}),
      ...(v.description.trim() ? { description: v.description.trim() } : {}),
    },
  };
}

const txnFailureTitle: Record<string, string> = {
  "PROJ-TXN-001": "The project is not in process",
  "PROJ-TXN-002": "Wrong currency",
  "PROJ-TXN-003": "That category does not fit",
  "AUTH-0003": "You can't record transactions",
};

function monthWindow(): { from: string; to: string } {
  const now = new Date();
  const from = new Date(Date.UTC(now.getFullYear(), now.getMonth(), 1));
  const to = new Date(Date.UTC(now.getFullYear(), now.getMonth() + 1, 0));
  return { from: from.toISOString().slice(0, 10), to: to.toISOString().slice(0, 10) };
}

/** Project transactions (FR-41.4): list, record a priced line, and ask why a line is not billed (BR-41.3). */
export function TransactionsPage() {
  const { id = "" } = useParams();
  const project = useLoad((signal) => getProject(id, signal), id);
  const txns = useLoad((signal) => listTransactions(id, undefined, signal), id);
  const txnRows = useMore(txns.data, (cursor) => listTransactions(id, cursor));
  const categories = useLoad((signal) => listCategories(signal), "categories");
  const [values, setValues] = useState<TransactionValues>(blankTransaction);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [billingWindow, setBillingWindow] = useState(monthWindow);
  const [why, setWhy] = useState<{ txn: Transaction; status?: BillingStatus; problem?: Problem } | null>(null);
  const key = useRef(newKey());

  if (project.error?.status === 404 || txns.error?.status === 404) {
    return <NotFound title="Transactions" text={`There is no project ${id} in your legal entities.`} back="/projects/list" backLabel="Back to projects" />;
  }
  const currency = project.data?.currency ?? "";
  const matching = (categories.data?.items ?? []).filter((c) => c.txnType === values.txnType);

  function change(patch: Partial<TransactionValues>) {
    setValues((v) => {
      const next = { ...v, ...patch };
      if (patch.txnType && patch.txnType !== v.txnType) next.categoryId = "";
      return next;
    });
    key.current = newKey();
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = transactionInput(id, currency, values);
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (!input) return;
    setBusy(true);
    try {
      const out = await createTransaction(input, key.current);
      setSaved(`Recorded ${txnTypeLabel[out.txnType].toLowerCase()} on ${out.txnDate}: cost ${out.costAmount}, sales ${out.salesAmount} ${out.currency}.`);
      setValues((v) => ({ ...blankTransaction, txnType: v.txnType, categoryId: v.categoryId, lineProperty: v.lineProperty, workerId: v.workerId }));
      key.current = newKey();
      txns.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  async function explain(txn: Transaction) {
    setWhy({ txn });
    try {
      setWhy({ txn, status: await getBillingStatus(txn.id, billingWindow) });
    } catch (error) {
      setWhy({ txn, problem: describeProblem(error) });
    }
  }

  const columns: DataTableColumn<Transaction>[] = [
    { key: "txnDate", header: "Date", mono: true },
    { key: "txnType", header: "Type", render: (t) => txnTypeLabel[t.txnType] },
    { key: "categoryId", header: "Category", mono: true },
    { key: "wbsCode", header: "WBS", mono: true, render: (t) => t.wbsCode ?? dash },
    { key: "lineProperty", header: "Line property", render: (t) => linePropertyLabel[t.lineProperty] },
    { key: "quantity", header: "Quantity", align: "end", mono: true },
    { key: "costAmount", header: "Cost", align: "end", mono: true },
    { key: "salesAmount", header: "Sales", align: "end", mono: true },
    {
      key: "proposalId",
      header: "Billing",
      render: (t) =>
        t.proposalId ? (
          <Link to={`${proposalsPath(id)}/${encodeURIComponent(t.proposalId)}`}>On a proposal</Link>
        ) : (
          <Button size="sm" variant="secondary" onClick={() => void explain(t)}>
            Check billing for {t.txnDate} {t.categoryId}
          </Button>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Transactions for ${id}`}
        description="Hours, expenses, items, fees and on-account lines. Amounts are quantity times unit price, rounded per line. Posting to the general ledger arrives in a later release."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={proposalsPath(id)}>
              Invoice proposals
            </Link>
            <Link className={buttonClassName("secondary")} to={projectPath(id)}>
              Back to project
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {saved}
      </Alert>
      <div className={styles.toolbar} role="group" aria-label="Billing window">
        <FormField id="why-from" label="Billing window from">
          {(control) => <Input {...control} type="date" value={billingWindow.from} onChange={(e) => setBillingWindow((w) => ({ ...w, from: e.target.value }))} />}
        </FormField>
        <FormField id="why-to" label="Billing window to">
          {(control) => <Input {...control} type="date" value={billingWindow.to} onChange={(e) => setBillingWindow((w) => ({ ...w, to: e.target.value }))} />}
        </FormField>
      </div>
      {why ? (
        why.problem ? (
          <Alert tone="danger" title="Could not check billing" live="alert">
            {why.problem.message} ({why.problem.code})
          </Alert>
        ) : why.status ? (
          <Alert tone={why.status.billable ? "success" : "info"} title={`Line of ${why.txn.txnDate} (${why.txn.categoryId})`} live="status">
            {why.status.billable
              ? `It would be billed by a proposal for ${billingWindow.from} to ${billingWindow.to}.`
              : (billingReasonLabel[why.status.reason ?? "OUT_OF_RANGE"] ?? why.status.reason)}
          </Alert>
        ) : null
      ) : null}
      {txns.error ? (
        <LoadFailure what="the transactions" problem={txns.error} retry={txns.reload} />
      ) : (
        <DataTable
          label="Transactions"
          columns={columns}
          rows={txnRows.rows}
          getRowKey={(t) => t.id}
          loading={txns.loading}
          empty={{ title: "No transactions yet", description: "Record hours, expenses or fees against the project below." }}
        />
      )}
      {!txns.error ? <LoadMore what="transactions" more={txnRows} /> : null}
      <Card title="Record a transaction">
        {project.data && project.data.stage !== "IN_PROCESS" ? (
          <p className={styles.note}>Transactions need a project in process; this project is {project.data.stage.toLowerCase().replace("_", " ")}.</p>
        ) : null}
        <form className={styles.stack} onSubmit={save} noValidate aria-label="New transaction">
          <div className={styles.form}>
            <FormField id="txn-type" label="Type">
              {(control) => (
                <Select {...control} value={values.txnType} onChange={(e) => change({ txnType: e.target.value as TxnType })}>
                  {TXN_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {txnTypeLabel[t]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField
              id="txn-category"
              label="Category"
              hint={matching.length === 0 && !categories.loading ? "No category of this type yet; add one under Project categories." : undefined}
              error={errors.categoryId}
            >
              {(control) => (
                <Select {...control} value={values.categoryId} invalid={Boolean(errors.categoryId)} onChange={(e) => change({ categoryId: e.target.value })}>
                  <option value="">Choose a category</option>
                  {matching.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.id} {c.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="txn-property" label="Line property">
              {(control) => (
                <Select {...control} value={values.lineProperty} onChange={(e) => change({ lineProperty: e.target.value as LineProperty })}>
                  {LINE_PROPERTIES.map((p) => (
                    <option key={p} value={p}>
                      {linePropertyLabel[p]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="txn-date" label="Date" error={errors.txnDate}>
              {(control) => <Input {...control} type="date" value={values.txnDate} invalid={Boolean(errors.txnDate)} onChange={(e) => change({ txnDate: e.target.value })} />}
            </FormField>
            <FormField id="txn-qty" label="Quantity" hint={values.txnType === "HOUR" ? "Hours" : "Units"} error={errors.quantity}>
              {(control) => (
                <Input {...control} inputMode="decimal" className={styles.mono} value={values.quantity} invalid={Boolean(errors.quantity)} onChange={(e) => change({ quantity: e.target.value })} />
              )}
            </FormField>
            <FormField id="txn-cost" label="Unit cost" hint={currency || undefined} error={errors.costPrice}>
              {(control) => (
                <Input {...control} inputMode="decimal" className={styles.mono} value={values.costPrice} invalid={Boolean(errors.costPrice)} onChange={(e) => change({ costPrice: e.target.value })} />
              )}
            </FormField>
            <FormField id="txn-sales" label="Unit sales price" hint={currency || undefined} error={errors.salesPrice}>
              {(control) => (
                <Input {...control} inputMode="decimal" className={styles.mono} value={values.salesPrice} invalid={Boolean(errors.salesPrice)} onChange={(e) => change({ salesPrice: e.target.value })} />
              )}
            </FormField>
            {values.txnType === "HOUR" ? (
              <FormField id="txn-worker" label="Worker" hint="Worker UUID until worker records exist." error={errors.workerId}>
                {(control) => (
                  <Input {...control} className={styles.mono} value={values.workerId} invalid={Boolean(errors.workerId)} onChange={(e) => change({ workerId: e.target.value })} />
                )}
              </FormField>
            ) : null}
            {values.txnType === "ITEM" ? (
              <FormField id="txn-item" label="Item" hint="Item UUID." error={errors.itemId}>
                {(control) => <Input {...control} className={styles.mono} value={values.itemId} invalid={Boolean(errors.itemId)} onChange={(e) => change({ itemId: e.target.value })} />}
              </FormField>
            ) : null}
            <FormField id="txn-node" label="WBS node" hint="Needed for fixed-price milestone billing." error={errors.wbsNodeId} optional>
              {(control) => (
                <Input {...control} className={styles.mono} value={values.wbsNodeId} invalid={Boolean(errors.wbsNodeId)} onChange={(e) => change({ wbsNodeId: e.target.value })} />
              )}
            </FormField>
            <FormField id="txn-description" label="Description" error={errors.description} optional>
              {(control) => (
                <Input {...control} value={values.description} maxLength={500} invalid={Boolean(errors.description)} onChange={(e) => change({ description: e.target.value })} />
              )}
            </FormField>
          </div>
          {failure ? (
            <Alert tone="danger" title={txnFailureTitle[failure.code] ?? "Could not record the transaction"} live="alert">
              {failure.message} ({failure.code})
            </Alert>
          ) : null}
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy} disabled={!currency}>
              Record transaction
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

// ---- Invoice proposals ----

const buildFailureTitle: Record<string, string> = {
  "PROJ-PROP-002": "Nothing to bill",
  "PROJ-PROP-004": "This project does not bill these lines",
  "AUTH-0003": "You can't build invoice proposals",
};

/** Invoice proposals of a project (FR-41.6): list by status and build a proposal from a date window. */
export function ProposalsPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [status, setStatus] = useState<ProposalStatus | "">("");
  const state = useLoad((signal) => listProposals(id, status, signal), `${id}|${status}`);
  const proposalRows = useMore(state.data, (cursor) => listProposals(id, status, undefined, cursor));
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [txnType, setTxnType] = useState<TxnType | "">("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (state.error?.status === 404) {
    return <NotFound title="Invoice proposals" text={`There is no project ${id} in your legal entities.`} back="/projects/list" backLabel="Back to projects" />;
  }

  function touched() {
    key.current = newKey();
  }

  async function build(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!DATE.test(from)) found.from = "Choose the first day of the window.";
    if (!DATE.test(to)) found.to = "Choose the last day of the window.";
    else if (DATE.test(from) && to < from) found.to = "The window must not end before it starts.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await buildProposal({ projectId: id, from, to, ...(txnType ? { txnTypes: [txnType] } : {}) }, key.current);
      key.current = newKey();
      navigate(`${proposalsPath(id)}/${encodeURIComponent(out.id)}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<InvoiceProposal>[] = [
    {
      key: "window",
      header: "Window",
      mono: true,
      render: (p) => <Link to={`${proposalsPath(id)}/${encodeURIComponent(p.id)}`}>{`${p.fromDate} to ${p.toDate}`}</Link>,
    },
    { key: "status", header: "Status", render: (p) => <ProposalBadge status={p.status} /> },
    { key: "txnTypes", header: "Types", render: (p) => p.txnTypes.map((t) => txnTypeLabel[t]).join(", ") },
    { key: "lineCount", header: "Lines", align: "end" },
    { key: "totalCost", header: "Cost", align: "end", mono: true },
    { key: "totalSales", header: "Sales", align: "end", mono: true, render: (p) => `${p.totalSales} ${p.currency}` },
    { key: "createdBy", header: "Built by", mono: true },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Invoice proposals for ${id}`}
        description="A proposal bills the chargeable lines of a window that are not on another proposal. Fixed-price projects bill on-account lines of billable milestones due by the end of the window."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={`${projectPath(id)}/transactions`}>
              Transactions
            </Link>
            <Link className={buttonClassName("secondary")} to={projectPath(id)}>
              Back to project
            </Link>
          </div>
        }
      />
      <div className={styles.toolbar} role="group" aria-label="Filters">
        <FormField id="prop-filter-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as ProposalStatus | "")}>
              <option value="">All statuses</option>
              {PROPOSAL_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {proposalStatusLabel[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadFailure what="the invoice proposals" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Invoice proposals"
          columns={columns}
          rows={proposalRows.rows}
          getRowKey={(p) => p.id}
          loading={state.loading}
          empty={
            status
              ? { title: "No proposals match", description: "Clear the status filter to see more." }
              : { title: "No invoice proposals yet", description: "Build one from the billable transactions of a date window." }
          }
        />
      )}
      {!state.error ? <LoadMore what="proposals" more={proposalRows} /> : null}
      <Card title="Build a proposal">
        <form className={styles.form} onSubmit={build} noValidate aria-label="Build invoice proposal">
          <FormField id="prop-from" label="From" error={errors.from}>
            {(control) => (
              <Input
                {...control}
                type="date"
                value={from}
                invalid={Boolean(errors.from)}
                onChange={(e) => {
                  setFrom(e.target.value);
                  touched();
                }}
              />
            )}
          </FormField>
          <FormField id="prop-to" label="To" error={errors.to}>
            {(control) => (
              <Input
                {...control}
                type="date"
                value={to}
                invalid={Boolean(errors.to)}
                onChange={(e) => {
                  setTo(e.target.value);
                  touched();
                }}
              />
            )}
          </FormField>
          <FormField id="prop-type" label="Transaction type" error={errors.txnTypes}>
            {(control) => (
              <Select
                {...control}
                value={txnType}
                onChange={(e) => {
                  setTxnType(e.target.value as TxnType | "");
                  touched();
                }}
              >
                <option value="">Every type the project bills</option>
                {TXN_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {txnTypeLabel[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone={failure.code === "PROJ-PROP-002" ? "info" : "danger"} title={buildFailureTitle[failure.code] ?? "Could not build the proposal"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Build proposal
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

const actionText: Record<ProposalAction, string> = { approve: "Approve proposal", cancel: "Cancel proposal" };
const actionDone: Record<ProposalAction, string> = { approve: "approved", cancel: "canceled; its lines are back in the pool" };

/** One invoice proposal: header, lines with remove, approve and cancel (FR-41.6). */
export function ProposalDetailPage() {
  const { id = "", proposalId = "" } = useParams();
  const state = useLoad((signal) => getProposal(proposalId, signal), proposalId);
  const [latest, setLatest] = useState<InvoiceProposal | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);

  const pr = latest ?? state.data;
  if (state.error && !pr) {
    if (state.error.status === 404) {
      return <NotFound title="Invoice proposal" text="There is no such proposal in your legal entities." back={proposalsPath(id)} backLabel="Back to invoice proposals" />;
    }
    return (
      <div className={styles.stack}>
        <PageHeader title="Invoice proposal" />
        <LoadFailure what="the proposal" problem={state.error} retry={state.reload} />
      </div>
    );
  }
  if (!pr) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Invoice proposal" />
        <span className="visually-hidden">Loading the proposal…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const current = pr;
  const editable = current.status === "DRAFT" || current.status === "APPROVED";

  async function run(label: string, step: () => Promise<InvoiceProposal>, message: string) {
    setBusy(label);
    setFailure(null);
    setDone(null);
    try {
      setLatest(await step());
      setDone(message);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  function reload() {
    setLatest(null);
    setFailure(null);
    state.reload();
  }

  const stale = failure?.code === "conflict_version";
  const facts: [string, ReactNode, boolean?][] = [
    ["Project", <Link to={projectPath(current.projectId)}>{current.projectId}</Link>, true],
    ["Window", `${current.fromDate} to ${current.toDate}`, true],
    ["Types", current.txnTypes.map((t) => txnTypeLabel[t]).join(", ")],
    ["Total cost", `${current.totalCost} ${current.currency}`, true],
    ["Total sales", `${current.totalSales} ${current.currency}`, true],
    ["Customer", current.customerPartyId ?? dash, true],
    ["Built by", current.createdBy, true],
    ["Approved by", current.approvedBy ?? dash, true],
    ["Version", current.version],
  ];
  const lineColumns: DataTableColumn<ProposalLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "txnDate", header: "Date", mono: true },
    { key: "txnType", header: "Type", render: (l) => txnTypeLabel[l.txnType] },
    { key: "categoryId", header: "Category", mono: true },
    { key: "description", header: "Description", render: (l) => l.description || dash },
    { key: "quantity", header: "Quantity", align: "end", mono: true },
    { key: "unitPrice", header: "Unit price", align: "end", mono: true },
    { key: "amount", header: "Amount", align: "end", mono: true },
    {
      key: "remove",
      header: "Actions",
      render: (l) =>
        editable ? (
          <Button
            size="sm"
            variant="secondary"
            loading={busy === l.transactionId}
            disabled={busy !== null && busy !== l.transactionId}
            onClick={() => void run(l.transactionId, () => removeProposalLine(current.id, l.transactionId, current.version), `Line ${l.lineNo} is removed and back in the pool.`)}
          >
            Remove line {l.lineNo}
          </Button>
        ) : (
          dash
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Invoice proposal ${current.fromDate} to ${current.toDate}`}
        description={`Project ${current.projectId}`}
        meta={<ProposalBadge status={current.status} />}
        actions={
          <Link className={buttonClassName("secondary")} to={proposalsPath(id || current.projectId)}>
            Back to invoice proposals
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={stale ? "Someone else changed this proposal" : "Could not complete the step"}
          live="alert"
          action={
            stale ? (
              <Button size="sm" onClick={reload}>
                Reload proposal
              </Button>
            ) : undefined
          }
        >
          {stale ? "Reload to see the latest version, then try again." : `${failure.message} (${failure.code})`}
        </Alert>
      ) : null}
      <Card title="Header">
        <dl className={styles.details}>
          {facts.map(([term, value, mono]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <DataTable
        label="Proposal lines"
        columns={lineColumns}
        rows={current.lines}
        getRowKey={(l) => l.transactionId}
        empty={{ title: "No lines", description: editable ? "Every line was removed; cancel the proposal or build a new one." : undefined }}
      />
      <Card title="Next step">
        {proposalActions[current.status].length === 0 ? (
          <p className={styles.note}>
            {current.status === "CANCELED" ? "This proposal is canceled; its lines went back to the pool." : "This proposal is posted and can no longer change."}
          </p>
        ) : (
          <div className={styles.stack}>
            <p className={styles.note}>
              {current.status === "APPROVED"
                ? "Approved proposals are handed to invoicing once that service is connected. Removing a line returns the proposal to draft."
                : "Review the lines, remove any that should not be billed, then approve. Canceling returns every line to the pool."}
            </p>
            <div className={styles.actions} role="group" aria-label="Proposal actions">
              {proposalActions[current.status].map((a) => (
                <Button
                  key={a}
                  variant={a === "cancel" ? "danger" : "primary"}
                  loading={busy === a}
                  disabled={(busy !== null && busy !== a) || (a === "approve" && current.lines.length === 0)}
                  onClick={() => void run(a, () => decideProposal(current.id, a, current.version), `The proposal is ${actionDone[a]}.`)}
                >
                  {actionText[a]}
                </Button>
              ))}
            </div>
          </div>
        )}
      </Card>
    </div>
  );
}
