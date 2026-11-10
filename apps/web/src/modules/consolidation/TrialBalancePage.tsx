import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { Alert, Button, Card, DataTable, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { getGroup, getTrialBalance, type GroupDetail, type TrialBalance, type TrialBalanceRow, type UnmappedLine } from "./api/groups";
import { amount, isNonZero } from "./format";
import styles from "./pages.module.css";
import { useLegalEntityNames } from "./useEntityNames";

/** The unit ids of a trial balance in a stable order, so each unit keeps its column. */
export function unitIds(tb: TrialBalance): string[] {
  const ids = new Set<string>();
  for (const row of tb.rows) for (const id of Object.keys(row.units)) ids.add(id);
  for (const id of Object.keys(tb.unitDifferences)) ids.add(id);
  return [...ids].sort();
}

/** The consolidated trial balance of one collection, before eliminations, with each unit's share (FR-40.11). */
export function TrialBalancePage() {
  const { id = "" } = useParams();
  const [tb, setTb] = useState<TrialBalance | null>(null);
  const [group, setGroup] = useState<GroupDetail | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [reload, setReload] = useState(0);
  const names = useLegalEntityNames(group?.units.map((u) => u.legalEntityId) ?? []);

  useEffect(() => {
    const ctl = new AbortController();
    setFailure(null);
    getTrialBalance(id, ctl.signal)
      .then(async (t) => {
        setTb(t);
        setGroup(await getGroup(t.groupId, ctl.signal));
      })
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setFailure(describeProblem(err));
      });
    return () => ctl.abort();
  }, [id, reload]);

  if (failure) {
    return (
      <Alert tone="danger" title="We couldn't load the trial balance" live="alert" action={<Button size="sm" onClick={() => setReload((n) => n + 1)}>Try again</Button>}>
        <ProblemMessage problem={failure} />
      </Alert>
    );
  }
  if (!tb) return <p className={styles.note}>Loading the trial balance…</p>;

  const unitName = (unitId: string) => {
    const u = group?.units.find((x) => x.id === unitId);
    return (u && names[u.legalEntityId]) ?? (u ? `Ledger ${u.ledgerId}` : "Unit");
  };
  const units = unitIds(tb);
  const columns: DataTableColumn<TrialBalanceRow>[] = [
    { key: "account", header: "Consolidation account", mono: true, render: (r) => r.account },
    { key: "type", header: "Type", render: (r) => (r.accountType === "income" ? "Income" : "Balance sheet") },
    { key: "total", header: `Total (${tb.currency})`, align: "end", render: (r) => amount(r.total) },
    ...units.map((u): DataTableColumn<TrialBalanceRow> => ({ key: `u-${u}`, header: unitName(u), align: "end", render: (r) => amount(r.units[u]) })),
  ];
  const unmappedColumns: DataTableColumn<UnmappedLine>[] = [
    { key: "unit", header: "Unit", render: (l) => unitName(l.unit) },
    { key: "main", header: "Main account", mono: true, render: (l) => l.mainAccount },
    { key: "unitAmount", header: "Amount in the unit's currency", align: "end", render: (l) => `${amount(l.unitAmount)} ${l.unitCurrency}` },
    { key: "groupAmount", header: `Amount (${tb.currency})`, align: "end", render: (l) => amount(l.groupAmount) },
  ];
  const differing = units.filter((u) => isNonZero(tb.unitDifferences[u] ?? "0"));

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Consolidated trial balance ${tb.period}`}
        description={`Collection version ${tb.version}, in ${tb.currency}, before eliminations. Each unit's column is what it contributes to the total.`}
        actions={group ? <Link to={`/consolidation/groups/${group.id}`}>Back to {group.name}</Link> : undefined}
      />
      <StatGroup label="Trial balance summary">
        <StatTile label="Accounts" value={tb.rows.length} />
        <StatTile label="Unmapped balances" value={tb.unmapped.length} tone={tb.unmapped.length > 0 ? "warning" : "neutral"} />
        <StatTile label={`Translation difference (${tb.currency})`} value={amount(tb.total)} tone={isNonZero(tb.total) ? "warning" : "neutral"} />
      </StatGroup>
      {tb.unmapped.length > 0 ? (
        <Alert tone="warning" title="Some balances have no consolidation account">
          Map these accounts on the group's page and collect again. Until then they are not in the totals below.
        </Alert>
      ) : null}
      {differing.length > 0 ? (
        <Alert tone="info" title="Translation differences">
          Balance sheet accounts translate at the closing rate and income at the average rate, so a foreign unit's translated balances do not net to zero. The consolidation run books the difference as CTA:{" "}
          {differing.map((u) => `${unitName(u)} ${amount(tb.unitDifferences[u])} ${tb.currency}`).join("; ")}.
        </Alert>
      ) : null}
      <DataTable label="Consolidated trial balance" columns={columns} rows={tb.rows} getRowKey={(r) => r.account} empty={{ title: "No mapped balances", description: "Map the units' accounts to consolidation accounts, then collect again." }} />
      {tb.unmapped.length > 0 ? (
        <Card title="Unmapped balances" headingLevel={3}>
          <DataTable label="Unmapped balances" columns={unmappedColumns} rows={tb.unmapped} getRowKey={(l) => `${l.unit}-${l.mainAccount}`} />
        </Card>
      ) : null}
    </div>
  );
}
