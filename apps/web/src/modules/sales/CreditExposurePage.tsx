import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Badge, ListPage, loadedRows, type BadgeTone, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { SalesApiError, UTILIZATION_STATUSES, listCreditExposure, type CreditExposureFilter, type CreditExposureRow, type UtilizationStatus } from "./api/client";
import { dash, money, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";
import { PlainName } from "./OrderNumber";

const PERCENT_2 = /^\d+(\.\d{1,2})?$/;

const tones: Record<UtilizationStatus, BadgeTone> = { NO_LIMIT: "neutral", WITHIN: "success", OVER_LIMIT: "warning", OVER_TOLERANCE: "danger" };

const labels: Record<UtilizationStatus, string> = {
  NO_LIMIT: "No limit",
  WITHIN: "Within limit",
  OVER_LIMIT: "Over limit",
  OVER_TOLERANCE: "Over tolerance",
};

const columns: ListColumn<CreditExposureRow>[] = [
  { key: "customerId", header: "Customer", render: (row) => <PlainName entity="customer" id={row.customerId} /> },
  { key: "legalEntityId", header: "Legal entity", render: (r) => <EntityName entity="legalEntity" id={r.legalEntityId} /> },
  { key: "limit", header: "Credit limit", align: "end", render: (r) => money(r.limit, r.currency) },
  { key: "limitType", header: "Limit type", render: (r) => sentence(r.limitType) },
  { key: "openAr", header: "Open AR", align: "end", render: (r) => money(r.exposure.openAr ?? "0", r.currency) },
  { key: "unapplied", header: "Unapplied", align: "end", render: (r) => money(r.exposure.unapplied ?? "0", r.currency) },
  { key: "dni", header: "Delivered, not invoiced", align: "end", render: (r) => money(r.exposure.deliveredNotInvoiced ?? "0", r.currency) },
  { key: "openOrders", header: "Open orders", align: "end", render: (r) => money(r.exposure.openOrders ?? "0", r.currency) },
  {
    key: "balance",
    header: "Counted against limit",
    align: "end",
    render: (r) => (r.understated ? `${money(r.balance, r.currency)} or more` : money(r.balance, r.currency)),
  },
  { key: "utilizationPct", header: "Used", align: "end", render: (r) => (r.utilizationPct === undefined ? dash : `${r.utilizationPct}%`) },
  { key: "status", header: "Status", render: (r) => <Badge tone={tones[r.status]}>{labels[r.status]}</Badge> },
];

const filters: ListFilterDef[] = [
  { kind: "entity", key: "legalEntityId", label: "Legal entity", entity: "legalEntity" },
  { kind: "entity", key: "customerId", label: "Customer", entity: "customer" },
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: UTILIZATION_STATUSES.map((s) => ({ value: s, label: labels[s] })) },
  { kind: "text", key: "minUtilizationPct", label: "Used at least (%)" },
];

/**
 * Credit exposure by customer (PRD §5.27.14): each customer's exposure against the credit
 * limit, highest use first, so a credit manager sees who is near or over the limit.
 */
export function CreditExposurePage() {
  const navigate = useNavigate();
  const [report, setReport] = useState<{ truncated: boolean; total: number; shown: number } | null>(null);
  const useRows = (query: ListQuery): ListData<CreditExposureRow> => {
    const f = query.filters;
    const filter: CreditExposureFilter = {
      legalEntityId: f.legalEntityId ?? "",
      customerId: f.customerId ?? "",
      status: (f.status ?? "") as UtilizationStatus | "",
      minUtilizationPct: f.minUtilizationPct ?? "",
    };
    const bad = filter.minUtilizationPct !== "" && !PERCENT_2.test(filter.minUtilizationPct ?? "");
    const state = useLoad(async (signal) => {
      if (bad) throw new SalesApiError(400, "SAL-FILTER-PERCENT", "Enter a percent such as 80 or 92.5.", {});
      const page = await listCreditExposure(filter, signal);
      setReport({ truncated: page.truncated, total: page.total, shown: page.items.length });
      return page.items;
    }, JSON.stringify(filter));
    return loadedRows(state);
  };
  return (
    <ListPage
      id="sales-credit-exposure"
      title="Credit exposure"
      description="Each customer's exposure against the credit limit, highest use first. The figure counted against the limit is the one the credit check starts from: open AR less unapplied cash, plus delivered goods not yet invoiced and open orders where the limit type counts them."
      notice={
        <>
          {report?.truncated ? (
            <Alert tone="warning" title="Partial ranking">
              This scope has more than 50,000 credit profiles, so only the first 50,000 were measured and some customers may be missing. Narrow it to one legal entity or customer.
            </Alert>
          ) : null}
          {report && report.total > report.shown ? <p className={styles.muted}>{`Showing ${report.shown} of ${report.total} customers.`}</p> : null}
        </>
      }
      noun="customers"
      singular="customer"
      columns={columns}
      filters={filters}
      useRows={useRows}
      getRowKey={(r) => `${r.legalEntityId}/${r.customerId}`}
      onRowOpen={(r) => navigate(`/sales/credit-profiles/${r.legalEntityId}/${r.customerId}`)}
      empty={{ title: "No customers match", description: "Credit profiles define the limits this report measures; change the filters or add a profile." }}
    />
  );
}
