import { Fragment, useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  CLAIM_DECISIONS,
  closeSupplierClaim,
  decideSupplierClaim,
  describeProblem,
  getSupplierClaim,
  newKey,
  settleSupplierClaim,
  submitSupplierClaim,
  type ClaimDecision,
  type ClaimDecisionInput,
  type ClaimLine,
  type CreditNoteMatch,
  type Problem,
  type SupplierClaim,
  type Versioned,
} from "./api/client";
import { ClaimStatusBadge } from "./badges";
import { Details } from "./Details";
import { dash, DECIMAL, formatDateTime, money, sentence, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

/** Checks of a vendor decision that mirror the service (domain.Decide): amounts per line for a partial acceptance, a reason for a rejection. */
export function validateDecision(decision: ClaimDecision | "", reason: string, amounts: string[], lines: ClaimLine[]): { errors: Errors; input?: ClaimDecisionInput } {
  const errors: Errors = {};
  if (!decision) errors.decision = "Choose the vendor's decision.";
  if (decision === "REJECTED" && !reason.trim()) errors.reason = "Say why the vendor rejected the claim.";
  if ([...reason.trim()].length > 500) errors.reason = "Use at most 500 characters.";
  if (decision === "PARTIALLY_ACCEPTED") {
    lines.forEach((line, i) => {
      const a = (amounts[i] ?? "").trim();
      if (!DECIMAL.test(a)) errors[`acceptedAmounts[${i}]`] = "Enter the accepted amount.";
      // Compared as decimals: the strings have at most six places, so scaling to integers is exact.
      else if (scaled(a) > scaled(line.claimedAmount)) errors[`acceptedAmounts[${i}]`] = `At most the claimed ${line.claimedAmount}.`;
    });
  }
  if (Object.keys(errors).length > 0 || !decision) return { errors };
  return {
    errors,
    input: {
      decision,
      ...(reason.trim() ? { reason: reason.trim() } : {}),
      ...(decision === "PARTIALLY_ACCEPTED" ? { acceptedAmounts: lines.map((_, i) => amounts[i].trim()) } : {}),
    },
  };
}

/** A non-negative decimal string with up to six places as an exact bigint of millionths. */
function scaled(value: string): bigint {
  const [whole, frac = ""] = value.split(".");
  return BigInt(whole || "0") * 1_000_000n + BigInt((frac + "000000").slice(0, 6));
}

const lineColumns = (currency: string): DataTableColumn<ClaimLine & { id: string }>[] => [
  { key: "sourceId", header: "Work order", mono: true, render: (row) => shortId(row.sourceId) },
  { key: "itemId", header: "Item", mono: true, render: (row) => shortId(row.itemId) },
  { key: "serial", header: "Serial", mono: true, render: (row) => row.serial || dash },
  { key: "failureCode", header: "Failure", mono: true, render: (row) => row.failureCode || dash },
  { key: "partAmount", header: "Part", mono: true, render: (row) => money(row.partAmount, currency) },
  { key: "laborHours", header: "Labor h", mono: true },
  { key: "laborRate", header: "Rate", mono: true },
  { key: "claimedAmount", header: "Claimed", mono: true, render: (row) => money(row.claimedAmount, currency) },
  { key: "acceptedAmount", header: "Accepted", mono: true, render: (row) => money(row.acceptedAmount, currency) },
];

/** One supplier claim: lines, submission, vendor decision, credit-note match and close (FR-45.15, BR-45.12). */
export function SupplierClaimDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getSupplierClaim(id, signal), id);
  const [latest, setLatest] = useState<Versioned<SupplierClaim> | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [confirmClose, setConfirmClose] = useState(false);
  const [submitForm, setSubmitForm] = useState({ vendorReference: "", returnOrderId: "" });
  const [decision, setDecision] = useState<ClaimDecision | "">("");
  const [reason, setReason] = useState("");
  const [amounts, setAmounts] = useState<string[]>([]);
  const [credit, setCredit] = useState({ creditNoteId: "", vendorReference: "", amount: "" });
  // One key per intent: a retry of the same move replays instead of failing on the version.
  const moveKey = useRef(newKey());

  const current = latest && latest.value.id === id && (!state.data || latest.value.version >= state.data.value.version) ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Supplier claim" />
        <Alert tone="danger" title="Could not load the claim" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Supplier claim" />
        <span className="visually-hidden">Loading the claim…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: c, etag } = current;

  async function run(action: () => Promise<Versioned<SupplierClaim>>, done: (next: SupplierClaim) => string) {
    setBusy(true);
    setFailure(null);
    setNotice("");
    try {
      const next = await action();
      moveKey.current = newKey();
      setLatest(next);
      setErrors({});
      setNotice(done(next.value));
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      if (problem.code === "conflict_version" || problem.code === "idempotency_key_reuse") moveKey.current = newKey();
      if (problem.code === "conflict_version") {
        setLatest(null);
        state.reload();
      }
    } finally {
      setBusy(false);
    }
  }

  function submitClaim(event: FormEvent) {
    event.preventDefault();
    if (c.returnRequired && !submitForm.returnOrderId.trim() && !c.returnOrderId) {
      setErrors({ returnOrderId: "The vendor wants the failed part back: enter the return order first." });
      return;
    }
    void run(
      () =>
        submitSupplierClaim(
          c.id,
          etag,
          {
            ...(submitForm.vendorReference.trim() ? { vendorReference: submitForm.vendorReference.trim() } : {}),
            ...(submitForm.returnOrderId.trim() ? { returnOrderId: submitForm.returnOrderId.trim() } : {}),
          },
          moveKey.current,
        ),
      () => "Claim submitted to the vendor.",
    );
  }

  function recordDecision(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateDecision(decision, reason, amounts, c.lines);
    setErrors(found);
    if (!input) return;
    void run(() => decideSupplierClaim(c.id, etag, input, moveKey.current), (n) => `Vendor decision recorded: ${sentence(n.status).toLowerCase()}.`);
  }

  function matchCredit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!credit.creditNoteId.trim()) found.creditNoteId = "Enter the credit note.";
    if (!credit.vendorReference.trim()) found.vendorReference = "Enter the vendor reference the credit note quotes.";
    if (!DECIMAL.test(credit.amount.trim()) || scaled(credit.amount.trim()) === 0n) found.amount = "Enter the credited amount.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const match: CreditNoteMatch = { creditNoteId: credit.creditNoteId.trim(), vendorReference: credit.vendorReference.trim(), amount: credit.amount.trim(), currency: c.currency };
    void run(() => settleSupplierClaim(c.id, etag, match, moveKey.current), (n) => `Claim settled: ${money(n.recoveredAmount, n.currency)} recovered.`);
  }

  const field = (name: string, label: string, value: string, set: (v: string) => void, hint?: string, optional = false) => (
    <FormField id={`claim-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => <Input {...control} className={styles.mono} value={value} onChange={(e) => set(e.target.value)} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Supplier claim ${c.number}`}
        description={`${sentence(c.status)}. Claimed ${money(c.claimedTotal, c.currency)}, accepted ${money(c.acceptedTotal, c.currency)}, recovered ${money(c.recoveredAmount, c.currency)}.`}
        actions={
          c.status === "REJECTED" || c.status === "SETTLED" ? (
            <Button onClick={() => setConfirmClose(true)} loading={busy}>
              Close claim
            </Button>
          ) : undefined
        }
      />
      {notice ? (
        <Alert tone="success" title={notice} live="status">
          <Link to="/service/supplier-claims">Back to the claims</Link>
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title={failure.code === "conflict_version" ? "The claim changed; it was reloaded" : "The action was refused"} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="Claim" headingLevel={3}>
        <Details
          label="Claim"
          items={[
            ["Status", <ClaimStatusBadge key="s" status={c.status} />],
            ["Vendor", c.vendorPartyId, true],
            ["Legal entity", c.legalEntityId, true],
            ["Source", c.sourceKind === "SERVICE_WO" ? "Service work orders" : "EAM work order"],
            ["Completed on", c.completedOn, true],
            ["Submit by", c.windowEndsOn, true],
            ["Return required", c.returnRequired ? `Yes${c.returnOrderId ? ` (${c.returnOrderId})` : ""}` : "No"],
            ["Submitted on", c.submittedOn ?? dash, true],
            ["Vendor reference", c.vendorReference || dash, true],
            ["Decided", formatDateTime(c.decidedAt), true],
            ["Decision reason", c.decisionReason || dash],
            ["Credit note", c.creditNoteId || dash, true],
            ["Settled", formatDateTime(c.settledAt), true],
          ]}
        />
      </Card>
      <Card title="Lines" headingLevel={3}>
        <DataTable
          label="Claim lines"
          columns={lineColumns(c.currency)}
          rows={c.lines.map((l, i) => ({ ...l, id: `${l.sourceId}-${i}` }))}
          getRowKey={(row) => row.id}
          empty={{ title: "No lines" }}
        />
      </Card>
      {c.status === "DRAFT" ? (
        <Card title="Submit to the vendor" headingLevel={3}>
          <form className={styles.form} onSubmit={submitClaim} noValidate aria-label="Submit claim">
            {field("vendorReference", "Vendor reference", submitForm.vendorReference, (v) => setSubmitForm((f) => ({ ...f, vendorReference: v })), "The vendor's claim or RMA number, if known.", true)}
            {field(
              "returnOrderId",
              "Return order",
              submitForm.returnOrderId,
              (v) => setSubmitForm((f) => ({ ...f, returnOrderId: v })),
              "The vendor return of the failed part.",
              !c.returnRequired,
            )}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Submit claim
              </Button>
              <p className={styles.note}>{`The vendor accepts claims until ${c.windowEndsOn}.`}</p>
            </div>
          </form>
        </Card>
      ) : null}
      {c.status === "SUBMITTED" ? (
        <Card title="Vendor decision" headingLevel={3}>
          <form className={styles.form} onSubmit={recordDecision} noValidate aria-label="Vendor decision">
            <FormField id="claim-decision" label="Decision" error={errors.decision}>
              {(control) => (
                <Select {...control} value={decision} onChange={(e) => setDecision(e.target.value as ClaimDecision | "")} invalid={Boolean(errors.decision)}>
                  <option value="">Choose</option>
                  {CLAIM_DECISIONS.map((d) => (
                    <option key={d} value={d}>
                      {sentence(d)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {decision === "PARTIALLY_ACCEPTED"
              ? c.lines.map((line, i) => (
                  <Fragment key={`${line.sourceId}-${i}`}>
                    {field(
                      `acceptedAmounts[${i}]`,
                      `Accepted on line ${i + 1}`,
                      amounts[i] ?? "",
                      (v) => setAmounts((a) => Object.assign([...a], { [i]: v })),
                      `Claimed ${money(line.claimedAmount, c.currency)}.`,
                    )}
                  </Fragment>
                ))
              : null}
            <div className={styles.wide}>
              <FormField id="claim-reason" label="Reason" error={errors.reason} optional={decision !== "REJECTED"}>
                {(control) => <Textarea {...control} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(errors.reason)} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Record decision
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {c.status === "ACCEPTED" || c.status === "PARTIALLY_ACCEPTED" ? (
        <Card title="Match the credit note" headingLevel={3}>
          <form className={styles.form} onSubmit={matchCredit} noValidate aria-label="Match credit note">
            {field("creditNoteId", "Credit note", credit.creditNoteId, (v) => setCredit((f) => ({ ...f, creditNoteId: v })))}
            {field("vendorReference", "Vendor reference", credit.vendorReference, (v) => setCredit((f) => ({ ...f, vendorReference: v })), `The claim's reference: ${c.vendorReference || dash}.`)}
            {field("amount", `Credited amount (${c.currency})`, credit.amount, (v) => setCredit((f) => ({ ...f, amount: v })), `Must equal the accepted ${c.acceptedTotal}.`)}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Settle claim
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {confirmClose ? (
        <ConfirmDialog
          title="Close this claim?"
          description={c.status === "REJECTED" ? "A rejected claim closes with its reason and posts nothing." : "The settled claim is closed; its recovery stays posted."}
          confirmLabel="Close claim"
          cancelLabel="Keep it open"
          onConfirm={() => {
            setConfirmClose(false);
            void run(() => closeSupplierClaim(c.id, etag, moveKey.current), () => "Claim closed.");
          }}
          onCancel={() => setConfirmClose(false)}
        />
      ) : null}
    </div>
  );
}
