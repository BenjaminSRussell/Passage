import { Link } from "react-router-dom";
import { Badge, buttonClassName, ListPage, type BadgeTone, type DataTableColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { listCharges, type ChargeRecord, type ChargeStatus } from "./api/postings";
import { amount } from "./format";
import { usePagedRows } from "./usePagedRows";

const tone: Record<ChargeStatus, BadgeTone> = { posted: "success", pending: "warning", suspense: "warning", compensated: "danger" };
const label: Record<ChargeStatus, string> = { posted: "Posted", pending: "Paused, can be resumed", suspense: "On suspense: no transfer price", compensated: "Reversed" };

const columns: DataTableColumn<ChargeRecord>[] = [
  { key: "source", header: "Source", mono: true, render: (c) => `${c.sourceId} v${c.version}` },
  { key: "pair", header: "Lender to borrower", mono: true, render: (c) => `${c.lendingEntity} → ${c.borrowingEntity}` },
  { key: "category", header: "Category", render: (c) => (c.resourceRole ? `${c.category}, ${c.resourceRole}` : c.category) },
  { key: "date", header: "Transaction date", mono: true, render: (c) => c.transactionDate },
  { key: "cost", header: "Cost", align: "end", render: (c) => (c.cost ? `${amount(c.cost)} ${c.currency}` : "—") },
  { key: "price", header: "Price", align: "end", render: (c) => (c.price ? `${amount(c.price)} ${c.currency}` : "—") },
  { key: "status", header: "Status", render: (c) => <Badge tone={tone[c.status]}>{label[c.status]}</Badge> },
];

const chargeFilters: ListFilterDef[] = [
  {
    kind: "select",
    key: "status",
    label: "Status",
    anyLabel: "All charges",
    options: [
      { value: "posted", label: "Posted" },
      { value: "suspense", label: "On suspense" },
      { value: "compensated", label: "Reversed" },
      { value: "pending", label: "Paused" },
    ],
  },
];

function useChargeRows(query: ListQuery): ListData<ChargeRecord> {
  const status = query.filters.status ?? "";
  return usePagedRows((after, signal) => listCharges({ status: status || undefined, after }, signal), status);
}

/** Every IC charge submitted, per source and version (FR-40.13, BR-40.12). */
export function ChargeLogPage() {
  return (
    <ListPage
      id="intercompany-charge-log"
      title="Intercompany charge log"
      description="A charge on suspense had no transfer price on its date, so nothing was posted. Add the price and submit the source again with the next version to post it."
      actions={
        <>
          <Link className={buttonClassName("primary")} to="/intercompany/charges">
            Price and post a charge
          </Link>
          <Link className={buttonClassName("secondary")} to="/intercompany/transfer-prices">
            Transfer prices
          </Link>
        </>
      }
      noun="charges"
      singular="charge"
      columns={columns}
      filters={chargeFilters}
      useRows={useChargeRows}
      getRowKey={(c) => c.id}
      search={(c) => ({ number: c.sourceId, name: c.resourceRole })}
      empty={{ title: "No charges yet", description: "Price a charge and post it to see it here." }}
    />
  );
}
