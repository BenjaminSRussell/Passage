import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, expireTenders, listTenders, TENDER_STATUSES, type Problem, type Tender, type TenderStatus } from "./api/client";
import { dash, formatDateTime, shortId, stateTone, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<Tender>[] = [
  { key: "id", header: "Tender", mono: true, render: (row) => shortId(row.id) },
  { key: "loadId", header: "Load", mono: true, render: (row) => <span title={row.loadId}>{shortId(row.loadId)}</span> },
  { key: "status", header: "Status", render: (row) => <Badge tone={stateTone(row.status)}>{statusLabel(row.status)}</Badge> },
  {
    key: "carrier",
    header: "Carrier",
    mono: true,
    render: (row) => {
      const carrier = row.acceptedCarrierId ?? row.pendingCarrierId;
      return carrier ? <span title={carrier}>{shortId(carrier)}</span> : dash;
    },
  },
  { key: "rank", header: "Rank", align: "end", render: (row) => `${row.attempts.length} of ${row.carriers.length}` },
  {
    key: "expires",
    header: "Expires",
    render: (row) =>
      row.pendingExpiresAt ? (
        <span className={styles.flags}>
          {formatDateTime(row.pendingExpiresAt)}
          {row.overdue ? <Badge tone="warning">Overdue</Badge> : null}
        </span>
      ) : (
        dash
      ),
  },
  { key: "createdAt", header: "Started", render: (row) => formatDateTime(row.createdAt) },
];

/** Tender board (PRD 6.34.6): loads offered to carriers in route-guide order, and who is answering. */
export function TendersPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<TenderStatus | "">("");
  const [sweeping, setSweeping] = useState(false);
  const [sweep, setSweep] = useState<{ count: number } | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const state = useLoad((signal) => listTenders({ status }, signal), `tenders:${status}`);

  async function cascade() {
    setSweeping(true);
    setFailure(null);
    setSweep(null);
    try {
      const advanced = await expireTenders();
      setSweep({ count: advanced.length });
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setSweeping(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Tenders"
        description="Each load goes to one carrier at a time in route-guide order. A carrier that declines or does not answer before the deadline passes the load to the next; after the last one the tender fails and a planner picks it up."
        actions={
          <>
            <Button onClick={cascade} loading={sweeping}>
              Cascade overdue tenders
            </Button>
            <Link className={buttonClassName("primary")} to="/transport/tenders/new">
              Start tender
            </Link>
          </>
        }
      />
      <Alert tone="success" live="status">
        {sweep ? (sweep.count === 0 ? "No tender was past its deadline." : `${sweep.count} overdue ${sweep.count === 1 ? "tender was" : "tenders were"} passed to the next carrier or failed.`) : null}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="Could not cascade overdue tenders" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <div className={styles.toolbar}>
        <FormField id="tender-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as TenderStatus | "")}>
              <option value="">All</option>
              {TENDER_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load tenders" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Tenders"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status === "" ? "No tenders yet" : `No ${statusLabel(status).toLowerCase()} tenders`,
            description: "Start a tender to offer a load to your route-guide carriers.",
          }}
          onRowOpen={(row) => navigate(`/transport/tenders/${row.id}`)}
        />
      )}
    </div>
  );
}
