import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { getLease, type LeaseLine } from "./api/client";
import { StatusBadge } from "./badges";
import { money, percent } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

const columns: DataTableColumn<LeaseLine>[] = [
  { key: "periodNo", header: "Period", align: "end" },
  { key: "opening", header: "Opening liability", align: "end" },
  { key: "interest", header: "Interest", align: "end" },
  { key: "payment", header: "Payment", align: "end" },
  { key: "closing", header: "Closing liability", align: "end" },
  { key: "rouDepreciation", header: "ROU depreciation", align: "end" },
];

/** Lease detail: recognized liability and ROU asset with the full schedule (FR-37.10). */
export function LeaseDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getLease(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Lease" />
        <Alert tone="danger" title="Could not load the lease" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/assets/leases">
          Back to leases
        </Link>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Lease" />
        <span className="visually-hidden">Loading the lease…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const lease = state.data;
  const schedule = lease.schedule ?? [];
  const last = schedule[schedule.length - 1];

  return (
    <div className={styles.stack}>
      <PageHeader title={`Lease ${lease.leaseNumber}`} meta={<StatusBadge status={lease.status} />} />
      <StatGroup label="Lease balances">
        <StatTile label="Lease liability" value={money(lease.liability, lease.currency)} />
        <StatTile label="ROU asset" value={money(lease.rouAsset, lease.currency)} />
        <StatTile label="Periods" value={schedule.length} />
      </StatGroup>
      <Card title="Terms">
        <dl className={styles.details}>
          <Detail term="Commencement">{lease.commencementDate}</Detail>
          <Detail term="Term">{lease.termMonths} months</Detail>
          <Detail term="Payment">{money(lease.paymentAmount, lease.currency)}</Detail>
          <Detail term="Payments per year">{lease.paymentsPerYear}</Detail>
          <Detail term="Timing">{lease.paymentTiming === "advance" ? "In advance" : "In arrears"}</Detail>
          <Detail term="Incremental borrowing rate">{percent(lease.ibrRate)}</Detail>
          <Detail term="Legal entity">
            <span className={styles.mono}>{lease.legalEntityId}</span>
          </Detail>
        </dl>
      </Card>
      <Card title="Liability and ROU schedule">
        <div className={styles.stack}>
          {last ? <p className={styles.note}>The final period closes the liability at {money(last.closing, lease.currency)}.</p> : null}
          <DataTable
            label="Lease schedule"
            columns={columns}
            rows={schedule}
            getRowKey={(l) => String(l.periodNo)}
            empty={{ title: "No schedule", description: "The service returns the schedule with the lease." }}
          />
        </div>
      </Card>
    </div>
  );
}
