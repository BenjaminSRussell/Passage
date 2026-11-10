import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  applyPayment,
  describeProblem,
  getPayment,
  listOpenItems,
  matchPayment,
  newKey,
  ReceivablesApiError,
  type ApplyResult,
  type MatchCandidate,
  type MatchResult,
  type OpenItem,
  type Payment,
  type Problem,
  type SettlementInput,
} from "./api/client";
import { compareDecimals, DECIMAL, DECISION_LABELS, MODE_LABELS, money, STATUS_LABELS, STATUS_TONES, subtract, sumDecimals } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const APPLIABLE = new Set(["RECEIVED", "SUGGESTED", "ON_ACCOUNT"]);

interface Line {
  amount: string;
  cashDiscount: string;
  writeOff: string;
}

type Plan = Record<string, Line>;

interface Loaded {
  payment: Payment;
  items: OpenItem[];
}

const emptyLine: Line = { amount: "", cashDiscount: "", writeOff: "" };

/**
 * The plan a matcher candidate proposes: each item settled in full, taking the cash discount when R3 scored it.
 * A single item the payment does not cover (a short pay, R7, or a partial payment naming the invoice) is settled
 * with what the payment has, leaving the rest of the item open for a deduction or a later payment.
 */
export function planFromCandidate(candidate: MatchCandidate, items: OpenItem[], unapplied?: string): Plan {
  const plan: Plan = {};
  const single = candidate.openItemIds.length === 1;
  for (const id of candidate.openItemIds) {
    const item = items.find((i) => i.id === id);
    if (!item) continue;
    const discounted = candidate.rules.includes("R3") && single;
    let line: Line = discounted
      ? { amount: subtract(item.openAmount, item.cashDiscountAmount), cashDiscount: item.cashDiscountAmount, writeOff: "" }
      : { amount: item.openAmount, cashDiscount: "", writeOff: "" };
    if (single && unapplied !== undefined && compareDecimals(line.amount, unapplied) > 0) line = { amount: unapplied, cashDiscount: "", writeOff: "" };
    plan[id] = line;
  }
  return plan;
}

