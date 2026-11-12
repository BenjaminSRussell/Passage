import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { ListPage, loadedRows, StatGroup, StatTile, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { deliveredNotInvoiced, FulfillmentApiError, type DniLine, type DniReport } from "./api/client";
import { formatDate, formatMoney, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";
import { OrderNumber } from "../sales/OrderNumber";

/** How many slips the page lists; the totals always cover every open slip. */
export const DNI_LINES = 500;

const columns: ListColumn<DniLine>[] = [
  { key: "packingSlipNumber", header: "Packing slip", mono: true },
  { key: "postingDate", header: "Posting date", render: (row) => formatDate(row.postingDate) },
  { key: "daysOutstanding", header: "Days outstanding", align: "end" },
  { key: "customerId", header: "Customer", render: (row) => <EntityName entity="customer" id={row.customerId} /> },
  { key: "orderId", header: "Order", render: (row) => <OrderNumber id={row.orderId} /> },
  { key: "amount", header: "Open on 1405", align: "end", render: (row) => formatMoney(row.amount, row.currency) },
];

const filters: ListFilterDef[] = [
  { kind: "entity", key: "legalEntityId", label: "Legal entity", entity: "legalEntity" },
  { kind: "entity", key: "customerId", label: "Customer", entity: "customer" },
  { kind: "text", key: "asOf", label: "As of (YYYY-MM-DD)" },
];

/**
 * Delivered not invoiced (FR-28.12, section 5.28.14): the open balance on
 * account 1405 of one legal entity as of a date, and the packing slips that make
 * it up. A slip leaves the report when it is invoiced or cancelled.
 */
export function DeliveredNotInvoicedPage() {
  const navigate = useNavigate();
  const [report, setReport] = useState<DniReport | null>(null);
  const [needEntity, setNeedEntity] = useState(true);
  const useRows = (query: ListQuery): ListData<DniLine> => {
    const legalEntityId = query.filters.legalEntityId ?? "";
    const customerId = query.filters.customerId ?? "";
    const asOf = query.filters.asOf || today();
    const state = useLoad<DniLine[] | null>(
      async (signal) => {
        setNeedEntity(!UUID.test(legalEntityId));
        if (!UUID.test(legalEntityId)) {
          setReport(null);
          return null;
        }
        if (!/^\d{4}-\d{2}-\d{2}$/.test(asOf)) throw new FulfillmentApiError(400, "FUL-FILTER-DATE", "Enter the report date as YYYY-MM-DD.", {});
        const found = await deliveredNotInvoiced({ legalEntityId, customerId: customerId || undefined, asOf, limit: DNI_LINES }, signal);
        setReport(found);
        return found.lines;
      },
      `${legalEntityId}/${customerId}/${asOf}`,
    );
    return loadedRows({ ...state, data: state.data ?? [] });
  };
  return (
    <ListPage
      id="fulfillment-dni"
      title="Delivered not invoiced"
      description="Cost of goods shipped on a posted packing slip and not yet invoiced: the balance on account 1405, as of today unless you pick a date. Slips posted with COGS at shipment never appear."
      notice={
        needEntity ? (
          <p className={styles.note}>Pick a legal entity in the filters to see its delivered-not-invoiced balance.</p>
        ) : report ? (
          <>
            {report.totals.length > 0 ? (
              <StatGroup label="Delivered not invoiced totals">
                {report.totals.map((t) => (
                  <StatTile key={t.currency} label={`Open on ${report.account} (${t.currency})`} value={formatMoney(t.amount, t.currency)} hint={`${t.count} packing slip${t.count === 1 ? "" : "s"}`} />
                ))}
              </StatGroup>
            ) : null}
            {report.lineCount > report.lines.length ? <p className={styles.note}>{`Showing the ${report.lines.length} oldest of ${report.lineCount} packing slips; the totals cover all of them.`}</p> : null}
          </>
        ) : null
      }
      noun="packing slips"
      singular="packing slip"
      columns={columns}
      filters={filters}
      useRows={useRows}
      search={(row) => ({ number: row.packingSlipNumber })}
      getRowKey={(row) => row.packingSlipId}
      onRowOpen={(row) => navigate(`/fulfillment/packing-slips/${row.packingSlipId}`)}
      empty={{ title: "Choose a legal entity", description: "Pick a legal entity in the filters to see its delivered-not-invoiced balance." }}
    />
  );
}
