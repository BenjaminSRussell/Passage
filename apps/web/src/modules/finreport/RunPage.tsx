import { useParams } from "react-router-dom";
import { useState } from "react";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  drillBalances,
  getDefinition,
  getRun,
  getRunDefinition,
  listCells,
  runOutputUrl,
  DOWNLOAD_FORMATS,
  type BalanceKey,
  type CellSummary,
  type Definition,
  type DefinitionRow,
  type RunSummary,
} from "./api/client";
import { checkDrill, displayAmount } from "./drill";
import { CURRENCY_TYPE_LABELS, periodLabel } from "./format";
import { LinesPanel } from "./LinesPanel";
import { RunTreeCard } from "./TreeRevisions";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Statement {
  run: RunSummary;
  definition: Definition;
  cells: Map<string, CellSummary>;
}

async function loadStatement(runId: string, signal: AbortSignal): Promise<Statement> {
  const [run, cells] = await Promise.all([getRun(runId, signal), listCells(runId, signal)]);
  const definition = run.hierarchy ? await getRunDefinition(runId, signal) : (await getDefinition(run.definitionCode, run.definitionVersion, signal)).definition;
  return { run, definition, cells: new Map(cells.map((c) => [`${c.row}/${c.column}`, c])) };
}

const CHECK_TITLES: Record<string, string> = {
  "REPORT-0004": "This statement is stale",
  "REPORT-0015": "Some expense lines have no functional area",
  "REPORT-0034": "Some members are not in the hierarchy",
  "REPORT-0041": "Some balances are outside the tree",
};

const indentClass = [undefined, styles.indent1, styles.indent2, styles.indent3];

function RowLabel({ row }: { row: DefinitionRow }) {
  const indent = indentClass[Math.min(row.format?.indent ?? 0, 3)];
  return (
    <span className={indent}>
      <span className={row.format?.bold || row.kind === "total" || row.kind === "header" ? styles.rowLabelBold : styles.rowLabel}>{row.label}</span>
    </span>
  );
}

/** A statement run: the statement grid, its "as of" stamp, and a drill panel for the selected cell (FR-15.7). */
export function RunPage() {
  const { runId = "" } = useParams();
  const state = useLoad((signal) => loadStatement(runId, signal), runId);
  const [selected, setSelected] = useState<{ row: string; column: string } | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Statement run" />
        <Alert tone="danger" title="This statement could not be loaded" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Statement run" />
        <p className={styles.note}>Loading statement…</p>
        <Skeleton width="full" height="tile" />
      </div>
    );
  }

  const { run, definition, cells } = state.data;
  const columns: DataTableColumn<DefinitionRow>[] = [
    { key: "label", header: "Row", render: (row) => <RowLabel row={row} /> },
    ...definition.columns.map<DataTableColumn<DefinitionRow>>((col) => ({
      key: col.code,
      header: col.label,
      align: "end",
      render: (row) => {
        const cell = cells.get(`${row.code}/${col.code}`);
        if (!cell || cell.blank) {
          return cell?.note ? <span className={styles.muted} title="Prior-year period not found (REPORT-0010)">n/a</span> : null;
        }
        if (row.kind !== "header" && !row.format?.printZero && /^-?0(\.0+)?$/.test(cell.value) && cell.keyCount === 0 && !cell.computed) {
          return null;
        }
        const isSelected = selected?.row === row.code && selected.column === col.code;
        return (
          <button
            type="button"
            className={`${styles.cellButton} ${isSelected ? styles.cellSelected : ""}`}
            aria-label={`${row.label}, ${col.label}: ${displayAmount(cell.value)}. Show balances`}
            aria-pressed={isSelected}
            onClick={() => setSelected({ row: row.code, column: col.code })}
          >
            {displayAmount(cell.value)}
          </button>
        );
      },
    })),
  ];
  const hwm = Object.entries(run.hwmByLedger).map(([, seq]) => seq).join(", ");
  const selectedRow = selected ? definition.rows.find((r) => r.code === selected.row) : undefined;
  const selectedCol = selected ? definition.columns.find((c) => c.code === selected.column) : undefined;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={definition.name}
        description={`${run.definitionCode} version ${run.definitionVersion}${run.hierarchy ? `, rows from hierarchy ${run.hierarchy.code} version ${run.hierarchy.version}` : ""}${run.tree ? `, ${run.tree.unit ? `unit ${run.tree.unit} of` : "all units of"} tree ${run.tree.code} revision ${run.tree.revision}` : ""}, period ${periodLabel(run.periodYear, run.periodNo)}, ${CURRENCY_TYPE_LABELS[run.currencyType as keyof typeof CURRENCY_TYPE_LABELS] ?? run.currencyType} currency.`}
        meta={<StatusPill status={run.status} />}
        actions={DOWNLOAD_FORMATS.map((format) => (
          <a key={format} className={buttonClassName("secondary")} href={runOutputUrl(run.id, format)} download>
            Download {format.toUpperCase()}
          </a>
        ))}
      />
      {(run.checks ?? []).map((check) => (
        <Alert key={`${check.code}/${check.detail}`} tone="warning" title={CHECK_TITLES[check.code] ?? "This statement has a warning"}>
          {check.detail} ({check.code})
        </Alert>
      ))}
      <Card title="As of">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Posting sequence</dt>
            <dd className={styles.value}>{hwm}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Ledger</dt>
            <dd className={styles.value}>{run.ledgerId}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Generated</dt>
            <dd className={styles.value}>
              <time dateTime={run.startedAt}>{run.startedAt}</time>
            </dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Result hash</dt>
            <dd className={styles.value}>{run.resultHash}</dd>
          </div>
        </dl>
      </Card>
      <DataTable
        label="Statement"
        columns={columns}
        rows={definition.rows}
        getRowKey={(row) => row.code}
        empty={{ title: "This statement has no rows" }}
      />
      {run.tree ? <RunTreeCard runId={run.id} /> : null}
      {selected && selectedRow && selectedCol ? (
        <DrillPanel
          key={`${selected.row}/${selected.column}`}
          runId={run.id}
          row={selected.row}
          column={selected.column}
          title={`${selectedRow.label}, ${selectedCol.label}`}
        />
      ) : (
        <p className={styles.note}>Select an amount to see the balances that make it up.</p>
      )}
    </div>
  );
}

