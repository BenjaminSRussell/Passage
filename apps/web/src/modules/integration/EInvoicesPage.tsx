import { useNavigate } from "react-router-dom";
import { Badge, ListPage, type BadgeTone, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { EntityName } from "../../shell/lookup/EntityName";
import { entityText } from "../../shell/ui";
import { EINVOICE_MANDATES, EINVOICE_STATUSES, listEInvoices, type EInvoice, type EInvoiceMandate, type EInvoiceStatus } from "./api/client";
import { formatDateTime } from "./format";
import { useLoad } from "./useLoad";

export const EINVOICE_STATUS_LABELS: Record<EInvoiceStatus, { label: string; tone: BadgeTone }> = {
  VALID: { label: "Ready to submit", tone: "info" },
  INVALID: { label: "Has blocking issues", tone: "danger" },
  SUBMITTING: { label: "Being submitted", tone: "warning" },
  SUBMITTED: { label: "Submitted", tone: "info" },
  ACCEPTED: { label: "Accepted", tone: "success" },
  REJECTED: { label: "Rejected", tone: "danger" },
};

export const MANDATE_LABELS: Record<EInvoiceMandate, string> = {
  PEPPOL: "Peppol (UBL 2.1)",
  IT_SDI: "Italy SDI",
  MX_CFDI: "Mexico CFDI",
  FACTURX: "Factur-X / ZUGFeRD",
};

const columns: ListColumn<EInvoice>[] = [
  { key: "number", header: "Invoice", mono: true },
  {
    key: "documentType",
    header: "Type",
    render: (row) => (row.documentType === "CREDIT_NOTE" ? "Credit note" : "Invoice"),
    sortValue: (row) => row.documentType,
    csv: (row) => (row.documentType === "CREDIT_NOTE" ? "Credit note" : "Invoice"),
  },
  { key: "mandate", header: "Mandate", render: (row) => MANDATE_LABELS[row.mandate] ?? row.mandate, sortValue: (row) => MANDATE_LABELS[row.mandate] ?? row.mandate, csv: (row) => MANDATE_LABELS[row.mandate] ?? row.mandate },
  {
    key: "status",
    header: "Status",
    render: (row) => {
      const s = EINVOICE_STATUS_LABELS[row.status] ?? { label: row.status, tone: "neutral" as const };
      return <Badge tone={s.tone}>{s.label}</Badge>;
    },
    sortValue: (row) => EINVOICE_STATUS_LABELS[row.status]?.label ?? row.status,
    csv: (row) => EINVOICE_STATUS_LABELS[row.status]?.label ?? row.status,
  },
  {
    key: "legalEntityId",
    header: "Legal entity",
    sortable: false,
    render: (row) => <EntityName entity="legalEntity" id={row.legalEntityId} />,
    csv: (row) => entityText("legalEntity", row.legalEntityId),
  },
  { key: "reference", header: "Network reference", mono: true, render: (row) => row.reference ?? "—", sortValue: (row) => row.reference ?? "", csv: (row) => row.reference ?? "" },
  { key: "updatedAt", header: "Updated", render: (row) => formatDateTime(row.updatedAt), sortValue: (row) => row.updatedAt, csv: (row) => row.updatedAt },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: EINVOICE_STATUSES.map((s) => ({ value: s, label: EINVOICE_STATUS_LABELS[s].label })) },
  { kind: "select", key: "mandate", label: "Mandate", anyLabel: "Any mandate", options: EINVOICE_MANDATES.map((m) => ({ value: m, label: MANDATE_LABELS[m] })) },
];

function useEInvoiceRows(query: ListQuery): ListData<EInvoice> {
  const status = (query.filters.status ?? "") as EInvoiceStatus | "";
  const mandate = (query.filters.mandate ?? "") as EInvoiceMandate | "";
  const state = useLoad((signal) => listEInvoices({ status, mandate }, signal), `einvoices|${status}|${mandate}`);
  return {
    rows: state.data ?? [],
    loading: state.loading,
    loadingMore: false,
    error: state.error ?? undefined,
    forbidden: state.error?.code === "AUTH-0003",
    hasMore: false,
    loadMore: () => undefined,
    retry: state.reload,
  };
}

/** The e-invoices built from posted billing invoices, per mandate, with where each one stands with its network (35-2). */
export function EInvoicesPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="integration-einvoices"
      title="E-invoices"
      description="The legal e-invoice of each posted invoice for the mandate of its customer, built automatically when the invoice posts. Open one to see its issues, submit it to the access point and record the network's answer."
      noun="e-invoices"
      singular="e-invoice"
      columns={columns}
      filters={filters}
      useRows={useEInvoiceRows}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/integration/einvoices/${row.id}`)}
      empty={{ title: "No e-invoices yet", description: "Register a customer for a mandate and its invoices are built here as they post." }}
    />
  );
}
