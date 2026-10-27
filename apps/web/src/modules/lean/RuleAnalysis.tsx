import { useState, type FormEvent } from "react";
import { Alert, Button, Card, EmptyState, FormField, Input, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import { getRuleAnalysis, type RuleAnalysis } from "./api/client";
import { dash, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** The service's longest analysis period, in days. */
export const MAX_ANALYSIS_DAYS = 366;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** date shifted by days, as YYYY-MM-DD (UTC). */
export function shiftDays(date: string, days: number): string {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

type PeriodErrors = Partial<Record<"from" | "to", string>>;

/** Checks that mirror the service before an analysis period is applied. */
export function validateAnalysisPeriod(values: { from: string; to: string }): PeriodErrors {
  const errors: PeriodErrors = {};
  if (!DATE.test(values.from)) errors.from = "Enter the first day.";
  if (!DATE.test(values.to)) errors.to = "Enter the last day.";
  if (errors.from || errors.to) return errors;
  if (values.to < values.from) errors.to = "The last day cannot be before the first day.";
  else if (shiftDays(values.from, MAX_ANALYSIS_DAYS - 1) < values.to) errors.from = `Choose a period of at most ${MAX_ANALYSIS_DAYS} days.`;
  return errors;
}

function leadTime(a: RuleAnalysis): string {
  return a.avgLeadTimeH === null ? dash : `${a.avgLeadTimeH} h`;
}

function activeCards(a: RuleAnalysis): string {
  return a.maxKanbans === null ? String(a.activeKanbans) : `${a.activeKanbans} of ${a.maxKanbans}`;
}

/** Kanban rule analysis (PRD 4.24.14): cycles, lead time, late share and active cards over a period. */
export function RuleAnalysisCard({ ruleId }: { ruleId: string }) {
  const [draft, setDraft] = useState(() => ({ from: shiftDays(today(), -29), to: today() }));
  const [period, setPeriod] = useState(draft);
  const [errors, setErrors] = useState<PeriodErrors>({});
  const state = useLoad((signal) => getRuleAnalysis(ruleId, period, signal), `${ruleId}|${period.from}|${period.to}`);
  const a = state.data;

  function apply(event: FormEvent) {
    event.preventDefault();
    const found = validateAnalysisPeriod(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) setPeriod(draft);
  }

  return (
    <Card title="Rule analysis">
      <div className={styles.stack}>
        <form className={styles.toolbar} onSubmit={apply} noValidate>
          <FormField id="analysis-from" label="First day" error={errors.from}>
            {(c) => <Input {...c} type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} invalid={Boolean(errors.from)} />}
          </FormField>
          <FormField id="analysis-to" label="Last day" error={errors.to}>
            {(c) => <Input {...c} type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} invalid={Boolean(errors.to)} />}
          </FormField>
          <Button type="submit" variant="secondary">
            Analyse
          </Button>
        </form>
        {state.error ? (
          <Alert tone="danger" title="Could not load the analysis" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : !a ? (
          <div aria-busy="true">
            <span className="visually-hidden">Loading the analysis…</span>
            <Skeleton height="tile" />
          </div>
        ) : (
          <>
            <StatGroup label="Rule analysis">
              <StatTile label="Cycles" value={a.cycles} />
              <StatTile label="Average lead time" value={leadTime(a)} hint={`Target ${a.targetLeadTimeH} h`} />
              <StatTile label="Late" value={a.latePct === null ? dash : `${a.latePct} %`} hint={`${a.lateCycles} of ${a.dueCycles} with a due time`} />
              <StatTile label="Active cards" value={activeCards(a)} />
              <StatTile label="Overdue now" value={a.overdueOpen} tone="warning" />
            </StatGroup>
            {a.cycles === 0 ? (
              <EmptyState title="No completed cycles in this period" description="Cycles appear here as the rule's cards are completed." />
            ) : (
              <p className={styles.muted}>
                {`${a.from} to ${a.to} (UTC). Lead time runs from planning to completion${
                  a.timedCycles < a.cycles ? `; ${a.cycles - a.timedCycles} older cycles have no planning time and are left out of the average` : ""
                }. A cycle is late when it completed after its due time.`}
              </p>
            )}
          </>
        )}
      </div>
    </Card>
  );
}
