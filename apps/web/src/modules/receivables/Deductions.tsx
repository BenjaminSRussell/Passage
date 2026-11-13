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
  ListPage,
  type ListColumn,
  type ListData,
  type ListFilterDef,
  type ListQuery,
} from "../../shell/ui";
import {
  CLAIM_TYPES,
  createDeduction,
  DEDUCTION_STATUSES,
  DEDUCTION_TYPES,
  DENIAL_REASONS,
  describeProblem,
  getDeduction,
  listDeductions,
  moveDeduction,
  newKey,
  type ClaimType,
  type Deduction,
  type DeductionAction,
  type DeductionInput,
  type DeductionStatus,
  type DeductionType,
  type DenialReason,
  type Problem,
} from "./api/client";
import {
  ageDays,
  CLAIM_TYPE_LABELS,
  compareDecimals,
  dateTime,
  DECIMAL,
  DEDUCTION_LABELS,
  DEDUCTION_TONES,
  DEDUCTION_TYPE_LABELS,
  DENIAL_LABELS,
  money,
  shortId,
  sumDecimals,
  UUID,
} from "./format";
import { CommandFailure, Detail, isStale, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useCappedRows, useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";

const PAGE = 500;
const ZERO = /^0+(\.0+)?$/;

/** deduction_write_off_limit defaults (BR-30.12); other currencies always need receivables.write-off.approve. */
const WRITE_OFF_LIMIT: Record<string, string> = { USD: "50.00", EUR: "45.00" };

function statusBadge(status: DeductionStatus) {
  return <Badge tone={DEDUCTION_TONES[status] ?? "neutral"}>{DEDUCTION_LABELS[status] ?? status}</Badge>;
}

function worked(d: Deduction): boolean {
  return d.status === "OPEN" || d.status === "MATCHED";
}

const columns: ListColumn<Deduction>[] = [
  { key: "documentNumber", header: "Item", mono: true },
  { key: "type", header: "Type", render: (row) => DEDUCTION_TYPE_LABELS[row.type] ?? row.type },
  { key: "customerId", header: "Customer", render: (row) => <EntityName entity="customer" id={row.customerId} /> },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "createdAt", header: "Age (days)", mono: true, align: "end", render: (row) => (worked(row) ? ageDays(row.createdAt) : "Closed") },
  { key: "claimRef", header: "Claim", mono: true, render: (row) => row.claimRef ?? "None" },
  { key: "status", header: "Status", render: (row) => statusBadge(row.status) },
];

const deductionFilters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: DEDUCTION_STATUSES.map((s) => ({ value: s, label: DEDUCTION_LABELS[s] })) },
  { kind: "select", key: "type", label: "Type", anyLabel: "All types", options: DEDUCTION_TYPES.map((t) => ({ value: t, label: DEDUCTION_TYPE_LABELS[t] })) },
];

function useDeductionRows(query: ListQuery, onCapped: (capped: boolean) => void): ListData<Deduction> {
  const status = (query.filters.status ?? "") as DeductionStatus | "";
  const type = (query.filters.type ?? "") as DeductionType | "";
  return useCappedRows((signal) => listDeductions({ status, type, limit: PAGE }, signal), `${status}|${type}`, PAGE, onCapped);
}

