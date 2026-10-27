import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  attributeFund,
  closeFund,
  describeProblem,
  FUND_SOURCES,
  getFundUsage,
  newKey,
  PricingApiError,
  reopenFund,
  reviseFund,
  type Fund,
  type FundAttributionInput,
  type FundPosition,
  type FundSource,
  type FundUsageLine,
  type Problem,
} from "./api/client";
import { DECIMAL } from "./format";
import { describeScope, FUND_SOURCE_LABELS, PERIOD } from "./fundFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

export const emptyAttribution: FundAttributionInput = { period: "", source: "CLAIM", reference: "", amount: "" };

/** Client checks that mirror the service (BR-26.15); the service has the last word. */
export function validateAttribution(v: FundAttributionInput): Errors {
  const errors: Errors = {};
  if (!PERIOD.test(v.period.trim())) errors.period = "Enter the fiscal period as YYYY-MM.";
  if (v.reference.trim() === "" || v.reference.trim().length > 128) errors.reference = "Enter the accrual, claim or deduction reference (up to 128 characters).";
  if (!DECIMAL.test(v.amount.trim()) || Number(v.amount) <= 0) errors.amount = "Enter an amount above zero.";
  return errors;
}

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const periodColumns: DataTableColumn<FundPosition>[] = [
  { key: "period", header: "Period", mono: true },
  { key: "budget", header: "Budget", mono: true, align: "end" },
  { key: "used", header: "Used", mono: true, align: "end" },
  { key: "remaining", header: "Remaining", mono: true, align: "end" },
];

const usageColumns: DataTableColumn<FundUsageLine>[] = [
  { key: "period", header: "Period", mono: true },
  { key: "source", header: "Source", render: (row) => FUND_SOURCE_LABELS[row.source] ?? row.source },
  { key: "amount", header: "Amount", mono: true, align: "end" },
];

