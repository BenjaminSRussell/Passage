import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { EntityName } from "../../shell/lookup/EntityName";
import { ListPage, type ListColumn, type ListData } from "../../shell/ui";
import { listPendingChanges, type PendingChange } from "./api/client";
import { formatDateTime, isUuid } from "./format";
import { useLoad } from "./useLoad";

const columns: ListColumn<PendingChange>[] = [
  { key: "customerId", header: "Customer", render: (c) => <EntityName entity="customer" id={c.customerId} />, sortValue: () => "" },
  { key: "legalEntityId", header: "Legal entity", render: (c) => <EntityName entity="legalEntity" id={c.legalEntityId} />, sortValue: () => "", defaultHidden: true },
  { key: "currentValue", header: "Current limit", align: "end", render: (c) => c.currentValue ?? "None", csv: (c) => c.currentValue ?? "" },
  { key: "requestedValue", header: "Requested limit", align: "end" },
  { key: "requestedBy", header: "Requested by" },
  { key: "requestedAt", header: "Requested", render: (c) => formatDateTime(c.requestedAt), csv: (c) => c.requestedAt },
];

interface MorePages {
  rows: PendingChange[];
  cursor?: string;
  loading: boolean;
}

/** The held increases, a page at a time. */
function usePendingRows(): ListData<PendingChange> {
  const [forbidden, setForbidden] = useState(false);
  const [more, setMore] = useState<MorePages | null>(null);
  const state = useLoad(async (signal) => {
    try {
      const page = await listPendingChanges(undefined, signal);
      if (!signal.aborted) setForbidden(false);
      return page;
    } catch (error) {
      if (!signal.aborted) setForbidden((error as { status?: number }).status === 403);
      throw error;
    }
  }, "pending-changes");
  const cursor = more ? more.cursor : state.data?.nextCursor;
  async function loadMore() {
    if (!cursor || more?.loading) return;
    setMore({ rows: more?.rows ?? [], cursor, loading: true });
    try {
      const page = await listPendingChanges(cursor);
      setMore({ rows: [...(more?.rows ?? []), ...page.items], cursor: page.nextCursor, loading: false });
    } catch {
      setMore({ rows: more?.rows ?? [], cursor, loading: false });
    }
  }
  return {
    rows: state.error ? [] : [...(state.data?.items ?? []), ...(more?.rows ?? [])],
    loading: state.loading,
    loadingMore: Boolean(more?.loading),
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

/** Credit-limit increases held for approval; open one to approve or reject it. A link with ?id= opens that change. */
export function PendingChangesPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const linked = params.get("id") ?? "";
  useEffect(() => {
    if (isUuid(linked)) navigate(`/party/pending-changes/${linked}`, { replace: true });
  }, [linked, navigate]);
  return (
    <ListPage
      id="party-pending-changes"
      title="Credit limit approvals"
      description="Credit-limit increases held for approval, newest first. The stored limit does not change until the request is approved."
      noun="changes"
      singular="change"
      columns={columns}
      useRows={usePendingRows}
      search={(c) => ({ number: c.requestedValue, name: c.requestedBy })}
      getRowKey={(c) => c.id}
      onRowOpen={(c) => navigate(`/party/pending-changes/${encodeURIComponent(c.id)}`)}
      empty={{ title: "No credit limit changes waiting", description: "A credit-limit increase above the approval threshold waits here until someone approves or rejects it." }}
    />
  );
}
