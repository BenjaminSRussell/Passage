import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Badge, buttonClassName, Card, DataTable, EmptyState, FormField, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { listAvailable, listRules } from "./api/client";
import { controlRows, controlTotals, periodRows, type ControlLevel, type ControlRow, type PeriodRow } from "./costControl";
import { formatAmount, UUID } from "./logic";
import styles from "./pages.module.css";
import { LoadError } from "./parts";
import { useLoad } from "./useLoad";

const LEVELS: Record<ControlLevel, { tone: "success" | "warning" | "danger"; label: string }> = {
  ok: { tone: "success", label: "Within plan" },
  near: { tone: "warning", label: "Near the plan" },
  over: { tone: "danger", label: "Over the plan" },
};

const pct = (v: string | null) => (v === null ? "No plan" : `${v}%`);

const rowColumns: DataTableColumn<ControlRow>[] = [
  { key: "dimension", header: "Control combination", mono: true },
  { key: "span", header: "Period", mono: true },
  { key: "plan", header: "Plan", mono: true, align: "end", render: (r) => formatAmount(r.plan) },
  { key: "actual", header: "Actual", mono: true, align: "end", render: (r) => formatAmount(r.actual) },
  { key: "committed", header: "Committed", mono: true, align: "end", render: (r) => formatAmount(r.committed) },
  { key: "earmarked", header: "Earmarked", mono: true, align: "end", render: (r) => formatAmount(r.earmarked) },
  { key: "variance", header: "Left", mono: true, align: "end", render: (r) => formatAmount(r.variance) },
  { key: "usedPct", header: "Used", mono: true, align: "end", render: (r) => pct(r.usedPct) },
  { key: "level", header: "Status", render: (r) => <Badge tone={LEVELS[r.level].tone}>{LEVELS[r.level].label}</Badge> },
];

const periodColumns: DataTableColumn<PeriodRow>[] = [
  { key: "span", header: "Period", mono: true },
  { key: "plan", header: "Plan", mono: true, align: "end", render: (r) => formatAmount(r.plan) },
  { key: "actual", header: "Actual", mono: true, align: "end", render: (r) => formatAmount(r.actual) },
  { key: "committed", header: "Committed", mono: true, align: "end", render: (r) => formatAmount(r.committed) },
  { key: "variance", header: "Left", mono: true, align: "end", render: (r) => formatAmount(r.variance) },
  { key: "usedPct", header: "Used", mono: true, align: "end", render: (r) => pct(r.usedPct) },
  { key: "actualChangePct", header: "Actual vs the period before", mono: true, align: "end", render: (r) => (r.actualChangePct === null ? "Not applicable" : `${r.actualChangePct}%`) },
];

/** Cost control workspace (FR-38.10): the plan against what was spent, what is committed and what is left, per control combination and per period. */
export function CostControlPage() {
  const [params, setParams] = useSearchParams();
  const legalEntityId = params.get("legalEntityId") ?? "";
  const ruleId = params.get("rule") ?? "";
  const [onlyOver, setOnlyOver] = useState(false);
  const chosen = UUID.test(legalEntityId);
  const rules = useLoad((signal) => (chosen ? listRules(legalEntityId, signal) : Promise.resolve({ items: [] })), legalEntityId);
  const balances = useLoad((signal) => (UUID.test(ruleId) ? listAvailable(ruleId, signal) : Promise.resolve({ items: [] })), ruleId);

  function set(name: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(name, value);
    else next.delete(name);
    if (name === "legalEntityId") next.delete("rule");
    setParams(next);
  }

  const rows = controlRows(balances.data?.items ?? []);
  const shown = onlyOver ? rows.filter((r) => r.level !== "ok") : rows;
  const totals = controlTotals(rows);
  const rule = rules.data?.items.find((r) => r.id === ruleId);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost control"
        description="Plan against actual and committed cost, with what is left. Committed is ordered but not yet invoiced; earmarked funds are held for a later order."
        actions={
          UUID.test(ruleId) ? (
            <Link className={buttonClassName("secondary")} to={`/budgeting/rules/${ruleId}`}>
              Open the rule
            </Link>
          ) : undefined
        }
      />
      <Card>
        <div className={styles.toolbar}>
          <FormField id="cc-entity" label="Legal entity">
            {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => set("legalEntityId", e.target.value)} />}
          </FormField>
          <FormField id="cc-rule" label="Budget control rule">
            {(c) => (
              <Select {...c} value={ruleId} onChange={(e) => set("rule", e.target.value)}>
                <option value="">Choose a rule version</option>
                {(rules.data?.items ?? []).map((r) => (
                  <option key={r.id} value={r.id}>{`${r.code} version ${r.version}`}</option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="cc-filter" label="Show">
            {(c) => (
              <Select {...c} value={onlyOver ? "attention" : "all"} onChange={(e) => setOnlyOver(e.target.value === "attention")}>
                <option value="all">All combinations</option>
                <option value="attention">Only near or over the plan</option>
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {!chosen || !UUID.test(ruleId) ? (
        <Card>
          <EmptyState icon="database" title={chosen ? "Choose a budget control rule" : "Choose a legal entity"} description="Cost control reads the plan, the actuals and the commitments of one rule version." />
        </Card>
      ) : balances.error ? (
        <LoadError title="Could not load the cost control figures" error={balances.error} onRetry={balances.reload} />
      ) : (
        <>
          <StatGroup label="Cost control summary">
            <StatTile label={`Plan (${rule?.currency ?? ""})`.trim()} value={formatAmount(totals.plan)} />
            <StatTile label="Actual" value={formatAmount(totals.actual)} />
            <StatTile label="Committed" value={formatAmount(totals.committed)} />
            <StatTile label="Left" value={formatAmount(totals.variance)} tone={totals.variance.startsWith("-") ? "danger" : undefined} />
            <StatTile label="Used" value={pct(totals.usedPct)} />
            <StatTile label="Over the plan" value={totals.over} tone={totals.over > 0 ? "danger" : undefined} />
          </StatGroup>
          <DataTable
            label="Cost control by combination"
            columns={rowColumns}
            rows={shown}
            getRowKey={(r) => `${r.dimension}|${r.span}`}
            loading={balances.loading && !balances.data}
            empty={{ title: onlyOver ? "Nothing is near or over the plan" : "No figures yet", description: onlyOver ? "Every combination is within its plan." : "Post an Original budget to the rule, or approve a budget plan." }}
          />
          <Card title="Period comparison" headingLevel={3}>
            <DataTable label="Cost control by period" columns={periodColumns} rows={periodRows(balances.data?.items ?? [])} getRowKey={(r) => r.span} empty={{ title: "No periods", description: "" }} />
          </Card>
        </>
      )}
    </div>
  );
}
