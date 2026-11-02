import { useState } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  expireQuotations,
  getSalesSummary,
  type ExpireResult,
  type PipelineCurrency,
  type Problem,
  type SalesSummaryFilter,
} from "./api/client";
import { dash, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const WINDOWS = [30, 90, 365] as const;
export const EXPIRY_WINDOWS = [7, 30, 90] as const;

/** Running the expiry needs this duty (enforced by the service; seeding is #313). */
export const DUTY_QUOTATION_MAINTAIN = "sales.quotation.maintain";

export interface WorkspaceDraft {
  legalEntity: string;
  windowDays: number;
  status: "" | "DRAFT" | "SENT";
  minProbability: string;
  expiringWithin: string;
}

/** Turns the filter form into the summary request, or the field errors that stop it. */
export function workspaceFilter(draft: WorkspaceDraft): { filter: SalesSummaryFilter } | { errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  const le = draft.legalEntity.trim();
  if (le && !UUID.test(le)) errors.legalEntity = "Enter a UUID.";
  const prob = draft.minProbability.trim();
  if (prob && (!/^\d{1,3}$/.test(prob) || Number(prob) > 100)) errors.minProbability = "Enter a whole percentage from 0 to 100.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    filter: {
      legalEntityId: le || undefined,
      windowDays: draft.windowDays,
      status: draft.status || undefined,
      minProbabilityPct: prob ? Number(prob) : undefined,
      expiringWithinDays: draft.expiringWithin ? Number(draft.expiringWithin) : undefined,
    },
  };
}

/** "42.9%" or the dash when no quotation closed in the window. */
export function conversionText(rate?: string): string {
  return rate === undefined ? dash : `${rate}%`;
}

/** Sentence for the outcome of an expiry run. */
export function expiryText(result: ExpireResult): string {
  if (result.count === 0) return "No quotation had passed its validity.";
  const noun = result.count === 1 ? "quotation" : "quotations";
  return `${result.count} ${noun} expired.${result.more ? " More are still lapsed; run it again." : ""}`;
}

const columns: DataTableColumn<PipelineCurrency>[] = [
  { key: "currency", header: "Currency", mono: true },
  { key: "openCount", header: "Open quotations", align: "end" },
  { key: "openNet", header: "Open value", align: "end", mono: true },
  { key: "weightedNet", header: "Weighted value", align: "end", mono: true },
];

/**
 * Sales workspace (PRD §5.27.14): tiles for active holds, backorder lines and
 * quotations about to expire, the open quotation pipeline per currency, the
 * conversion rate, and the expiry run that closes lapsed quotations.
 */