/** Settlements to send and the cash left on account, or the reason the plan cannot be sent. */
export function buildApply(plan: Plan, unapplied: string): { settlements: SettlementInput[]; onAccount: string } | { error: string } {
  const settlements: SettlementInput[] = [];
  for (const [openItemId, line] of Object.entries(plan)) {
    const parts = [line.amount, line.cashDiscount, line.writeOff].map((v) => v.trim());
    if (parts.every((v) => v === "")) continue;
    if (parts.some((v) => v !== "" && !DECIMAL.test(v))) return { error: "Amounts must be numbers such as 980.00." };
    const [amount, cashDiscount, writeOff] = parts.map((v) => (v === "" ? "0" : v));
    if (compareDecimals(sumDecimals([amount, cashDiscount, writeOff]), "0") === 0) continue;
    settlements.push({ openItemId, amount, cashDiscount, writeOff });
  }
  const onAccount = subtract(unapplied, sumDecimals(settlements.map((s) => s.amount)));
  if (compareDecimals(onAccount, "0") < 0) return { error: `The settlements exceed the ${unapplied} still unapplied.` };
  return { settlements, onAccount };
}

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** One payment (FR-30.2) and its cash application: scored candidates (BR-30.1) and the settlement plan (BR-30.4). */
export function PaymentDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<Loaded>(async (signal) => {
    const payment = await getPayment(id, signal);
    const items = await listOpenItems({ customer: payment.customerId ?? "", status: "open" }, signal);
    // A payment applies to debits only: credit-side items (credit notes, unapplied
    // payments) are refused by the service (AR-CREDIT-ITEM).
    return {
      payment,
      items: items.filter((i) => i.side !== "CREDIT" && i.currency === payment.currency && i.legalEntityId === payment.legalEntityId),
    };
  }, id);
  const [plan, setPlan] = useState<Plan>({});
  const [match, setMatch] = useState<MatchResult | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [result, setResult] = useState<ApplyResult | null>(null);
  const [busy, setBusy] = useState<"match" | "apply" | null>(null);
  const applyKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Payment" />
        <Alert tone="danger" title="Could not load the payment" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Payment" />
        <span className="visually-hidden">Loading the payment…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { payment, items } = state.data;
  const appliable = APPLIABLE.has(payment.status) && compareDecimals(payment.unappliedAmount, "0") > 0;
  const built = buildApply(plan, payment.unappliedAmount);
  // An ON_ACCOUNT payment is already on account; applying it again needs items.
  const idle = !("error" in built) && built.settlements.length === 0 && payment.status === "ON_ACCOUNT";
  const docOf = (itemId: string) => items.find((i) => i.id === itemId)?.documentNumber ?? itemId;

  function setLine(itemId: string, patch: Partial<Line>) {
    setPlan((prev) => ({ ...prev, [itemId]: { ...(prev[itemId] ?? emptyLine), ...patch } }));
    applyKey.current = newKey();
  }

  async function findMatches() {
    setBusy("match");
    setFailure(null);
    try {
      setMatch(await matchPayment(payment.id));
    } catch (error) {
      setFailure({ title: "Could not score the candidates", problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  async function apply(event: FormEvent) {
    event.preventDefault();
    if ("error" in built || idle) return;
    setBusy("apply");
    setFailure(null);
    try {
      const out = await applyPayment(payment.id, payment.version, built, applyKey.current);
      setResult(out);
      setPlan({});
      setMatch(null);
      applyKey.current = newKey();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      const stale = error instanceof ReceivablesApiError && error.status === 412;
      setFailure({ title: stale ? "The payment changed since you opened it. Reload it and apply again." : "Could not apply the payment", problem });
      if (!(error instanceof ReceivablesApiError) || error.status < 500) applyKey.current = newKey();
    } finally {
      setBusy(null);
    }
  }

  const candidateColumns: DataTableColumn<MatchCandidate>[] = [
    { key: "openItemIds", header: "Documents", mono: true, render: (row) => row.openItemIds.map(docOf).join(", ") },
    { key: "score", header: "Score", align: "end", mono: true },
    { key: "rules", header: "Rules", mono: true, render: (row) => row.rules.join(" ") },
    {
      key: "use",
      header: "Plan",
      render: (row) => (
        <Button size="sm" onClick={() => setPlan(planFromCandidate(row, items, payment.unappliedAmount))}>
          Use {row.openItemIds.map(docOf).join(", ")}
        </Button>
      ),
    },
  ];

  const itemColumns: DataTableColumn<OpenItem>[] = [
    { key: "documentNumber", header: "Document", mono: true },
    { key: "dueDate", header: "Due", mono: true },
    { key: "openAmount", header: "Open", mono: true, align: "end", render: (row) => money(row.openAmount, row.currency) },
    {
      key: "discount",
      header: "Discount available",
      mono: true,
      align: "end",
      render: (row) => (row.cashDiscountDate ? `${money(row.cashDiscountAmount, row.currency)} by ${row.cashDiscountDate}` : "None"),
    },
    ...(["amount", "cashDiscount", "writeOff"] as const).map(
      (field): DataTableColumn<OpenItem> => ({
        key: field,
        header: field === "amount" ? "Apply" : field === "cashDiscount" ? "Cash discount" : "Write-off",
        render: (row) => (
          <Input
            aria-label={`${field === "amount" ? "Apply" : field === "cashDiscount" ? "Cash discount" : "Write-off"} for ${row.documentNumber}`}
            className={styles.mono}
            inputMode="decimal"
            value={plan[row.id]?.[field] ?? ""}
            onChange={(e) => setLine(row.id, { [field]: e.target.value })}
          />
        ),
      }),
    ),
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Payment ${payment.sourceRef ?? payment.receivedDate}`}
        description="Score the open items against this payment, choose what it settles and apply it. Whatever is not settled stays on account."
        meta={<Badge tone={STATUS_TONES[payment.status] ?? "neutral"}>{STATUS_LABELS[payment.status] ?? payment.status}</Badge>}
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/payments">
            All payments
          </Link>
        }
      />
      <Card title="Payment" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Amount" mono>{money(payment.amount, payment.currency)}</Detail>
          <Detail term="Unapplied" mono>{money(payment.unappliedAmount, payment.currency)}</Detail>
          <Detail term="Mode">{MODE_LABELS[payment.paymentMode] ?? payment.paymentMode}</Detail>
          <Detail term="Received" mono>{payment.receivedDate}</Detail>
          <Detail term="Value date" mono>{payment.valueDate}</Detail>
          <Detail term="Customer" mono>{payment.customerId ?? "Unidentified"}</Detail>
          <Detail term="Payer">{payment.payerName ?? "Not given"}</Detail>
          <Detail term="Payer account">{payment.payerAccountHash ? "Given (kept as a hash)" : "Not given"}</Detail>
          <Detail term="Short-pay reason">{payment.shortPayReason ?? "None"}</Detail>
          <Detail term="Remittance" mono>{payment.remittance.length > 0 ? payment.remittance.map((r) => r.reference).join(", ") : "None"}</Detail>
        </dl>
      </Card>
      {result ? (
        <Alert tone="success" title="Payment applied" live="status">
          {result.settlements.length} item(s) settled; {money(result.payment.unappliedAmount, result.payment.currency)} on account. Status:{" "}
          {STATUS_LABELS[result.payment.status] ?? result.payment.status}.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title={failure.title} live="alert">
          <ProblemMessage problem={failure.problem} />
        </Alert>
      ) : null}
      {appliable ? (
        <>
          <Card
            title="Match candidates"
            headingLevel={3}
            actions={
              <Button size="sm" variant="secondary" loading={busy === "match"} onClick={findMatches}>
                Find matches
              </Button>
            }
          >
            {match ? (
              <div className={styles.stack}>
                <p className={styles.note}>{DECISION_LABELS[match.decision] ?? match.decision}</p>
                <DataTable
                  label="Match candidates"
                  columns={candidateColumns}
                  rows={match.candidates}
                  getRowKey={(row) => row.openItemIds.join(",")}
                  empty={{ title: "No candidates", description: "No open item matches by reference, amount or discount. Apply by hand or leave the payment on account." }}
                />
              </div>
            ) : (
              <p className={styles.note}>The matcher scores reference, amount and discount (R1, R2, R3), a payer account or name seen before for the customer (R4, R5), due date (R6) and a short payment with a reason (R7). Nothing changes until you apply.</p>
            )}
          </Card>
          <Card title="Apply" headingLevel={3}>
            <form className={styles.stack} onSubmit={apply} noValidate>
              <DataTable
                label="Open items to settle"
                columns={itemColumns}
                rows={items}
                getRowKey={(row) => row.id}
                empty={{ title: "No open items", description: `There are no open ${payment.currency} items for this customer. Apply leaves the whole payment on account.` }}
              />
              {"error" in built ? (
                <p className={`${styles.note} ${styles.warn}`}>{built.error}</p>
              ) : (
                <p className={styles.note}>
                  {built.settlements.length} item(s) selected; {money(built.onAccount, payment.currency)} goes on account.
                </p>
              )}
              <div className={styles.actions}>
                <Button type="submit" variant="primary" loading={busy === "apply"} disabled={"error" in built || idle}>
                  Apply payment
                </Button>
                <Button variant="ghost" onClick={() => setPlan({})}>
                  Clear plan
                </Button>
              </div>
            </form>
          </Card>
        </>
      ) : (
        <p className={styles.note}>This payment is {STATUS_LABELS[payment.status]?.toLowerCase() ?? payment.status} with nothing left to apply.</p>
      )}
    </div>
  );
}
