import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { BUCKETS, fetchAging, listDisputes, listPayments, type AgingResponse, type Bucket } from "./api/client";
import { DAY, sumDecimals, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const PAGE = 500;

const BUCKET_LABELS: Record<Bucket, string> = {
  CURRENT: "Current",
  "1-30": "1-30 days",
  "31-60": "31-60 days",
  "61-90": "61-90 days",
  "91-180": "91-180 days",
  "OVER-180": "Over 180 days",
};

interface AgingRow {
  currency: string;
  cells: Record<Bucket, string>;
  total: string;
}

interface Overview {
  aging: AgingResponse;
  received: number;
  onAccount: number;
  disputes: number;
}

/** One row per currency, one column per bucket; empty buckets show 0.00. */
export function agingRows(aging: AgingResponse): AgingRow[] {
  const currencies = [...new Set(aging.totals.map((t) => t.currency))].sort();
  return currencies.map((currency) => {
    const cells = Object.fromEntries(BUCKETS.map((b) => [b, "0.00"])) as Record<Bucket, string>;
    for (const t of aging.totals) {
      if (t.currency === currency) cells[t.bucket] = sumDecimals([t.amount]);
    }
    return { currency, cells, total: sumDecimals(Object.values(cells)) };
  });
}

const columns: DataTableColumn<AgingRow>[] = [
  { key: "currency", header: "Currency", mono: true },
  ...BUCKETS.map(
    (b): DataTableColumn<AgingRow> => ({ key: b, header: BUCKET_LABELS[b], align: "end", mono: true, render: (row) => row.cells[b] }),
  ),
  { key: "total", header: "Total open", align: "end", mono: true },
];

function count(n: number): string {
  return n >= PAGE ? `${PAGE}+` : String(n);
}

/** AR workspace (PRD §5.30.6): aging by currency and bucket (FR-30.6) and the payments waiting for cash application. */
export function WorkspacePage() {
  const [asOf, setAsOf] = useState(today());
  const valid = DAY.test(asOf);
  const state = useLoad<Overview>(
    async (signal) => {
      const [aging, received, onAccount, disputes] = await Promise.all([
        fetchAging(valid ? asOf : undefined, signal),
        listPayments({ status: "RECEIVED", limit: PAGE }, signal),
        listPayments({ status: "ON_ACCOUNT", limit: PAGE }, signal),
        listDisputes({ status: "active", limit: PAGE }, signal),
      ]);
      return { aging, received: received.length, onAccount: onAccount.length, disputes: disputes.length };
    },
    valid ? asOf : "",
  );
  const rows = state.data ? agingRows(state.data.aging) : [];
  const overdue = rows.filter((r) => r.cells.CURRENT !== r.total).length;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="AR workspace"
        description="Open receivables by age, the customer payments waiting to be applied and the disputes being worked."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("primary")} to="/receivables/payments/new">
              Record payment
            </Link>
            <Link className={buttonClassName("secondary")} to="/receivables/payments">
              Payments
            </Link>
            <Link className={buttonClassName("secondary")} to="/receivables/open-items">
              Open items
            </Link>
            <Link className={buttonClassName("secondary")} to="/receivables/worklist">
              Collections worklist
            </Link>
          </div>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the AR workspace" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          <StatGroup label="Receivables tiles">
            <StatTile label="Payments to apply" value={state.data ? count(state.data.received) : "…"} tone="warning" />
            <StatTile label="Payments on account" value={state.data ? count(state.data.onAccount) : "…"} tone="warning" />
            <StatTile label="Currencies with overdue items" value={state.data ? overdue : "…"} tone="danger" />
            <StatTile label="Active disputes" value={state.data ? count(state.data.disputes) : "…"} tone="warning" />
          </StatGroup>
          <Card title="Aging" headingLevel={3}>
            <div className={styles.stack}>
              <div className={styles.toolbar}>
                <FormField id="ar-as-of" label="As of" error={valid ? undefined : "Enter a date as YYYY-MM-DD."}>
                  {(c) => <Input {...c} type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} invalid={!valid} />}
                </FormField>
              </div>
              <DataTable
                label="Aging by currency"
                columns={columns}
                rows={rows}
                getRowKey={(row) => row.currency}
                loading={state.loading}
                empty={{ title: "No open items", description: `Nothing is open as of ${state.data?.aging.asOf ?? asOf}.` }}
              />
              <p className={styles.note}>Buckets count whole days past the due date. Items not yet due are current.</p>
            </div>
          </Card>
        </>
      )}
    </div>
  );
}
