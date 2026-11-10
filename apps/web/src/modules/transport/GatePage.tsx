import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listGateVisits, VISIT_STATUSES, type GateVisit, type VisitStatus } from "./api/client";
import { DATE, dash, formatDateTime, gateTone, statusLabel, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<GateVisit>[] = [
  { key: "checkedInAt", header: "Checked in", render: (row) => formatDateTime(row.checkedInAt) },
  { key: "trailerNo", header: "Trailer", mono: true },
  { key: "carrierScac", header: "Carrier", mono: true },
  { key: "direction", header: "Direction", render: (row) => statusLabel(row.direction) },
  {
    key: "arrival",
    header: "Arrival",
    render: (row) => (row.arrival ? <Badge tone={gateTone(row.arrival.status)}>{statusLabel(row.arrival.status)}</Badge> : <Badge tone="neutral">Walk-in</Badge>),
  },
  { key: "sealNo", header: "Seal", mono: true, render: (row) => row.sealNo || dash },
  { key: "status", header: "Status", render: (row) => <Badge tone={gateTone(row.status)}>{statusLabel(row.status)}</Badge> },
  { key: "checkedOutAt", header: "Checked out", render: (row) => formatDateTime(row.checkedOutAt) },
  {
    key: "detention",
    header: "Detention",
    align: "end",
    render: (row) => (row.detention ? (row.detention.hours > 0 ? `${row.detention.hours} h` : "None") : dash),
  },
];

/** The gate log (PRD 6.34.14): trucks and containers in the yard and those that have left, by site, day and carrier. */
export function GatePage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<VisitStatus | "">("IN_YARD");
  const [site, setSite] = useState("");
  const [date, setDate] = useState("");
  const [carrier, setCarrier] = useState("");
  const siteId = UUID.test(site.trim()) ? site.trim() : "";
  const day = DATE.test(date) ? date : "";
  const scac = /^[A-Z]{2,4}$/.test(carrier) ? carrier : "";
  const state = useLoad((signal) => listGateVisits({ status, siteId, date: day, carrierScac: scac }, signal), `gate:${status}:${siteId}:${day}:${scac}`);
  const detentionDue = (state.data ?? []).filter((v) => v.detention && v.detention.hours > 0).length;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Gate"
        description="Drivers check in against their dock appointment and check out with the seal verified. Every check-in, seal and check-out attempt stays in the gate log."
        actions={
          <Link className={buttonClassName("primary")} to="/transport/gate/check-in">
            Check in
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="gate-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as VisitStatus | "")}>
              <option value="">All</option>
              {VISIT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="gate-site" label="Site id" hint="Filter by site (UUID).">
          {(control) => <Input {...control} className={styles.mono} value={site} onChange={(e) => setSite(e.target.value)} />}
        </FormField>
        <FormField id="gate-date" label="Check-in day" hint="UTC day.">
          {(control) => <Input {...control} type="date" value={date} onChange={(e) => setDate(e.target.value)} />}
        </FormField>
        <FormField id="gate-carrier" label="Carrier SCAC">
          {(control) => <Input {...control} className={styles.mono} maxLength={4} value={carrier} onChange={(e) => setCarrier(e.target.value.toUpperCase())} />}
        </FormField>
      </div>
      {detentionDue > 0 ? (
        <p className={styles.note}>
          {detentionDue} {detentionDue === 1 ? "visit" : "visits"} accrued detention (TMS-0126); add the DETENTION charge to the carrier's freight bill.
        </p>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load the gate log" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Gate visits"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status === "IN_YARD" ? "No trucks in the yard" : "No gate visits",
            description: "Check a driver in when a truck arrives at the gate.",
          }}
          onRowOpen={(row) => navigate(`/transport/gate/${row.id}`)}
        />
      )}
    </div>
  );
}
