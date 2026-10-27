import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  buttonClassName,
  Card,
  DataTable,
  FormField,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  getRule,
  listAvailable,
  listChecks,
  listEncumbrances,
  listRegisterEntries,
  type Balance,
  type CheckOutcome,
  type Encumbrance,
  type EncumbranceStatus,
  type RegisterEntry,
} from "./api/client";
import { formatAmount, fromUnits, sumAmounts } from "./logic";
import styles from "./pages.module.css";
import { Detail, formatDateTime, LoadError, ResultBadge, statusLabel } from "./parts";
import { useLoad } from "./useLoad";

const money = (key: string, header: string) => ({ key, header, align: "end" as const, mono: true });

const balanceColumns: DataTableColumn<Balance>[] = [
  { key: "controlDimensionKey", header: "Dimension", mono: true },
  { key: "timeSpanKey", header: "Time span", mono: true },
  { ...money("budgetTotal", "Budget"), render: (r) => formatAmount(r.budgetTotal) },
  { ...money("actuals", "Actuals"), render: (r) => formatAmount(r.actuals) },
  { ...money("encumbrances", "Encumbered"), render: (r) => formatAmount(r.encumbrances) },
  { ...money("preEncumbrances", "Pre-encumbered"), render: (r) => formatAmount(r.preEncumbrances) },
  { ...money("reservations", "Earmarked"), render: (r) => formatAmount(r.reservations) },
  { ...money("available", "Available"), render: (r) => formatAmount(r.available) },
];

const entryColumns: DataTableColumn<RegisterEntry>[] = [
  { key: "postedAt", header: "Posted", render: (r) => formatDateTime(r.postedAt) },
  { key: "entryType", header: "Type" },
  { key: "lines", header: "Lines", align: "end", render: (r) => r.lines.length },
  { ...money("total", "Net amount"), render: (r) => formatAmount(r.total) },
  { key: "status", header: "Status", render: (r) => <StatusPill status={r.status} /> },
];

const encumbranceColumns: DataTableColumn<Encumbrance>[] = [
  { key: "sourceDocumentId", header: "Document", mono: true },
  { key: "sourceType", header: "Source" },
  { key: "controlDimensionKey", header: "Dimension", mono: true },
  { ...money("reserved", "Reserved"), render: (r) => formatAmount(r.reserved) },
  { ...money("relieved", "Relieved"), render: (r) => formatAmount(r.relieved) },
  { ...money("remaining", "Remaining"), render: (r) => formatAmount(r.remaining) },
  { key: "status", header: "Status", render: (r) => <StatusPill status={statusLabel(r.status)} /> },
];

const checkColumns: DataTableColumn<CheckOutcome>[] = [
  { key: "documentId", header: "Document", mono: true },
  { key: "documentType", header: "Type" },
  { key: "checkedAt", header: "Checked", render: (r) => formatDateTime(r.checkedAt) },
  { key: "lines", header: "Lines", align: "end", render: (r) => r.lines.length },
  { key: "result", header: "Result", render: (r) => <ResultBadge result={r.result} /> },
];

