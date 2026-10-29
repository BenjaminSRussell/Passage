import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  getAsset,
  getSchedule,
  listAdditions,
  listPolicies,
  listTransactions,
  listTransfers,
  type Addition,
  type Book,
  type InsurancePolicy,
  type ScheduleLine,
  type Transaction,
  type Transfer,
} from "./api/client";
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
  { key: "lastDepreciatedPeriod", header: "Depreciated to", mono: true, render: (b) => b.lastDepreciatedPeriod ?? dash },
  { key: "cost", header: "Cost", align: "end", render: (b) => money(b.cost, b.currency) },
  { key: "accumulatedDepreciation", header: "Accumulated", align: "end", render: (b) => money(b.accumulatedDepreciation, b.currency) },
  { key: "nbv", header: "NBV", align: "end", render: (b) => money(b.nbv, b.currency) },
  { key: "status", header: "Status", render: (b) => <StatusBadge status={b.status} /> },
  {
    key: "groupCode",
    header: "Group asset",
    mono: true,
    render: (b) => (b.groupAssetId ? <Link to={`/assets/group-assets/${b.groupAssetId}`}>{b.groupCode ?? b.groupAssetId}</Link> : dash),
  },
];

/** Transfers and splits the asset took part in, as source or as target (FR-37.7). */
function transferColumns(assetId: string): DataTableColumn<Transfer>[] {
  return [
    { key: "transferDate", header: "Date", mono: true },
    { key: "kind", header: "Kind", render: (t) => sentence(t.kind) },
    {
      key: "sourceAssetNumber",
      header: "From",
      mono: true,
      render: (t) => (t.sourceAssetId === assetId ? t.sourceAssetNumber : <Link to={`/assets/${t.sourceAssetId}`}>{t.sourceAssetNumber}</Link>),
    },
    {
      key: "targets",
      header: "To",
      render: (t) => (
        <span className={styles.actions}>
          {t.targets.map((x) =>
            x.asset.id === assetId ? (
              <span key={x.asset.id} className={styles.mono}>{`${x.asset.assetNumber} (${x.percentage}%)`}</span>
            ) : (
              <Link key={x.asset.id} className={styles.mono} to={`/assets/${x.asset.id}`}>{`${x.asset.assetNumber} (${x.percentage}%)`}</Link>
            ),
          )}
        </span>
      ),
    },
  ];
}

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

const additionColumns: DataTableColumn<Addition>[] = [
  { key: "addedDate", header: "Date", mono: true },
  { key: "type", header: "Type" },
  { key: "description", header: "Description" },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "serialNumber", header: "Serial number", mono: true, render: (a) => a.serialNumber ?? dash },
  { key: "cost", header: "Cost posted", align: "end", render: (a) => (a.financial && a.currency ? `${money(a.cost, a.currency)} on ${a.transactions.length} ${a.transactions.length === 1 ? "book" : "books"}` : "Descriptive") },
];

const policyColumns: DataTableColumn<InsurancePolicy>[] = [
  { key: "policyNumber", header: "Policy", mono: true },
  { key: "insuranceType", header: "Type", render: (p) => sentence(p.insuranceType) },
  { key: "coverageTo", header: "Coverage", mono: true, render: (p) => `${p.coverageFrom} to ${p.coverageTo}` },
  { key: "sumInsured", header: "Sum insured", align: "end", render: (p) => money(p.sumInsured, p.currency) },
  { key: "indexedValue", header: "Indexed value", align: "end", render: (p) => (p.indexedValue ? money(p.indexedValue, p.currency) : dash) },
  { key: "underInsured", header: "Cover", render: (p) => (p.indexedValue ? <Badge tone={p.underInsured ? "warning" : "success"}>{p.underInsured ? "Under-insured" : "Covered"}</Badge> : dash) },
];

/** A list card with its own loading, error and empty states. */
function ListCard<T>({ title, label, state, columns, rowKey, empty }: {
  title: string;
  label: string;
  state: { data: T[] | null; loading: boolean; error: { message: string; code: string } | null; reload(): void };
  columns: DataTableColumn<T>[];
  rowKey(row: T): string;
  empty: { title: string; description: string };
}) {
  return (
    <Card title={title}>
      {state.error ? (
        <Alert tone="danger" title={`Could not load ${label.toLowerCase()}`} live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable label={label} columns={columns} rows={state.data ?? []} getRowKey={rowKey} loading={state.loading} empty={empty} />
      )}
    </Card>
  );
}

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
  const additions = useLoad((signal) => listAdditions(id, signal), id);
  const policies = useLoad((signal) => listPolicies(id, signal), id);
  const transfers = useLoad((signal) => listTransfers(id, signal), id);
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
              <Link className={buttonClassName("secondary")} to={`/assets/${asset.id}/adjust`}>
                Adjust
              </Link>
            ) : null}
            {asset.status === "NotYetAcquired" || asset.status === "Open" ? (
              <>
                <Link className={buttonClassName("secondary")} to={`/assets/${asset.id}/additions/new`}>
                  Add addition
                </Link>
                <Link className={buttonClassName("secondary")} to={`/assets/${asset.id}/insurance/new`}>
                  Add policy
                </Link>
              </>
            ) : null}
            {asset.status === "Open" ? (
              <>
                <Link className={buttonClassName("secondary")} to={`/assets/${asset.id}/transfer`}>
                  Transfer or split
                </Link>
                <Link className={buttonClassName("secondary")} to={`/assets/${asset.id}/dispose`}>
                  Dispose
                </Link>
              </>
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
            empty={{ title: "No transactions yet", description: "Acquisitions, adjustments, depreciation and disposals appear here, one row per book." }}
          />
        )}
      </Card>
      <ListCard
        title="Additions and components"
        label="Additions"
        state={additions}
        columns={additionColumns}
        rowKey={(a) => a.id}
        empty={{ title: "No additions yet", description: "Improvements, components and accessories recorded under the asset appear here." }}
      />
      <ListCard
        title="Insurance policies"
        label="Insurance policies"
        state={policies}
        columns={policyColumns}
        rowKey={(p) => p.id}
        empty={{ title: "No insurance policies", description: "Record a policy to track the insured value against the indexed replacement value." }}
      />
      <ListCard
        title="Transfers and splits"
        label="Transfers"
        state={transfers}
        columns={transferColumns(asset.id)}
        rowKey={(t) => t.id}
        empty={{ title: "No transfers", description: "Transfers and splits from or into this asset appear here." }}
      />
      <div>
        <Button variant="ghost" onClick={() => navigate("/assets")}>
          Back to the register
        </Button>
      </div>
    </div>
  );
}