export function SalesWorkspacePage() {
  const canExpire = holdsDuty(usePrincipalDuties(), DUTY_QUOTATION_MAINTAIN);
  const [draft, setDraft] = useState<WorkspaceDraft>({ legalEntity: "", windowDays: 90, status: "", minProbability: "", expiringWithin: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [applied, setApplied] = useState<SalesSummaryFilter>({ windowDays: 90 });
  const state = useLoad((signal) => getSalesSummary(applied, signal), JSON.stringify(applied));
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<ExpireResult | null>(null);
  const [runError, setRunError] = useState<Problem | null>(null);
  const summary = state.data;

  const set = <K extends keyof WorkspaceDraft>(key: K, value: WorkspaceDraft[K]) => setDraft((d) => ({ ...d, [key]: value }));
  const filtered = Boolean(applied.status || applied.minProbabilityPct !== undefined || applied.expiringWithinDays);

  function apply(event: React.FormEvent) {
    event.preventDefault();
    const outcome = workspaceFilter(draft);
    if ("errors" in outcome) {
      setErrors(outcome.errors);
      return;
    }
    setErrors({});
    setApplied(outcome.filter);
  }

  async function expire() {
    setRunning(true);
    setRunError(null);
    setResult(null);
    try {
      setResult(await expireQuotations(applied.legalEntityId ?? ""));
      state.reload();
    } catch (failure) {
      setRunError(describeProblem(failure));
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Sales workspace" description="What needs attention in sales today, and how the open quotation pipeline is converting." />
      <form className={styles.toolbar} onSubmit={apply} noValidate aria-label="Filter the workspace">
        <FormField id="ws-le" label="Legal entity" error={errors.legalEntity} optional>
          {(control) => (
            <Input {...control} className={styles.mono} value={draft.legalEntity} onChange={(e) => set("legalEntity", e.target.value)} invalid={Boolean(errors.legalEntity)} />
          )}
        </FormField>
        <FormField id="ws-window" label="Conversion period">
          {(control) => (
            <Select {...control} value={String(draft.windowDays)} onChange={(e) => set("windowDays", Number(e.target.value))}>
              {WINDOWS.map((d) => (
                <option key={d} value={d}>{`Last ${d} days`}</option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="ws-status" label="Pipeline status">
          {(control) => (
            <Select {...control} value={draft.status} onChange={(e) => set("status", e.target.value as WorkspaceDraft["status"])}>
              <option value="">Draft and sent</option>
              <option value="DRAFT">Draft</option>
              <option value="SENT">Sent</option>
            </Select>
          )}
        </FormField>
        <FormField id="ws-prob" label="Minimum probability (%)" error={errors.minProbability} optional>
          {(control) => (
            <Input {...control} inputMode="numeric" value={draft.minProbability} onChange={(e) => set("minProbability", e.target.value)} invalid={Boolean(errors.minProbability)} />
          )}
        </FormField>
        <FormField id="ws-expiry" label="Valid until">
          {(control) => (
            <Select {...control} value={draft.expiringWithin} onChange={(e) => set("expiringWithin", e.target.value)}>
              <option value="">Any date</option>
              {EXPIRY_WINDOWS.map((d) => (
                <option key={d} value={d}>{`Ends within ${d} days`}</option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Apply</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load the workspace" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : state.loading && !summary ? (
        <p role="status" className={styles.muted}>Loading the workspace.</p>
      ) : summary ? (
        <>
          <StatGroup label="Sales attention">
            <StatTile label="Orders on hold" value={summary.activeHolds} tone="warning" hint="Active holds of every type" />
            <StatTile label="Backorder lines" value={summary.backorderLines} tone="warning" hint="Promised late or not at all" />
            <StatTile label="Quotes expiring in 7 days" value={summary.quotationsExpiring} tone="warning" hint="Draft or sent, still valid" />
            <StatTile label="Lapsed, not yet expired" value={summary.quotationsLapsed} tone="danger" hint="Past validity, still open" />
            <StatTile label="Intake errors" value={summary.openIntakeErrors} tone="danger" hint="Channel orders refused, not yet closed" />
          </StatGroup>
          <h2 className={styles.sectionTitle}>Quotation pipeline</h2>
          <p className={styles.muted}>The status, probability and validity filters narrow this table only; the tiles and the conversion rate cover every quotation.</p>
          <DataTable
            label="Open quotation pipeline"
            columns={columns}
            rows={summary.pipeline}
            getRowKey={(p) => p.currency}
            loading={state.loading}
            empty={
              filtered
                ? { title: "No quotations match the filters", description: "Widen the status, probability or validity filters to see more of the pipeline." }
                : { title: "No open quotations", description: "Draft and sent quotations that are still valid appear here, by currency." }
            }
          />
          <StatGroup label={`Conversion over the last ${summary.windowDays} days`}>
            <StatTile label="Conversion rate" value={conversionText(summary.conversionRatePct)} hint="Won of won, lost and expired" />
            <StatTile label="Won" value={summary.won} />
            <StatTile label="Lost" value={summary.lost} />
            <StatTile label="Expired" value={summary.expired} />
          </StatGroup>
          <div className={styles.actions}>
            {canExpire ? (
              <Button onClick={expire} disabled={running || summary.quotationsLapsed === 0}>
                {running ? "Expiring" : "Expire lapsed quotations"}
              </Button>
            ) : (
              <span className={styles.muted}>Expiring lapsed quotations needs the {DUTY_QUOTATION_MAINTAIN} duty.</span>
            )}
            <Link to="/sales/quotations">Open quotations</Link>
            <Link to="/sales/credit-holds">Open the release queue</Link>
            <Link to="/sales/backorders">Open backorders</Link>
          </div>
          {runError ? (
            <Alert tone="danger" title="Could not expire the quotations" live="alert">
              {runError.message} ({runError.code})
            </Alert>
          ) : null}
          {result ? (
            <Alert tone="success" title="Expiry run finished" live="status">
              {expiryText(result)}
            </Alert>
          ) : null}
        </>
      ) : null}
    </div>
  );
}
