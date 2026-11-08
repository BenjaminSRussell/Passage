import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, type DataTableColumn } from "../../shell/ui";
import { describeProblem, drillLines, type DrillLine, type LinePage, type Problem } from "./api/client";
import { displayAmount, httpUrl, sourceLink } from "./drill";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface LinesPanelProps {
  runId: string;
  row: string;
  column: string;
  /** Position of one balance key in the level 1 list, or null for the whole cell. */
  keyIndex: number | null;
  /** "Row, Column" of the selected cell. */
  title: string;
  /** Account and period of the selected balance key, when one is selected. */
  keyLabel?: string;
  onClose(): void;
}

function SourceCell({ line }: { line: DrillLine }) {
  const link = sourceLink(line.sourceUri);
  const name = `Open source document of ${line.voucher || `line ${line.postingSequence}/${line.lineNumber}`}`;
  if (!link) {
    return <span className={styles.muted}>None</span>;
  }
  if (link.kind === "route") {
    return (
      <Link className={styles.link} to={link.to} aria-label={name}>
        Document
      </Link>
    );
  }
  return (
    <a className={styles.link} href={link.href} target="_blank" rel="noopener noreferrer" aria-label={`${name} (opens in a new tab)`}>
      Document
    </a>
  );
}

function TraceCell({ line }: { line: DrillLine }) {
  if (!line.traceId) {
    return <span className={styles.muted}>None</span>;
  }
  const href = httpUrl(line.traceUrl);
  if (!href) {
    return <span className={styles.mono}>{line.traceId}</span>;
  }
  return (
    <a className={`${styles.link} ${styles.mono}`} href={href} target="_blank" rel="noopener noreferrer" aria-label={`Open trace ${line.traceId} (opens in a new tab)`}>
      {line.traceId.slice(0, 12)}
    </a>
  );
}

const lineColumns: DataTableColumn<DrillLine>[] = [
  { key: "postingDate", header: "Date", mono: true },
  {
    key: "voucher",
    header: "Voucher",
    mono: true,
    render: (l) => (
      <>
        {l.voucher}
        {l.closingEntry ? (
          <>
            {" "}
            <Badge tone="neutral">Closing</Badge>
          </>
        ) : null}
      </>
    ),
  },
  { key: "account", header: "Account", mono: true },
  { key: "period", header: "Period", mono: true },
  { key: "debit", header: "Debit", mono: true, align: "end", render: (l) => displayAmount(l.debit) },
  { key: "credit", header: "Credit", mono: true, align: "end", render: (l) => displayAmount(l.credit) },
  { key: "amount", header: "Amount shown", mono: true, align: "end", render: (l) => displayAmount(l.amount) },
  { key: "source", header: "Source", render: (l) => <SourceCell line={l} /> },
  { key: "trace", header: "Trace", render: (l) => <TraceCell line={l} /> },
];

/** Shown first when the lines span ledgers (a consolidated column). */
const ledgerColumn: DataTableColumn<DrillLine> = { key: "ledgerId", header: "Ledger", mono: true, render: (l) => <span title={l.ledgerId}>{l.ledgerId.slice(0, 8)}</span> };

function failure(problem: Problem, retry: () => void) {
  if (problem.status === 403) {
    return (
      <Alert tone="danger" title="You cannot see journal lines" live="alert">
        Drilling to journal lines needs the View ledger transactions duty. ({problem.code})
      </Alert>
    );
  }
  return (
    <Alert tone="danger" title="Journal lines are unavailable" live="alert" action={<Button size="sm" onClick={retry}>Try again</Button>}>
      {problem.message} ({problem.code})
    </Alert>
  );
}

/**
 * Drill level 2 (FR-15.7, BR-15.4): the journal lines behind a cell or one of
 * its balances, a page at a time, with links to the source document and the
 * trace. When the lines do not add up to the amount the panel says so and by
 * how much; it never shows a partial list as if it were complete.
 */