/** One trade fund: budget, usage by period and source (Fund usage inquiry), attribution, budget revision, close and reopen. */
export function FundDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getFundUsage(id, signal), id);
  const [changed, setChanged] = useState<Fund | null>(null);
  const [draft, setDraft] = useState<FundAttributionInput>(emptyAttribution);
  const [errors, setErrors] = useState<Errors>({});
  const [budget, setBudget] = useState<{ budgetPerPeriod: string; allowOverspend: boolean } | null>(null);
  const [budgetError, setBudgetError] = useState("");
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState<"attribute" | "revise" | "close" | "reopen" | null>(null);
  const attributeKey = useRef(newKey());
  const commandKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Trade fund" />
        <Alert tone="danger" title="Could not load the fund" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Trade fund" />
        <span className="visually-hidden">Loading the fund…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const usage = state.data;
  // A fund returned by a command belongs to this id only and is newer than the loaded one.
  const fund = changed && changed.id === id && changed.version >= usage.fund.version ? changed : usage.fund;
  const budgetDraft = budget ?? { budgetPerPeriod: fund.budgetPerPeriod, allowOverspend: fund.allowOverspend };

  function refused(title: string, error: unknown, key: { current: string }) {
    setFailure({ title, problem: describeProblem(error) });
    // A refused command is final; only a network or server failure may be retried with the same key.
    if (!(error instanceof PricingApiError) || error.status < 500) key.current = newKey();
  }

  async function attribute(event: FormEvent) {
    event.preventDefault();
    const found = validateAttribution(draft);
    setErrors(found);
    setFailure(null);
    setDone("");
    if (Object.keys(found).length > 0) return;
    setBusy("attribute");
    try {
      const pos = await attributeFund(fund.id, { ...draft, period: draft.period.trim(), reference: draft.reference.trim(), amount: draft.amount.trim() }, attributeKey.current);
      setDone(`Recorded. ${pos.period}: ${pos.used} used of ${pos.budget} ${fund.currency}, ${pos.remaining} remaining.`);
      setDraft({ ...emptyAttribution, period: draft.period, source: draft.source });
      attributeKey.current = newKey();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) {
        setErrors(problem.fields);
        attributeKey.current = newKey();
      } else {
        refused(problem.code === "PRC-0012" ? "Over the period budget" : problem.code === "PRC-0011" ? "The fund is closed" : "Could not record the usage", error, attributeKey);
      }
    } finally {
      setBusy(null);
    }
  }

  async function revise(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setDone("");
    if (!DECIMAL.test(budgetDraft.budgetPerPeriod.trim())) {
      setBudgetError("Enter the budget per period as a number such as 25000.");
      return;
    }
    setBudgetError("");
    setBusy("revise");
    try {
      const next = await reviseFund(fund.id, { version: fund.version, budgetPerPeriod: budgetDraft.budgetPerPeriod.trim(), allowOverspend: budgetDraft.allowOverspend }, commandKey.current);
      setChanged(next);
      setBudget(null);
      setDone(`Budget per period is now ${next.budgetPerPeriod} ${next.currency}; overspend ${next.allowOverspend ? "allowed" : "blocked"}.`);
      commandKey.current = newKey();
      state.reload();
    } catch (error) {
      refused(describeProblem(error).code === "PRC-0012" ? "The budget is below a period's usage" : "Could not change the budget", error, commandKey);
    } finally {
      setBusy(null);
    }
  }

  async function setClosed(close: boolean) {
    setFailure(null);
    setDone("");
    setBusy(close ? "close" : "reopen");
    try {
      const next = close ? await closeFund(fund.id, fund.version, commandKey.current) : await reopenFund(fund.id, fund.version, commandKey.current);
      setChanged(next);
      setDone(close ? "The fund is closed. Its usage is frozen and new usage is refused." : "The fund is open again and takes new usage.");
      commandKey.current = newKey();
    } catch (error) {
      refused(close ? "Could not close the fund" : "Could not reopen the fund", error, commandKey);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={fund.name}
        description={`${fund.type} · ${fund.currency} · version ${fund.version}`}
        meta={<StatusPill status={fund.closed ? "closed" : "open"} />}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/pricing/promotions/new">
              Plan a promotion
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/funds">
              All funds
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.title} live="alert">
          {failure.problem.message} ({failure.problem.code})
        </Alert>
      ) : null}
      <Card title="Fund">
        <dl className={styles.details}>
          <Detail term="Budget per period" mono>{`${fund.budgetPerPeriod} ${fund.currency}`}</Detail>
          <Detail term="Overspend">{fund.allowOverspend ? "Allowed" : "Blocked"}</Detail>
          <Detail term="Customers">{describeScope(fund.customerScope)}</Detail>
          <Detail term="Owner">{fund.owner}</Detail>
          <Detail term="Legal entity" mono>
            {fund.legalEntityId}
          </Detail>
          <Detail term="Fund id" mono>
            {fund.id}
          </Detail>
        </dl>
      </Card>
      <Card title="Usage by period">
        <DataTable
          label="Usage by period"
          columns={periodColumns}
          rows={usage.periods}
          getRowKey={(row) => row.period}
          empty={{ title: "No usage yet", description: "Usage appears here per period once accruals, claims or deductions are attributed." }}
        />
      </Card>
      <Card title="Usage by source">
        <DataTable label="Usage by source" columns={usageColumns} rows={usage.items} getRowKey={(row) => `${row.period}|${row.source}`} empty={{ title: "No usage yet" }} />
      </Card>
      {fund.closed ? null : (
        <Card title="Attribute usage">
          <form className={styles.form} onSubmit={attribute} noValidate>
            <FormField id="fu-period" label="Period" hint="Fiscal period as YYYY-MM." error={errors.period}>
              {(c) => <Input {...c} className={styles.mono} placeholder="2027-03" value={draft.period} onChange={(e) => setDraft({ ...draft, period: e.target.value })} invalid={Boolean(errors.period)} />}
            </FormField>
            <FormField id="fu-source" label="Source" error={errors.source}>
              {(c) => (
                <Select {...c} value={draft.source} onChange={(e) => setDraft({ ...draft, source: e.target.value as FundSource })}>
                  {FUND_SOURCES.map((s) => (
                    <option key={s} value={s}>
                      {FUND_SOURCE_LABELS[s]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="fu-reference" label="Reference" hint="The accrual, claim or deduction this usage comes from." error={errors.reference}>
              {(c) => <Input {...c} className={styles.mono} value={draft.reference} onChange={(e) => setDraft({ ...draft, reference: e.target.value })} invalid={Boolean(errors.reference)} />}
            </FormField>
            <FormField id="fu-amount" label={`Amount (${fund.currency})`} error={errors.amount}>
              {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={draft.amount} onChange={(e) => setDraft({ ...draft, amount: e.target.value })} invalid={Boolean(errors.amount)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "attribute"}>
                Record usage
              </Button>
            </div>
          </form>
        </Card>
      )}
      {fund.closed ? null : (
        <Card title="Budget">
          <form className={styles.form} onSubmit={revise} noValidate>
            <FormField id="fu-budget" label="Budget per period" error={budgetError}>
              {(c) => (
                <Input
                  {...c}
                  inputMode="decimal"
                  className={styles.mono}
                  value={budgetDraft.budgetPerPeriod}
                  onChange={(e) => setBudget({ ...budgetDraft, budgetPerPeriod: e.target.value })}
                  invalid={Boolean(budgetError)}
                />
              )}
            </FormField>
            <div className={styles.formActions}>
              <span className={styles.check}>
                <Input id="fu-overspend" type="checkbox" checked={budgetDraft.allowOverspend} onChange={(e) => setBudget({ ...budgetDraft, allowOverspend: e.target.checked })} />
                <label htmlFor="fu-overspend">Allow overspend</label>
              </span>
              <span className={styles.note}>Without overspend the budget must cover the busiest period's usage.</span>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="secondary" loading={busy === "revise"}>
                Save budget
              </Button>
            </div>
          </form>
        </Card>
      )}
      <Card title={fund.closed ? "Reopen" : "Close"}>
        <div className={styles.stack}>
          <p className={styles.note}>
            {fund.closed ? "Reopening lets accruals, claims and deductions draw on the fund again." : "Closing freezes the usage recorded so far; new usage is refused with PRC-0011 until the fund is reopened."}
          </p>
          <div className={styles.actions}>
            {fund.closed ? (
              <Button variant="secondary" loading={busy === "reopen"} onClick={() => setClosed(false)}>
                Reopen fund
              </Button>
            ) : (
              <Button variant="danger" loading={busy === "close"} onClick={() => setClosed(true)}>
                Close fund
              </Button>
            )}
          </div>
        </div>
      </Card>
    </div>
  );
}
