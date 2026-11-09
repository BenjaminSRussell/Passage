import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import type { LookupEntityKey } from "../../shell/lookup/entities";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  assignCase,
  CASE_CATEGORIES,
  CASE_PRIORITIES,
  closeCase,
  createCase,
  describeProblem,
  getCase,
  listCases,
  newKey,
  type CaseCategory,
  type CaseInput,
  type CasePriority,
  type CaseStatus,
  type CollectionCase,
  type Problem,
} from "./api/client";
import { CASE_CATEGORY_LABELS, CASE_PRIORITY_LABELS, CURRENCY, dateTime, DECIMAL, money, PRIORITY_TONES, shortId, UUID } from "./format";
import { CommandFailure, Detail, isStale, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const PAGE = 500;
const SLA_HOURS: Record<CaseCategory, number> = { COLLECTION: 72, DISPUTE: 120, DEDUCTION: 240 };

function statusBadge(c: CollectionCase) {
  if (c.status === "CLOSED") return <Badge tone="neutral">Closed</Badge>;
  return c.slaBreached ? <Badge tone="danger">SLA breached</Badge> : <Badge tone="info">Open</Badge>;
}

const columns: DataTableColumn<CollectionCase>[] = [
  { key: "category", header: "Category", render: (row) => CASE_CATEGORY_LABELS[row.category] ?? row.category },
  { key: "customerId", header: "Customer", mono: true, render: (row) => <span title={row.customerId}>{shortId(row.customerId)}</span> },
  { key: "reason", header: "Reason" },
  { key: "documentNumber", header: "Item", mono: true, render: (row) => row.documentNumber ?? "None" },
  {
    key: "attachedAmount",
    header: "Amount",
    mono: true,
    align: "end",
    render: (row) => (row.attachedAmount ? money(row.attachedAmount, row.currency) : "None"),
  },
  { key: "owner", header: "Owner" },
  { key: "priority", header: "Priority", render: (row) => <Badge tone={PRIORITY_TONES[row.priority]}>{CASE_PRIORITY_LABELS[row.priority]}</Badge> },
  { key: "slaDue", header: "SLA due", mono: true, render: (row) => dateTime(row.slaDue) },
  { key: "status", header: "Status", render: statusBadge },
];

/** Collection cases (FR-30.18): grouped collection work with an SLA per category. */
export function CasesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<CaseStatus | "">("OPEN");
  const [category, setCategory] = useState<CaseCategory | "">("");
  const state = useLoad((signal) => listCases({ status, category, limit: PAGE }, signal), `${status}|${category}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Collection cases"
        description="Open cases first, nearest SLA first. A disagreed balance confirmation opens a dispute case on its own."
        actions={
          <Link className={buttonClassName("primary")} to="/receivables/cases/new">
            Open case
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="cs-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as CaseStatus | "")}>
              <option value="OPEN">Open</option>
              <option value="CLOSED">Closed</option>
              <option value="">All statuses</option>
            </Select>
          )}
        </FormField>
        <FormField id="cs-category" label="Category">
          {(c) => (
            <Select {...c} value={category} onChange={(e) => setCategory(e.target.value as CaseCategory | "")}>
              <option value="">All categories</option>
              {CASE_CATEGORIES.map((k) => (
                <option key={k} value={k}>
                  {CASE_CATEGORY_LABELS[k]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadFailure title="Could not load the cases" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Collection cases"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No cases", description: "Open a case to group the collection work on a customer." }}
          onRowOpen={(row) => navigate(`/receivables/cases/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= PAGE ? <p className={styles.note}>Showing the first {PAGE} cases.</p> : null}
    </div>
  );
}

type Errors = Record<string, string>;

export interface CaseDraft {
  legalEntityId: string;
  customerId: string;
  category: CaseCategory;
  priority: CasePriority;
  owner: string;
  reason: string;
  openItemId: string;
  currency: string;
  attachedAmount: string;
}

