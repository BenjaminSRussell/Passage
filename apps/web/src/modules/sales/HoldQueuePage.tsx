import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, etagOf, HOLD_TYPES, listHolds, releaseHold, type HoldQueueFilter, type HoldQueueItem, type HoldType, type Problem } from "./api/client";
import { dash, formatDateTime, money, sentence, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Business days in the release SLA (credit_hold_sla_business_days, FR-27.8). */
const SLA_DAYS = 2;

/** "3 d 4 h", "5 h", "12 min": how long a hold has waited. */
export function holdAge(appliedAt: string, now: Date = new Date()): string {
  const minutes = Math.max(0, Math.floor((now.getTime() - new Date(appliedAt).getTime()) / 60000));
  if (Number.isNaN(minutes)) return dash;
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  if (days > 0) return hours > 0 ? `${days} d ${hours} h` : `${days} d`;
  if (hours > 0) return `${hours} h`;
  return `${minutes} min`;
}

/** Whether a hold has waited longer than the SLA, counted in calendar days until the work calendar exists. */
export function overSla(appliedAt: string, now: Date = new Date()): boolean {
  return now.getTime() - new Date(appliedAt).getTime() > SLA_DAYS * 24 * 3600 * 1000;
}

/** "11500 vs 11000" for the failed rule, with its note when there is one. */
function ruleText(item: HoldQueueItem): ReactNode {
  const rule = item.failedRule;
  if (!rule) return item.reasonCode;
  return (
    <>
      {rule.id}: {rule.value} vs {rule.threshold}
      {rule.note ? <span className={styles.muted}> ({rule.note})</span> : null}
    </>
  );
}

/**
 * Credit release queue (PRD §5.27.6): ACTIVE holds oldest first with the order,
 * amount, the failed rule against its threshold, the credit limit and the age.
 * A credit manager releases a CREDIT hold here; the release confirms the order.
 */
export function HoldQueuePage() {
  const [draft, setDraft] = useState<{ legalEntityId: string; holdType: HoldType | "" }>({ legalEntityId: "", holdType: "CREDIT" });
  const [filter, setFilter] = useState<HoldQueueFilter>({ holdType: "CREDIT" });
  const [busy, setBusy] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<{ tone: "success"; text: ReactNode } | { tone: "danger"; problem: Problem } | null>(null);
  const invalid = draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim()) ? "Enter a legal entity UUID." : undefined;
  const state = useLoad((signal) => listHolds(filter, signal), JSON.stringify(filter));

  async function release(item: HoldQueueItem) {
    setBusy(item.holdId);
    setOutcome(null);
    try {
      const res = await releaseHold(item.orderId, item.holdId, etagOf(item.orderVersion));
      const o = res.value;
      setOutcome({
        tone: "success",
        text: (
          <>
            {sentence(item.holdType)} hold on <Link to={`/sales/orders/${o.id}`}>{o.orderNumber}</Link> released{o.confirmed ? " and the order confirmed" : ""}.
          </>
        ),
      });
      state.reload();
    } catch (error) {
      setOutcome({ tone: "danger", problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<HoldQueueItem>[] = [
    { key: "orderNumber", header: "Order", mono: true, render: (h) => <Link to={`/sales/orders/${h.orderId}`}>{h.orderNumber}</Link> },
    { key: "customerId", header: "Customer", mono: true, render: (h) => shortId(h.customerId) },
    { key: "holdType", header: "Hold", render: (h) => <Badge tone={h.holdType === "CREDIT" || h.holdType === "FRAUD" ? "danger" : "warning"}>{sentence(h.holdType)}</Badge> },
    { key: "amount", header: "Amount", align: "end", render: (h) => money(h.amount, h.currency) },
    { key: "rule", header: "Rule failed", render: ruleText },
    { key: "creditLimit", header: "Credit limit", align: "end", render: (h) => (h.creditLimit ? money(h.creditLimit, h.limitCurrency ?? h.currency) : "No profile") },
    {
      key: "age",
      header: "Waiting",
      render: (h) => (
        <span title={formatDateTime(h.appliedAt)}>
          {holdAge(h.appliedAt)} {overSla(h.appliedAt) ? <Badge tone="warning">Past SLA</Badge> : null}
        </span>
      ),
    },
    {
      key: "release",
      header: "Action",
      render: (h) => (
        <Button size="sm" loading={busy === h.holdId} disabled={busy !== null && busy !== h.holdId} onClick={() => void release(h)} aria-label={`Release ${sentence(h.holdType)} hold on ${h.orderNumber}`}>
          Release
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Credit release queue"
        description="Orders on hold, oldest first, with the rule that failed against its threshold. Releasing a credit hold confirms the order when nothing else blocks it."
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (!invalid) setFilter({ legalEntityId: draft.legalEntityId.trim(), holdType: draft.holdType });
        }}
      >
        <FormField id="queue-le" label="Legal entity" error={invalid} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid)} />}
        </FormField>
        <FormField id="queue-type" label="Hold type">
          {(control) => (
            <Select {...control} value={draft.holdType} onChange={(e) => setDraft({ ...draft, holdType: e.target.value as HoldType | "" })}>
              <option value="">Any type</option>
              {HOLD_TYPES.map((t) => (
                <option key={t} value={t}>
                  {sentence(t)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {outcome?.tone === "danger" ? (
        <Alert tone="danger" title="Could not release the hold" live="alert">
          {outcome.problem.message} ({outcome.problem.code})
        </Alert>
      ) : null}
      <Alert tone="success" live="status">
        {outcome?.tone === "success" ? outcome.text : null}
      </Alert>
      {state.error ? (
        <Alert tone="danger" title="Could not load the release queue" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Holds waiting for release"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(h) => h.holdId}
          loading={state.loading}
          empty={{ title: "Nothing waiting for release", description: "Orders the credit check holds at confirmation wait here for a credit manager." }}
        />
      )}
    </div>
  );
}