type IndexedKey = BalanceKey & { index: number };

function keyLabel(k: BalanceKey): string {
  return `account ${k.account}, ${k.period}`;
}

/**
 * Drill level 1: the balances behind a cell, with the exact-sum check shown
 * (BR-15.4), and the way into level 2: every balance, or all of them, opens
 * its journal lines.
 */
export function DrillPanel({ runId, row, column, title }: { runId: string; row: string; column: string; title: string }) {
  const drill = useLoad((signal) => drillBalances(runId, row, column, signal), `${runId}/${row}/${column}`);
  const [lines, setLines] = useState<{ key: number | null } | null>(null);

  const keyColumns: DataTableColumn<IndexedKey>[] = [
    { key: "account", header: "Account", mono: true },
    { key: "period", header: "Period", mono: true },
    { key: "combination", header: "Dimensions", mono: true },
    { key: "currencyType", header: "Currency type", mono: true },
    { key: "amount", header: "Amount", mono: true, align: "end", render: (k) => displayAmount(k.amount) },
    {
      key: "lines",
      header: "Journal lines",
      render: (k) => (
        <Button size="sm" variant="ghost" aria-pressed={lines?.key === k.index} onClick={() => setLines({ key: k.index })} aria-label={`Show journal lines of ${keyLabel(k)}`}>
          Lines
        </Button>
      ),
    },
  ];

  let body;
  if (drill.error) {
    body = (
      <Alert tone="danger" title="Balances are unavailable" live="alert" action={<Button size="sm" onClick={drill.reload}>Try again</Button>}>
        {drill.error.message} ({drill.error.code})
      </Alert>
    );
  } else if (!drill.data || drill.loading) {
    body = <DataTable label="Balances" columns={keyColumns} rows={[]} getRowKey={(k) => String(k.index)} loading />;
  } else if (drill.data.computed) {
    body = (
      <p className={styles.note}>
        This amount is calculated from other rows or columns of the statement, so no balances sit behind it. Select the amounts it is calculated from to see their balances.
      </p>
    );
  } else {
    const check = checkDrill(drill.data.value, drill.data.keys);
    body = (
      <>
        {check.ok ? (
          <p className={styles.note}>
            {drill.data.keys.length} balances sum to <span className={styles.amount}>{displayAmount(drill.data.value)}</span>, the amount shown.
          </p>
        ) : (
          <Alert tone="warning" title="The balances do not add up to this amount" live="alert">
            They sum to {check.sum}, not {drill.data.value}; the difference is {check.difference}.
          </Alert>
        )}
        <DataTable
          label="Balances"
          columns={keyColumns}
          rows={drill.data.keys.map((k, index) => ({ ...k, index }))}
          getRowKey={(k) => String(k.index)}
          empty={{ title: "No balances", description: "No posting falls in this row and column." }}
        />
      </>
    );
  }
  const keys = drill.data?.keys ?? [];
  const canShowLines = Boolean(drill.data && !drill.data.computed && keys.length > 0);
  const selectedKey = lines?.key !== null && lines?.key !== undefined ? keys[lines.key] : undefined;
  return (
    <>
      <Card
        title={`Balances for ${title}`}
        actions={
          <>
            <Badge tone="neutral">Drill level 1</Badge>
            {canShowLines ? (
              <Button size="sm" aria-pressed={lines?.key === null} onClick={() => setLines({ key: null })}>
                Show all journal lines
              </Button>
            ) : null}
          </>
        }
      >
        {body}
      </Card>
      {lines && canShowLines ? (
        <LinesPanel
          runId={runId}
          row={row}
          column={column}
          keyIndex={lines.key}
          title={title}
          keyLabel={selectedKey ? keyLabel(selectedKey) : undefined}
          onClose={() => setLines(null)}
        />
      ) : null}
    </>
  );
}
