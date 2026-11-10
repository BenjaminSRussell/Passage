import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getContract,
  getContractRollForward,
  satisfyMilestone,
  type ContractObligation,
  type Problem,
  type RevenueContract,
  type ScheduleLine,
} from "./api/client";
import { RollForwardTiles } from "./DeferredRevenuePage";
import { BASIS_LABELS, CONTRACT_STATUS_LABELS, DAY, METHOD_LABELS, PERIOD, SCHEDULE_STATUS_LABELS, SSP_SOURCE_LABELS, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const scheduleColumns: DataTableColumn<ScheduleLine>[] = [
  { key: "seq", header: "Line", mono: true, align: "end" },
  { key: "date", header: "Date", mono: true, render: (l) => l.date ?? "On satisfaction" },
  { key: "amount", header: "Amount", mono: true, align: "end" },
  { key: "status", header: "Status", render: (l) => <StatusPill status={SCHEDULE_STATUS_LABELS[l.status] ?? l.status} /> },
  { key: "recognizedOn", header: "Recognized on", mono: true, render: (l) => l.recognizedOn ?? "—" },
];

function obligationTerm(o: ContractObligation): string {
  if (o.method === "RATABLE" && o.termStart && o.termEnd) return `${o.termStart} to ${o.termEnd}`;
  if (o.satisfiedOn) return `Satisfied ${o.satisfiedOn}`;
  if (o.method === "MILESTONE") return "Awaiting milestone";
  return "—";
}

/** Why a satisfaction failed, in words the revenue accountant can act on. */
function failureTitle(code: string): string {
  switch (code) {
    case "REV-0103":
      return "The contract changed since you opened it";
    case "REV-0004":
      return "The milestone is already satisfied";
    case "REV-0006":
      return "The date is before the contract date";
    default:
      return "The milestone was not satisfied";
  }
}

function pendingMilestone(o: ContractObligation): boolean {
  return o.method === "MILESTONE" && o.schedule.some((l) => l.status === "SCHEDULED");
}

/** Revenue contract (§5.29.6): allocation per obligation, schedules and milestone satisfaction. */
export function RevenueContractDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getContract(id, signal), id);
  const [dates, setDates] = useState<Record<string, string>>({});
  const [dateErrors, setDateErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  // The contract's roll-forward for one month (FR-29.9 drill-down); it opens on the contract's month.
  const [month, setMonth] = useState<string | null>(null);
  const [monthError, setMonthError] = useState("");
  const [shownMonth, setShownMonth] = useState("");
  const period = shownMonth || (state.data?.contractDate.slice(0, 7) ?? "");
  const rollForward = useLoad(
    (signal) => (period ? getContractRollForward(id, period, signal) : Promise.resolve(null)),
    `${id}|${period}|${state.data?.version ?? 0}`,
  );

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Revenue contract" />
        <Alert tone="danger" title="Could not load the revenue contract" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const contract = state.data;
  if (!contract) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Revenue contract" />
        <span className="visually-hidden">Loading the revenue contract…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  async function satisfy(event: FormEvent, c: RevenueContract, o: ContractObligation) {
    event.preventDefault();
    const on = dates[o.id] ?? today();
    setFailure(null);
    setDone("");
    if (!DAY.test(on)) {
      setDateErrors((prev) => ({ ...prev, [o.id]: "Enter the date the milestone was met as YYYY-MM-DD." }));
      return;
    }
    setDateErrors((prev) => ({ ...prev, [o.id]: "" }));
    setBusy(o.id);
    try {
      const result = await satisfyMilestone(o.id, on, c.version);
      setDone(`Line ${o.lineNo} satisfied on ${result.recognizedOn}: ${result.amount} ${c.currency} moved from deferred revenue to revenue.`);
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.fields.satisfiedOn) setDateErrors((prev) => ({ ...prev, [o.id]: problem.fields.satisfiedOn }));
      else setFailure(problem);
      if (problem.code === "REV-0103" || problem.code === "REV-0004") state.reload();
    } finally {
      setBusy("");
    }
  }

  function showMonth(event: FormEvent) {
    event.preventDefault();
    const m = (month ?? period).trim();
    if (!PERIOD.test(m)) {
      setMonthError("Enter the month as YYYY-MM, for example 2027-03.");
      return;
    }
    setMonthError("");
    setShownMonth(m);
  }

  const currency = contract.currency;
  const obligationColumns: DataTableColumn<ContractObligation>[] = [
    { key: "lineNo", header: "Line", mono: true, align: "end" },
    { key: "productId", header: "Product", mono: true },
    { key: "method", header: "Recognition", render: (o) => METHOD_LABELS[o.method] ?? o.method },
    { key: "sspSource", header: "SSP source", render: (o) => SSP_SOURCE_LABELS[o.sspSource] ?? o.sspSource },
    { key: "ssp", header: "SSP", mono: true, align: "end", render: (o) => `${o.ssp} ${currency}` },
    { key: "allocated", header: "Allocated", mono: true, align: "end", render: (o) => `${o.allocated} ${currency}` },
    { key: "recognized", header: "Recognized", mono: true, align: "end", render: (o) => `${o.recognized} ${currency}` },
    { key: "term", header: "Term or event", mono: true, render: obligationTerm },
  ];
  const milestones = contract.obligations.filter(pendingMilestone);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Revenue contract"
        description={`${contract.transactionPrice} ${currency} · contract date ${contract.contractDate} · ${contract.recognized} ${currency} recognized`}
        meta={<StatusPill status={CONTRACT_STATUS_LABELS[contract.status] ?? contract.status} />}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/billing/deferred-revenue">
              Deferred revenue
            </Link>
            <Link className={buttonClassName("ghost")} to="/billing/revenue-contracts">
              All contracts
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failureTitle(failure.code)} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {contract.warnings.map((w) => (
        <Alert key={w.code + w.detail} tone="warning" title="Standalone selling price missing">
          {w.detail} ({w.code})
        </Alert>
      ))}
      <Card title="Contract">
        <dl className={styles.details}>
          <Detail term="Price" mono>{`${contract.transactionPrice} ${currency}`}</Detail>
          <Detail term="Recognized" mono>{`${contract.recognized} ${currency}`}</Detail>
          <Detail term="Remaining" mono>{`${contract.remaining} ${currency}`}</Detail>
          <Detail term="Contract date" mono>
            {contract.contractDate}
          </Detail>
          <Detail term="Customer" mono>
            {contract.customerId}
          </Detail>
          <Detail term="Sales order" mono>
            {contract.orderId}
          </Detail>
          <Detail term="Legal entity" mono>
            {contract.legalEntityId}
          </Detail>
          <Detail term="Rounding residual to">{`${contract.residualRule.toLowerCase()} obligation`}</Detail>
          <Detail term="Ratable basis">{BASIS_LABELS[contract.ratableBasis] ?? contract.ratableBasis}</Detail>
        </dl>
      </Card>
      <Card title="Obligations">
        <DataTable
          label="Performance obligations"
          columns={obligationColumns}
          rows={contract.obligations}
          getRowKey={(o) => o.id}
          empty={{ title: "No obligations", description: "This contract has no performance obligations." }}
        />
      </Card>
      {milestones.map((o) => (
        <Card key={o.id} title={`Milestone: line ${o.lineNo}`}>
          <form className={styles.toolbar} onSubmit={(e) => satisfy(e, contract, o)} noValidate>
            <FormField id={`ms-${o.id}`} label="Satisfied on" hint={`On or after ${contract.contractDate}.`} error={dateErrors[o.id] || undefined}>
              {(c) => (
                <Input
                  {...c}
                  type="date"
                  className={styles.mono}
                  value={dates[o.id] ?? today()}
                  onChange={(e) => setDates((prev) => ({ ...prev, [o.id]: e.target.value }))}
                  invalid={Boolean(dateErrors[o.id])}
                />
              )}
            </FormField>
            <Button type="submit" variant="primary" loading={busy === o.id}>
              {`Satisfy milestone (${o.allocated} ${currency})`}
            </Button>
          </form>
        </Card>
      ))}
      <Card title="Deferred revenue of this contract">
        <div className={styles.stack}>
          <form className={styles.toolbar} onSubmit={showMonth} noValidate>
            <FormField id="rc-rf-month" label="Month" hint="YYYY-MM" error={monthError || undefined}>
              {(c) => <Input {...c} className={styles.mono} value={month ?? period} onChange={(e) => setMonth(e.target.value)} invalid={Boolean(monthError)} />}
            </FormField>
            <Button type="submit" variant="secondary">
              Show month
            </Button>
          </form>
          {rollForward.error ? (
            <Alert tone="danger" title="Could not load the contract's roll-forward" live="alert" action={<Button size="sm" onClick={rollForward.reload}>Try again</Button>}>
              <ProblemMessage problem={rollForward.error} />
            </Alert>
          ) : rollForward.data && !rollForward.loading ? (
            <RollForwardTiles rf={rollForward.data} />
          ) : (
            <div aria-busy="true">
              <span className="visually-hidden">Loading the contract's roll-forward…</span>
              <Skeleton height="tile" />
            </div>
          )}
          <p className={styles.note}>Only ratable and milestone obligations are deferred. The schedules below show each line by date.</p>
        </div>
      </Card>
      {contract.obligations
        .filter((o) => o.schedule.length > 0)
        .map((o) => (
          <Card key={`schedule-${o.id}`} title={`Schedule: line ${o.lineNo} (${METHOD_LABELS[o.method] ?? o.method})`}>
            <DataTable label={`Schedule for line ${o.lineNo}`} columns={scheduleColumns} rows={o.schedule} getRowKey={(l) => l.id} />
          </Card>
        ))}
    </div>
  );
}