export function LinesPanel({ runId, row, column, keyIndex, title, keyLabel, onClose }: LinesPanelProps) {
  const id = `${runId}/${row}/${column}/${keyIndex ?? "all"}`;
  const first = useLoad((signal) => drillLines(runId, row, column, keyIndex === null ? {} : { key: keyIndex }, signal), id);
  const [more, setMore] = useState<LinePage[]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const current = useRef(id);
  current.current = id;

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setLoadingMore(false);
  }, [id, first.data]);

  const pages = first.data ? [first.data, ...more] : [];
  const last = pages[pages.length - 1];

  const loadMore = async () => {
    if (!last?.nextCursor) return;
    const requested = id;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await drillLines(runId, row, column, { key: keyIndex ?? undefined, cursor: last.nextCursor });
      if (current.current === requested) setMore((prev) => [...prev, page]);
    } catch (error) {
      if (current.current === requested) setMoreError(describeProblem(error));
    } finally {
      if (current.current === requested) setLoadingMore(false);
    }
  };

  let body;
  if (first.error) {
    body = failure(first.error, first.reload);
  } else if (!first.data || first.loading) {
    body = <DataTable label="Journal lines" columns={lineColumns} rows={[]} getRowKey={(l) => `${l.key}/${l.postingSequence}/${l.lineNumber}`} loading />;
  } else if (first.data.computed) {
    body = <p className={styles.note}>This amount is calculated from other rows or columns, so no journal lines sit behind it.</p>;
  } else {
    const items = pages.flatMap((p) => p.items);
    const columns = new Set(items.map((l) => l.ledgerId)).size > 1 ? [ledgerColumn, ...lineColumns] : lineColumns;
    body = (
      <>
        {last.reconciled ? (
          <p className={styles.note}>
            The journal lines sum to <span className={styles.amount}>{displayAmount(last.linesTotal)}</span>, the amount shown.
          </p>
        ) : (
          <Alert tone="warning" title="The journal lines do not add up to this amount" live="alert">
            They sum to {last.linesTotal}, not {last.value}: {last.difference} is not explained by lines in the reporting read model
            ({last.unreconciledKeys} {last.unreconciledKeys === 1 ? "balance" : "balances"} affected).
          </Alert>
        )}
        <DataTable
          label="Journal lines"
          columns={columns}
          rows={items}
          getRowKey={(l) => `${l.key}/${l.postingSequence}/${l.lineNumber}`}
          empty={{ title: "No journal lines", description: "No lines in the reporting read model sit behind this amount." }}
        />
        {moreError ? failure(moreError, loadMore) : null}
        <div className={styles.drillFooter}>
          <p className={styles.note}>
            Showing {items.length} {items.length === 1 ? "line" : "lines"}, running sum{" "}
            <span className={styles.amount}>{displayAmount(last.cumulativeSum)}</span>,{" "}
            {last.hwm > 0 ? `as of posting sequence ${last.hwm}` : "each ledger as of its posting sequence in the run"}.
          </p>
          {last.nextCursor ? (
            <Button size="sm" onClick={loadMore} loading={loadingMore}>
              Load more lines
            </Button>
          ) : null}
        </div>
      </>
    );
  }

  return (
    <Card
      title={`Journal lines for ${keyLabel ? `${title}, ${keyLabel}` : title}`}
      actions={
        <>
          <Badge tone="neutral">Drill level 2</Badge>
          <Button size="sm" variant="ghost" onClick={onClose}>
            Close lines
          </Button>
        </>
      }
    >
      <div className={styles.stack}>
        <nav aria-label="Drill path">
          <ol className={styles.breadcrumb}>
            <li>Statement</li>
            <li>{title}</li>
            <li>Balances</li>
            <li aria-current="location">{keyLabel ? `Lines of ${keyLabel}` : "All lines"}</li>
          </ol>
        </nav>
        {body}
      </div>
    </Card>
  );
}
