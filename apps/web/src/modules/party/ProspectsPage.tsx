import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Badge, ListPage, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { listProspects, PROSPECT_STATUSES, type Prospect, type ProspectStatus } from "./api/client";
import { formatDateTime } from "./format";
import { useLoad } from "./useLoad";

export const prospectStatusLabel: Record<ProspectStatus, string> = {
  NEW: "Waiting for review",
  ACCEPTED: "Accepted",
  REJECTED: "Rejected",
};

export const prospectStatusTone: Record<ProspectStatus, "info" | "success" | "neutral"> = {
  NEW: "info",
  ACCEPTED: "success",
  REJECTED: "neutral",
};

const columns: ListColumn<Prospect>[] = [
  { key: "companyName", header: "Company" },
  { key: "contactName", header: "Contact" },
  { key: "email", header: "Email" },
  { key: "country", header: "Country" },
  {
    key: "status",
    header: "Status",
    render: (p) => <Badge tone={prospectStatusTone[p.status]}>{prospectStatusLabel[p.status]}</Badge>,
    sortValue: (p) => prospectStatusLabel[p.status],
    csv: (p) => prospectStatusLabel[p.status],
  },
  { key: "createdAt", header: "Registered", render: (p) => formatDateTime(p.createdAt), csv: (p) => p.createdAt },
  { key: "taxId", header: "Tax number", defaultHidden: true },
  { key: "website", header: "Website", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: PROSPECT_STATUSES.map((s) => ({ value: s, label: prospectStatusLabel[s] })) },
];

interface MorePages {
  status: string;
  rows: Prospect[];
  cursor?: string;
  loading: boolean;
}

/** The registrations of the filter, newest first, a page at a time. */
function useProspectRows(query: ListQuery): ListData<Prospect> {
  const status = (query.filters.status ?? "") as ProspectStatus | "";
  const [forbidden, setForbidden] = useState(false);
  const [more, setMore] = useState<MorePages | null>(null);
  const state = useLoad(async (signal) => {
    try {
      const page = await listProspects(status, undefined, signal);
      if (!signal.aborted) setForbidden(false);
      return page;
    } catch (error) {
      if (!signal.aborted) setForbidden((error as { status?: number }).status === 403);
      throw error;
    }
  }, status);
  const extra = more && more.status === status ? more : null;
  const cursor = extra ? extra.cursor : state.data?.nextCursor;
  async function loadMore() {
    if (!cursor || extra?.loading) return;
    setMore({ status, rows: extra?.rows ?? [], cursor, loading: true });
    try {
      const page = await listProspects(status, cursor);
      setMore({ status, rows: [...(extra?.rows ?? []), ...page.items], cursor: page.nextCursor, loading: false });
    } catch {
      setMore({ status, rows: extra?.rows ?? [], cursor, loading: false });
    }
  }
  return {
    rows: state.error ? [] : [...(state.data?.items ?? []), ...(extra?.rows ?? [])],
    loading: state.loading,
    loadingMore: Boolean(extra?.loading),
    error: state.error ?? undefined,
    forbidden,
    hasMore: Boolean(cursor),
    loadMore: () => void loadMore(),
    retry: () => {
      setMore(null);
      state.reload();
    },
  };
}

/** Supplier registrations from the public portal page, for a buyer to accept as a vendor or reject (#511). */
export function ProspectsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="party-vendor-prospects"
      title="Supplier registrations"
      description="Companies that registered themselves on the supplier portal. Accept one to create its party and a blocked vendor role with an onboarding questionnaire, or reject it with a note."
      noun="registrations"
      singular="registration"
      columns={columns}
      filters={filters}
      useRows={useProspectRows}
      search={(p) => ({ number: p.email, name: `${p.companyName} ${p.contactName}` })}
      getRowKey={(p) => p.id}
      onRowOpen={(p) => navigate(`/party/vendor-prospects/${encodeURIComponent(p.id)}`)}
      empty={{ title: "No supplier registrations", description: "Registrations from the supplier portal's public page appear here for review." }}
    />
  );
}
