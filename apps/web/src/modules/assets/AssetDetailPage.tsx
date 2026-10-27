import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getAsset, getSchedule, listTransactions, type Book, type ScheduleLine, type Transaction } from "./api/client";
import { StatusBadge } from "./badges";
import { dash, money, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

const bookColumns: DataTableColumn<Book>[] = [
  { key: "bookCode", header: "Book", mono: true },
  { key: "method", header: "Method", render: (b) => `${sentence(b.method)}${b.method === "DecliningBalance" ? ` ${b.factor}×${b.switchToStraightLine ? ", switch to SL" : ""}` : ""}` },
  { key: "serviceLifePeriods", header: "Life", render: (b) => `${b.serviceLifePeriods} ${b.method === "StraightLine" ? "months" : "years"}` },
  { key: "convention", header: "Convention", render: (b) => sentence(b.convention) },
  { key: "inServiceDate", header: "In service", mono: true },
  { key: "cost", header: "Cost", align: "end", render: (b) => money(b.cost, b.currency) },
  { key: "accumulatedDepreciation", header: "Accumulated", align: "end", render: (b) => money(b.accumulatedDepreciation, b.currency) },
  { key: "nbv", header: "NBV", align: "end", render: (b) => money(b.nbv, b.currency) },
  { key: "status", header: "Status", render: (b) => <StatusBadge status={b.status} /> },
];

const scheduleColumns: DataTableColumn<ScheduleLine>[] = [
  { key: "period", header: "Period", mono: true },
  { key: "amount", header: "Depreciation", align: "end" },
  { key: "accumulated", header: "Accumulated", align: "end" },
  { key: "nbv", header: "NBV", align: "end" },
];

const transactionColumns: DataTableColumn<Transaction>[] = [
  { key: "transactionDate", header: "Date", mono: true },
  { key: "bookCode", header: "Book", mono: true },
  { key: "type", header: "Type", render: (t) => sentence(t.type) },
  { key: "costAmount", header: "Cost", align: "end", render: (t) => money(t.costAmount, t.currency) },
  { key: "accumulatedAmount", header: "Accumulated", align: "end", render: (t) => money(t.accumulatedAmount, t.currency) },
  { key: "proceedsAmount", header: "Proceeds", align: "end", render: (t) => money(t.proceedsAmount, t.currency) },
  { key: "gainLoss", header: "Gain or loss", align: "end", render: (t) => money(t.gainLoss, t.currency) },
];

/** Projected depreciation of one book, computed by the service on read (FR-37.3, FR-37.4). */
function ScheduleCard({ assetId, book }: { assetId: string; book: Book }) {
  const state = useLoad((signal) => getSchedule(assetId, book.id, signal), `${assetId}|${book.id}`);
  return (
    <Card title={`Depreciation schedule: ${book.bookCode}`}>
      <div className={styles.stack}>
        {state.error ? (
          <Alert tone="danger" title="Could not load the schedule" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : (
          <>
            <p className={styles.note} role="status">
              {state.data ? `${state.data.lines.length} periods, total ${money(state.data.total, state.data.currency)}.` : "Loading the schedule…"}
            </p>
            <DataTable
              label={`Schedule for ${book.bookCode}`}
              columns={scheduleColumns}
              rows={state.data?.lines ?? []}
              getRowKey={(l) => String(l.index)}
              loading={state.loading}
              empty={{ title: "Nothing left to depreciate", description: "A sold or scrapped book has no remaining schedule." }}
            />
          </>
        )}
      </div>
    </Card>
  );
}

/** Asset detail: master data, books with NBV, schedule per book and the transaction log. */
export function AssetDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getAsset(id, signal), id);
  const transactions = useLoad((signal) => listTransactions(id, signal), id);
  const [selected, setSelected] = useState<string | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Asset" />
        <Alert tone="danger" title="Could not load the asset" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/assets">
          Back to the register
        </Link>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Asset" />
        <span className="visually-hidden">Loading the asset…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const asset = state.data.value;
  const open = asset.books.filter((b) => b.status === "Open");
  const book = asset.books.find((b) => b.id === selected) ?? open[0] ?? asset.books[0];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${asset.assetNumber} ${asset.name}`}
        meta={<StatusBadge status={asset.status} />}
        actions={
          <div className={styles.actions}>
            {asset.status === "NotYetAcquired" || asset.status === "Open" ? (
              <Link className={buttonClassName("primary")} to={`/assets/${asset.id}/books/new`}>
                Add book
              </Link>
            ) : null}
            {asset.status === "Open" ? (
              <Link className={buttonClassName("secondary")} to={`/assets/${asset.id}/dispose`}>
                Dispose
              </Link>
            ) : null}
          </div>
        }
      />
      <Card title="Asset">
        <dl className={styles.details}>
          <Detail term="Asset number">
            <span className={styles.mono}>{asset.assetNumber}</span>
          </Detail>
          <Detail term="Class">
            <span className={styles.mono}>{asset.classCode}</span>
          </Detail>
          <Detail term="Barcode">
            <span className={styles.mono}>{asset.barcode ?? dash}</span>
          </Detail>
          <Detail term="Quantity">{asset.quantity}</Detail>
          <Detail term="In service">{asset.acquiredDate ?? dash}</Detail>
          <Detail term="Legal entity">
            <span className={styles.mono}>{asset.legalEntityId}</span>
          </Detail>
        </dl>
      </Card>
      <Card title="Books">
        <DataTable
          label="Books"
          columns={bookColumns}
          rows={asset.books}
          getRowKey={(b) => b.id}
          empty={{ title: "No books yet", description: "Add a book to acquire the asset and start its depreciation schedule." }}
          onRowOpen={(b) => setSelected(b.id)}
        />
      </Card>
      {book ? <ScheduleCard assetId={asset.id} book={book} /> : null}
      <Card title="Transactions">
        {transactions.error ? (
          <Alert tone="danger" title="Could not load transactions" live="alert" action={<Button size="sm" onClick={transactions.reload}>Try again</Button>}>
            {transactions.error.message} ({transactions.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Transactions"
            columns={transactionColumns}
            rows={transactions.data ?? []}
            getRowKey={(t) => t.id}
            loading={transactions.loading}
            empty={{ title: "No transactions yet", description: "Acquisitions and disposals appear here, one row per book." }}
          />
        )}
      </Card>
      <div>
        <Button variant="ghost" onClick={() => navigate("/assets")}>
          Back to the register
        </Button>
      </div>
    </div>
  );
}
