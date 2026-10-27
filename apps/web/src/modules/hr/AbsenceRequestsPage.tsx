import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  cancelAbsenceRequest,
  decideAbsenceRequest,
  describeProblem,
  listAbsenceRequests,
  type AbsenceRequest,
  type AbsenceRequestStatus,
  type Problem,
} from "./api/client";
import { absenceUnitLabel, requestStatusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Action = "APPROVE" | "REJECT" | "CANCEL";
const STATUSES: (AbsenceRequestStatus | "")[] = ["SUBMITTED", "APPROVED", "REJECTED", "CANCELED", ""];

export function RequestStatus({ status }: { status: string }) {
  const known = requestStatusLabel[status];
  return <Badge tone={known?.tone ?? "neutral"}>{known?.label ?? status}</Badge>;
}

/**
 * Absence requests, oldest first (FR-42.7, MSS approvals). Managers and absence
 * administrators see the tenant's requests; an employee sees their own.
 */
export function AbsenceRequestsPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("status");
  const status = (raw === null ? "SUBMITTED" : STATUSES.includes(raw as AbsenceRequestStatus) ? raw : "") as AbsenceRequestStatus | "";
  const state = useLoad((signal) => listAbsenceRequests({ status }, signal), `requests|${status}`);
  const [more, setMore] = useState<{ status: string; items: AbsenceRequest[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [busy, setBusy] = useState<{ id: string; action: Action } | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const extra = more && more.status === status ? more : null;
  const rows = [...(state.data?.items ?? []), ...(extra?.items ?? [])];
  const cursor = extra ? extra.cursor : state.data?.nextCursor;

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setFailure(null);
    try {
      const next = await listAbsenceRequests({ status, cursor });
      setMore({ status, items: [...(extra?.items ?? []), ...next.items], cursor: next.nextCursor });
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  async function act(row: AbsenceRequest, action: Action) {
    setBusy({ id: row.id, action });
    setFailure(null);
    setNotice(null);
    try {
      const out = action === "CANCEL" ? await cancelAbsenceRequest(row.id) : await decideAbsenceRequest(row.id, action);
      setNotice(`${row.workerName}'s request from ${row.from} is now ${(requestStatusLabel[out.status]?.label ?? out.status).toLowerCase()}.`);
      setMore(null);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<AbsenceRequest>[] = [
    {
      key: "workerName",
      header: "Worker",
      render: (row) => (
        <Link to={`/hr/workers/${row.workerId}`}>
          {row.workerName} ({row.personnelNumber})
        </Link>
      ),
    },
    { key: "planCode", header: "Plan", mono: true },
    { key: "from", header: "From", mono: true },
    { key: "to", header: "To", mono: true },
    { key: "quantity", header: "Quantity", mono: true, align: "end", render: (row) => `${row.quantity} ${absenceUnitLabel[row.unit] ?? row.unit}` },
    { key: "status", header: "Status", render: (row) => <RequestStatus status={row.status} /> },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.status === "SUBMITTED" ? (
          <div className={styles.rowActions}>
            {(["APPROVE", "REJECT", "CANCEL"] as Action[]).map((action) => (
              <Button
                key={action}
                size="sm"
                variant={action === "APPROVE" ? "primary" : action === "REJECT" ? "danger" : "ghost"}
                loading={busy?.id === row.id && busy.action === action}
                disabled={busy !== null}
                aria-label={`${action === "APPROVE" ? "Approve" : action === "REJECT" ? "Reject" : "Cancel"} ${row.workerName}'s request from ${row.from}`}
                onClick={() => void act(row, action)}
              >
                {action === "APPROVE" ? "Approve" : action === "REJECT" ? "Reject" : "Cancel"}
              </Button>
            ))}
          </div>
        ) : null,
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Absence requests"
        description="Approving spends the balance, carry-over first, and tells time and payroll. You can't approve your own request."
      />
      <div className={styles.toolbar}>
        <FormField id="request-status" label="Status">
          {(control) => (
            <Select
              {...control}
              value={status}
              onChange={(event) => {
                setMore(null);
                setParams({ status: event.target.value }, { replace: true });
              }}
            >
              <option value="SUBMITTED">Submitted</option>
              <option value="APPROVED">Approved</option>
              <option value="REJECTED">Rejected</option>
              <option value="CANCELED">Cancelled</option>
              <option value="">All</option>
            </Select>
          )}
        </FormField>
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="That didn't work" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load absence requests" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Absence requests"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading && !state.data}
          empty={
            status === "SUBMITTED"
              ? { title: "Nothing waiting for a decision", description: "New requests appear here when they're submitted." }
              : { title: "No requests", description: "Try another status." }
          }
        />
      )}
      {!state.error && !state.loading && cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more requests
          </Button>
        </div>
      ) : null}
    </div>
  );
}
