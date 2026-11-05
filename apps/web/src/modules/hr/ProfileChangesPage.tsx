import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  DataTable,
  FormField,
  PageHeader,
  Select,
  type DataTableColumn,
} from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  decideProfileChange,
  listProfileChanges,
  type ChangeQueue,
  type ProfileChange,
  type ProfileChangeStatus,
} from "./api/selfService";
import { dayOf } from "./format";
import styles from "./pages.module.css";
import { ChangeStatus, changeKindLabel } from "./ProfileChangesCard";
import { useLoad } from "./useLoad";

const STATUSES: (ProfileChangeStatus | "")[] = ["PENDING", "APPROVED", "REJECTED", "CANCELED", "APPLIED", ""];

const copy: Record<ChangeQueue, { title: string; description: string }> = {
  team: {
    title: "Team changes",
    description: "Home address changes from people who report to you. HR approves bank account changes.",
  },
  hr: {
    title: "Profile changes",
    description: "Every self-service change in the tenant. Approving writes the new value to the protected record. You can't decide your own.",
  },
};

/** MSS and HR approval queue for self-service profile changes. The service limits the team queue to direct reports. */
export function ProfileChangesPage({ queue }: { queue: ChangeQueue }) {
  const [params, setParams] = useSearchParams();
  const raw = params.get("status");
  const status = (raw === null ? "PENDING" : STATUSES.includes(raw as ProfileChangeStatus) ? raw : "") as ProfileChangeStatus | "";
  const state = useLoad((signal) => listProfileChanges(queue, { status }, signal), `changes|${queue}|${status}`);
  const [busy, setBusy] = useState<{ id: string; decision: "APPROVE" | "REJECT" } | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  async function decide(row: ProfileChange, decision: "APPROVE" | "REJECT") {
    setBusy({ id: row.id, decision });
    setFailure(null);
    setNotice(null);
    try {
      await decideProfileChange(queue, row.id, decision);
      setNotice(`${row.workerName}'s ${changeKindLabel[row.kind].toLowerCase()} change is ${decision === "APPROVE" ? "approved" : "rejected"}.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<ProfileChange>[] = [
    { key: "workerName", header: "Worker", render: (row) => `${row.workerName} (${row.personnelNumber})` },
    { key: "kind", header: "Change", render: (row) => changeKindLabel[row.kind] ?? row.kind },
    { key: "summary", header: "New value", mono: true },
    { key: "createdAt", header: "Requested", mono: true, render: (row) => dayOf(row.createdAt) },
    { key: "status", header: "Status", render: (row) => <ChangeStatus status={row.status} /> },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.status === "PENDING" ? (
          <div className={styles.rowActions}>
            {(["APPROVE", "REJECT"] as const).map((decision) => (
              <Button
                key={decision}
                size="sm"
                variant={decision === "APPROVE" ? "primary" : "danger"}
                loading={busy?.id === row.id && busy.decision === decision}
                disabled={busy !== null}
                aria-label={`${decision === "APPROVE" ? "Approve" : "Reject"} ${row.workerName}'s ${changeKindLabel[row.kind].toLowerCase()} change`}
                onClick={() => void decide(row, decision)}
              >
                {decision === "APPROVE" ? "Approve" : "Reject"}
              </Button>
            ))}
          </div>
        ) : null,
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title={copy[queue].title} description={copy[queue].description} />
      <div className={styles.toolbar}>
        <FormField id="change-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setParams({ status: event.target.value }, { replace: true })}>
              <option value="PENDING">Waiting for approval</option>
              <option value="APPROVED">Approved</option>
              <option value="REJECTED">Rejected</option>
              <option value="CANCELED">Withdrawn</option>
              <option value="APPLIED">Applied</option>
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
        <Alert tone="danger" title="Could not load changes" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label={copy[queue].title}
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading && !state.data}
          empty={status === "PENDING" ? { title: "Nothing waiting for a decision", description: "New changes appear here when they're sent." } : { title: "No changes", description: "Try another status." }}
        />
      )}
    </div>
  );
}

export function TeamChangesPage() {
  return <ProfileChangesPage queue="team" />;
}

export function HrProfileChangesPage() {
  return <ProfileChangesPage queue="hr" />;
}