/** Client checks that mirror the service validation; the service has the last word. */
export function validateCase(v: CaseDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(v.customerId.trim())) errors.customerId = "Enter the customer id (a UUID).";
  const owner = v.owner.trim();
  if (owner === "" || owner.length > 120) errors.owner = "Enter who owns the case (up to 120 characters).";
  const reason = v.reason.trim();
  if (reason === "" || reason.length > 500) errors.reason = "Say why the case is opened (up to 500 characters).";
  const item = v.openItemId.trim();
  if (item !== "" && !UUID.test(item)) errors.openItemId = "Enter the open item id (a UUID) or leave it empty.";
  const currency = v.currency.trim();
  if (item === "" && !CURRENCY.test(currency)) errors.currency = "Enter a currency such as USD, or link an open item.";
  if (item !== "" && currency !== "" && !CURRENCY.test(currency)) errors.currency = "Enter a currency such as USD, or leave it empty.";
  const amount = v.attachedAmount.trim();
  if (amount !== "" && !DECIMAL.test(amount)) errors.attachedAmount = "Enter an amount such as 1200.00, or leave it empty.";
  return errors;
}

export function toCaseInput(v: CaseDraft): CaseInput {
  const item = v.openItemId.trim();
  const currency = v.currency.trim();
  const amount = v.attachedAmount.trim();
  return {
    legalEntityId: v.legalEntityId.trim(),
    customerId: v.customerId.trim(),
    category: v.category,
    priority: v.priority,
    owner: v.owner.trim(),
    reason: v.reason.trim(),
    ...(currency !== "" ? { currency } : {}),
    openItemId: item === "" ? null : item,
    attachedAmount: amount === "" ? null : amount,
  };
}

