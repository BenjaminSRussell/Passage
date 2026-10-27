import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listReservations,
  releaseReservation,
  RESERVATION_STATUSES,
  type Page,
  type Problem,
  type Reservation,
  type ReservationStatus,
} from "./api/client";
import { dash, describeDimensions, formatDateTime, sentence, shortId } from "./format";
import { ALL_DIMENSIONS, ProblemAlert, RowAction, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Reservation inquiry (PRD 3.16.14) with release of active reservations. */
export function ReservationsPage() {
  const [status, setStatus] = useState<ReservationStatus | "">("ACTIVE");
  const [attempt, setAttempt] = useState(0);
  const key = `${status}|${attempt}`;
  const state = useLoad((signal) => listReservations({ status }, signal), key);
  const [more, setMore] = useState<{ key: string; page: Page<Reservation> } | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [loadingMore, setLoadingMore] = useState(false);

  const extra = more && more.key === key ? more.page : null;
  const rows = [...(state.data?.items ?? []), ...(extra?.items ?? [])];
  const nextCursor = extra ? extra.nextCursor : state.data?.nextCursor;

  async function loadMore() {
    if (!nextCursor) return;
    setLoadingMore(true);
    setFailure(null);
    try {
      const page = await listReservations({ status, cursor: nextCursor });
      setMore({ key, page: { items: [...(extra?.items ?? []), ...page.items], nextCursor: page.nextCursor } });
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  const columns: DataTableColumn<Reservation>[] = [
    { key: "createdAt", header: "Reserved", render: (row) => formatDateTime(row.createdAt) },
    { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
    { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
    { key: "qty", header: "Quantity", align: "end", mono: true },
    { key: "demand", header: "Demand", render: (row) => `${sentence(row.demandType)} ${row.demandId}` },
    { key: "policy", header: "Policy", render: (row) => row.policy },
    { key: "status", header: "Status", render: (row) => <Badge tone={stateTones[row.status] ?? "neutral"}>{sentence(row.status)}</Badge> },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.status === "ACTIVE" ? (
          <RowAction
            label="Release"
            name={`reservation ${shortId(row.id)}`}
            run={() => releaseReservation(row.id)}
            onDone={() => {
              setFailure(null);
              setNotice(`Reservation ${shortId(row.id)} released: ${row.qty} available again.`);
              setAttempt((n) => n + 1);
            }}
            onError={setFailure}
          />
        ) : (
          dash
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Reservations"
        description="Stock held for a demand. Reserved stock cannot be issued or blocked until it is released."
        actions={
          <Link className={buttonClassName("primary")} to="/inventory/reservations/new">
            New reservation
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="reservations-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as ReservationStatus | "")}>
              <option value="">All statuses</option>
              {RESERVATION_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {state.error ? <ProblemAlert title="Could not load reservations" problem={state.error} onRetry={state.reload} /> : null}
      {failure ? <ProblemAlert title="The action did not complete" problem={failure} /> : null}
      {state.error ? null : (
        <DataTable
          label="Reservations"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No reservations", description: "Reserve stock for a sales, production or transfer order, or by hand." }}
        />
      )}
      {nextCursor && !state.loading ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more reservations
          </Button>
        </div>
      ) : null}
    </div>
  );
}
