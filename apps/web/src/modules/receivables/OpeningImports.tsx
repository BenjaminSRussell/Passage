import { Link, useSearchParams } from "react-router-dom";
import { buttonClassName, DataTable, FormField, Input, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listOpeningImports, listOpenItems, type OpenItem, type OpeningImportSummary } from "./api/client";
import { dateTime, money } from "./format";
import { LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";

const PAGE = 500;
/** The label a legacy system is loaded under (open-items:batch importSource). */
const SOURCE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$/;

const summaryColumns: DataTableColumn<OpeningImportSummary>[] = [
  { key: "importSource", header: "Import source", mono: true },
  { key: "legalEntityId", header: "Legal entity", render: (row) => <EntityName entity="legalEntity" id={row.legalEntityId} /> },
  { key: "currency", header: "Currency", mono: true },
  { key: "debitCount", header: "Debit items", mono: true, align: "end" },
  { key: "debitOpen", header: "Debit open", mono: true, align: "end", render: (row) => money(row.debitOpen, row.currency) },
  { key: "creditCount", header: "Credit items", mono: true, align: "end" },
  { key: "creditOpen", header: "Credit open", mono: true, align: "end", render: (row) => money(row.creditOpen, row.currency) },
  { key: "netOpen", header: "Net open", mono: true, align: "end", render: (row) => money(row.netOpen, row.currency) },
  { key: "lastImportedAt", header: "Last loaded", mono: true, render: (row) => dateTime(row.lastImportedAt) },
  {
    key: "items",
    header: "Items",
    render: (row) => (
      <Link className={buttonClassName("ghost", "sm")} to={`/receivables/opening-items?source=${encodeURIComponent(row.importSource)}`} aria-label={`View items from ${row.importSource}`}>
        View items
      </Link>
    ),
  },
];

const itemColumns: DataTableColumn<OpenItem>[] = [
  { key: "legacyKey", header: "Legacy key", mono: true },
  { key: "documentNumber", header: "Document", mono: true },
  { key: "sourceType", header: "Type" },
  { key: "side", header: "Side", render: (row) => row.side ?? "DEBIT" },
  { key: "customerId", header: "Customer", render: (row) => <EntityName entity="customer" id={row.customerId} /> },
  { key: "documentDate", header: "Document date", mono: true },
  { key: "dueDate", header: "Due", mono: true },
  { key: "amount", header: "Original", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "openAmount", header: "Open", mono: true, align: "end", render: (row) => money(row.openAmount, row.currency) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

/**
 * Imported opening items (integration handoff H5): what the ERP migration loader
 * loaded at cutover, per legacy system, with the totals to reconcile against the
 * legacy ledger and the items of one source. Credit-side items are negative.
 */
export function OpeningImportsPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("source") ?? "";
  const source = raw.trim();
  const sourceValid = source === "" || SOURCE.test(source);
  const summaries = useLoad((signal) => listOpeningImports(signal), "summary");
  const items = useLoad(
    (signal) => (source !== "" && sourceValid ? listOpenItems({ importSource: source, status: "all", limit: PAGE }, signal) : Promise.resolve([] as OpenItem[])),
    `${sourceValid ? source : ""}`,
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Imported opening items"
        description="Open items the migration loader brought over at cutover, by legacy system. Reload them from the loader; items settled in Passage are never overwritten."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/open-items">
            All open items
          </Link>
        }
      />
      {summaries.error ? (
        <LoadFailure title="Could not load the import totals" problem={summaries.error} retry={summaries.reload} />
      ) : (
        <DataTable
          label="Import totals"
          columns={summaryColumns}
          rows={summaries.data ?? []}
          getRowKey={(row) => `${row.importSource}|${row.legalEntityId}|${row.currency}`}
          loading={summaries.loading}
          empty={{ title: "Nothing imported yet", description: "Items appear here once the migration loader posts an open-items batch." }}
        />
      )}
      <div className={styles.toolbar}>
        <FormField id="oi-source" label="Import source" error={sourceValid ? undefined : "Use the legacy system's label, for example SAP-PRD."}>
          {(c) => (
            <Input
              {...c}
              className={styles.mono}
              value={raw}
              invalid={!sourceValid}
              onChange={(e) => setParams(e.target.value === "" ? {} : { source: e.target.value }, { replace: true })}
            />
          )}
        </FormField>
      </div>
      {source === "" || !sourceValid ? (
        <p className={styles.note}>Choose an import source above, or open one from the totals, to list its items.</p>
      ) : items.error ? (
        <LoadFailure title="Could not load the imported items" problem={items.error} retry={items.reload} />
      ) : (
        <DataTable
          label="Imported items"
          columns={itemColumns}
          rows={items.data ?? []}
          getRowKey={(row) => row.id}
          loading={items.loading}
          empty={{ title: "No items from this source", description: "Check the label, or run the loader's opening-item step." }}
        />
      )}
      {items.data && items.data.length >= PAGE ? <p className={styles.note}>Showing the first {PAGE} items of this source.</p> : null}
    </div>
  );
}