/** Open a collection case. The open items page can link here with the customer, entity and item filled in. */
export function NewCasePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<CaseDraft>({
    legalEntityId: params.get("legalEntity") ?? "",
    customerId: params.get("customer") ?? "",
    category: "COLLECTION",
    priority: "NORMAL",
    owner: "",
    reason: "",
    openItemId: params.get("openItem") ?? "",
    currency: "",
    attachedAmount: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CaseDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateCase(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createCase(toCaseInput(values), key.current);
      navigate(`/receivables/cases/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (!keepKeyAfter(error)) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: keyof CaseDraft, label: string, opts: { entity?: LookupEntityKey; hint?: string; mono?: boolean; optional?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} error={errors[field]} optional={opts.optional}>
        {(c) =>
        opts.entity ? (
          <EntityLookup
            {...c}
            entity={opts.entity}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<CaseDraft>)}
            invalid={Boolean(errors[field])}
          />
        ) : (
          <Input
            {...c}
            className={opts.mono ? styles.mono : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<CaseDraft>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Open collection case" description={`The SLA follows the category: ${SLA_HOURS[values.category]} hours for ${CASE_CATEGORY_LABELS[values.category].toLowerCase()} cases.`} />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("nc-entity", "legalEntityId", "Legal entity", { entity: "legalEntity", mono: true })}
          {text("nc-customer", "customerId", "Customer", { entity: "customer", mono: true })}
          <FormField id="nc-category" label="Category">
            {(c) => (
              <Select {...c} value={values.category} onChange={(e) => update({ category: e.target.value as CaseCategory })}>
                {CASE_CATEGORIES.map((k) => (
                  <option key={k} value={k}>
                    {CASE_CATEGORY_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="nc-priority" label="Priority">
            {(c) => (
              <Select {...c} value={values.priority} onChange={(e) => update({ priority: e.target.value as CasePriority })}>
                {CASE_PRIORITIES.map((p) => (
                  <option key={p} value={p}>
                    {CASE_PRIORITY_LABELS[p]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("nc-owner", "owner", "Owner")}
          {text("nc-item", "openItemId", "Open item id", { mono: true, optional: true, hint: "Links the case to one item; the case takes its currency." })}
          {text("nc-currency", "currency", "Currency", { mono: true, optional: true, hint: "Needed when no open item is linked." })}
          {text("nc-amount", "attachedAmount", "Amount", { mono: true, optional: true })}
          <div className={styles.wide}>
            <FormField id="nc-reason" label="Reason" error={errors.reason}>
              {(c) => <Textarea {...c} value={values.reason} maxLength={500} onChange={(e) => update({ reason: e.target.value })} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not open the case" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Open case
            </Button>
            <Link className={buttonClassName("ghost")} to="/receivables/cases">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One collection case: hand it to another owner or close it with a note. */
export function CaseDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCase(id, signal), id);
  const [owner, setOwner] = useState("");
  const [note, setNote] = useState("");
  const [ownerError, setOwnerError] = useState<string | undefined>();
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState<"assign" | "close" | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Collection case" />
        <LoadFailure title="Could not load the case" problem={state.error} retry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Collection case" />
        <span className="visually-hidden">Loading the case…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const record = state.data;

  async function run(kind: "assign" | "close", action: () => Promise<CollectionCase>, message: string) {
    setBusy(kind);
    setFailure(null);
    setDone(null);
    try {
      await action();
      setDone(message);
      setOwner("");
      setNote("");
      state.reload();
    } catch (error) {
      setFailure({
        title: isStale(error) ? "The case changed since you opened it. Reload it and try again." : "Could not update the case",
        problem: describeProblem(error),
      });
    } finally {
      setBusy(null);
    }
  }

  function assign(event: FormEvent) {
    event.preventDefault();
    const next = owner.trim();
    if (next === "" || next.length > 120) {
      setOwnerError("Enter the new owner (up to 120 characters).");
      return;
    }
    setOwnerError(undefined);
    void run("assign", () => assignCase(record.id, record.version, next), `The case now belongs to ${next}.`);
  }

  function close(event: FormEvent) {
    event.preventDefault();
    const text = note.trim();
    void run("close", () => closeCase(record.id, record.version, text === "" ? null : text), "The case is closed.");
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${CASE_CATEGORY_LABELS[record.category]} case`}
        description={record.reason}
        meta={statusBadge(record)}
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/cases">
            All cases
          </Link>
        }
      />
      <Card title="Case" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Owner">{record.owner}</Detail>
          <Detail term="Priority">{CASE_PRIORITY_LABELS[record.priority]}</Detail>
          <Detail term="Amount" mono>{record.attachedAmount ? money(record.attachedAmount, record.currency) : "None"}</Detail>
          <Detail term="Open item" mono>{record.documentNumber ?? "None"}</Detail>
          <Detail term="SLA" mono>{`${record.slaHours} h, due ${dateTime(record.slaDue)}`}</Detail>
          <Detail term="Opened" mono>{dateTime(record.createdAt)}</Detail>
          <Detail term="Closed" mono>{dateTime(record.closedAt)}</Detail>
          <Detail term="Customer" mono>{record.customerId}</Detail>
          <Detail term="Close note">{record.closeNote ?? "None"}</Detail>
        </dl>
      </Card>
      {record.status === "OPEN" && record.slaBreached ? (
        <Alert tone="warning" title="SLA breached" live="status">
          The case passed its {record.slaHours}-hour SLA. Escalate it or close it (AR-0018).
        </Alert>
      ) : null}
      {done ? (
        <Alert tone="success" title="Case updated" live="status">
          {done}
        </Alert>
      ) : null}
      {failure ? <CommandFailure title={failure.title} problem={failure.problem} /> : null}
      {record.status === "OPEN" ? (
        <Card title="Work the case" headingLevel={3}>
          <div className={styles.stack}>
            <form className={styles.form} onSubmit={assign} noValidate>
              <FormField id="cd-owner" label="New owner" error={ownerError}>
                {(c) => <Input {...c} value={owner} onChange={(e) => setOwner(e.target.value)} invalid={Boolean(ownerError)} />}
              </FormField>
              <div className={styles.formActions}>
                <Button type="submit" variant="secondary" loading={busy === "assign"}>
                  Assign
                </Button>
              </div>
            </form>
            <form className={styles.form} onSubmit={close} noValidate>
              <div className={styles.wide}>
                <FormField id="cd-note" label="Close note" optional hint="For example how the customer settled.">
                  {(c) => <Textarea {...c} value={note} maxLength={2000} onChange={(e) => setNote(e.target.value)} />}
                </FormField>
              </div>
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy === "close"}>
                  Close case
                </Button>
              </div>
            </form>
          </div>
        </Card>
      ) : (
        <p className={styles.note}>This case is closed.</p>
      )}
    </div>
  );
}