/** One rule version: available funds per combination, register entries, encumbrances and recorded checks. */
export function RuleDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [status, setStatus] = useState<EncumbranceStatus | "">("");
  const rule = useLoad((signal) => getRule(id, signal), id);
  const balances = useLoad((signal) => listAvailable(id, signal), id);
  const entries = useLoad((signal) => listRegisterEntries(id, signal), id);
  const encumbrances = useLoad((signal) => listEncumbrances(id, status, signal), `${id}|${status}`);
  const checks = useLoad((signal) => listChecks(id, signal), id);

  if (rule.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Budget control rule" />
        <LoadError title="Could not load this rule version" error={rule.error} onRetry={rule.reload} />
      </div>
    );
  }
  if (!rule.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Budget control rule" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const r = rule.data;
  const rows = balances.data?.items ?? [];
  const total = (pick: (b: Balance) => string) => fromUnits(sumAmounts(rows.map(pick)));
  const overBudget = rows.filter((b) => b.available.startsWith("-")).length;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Rule ${r.code} v${r.version}`}
        description="Budget, actuals and reservations per control combination. Checks against this version reserve funds here."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/budgeting/check?rule=${r.id}`}>
              Run a budget check
            </Link>
            <Link className={buttonClassName("secondary")} to={`/budgeting/reservations?rule=${r.id}`}>
              Earmarked funds
            </Link>
            <Link className={buttonClassName("primary")} to={`/budgeting/rules/${r.id}/entries/new`}>
              Post register entry
            </Link>
          </>
        }
      />
      <Card>
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {r.legalEntityId}
          </Detail>
          <Detail term="Warn threshold">{r.warnPct}%</Detail>
          <Detail term="Block threshold">{r.blockPct}%</Detail>
          <Detail term="Currency" mono>
            {r.currency}
          </Detail>
          <Detail term="Effective from" mono>
            {r.effectiveFrom}
          </Detail>
        </dl>
      </Card>
      <StatGroup label="Funds summary">
        <StatTile label={`Budget (${r.currency})`} value={total((b) => b.budgetTotal)} />
        <StatTile label="Encumbered" value={total((b) => b.encumbrances)} />
        <StatTile label="Earmarked" value={total((b) => b.reservations)} />
        <StatTile label="Available" value={total((b) => b.available)} />
        <StatTile label="Over-budget combinations" value={overBudget} tone="danger" />
      </StatGroup>

      <h3 className={styles.sectionTitle}>Available funds</h3>
      {balances.error ? (
        <LoadError title="Could not load available funds" error={balances.error} onRetry={balances.reload} />
      ) : (
        <DataTable
          label="Available funds"
          columns={balanceColumns}
          rows={rows}
          getRowKey={(b) => b.id}
          loading={balances.loading && !balances.data}
          empty={{ title: "No budget on this rule yet", description: "Post an Original register entry to give a combination its budget." }}
        />
      )}

      <h3 className={styles.sectionTitle}>Register entries</h3>
      {entries.error ? (
        <LoadError title="Could not load register entries" error={entries.error} onRetry={entries.reload} />
      ) : (
        <DataTable
          label="Register entries"
          columns={entryColumns}
          rows={entries.data?.items ?? []}
          getRowKey={(e) => e.id}
          loading={entries.loading && !entries.data}
          empty={{ title: "No register entries yet", description: "Original, revision, transfer and carry-forward entries appear here once posted." }}
        />
      )}

      <h3 className={styles.sectionTitle}>Encumbrances</h3>
      <div className={styles.toolbar}>
        <FormField id="encumbrance-status" label="Encumbrance status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as EncumbranceStatus | "")}>
              <option value="">All</option>
              <option value="Open">Open</option>
              <option value="PartiallyRelieved">Partially relieved</option>
              <option value="Relieved">Relieved</option>
              <option value="Released">Released</option>
            </Select>
          )}
        </FormField>
      </div>
      {encumbrances.error ? (
        <LoadError title="Could not load encumbrances" error={encumbrances.error} onRetry={encumbrances.reload} />
      ) : (
        <DataTable
          label="Encumbrances"
          columns={encumbranceColumns}
          rows={encumbrances.data?.items ?? []}
          getRowKey={(e) => e.id}
          loading={encumbrances.loading && !encumbrances.data}
          empty={{ title: "No encumbrances", description: "Checks that pass or warn reserve funds as encumbrances." }}
          onRowOpen={(e) => navigate(`/budgeting/encumbrances/${e.id}`)}
        />
      )}

      <h3 className={styles.sectionTitle}>Budget checks</h3>
      {checks.error ? (
        <LoadError title="Could not load budget checks" error={checks.error} onRetry={checks.reload} />
      ) : (
        <DataTable
          label="Budget checks"
          columns={checkColumns}
          rows={checks.data?.items ?? []}
          getRowKey={(c) => c.id}
          loading={checks.loading && !checks.data}
          empty={{ title: "No checks yet", description: "Every check is recorded here, including the ones that failed." }}
          onRowOpen={(c) => navigate(`/budgeting/checks/${c.id}`)}
        />
      )}
    </div>
  );
}