/** The deductions workbench (FR-30.17): deductions being worked first, oldest first. */
export function DeductionsPage() {
  const navigate = useNavigate();
  const [capped, setCapped] = useState(false);
  return (
    <ListPage
      id="receivables-deductions"
      title="Deductions"
      description="Short-paid amounts moved off their invoices. Match each to a claim and approve it, deny and re-bill it, split it, or write it off. Deductions are aged here, apart from invoices, and never dunned."
      actions={
        <Link className={buttonClassName("primary")} to="/receivables/deductions/new">
          Record deduction
        </Link>
      }
      noun="deductions"
      singular="deduction"
      columns={columns}
      filters={deductionFilters}
      useRows={(query) => useDeductionRows(query, setCapped)}
      search={(row) => ({ number: row.documentNumber, name: row.claimRef ?? undefined })}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/receivables/deductions/${row.id}`)}
      empty={{ title: "No deductions", description: "Record a deduction from a short-paid open item." }}
      footer={capped ? <p className={styles.note}>Showing the first {PAGE} deductions. Narrow the filters to see the others.</p> : null}
    />
  );
}

type Errors = Record<string, string>;

export interface DeductionDraft {
  legalEntityId: string;
  openItemId: string;
  paymentId: string;
  type: DeductionType;
  reasonCode: string;
  amount: string;
  /** The item's open amount when the page was opened from it; bounds the amount client-side. */
  openAmount: string;
}

/** Client checks that mirror the service validation; the service has the last word. */
export function validateDeduction(v: DeductionDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (!UUID.test(v.openItemId.trim())) errors.openItemId = "Enter the short-paid open item id (a UUID).";
  const payment = v.paymentId.trim();
  if (payment !== "" && !UUID.test(payment)) errors.paymentId = "Enter the payment id (a UUID) or leave it empty.";
  const reason = v.reasonCode.trim();
  if (reason === "" || reason.length > 60) errors.reasonCode = "Enter the customer's reason, up to 60 characters.";
  const amount = v.amount.trim();
  if (!DECIMAL.test(amount) || ZERO.test(amount)) errors.amount = "Enter an amount above zero, such as 300.00.";
  else if (DECIMAL.test(v.openAmount) && compareDecimals(amount, v.openAmount) > 0) errors.amount = `At most ${v.openAmount} is open on the item.`;
  return errors;
}

export function toDeductionInput(v: DeductionDraft): DeductionInput {
  const payment = v.paymentId.trim();
  return {
    legalEntityId: v.legalEntityId.trim(),
    openItemId: v.openItemId.trim(),
    paymentId: payment === "" ? null : payment,
    type: v.type,
    reasonCode: v.reasonCode.trim(),
    amount: v.amount.trim(),
  };
}

/** Record a deduction: the amount moves off the short-paid item into the workbench. */
export function NewDeductionPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<DeductionDraft>({
    legalEntityId: params.get("legalEntity") ?? "",
    openItemId: params.get("openItem") ?? "",
    paymentId: params.get("payment") ?? "",
    type: "UNKNOWN",
    reasonCode: "",
    amount: params.get("amount") ?? "",
    openAmount: params.get("amount") ?? "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<DeductionDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateDeduction(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createDeduction(toDeductionInput(values), key.current);
      navigate(`/receivables/deductions/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (!keepKeyAfter(error)) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: "legalEntityId" | "openItemId" | "paymentId" | "reasonCode" | "amount", label: string, opts: { entity?: LookupEntityKey; hint?: string; optional?: boolean; mono?: boolean; decimal?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} error={errors[field]} optional={opts.optional}>
        {(c) =>
        opts.entity ? (
          <EntityLookup
            {...c}
            entity={opts.entity}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<DeductionDraft>)}
            invalid={Boolean(errors[field])}
          />
        ) : (
          <Input
            {...c}
            className={opts.mono === false ? undefined : styles.mono}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<DeductionDraft>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Record deduction"
        description="The amount leaves the open item, so it is no longer dunned, and waits here to be matched to a claim, denied, split or written off."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("nde-entity", "legalEntityId", "Legal entity", { entity: "legalEntity" })}
          {text("nde-item", "openItemId", "Open item", { entity: "openItem", hint: "The short-paid invoice; the deduction takes its customer and currency." })}
          {text("nde-payment", "paymentId", "Payment", { entity: "arPayment", optional: true, hint: "The short payment, if it is recorded." })}
          <FormField id="nde-type" label="Type">
            {(c) => (
              <Select {...c} value={values.type} onChange={(e) => update({ type: e.target.value as DeductionType })}>
                {DEDUCTION_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {DEDUCTION_TYPE_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("nde-reason", "reasonCode", "Customer's reason", { mono: false, hint: "As stated on the remittance, such as a promotion number." })}
          {text("nde-amount", "amount", "Amount", { decimal: true, hint: values.openAmount ? `${values.openAmount} is open on the item.` : undefined })}
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not record the deduction" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record deduction
            </Button>
            <Link className={buttonClassName("ghost")} to="/receivables/deductions">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** Checks the match form: the whole deduction must fit the claim's remaining amount (AR-0014). */
export function validateMatch(claimRef: string, claimRemaining: string, amount: string): Errors {
  const errors: Errors = {};
  const ref = claimRef.trim();
  if (ref === "" || ref.length > 200) errors.claimRef = "Enter the claim reference.";
  const remaining = claimRemaining.trim();
  if (!DECIMAL.test(remaining)) errors.claimRemaining = "Enter the claim's remaining amount, such as 300.00.";
  else if (compareDecimals(amount, remaining) > 0) errors.claimRemaining = `The claim does not cover ${amount}; split the deduction first.`;
  return errors;
}

/** Splits the comma-separated part amounts and checks they sum to the deduction (BR-30.12). */
export function parseParts(text: string, amount: string): { parts: string[]; error?: string } {
  const parts = text
    .split(",")
    .map((p) => p.trim())
    .filter((p) => p !== "");
  if (parts.length < 2 || parts.length > 20) return { parts, error: "Enter 2 to 20 amounts separated by commas." };
  if (parts.some((p) => !DECIMAL.test(p) || ZERO.test(p))) return { parts, error: "Every part must be an amount above zero." };
  const sum = sumDecimals(parts);
  if (compareDecimals(sum, amount) !== 0) return { parts, error: `The parts sum to ${sum}; they must sum to ${amount}.` };
  return { parts };
}

/** Whether a write-off of amount needs receivables.write-off.approve. */
export function needsApproval(amount: string, currency: string): boolean {
  const limit = WRITE_OFF_LIMIT[currency];
  return limit === undefined || compareDecimals(amount, limit) > 0;
}

type Busy = DeductionAction["action"] | null;

const DONE: Record<DeductionAction["action"], string> = {
  match: "Matched to the claim. Approve it to have billing credit the claim.",
  approve: "Approved. Billing issues the credit note against the claim.",
  deny: "Denied. The amount is re-billed to the customer as a new open item.",
  split: "Split. Work each part on its own.",
  "write-off": "Written off to 6110.",
};

const childColumns: DataTableColumn<Deduction>[] = [
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "status", header: "Status", render: (row) => statusBadge(row.status) },
  { key: "claimRef", header: "Claim", mono: true, render: (row) => row.claimRef ?? "None" },
];

/** One deduction: match, approve, deny, split or write off (BR-30.12). Every move sends If-Match. */
export function DeductionDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getDeduction(id, signal), id);
  const [claimType, setClaimType] = useState<ClaimType>("PROMOTION_FUND");
  const [claimRef, setClaimRef] = useState("");
  const [claimRemaining, setClaimRemaining] = useState("");
  const [reason, setReason] = useState<DenialReason>("NOT_AUTHORIZED");
  const [note, setNote] = useState("");
  const [parts, setParts] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState<Busy>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Deduction" />
        <LoadFailure title="Could not load the deduction" problem={state.error} retry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Deduction" />
        <span className="visually-hidden">Loading the deduction…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const d = state.data;
  const noteText = note.trim();
  const withNote = noteText === "" ? {} : { note: noteText };
  const approval = needsApproval(d.amount, d.currency);

  async function run(command: DeductionAction) {
    setBusy(command.action);
    setFailure(null);
    setDone(null);
    try {
      await moveDeduction(d.id, d.version, command);
      setDone(DONE[command.action]);
      setNote("");
      setParts("");
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure({
        title: isStale(error) ? "The deduction changed since you opened it. Reload it and try again." : "Could not update the deduction",
        problem,
      });
    } finally {
      setBusy(null);
    }
  }

  function match(event: FormEvent) {
    event.preventDefault();
    const found = validateMatch(claimRef, claimRemaining, d.amount);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    void run({ action: "match", claimType, claimRef: claimRef.trim(), claimRemaining: claimRemaining.trim() });
  }

  function split(event: FormEvent) {
    event.preventDefault();
    const parsed = parseParts(parts, d.amount);
    setErrors(parsed.error ? { parts: parsed.error } : {});
    if (parsed.error) return;
    void run({ action: "split", parts: parsed.parts });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Deduction ${money(d.amount, d.currency)}`}
        description={`${DEDUCTION_TYPE_LABELS[d.type]} deduction taken off ${d.documentNumber}.`}
        meta={statusBadge(d.status)}
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/deductions">
            All deductions
          </Link>
        }
      />
      <Card title="Details" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Amount" mono>{money(d.amount, d.currency)}</Detail>
          <Detail term="Open item" mono>{d.documentNumber}</Detail>
          <Detail term="Customer's reason">{d.reasonCode}</Detail>
          <Detail term="Customer"><EntityName entity="customer" id={d.customerId} /></Detail>
          <Detail term="Payment" mono>{d.paymentId ?? "None"}</Detail>
          <Detail term="Recorded" mono>{dateTime(d.createdAt)}</Detail>
          {worked(d) ? <Detail term="Age" mono>{`${ageDays(d.createdAt)} days`}</Detail> : <Detail term="Resolved" mono>{dateTime(d.resolvedAt)}</Detail>}
          {d.claimRef ? (
            <Detail term="Claim" mono>
              {`${d.claimType ? CLAIM_TYPE_LABELS[d.claimType] : ""} ${d.claimRef}, ${money(d.claimRemaining ?? "0", d.currency)} remaining`}
            </Detail>
          ) : null}
          {d.denialReason ? <Detail term="Denial reason">{DENIAL_LABELS[d.denialReason]}</Detail> : null}
          {d.decisionNote ? <Detail term="Note">{d.decisionNote}</Detail> : null}
          {d.rebillItemId ? <Detail term="Re-billed as item" mono>{d.rebillItemId}</Detail> : null}
          {d.parentId ? (
            <Detail term="Split from" mono>
              <Link to={`/receivables/deductions/${d.parentId}`}>{shortId(d.parentId)}</Link>
            </Detail>
          ) : null}
        </dl>
      </Card>
      {done ? (
        <Alert tone="success" title="Deduction updated" live="status">
          {done}
        </Alert>
      ) : null}
      {failure ? <CommandFailure title={failure.title} problem={failure.problem} /> : null}
      {d.status === "OPEN" ? (
        <Card title="Match to a claim" headingLevel={3}>
          <form className={styles.form} onSubmit={match} noValidate>
            <FormField id="dd-claim-type" label="Claim type">
              {(c) => (
                <Select {...c} value={claimType} onChange={(e) => setClaimType(e.target.value as ClaimType)}>
                  {CLAIM_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {CLAIM_TYPE_LABELS[t]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="dd-claim-ref" label="Claim reference" error={errors.claimRef}>
              {(c) => <Input {...c} className={styles.mono} value={claimRef} onChange={(e) => setClaimRef(e.target.value)} invalid={Boolean(errors.claimRef)} />}
            </FormField>
            <FormField id="dd-claim-remaining" label="Claim remaining" error={errors.claimRemaining} hint="As pricing shows it. The whole deduction must fit, or split it first.">
              {(c) => (
                <Input {...c} className={styles.mono} inputMode="decimal" value={claimRemaining} onChange={(e) => setClaimRemaining(e.target.value)} invalid={Boolean(errors.claimRemaining)} />
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "match"}>
                Match
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {d.status === "OPEN" ? (
        <Card title="Split" headingLevel={3}>
          <form className={styles.form} onSubmit={split} noValidate>
            <FormField id="dd-parts" label="Part amounts" error={errors.parts} hint={`Separate with commas; they must sum to ${d.amount}.`}>
              {(c) => <Input {...c} className={styles.mono} value={parts} onChange={(e) => setParts(e.target.value)} invalid={Boolean(errors.parts)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="secondary" loading={busy === "split"}>
                Split
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {worked(d) ? (
        <Card title="Decide" headingLevel={3}>
          <div className={styles.form}>
            <FormField id="dd-reason" label="Denial reason" hint="Used when you deny.">
              {(c) => (
                <Select {...c} value={reason} onChange={(e) => setReason(e.target.value as DenialReason)}>
                  {DENIAL_REASONS.map((r) => (
                    <option key={r} value={r}>
                      {DENIAL_LABELS[r]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.wide}>
              <FormField id="dd-note" label="Decision note" optional error={errors.note}>
                {(c) => <Textarea {...c} rows={3} value={note} onChange={(e) => setNote(e.target.value)} invalid={Boolean(errors.note)} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              {d.status === "MATCHED" ? (
                <Button variant="primary" loading={busy === "approve"} onClick={() => void run({ action: "approve", ...withNote })}>
                  Approve
                </Button>
              ) : null}
              <Button variant="danger" loading={busy === "deny"} onClick={() => void run({ action: "deny", reason, ...withNote })}>
                Deny and re-bill
              </Button>
              <Button variant="secondary" loading={busy === "write-off"} onClick={() => void run({ action: "write-off", ...withNote })}>
                Write off
              </Button>
            </div>
            <p className={styles.note}>
              {approval
                ? `Writing off ${money(d.amount, d.currency)} is above the deduction limit; it needs the receivables.write-off.approve duty.`
                : `Write-offs up to ${money(WRITE_OFF_LIMIT[d.currency] ?? "0", d.currency)} post Dr 6110 / Cr 1200.`}
            </p>
          </div>
        </Card>
      ) : null}
      {d.children && d.children.length > 0 ? (
        <Card title="Parts" headingLevel={3}>
          <DataTable
            label="Parts"
            columns={childColumns}
            rows={d.children}
            getRowKey={(row) => row.id}
            empty={{ title: "No parts", description: "A split deduction lists its parts here." }}
            onRowOpen={(row) => navigate(`/receivables/deductions/${row.id}`)}
          />
        </Card>
      ) : null}
    </div>
  );
}
