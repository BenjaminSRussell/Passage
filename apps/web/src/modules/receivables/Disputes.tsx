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
  describeProblem,
  DISPUTE_STATUSES,
  getDispute,
  listDisputes,
  newKey,
  openDispute,
  RESOLUTIONS,
  resolveDispute,
  reviewDispute,
  type Dispute,
  type DisputeInput,
  type DisputeStatus,
  type Problem,
  type Resolution,
} from "./api/client";
import { dateTime, DECIMAL, DISPUTE_LABELS, DISPUTE_TONES, money, RESOLUTION_LABELS, shortId, UUID } from "./format";
import { CommandFailure, Detail, isStale, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const PAGE = 500;
const REASON = /^[A-Z][A-Z_]{1,31}$/;
const ACTIVE = new Set<DisputeStatus>(["OPEN", "UNDER_REVIEW"]);
const RESOLVED_MESSAGES: Record<Resolution, string> = {
  CREDIT: "Resolved as a credit. Post the credit note against the item.",
  PAID: "Resolved as paid.",
  WRITE_OFF: "Resolved as a write-off.",
  REJECTED: "The dispute was rejected.",
};

function statusBadge(status: DisputeStatus) {
  return <Badge tone={DISPUTE_TONES[status] ?? "neutral"}>{DISPUTE_LABELS[status] ?? status}</Badge>;
}

const columns: DataTableColumn<Dispute>[] = [
  { key: "documentNumber", header: "Document", mono: true },
  { key: "customerId", header: "Customer", mono: true, render: (row) => <span title={row.customerId}>{shortId(row.customerId)}</span> },
  { key: "reasonCode", header: "Reason", mono: true },
  { key: "disputedAmount", header: "Disputed", mono: true, align: "end", render: (row) => money(row.disputedAmount, row.currency) },
  { key: "owner", header: "Owner" },
  { key: "slaDue", header: "SLA due", mono: true, render: (row) => dateTime(row.slaDue) },
  { key: "status", header: "Status", render: (row) => statusBadge(row.status) },
];

/** Disputes (FR-30.9): items the customer contests. An active dispute keeps its item out of dunning and interest. */
export function DisputesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<DisputeStatus | "active" | "">("active");
  const state = useLoad((signal) => listDisputes({ status, limit: PAGE }, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Disputes"
        description="Disputed items, nearest SLA first. While a dispute is open or under review, dunning and interest skip its item."
        actions={
          <Link className={buttonClassName("primary")} to="/receivables/disputes/new">
            Open dispute
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="dsp-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as DisputeStatus | "active" | "")}>
              <option value="active">Open and under review</option>
              <option value="">All statuses</option>
              {DISPUTE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {DISPUTE_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadFailure title="Could not load the disputes" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Disputes"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No disputes", description: "Open a dispute from an open item when the customer contests it." }}
          onRowOpen={(row) => navigate(`/receivables/disputes/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= PAGE ? <p className={styles.note}>Showing the first {PAGE} disputes.</p> : null}
    </div>
  );
}

type Errors = Record<string, string>;

export interface DisputeDraft {
  legalEntityId: string;
  openItemId: string;
  reasonCode: string;
  disputedAmount: string;
  owner: string;
}

/** Client checks that mirror the service validation; the service has the last word. */
export function validateDispute(v: DisputeDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (!UUID.test(v.openItemId.trim())) errors.openItemId = "Enter the open item id (a UUID).";
  if (!REASON.test(v.reasonCode.trim())) errors.reasonCode = "Use an upper-case code such as PRICE or DAMAGE.";
  if (!DECIMAL.test(v.disputedAmount.trim()) || /^0+(\.0+)?$/.test(v.disputedAmount.trim())) {
    errors.disputedAmount = "Enter an amount above zero, such as 500.00.";
  }
  const owner = v.owner.trim();
  if (owner === "" || owner.length > 120) errors.owner = "Enter who owns the dispute (up to 120 characters).";
  return errors;
}

function toDisputeInput(v: DisputeDraft): DisputeInput {
  return {
    legalEntityId: v.legalEntityId.trim(),
    openItemId: v.openItemId.trim(),
    reasonCode: v.reasonCode.trim(),
    disputedAmount: v.disputedAmount.trim(),
    owner: v.owner.trim(),
  };
}

/** Open a dispute on an open item. The open items page links here with the item and entity filled in. */
export function NewDisputePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<DisputeDraft>({
    legalEntityId: params.get("legalEntity") ?? "",
    openItemId: params.get("openItem") ?? "",
    reasonCode: "",
    disputedAmount: params.get("amount") ?? "",
    owner: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<DisputeDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateDispute(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await openDispute(toDisputeInput(values), key.current);
      navigate(`/receivables/disputes/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (!keepKeyAfter(error)) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: keyof DisputeDraft, label: string, opts: { entity?: LookupEntityKey; hint?: string; mono?: boolean; decimal?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} error={errors[field]}>
        {(c) =>
        opts.entity ? (
          <EntityLookup
            {...c}
            entity={opts.entity}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<DisputeDraft>)}
            invalid={Boolean(errors[field])}
          />
        ) : (
          <Input
            {...c}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<DisputeDraft>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Open dispute" description="The item leaves dunning and interest until the dispute is resolved or rejected. The SLA is 120 hours." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("nd-entity", "legalEntityId", "Legal entity", { entity: "legalEntity", mono: true })}
          {text("nd-item", "openItemId", "Open item id", { mono: true })}
          {text("nd-reason", "reasonCode", "Reason code", { mono: true, hint: "For example PRICE, QUANTITY, DAMAGE or DELIVERY." })}
          {text("nd-amount", "disputedAmount", "Disputed amount", { mono: true, decimal: true, hint: "At most the item's open amount." })}
          {text("nd-owner", "owner", "Owner")}
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not open the dispute" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Open dispute
            </Button>
            <Link className={buttonClassName("ghost")} to="/receivables/disputes">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One dispute and its BR-30.6 moves: review, then resolve as credit, paid or write-off; reject from open or review. */
export function DisputeDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getDispute(id, signal), id);
  const [resolution, setResolution] = useState<Resolution>("CREDIT");
  const [note, setNote] = useState("");
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState<"review" | "resolve" | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Dispute" />
        <LoadFailure title="Could not load the dispute" problem={state.error} retry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Dispute" />
        <span className="visually-hidden">Loading the dispute…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const dispute = state.data;
  const choices: Resolution[] = dispute.status === "OPEN" ? ["REJECTED"] : [...RESOLUTIONS];
  const chosen = choices.includes(resolution) ? resolution : choices[0];

  async function run(kind: "review" | "resolve", action: () => Promise<Dispute>, message: string) {
    setBusy(kind);
    setFailure(null);
    setDone(null);
    try {
      await action();
      setDone(message);
      setNote("");
      state.reload();
    } catch (error) {
      setFailure({
        title: isStale(error) ? "The dispute changed since you opened it. Reload it and try again." : "Could not update the dispute",
        problem: describeProblem(error),
      });
    } finally {
      setBusy(null);
    }
  }

  function resolve(event: FormEvent) {
    event.preventDefault();
    if (!chosen) return;
    const text = note.trim();
    void run("resolve", () => resolveDispute(dispute.id, dispute.version, chosen, text === "" ? null : text), RESOLVED_MESSAGES[chosen]);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Dispute on ${dispute.documentNumber}`}
        description="While the dispute is open or under review, dunning and interest skip this item."
        meta={statusBadge(dispute.status)}
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/disputes">
            All disputes
          </Link>
        }
      />
      <Card title="Dispute" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Disputed amount" mono>{money(dispute.disputedAmount, dispute.currency)}</Detail>
          <Detail term="Reason" mono>{dispute.reasonCode}</Detail>
          <Detail term="Owner">{dispute.owner}</Detail>
          <Detail term="SLA due" mono>{dateTime(dispute.slaDue)}</Detail>
          <Detail term="Opened" mono>{dateTime(dispute.createdAt)}</Detail>
          <Detail term="Resolved" mono>{dateTime(dispute.resolvedAt)}</Detail>
          <Detail term="Customer" mono>{dispute.customerId}</Detail>
          <Detail term="Note">{dispute.resolutionNote ?? "None"}</Detail>
        </dl>
      </Card>
      {done ? (
        <Alert tone="success" title="Dispute updated" live="status">
          {done}
        </Alert>
      ) : null}
      {failure ? <CommandFailure title={failure.title} problem={failure.problem} /> : null}
      {ACTIVE.has(dispute.status) ? (
        <Card title="Work the dispute" headingLevel={3}>
          <div className={styles.stack}>
            {dispute.status === "OPEN" ? (
              <div className={styles.actions}>
                <Button variant="primary" loading={busy === "review"} onClick={() => void run("review", () => reviewDispute(dispute.id, dispute.version), "The dispute is under review.")}>
                  Start review
                </Button>
                <p className={styles.note}>Credit, paid and write-off resolutions open once the dispute is under review.</p>
              </div>
            ) : null}
            <form className={styles.form} onSubmit={resolve} noValidate>
              <FormField id="dd-resolution" label="Resolution">
                {(c) => (
                  <Select {...c} value={chosen} onChange={(e) => setResolution(e.target.value as Resolution)}>
                    {choices.map((r) => (
                      <option key={r} value={r}>
                        {RESOLUTION_LABELS[r]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="dd-note" label="Note" optional hint="For example the credit memo number.">
                {(c) => <Textarea {...c} value={note} maxLength={2000} onChange={(e) => setNote(e.target.value)} />}
              </FormField>
              <div className={styles.formActions}>
                <Button type="submit" variant={chosen === "REJECTED" ? "danger" : "primary"} loading={busy === "resolve"}>
                  {chosen === "REJECTED" ? "Reject dispute" : "Resolve dispute"}
                </Button>
              </div>
            </form>
          </div>
        </Card>
      ) : (
        <p className={styles.note}>This dispute is closed. The item is back in dunning and interest if it is still open.</p>
      )}
    </div>
  );
}
