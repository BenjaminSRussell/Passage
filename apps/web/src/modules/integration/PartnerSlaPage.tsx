import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { partnerSla, SLA_WINDOWS, type PartnerSla } from "./api/client";
import { dash } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const percent = (value: number) => `${value.toFixed(2)} %`;

const columns: DataTableColumn<PartnerSla>[] = [
  { key: "partnerName", header: "Partner", render: (row) => `${row.partnerName} (${row.environment})` },
  { key: "messages", header: "Transactions", align: "end", render: (row) => String(row.messages) },
  { key: "failed", header: "Failed", align: "end", render: (row) => String(row.failed) },
  {
    key: "failureRatePct",
    header: "Failure rate",
    align: "end",
    render: (row) => (row.messages === 0 ? dash : percent(row.failureRatePct)),
  },
  { key: "acked", header: "997s back", align: "end", render: (row) => `${row.acked} of ${row.outbound}` },
  {
    key: "p95AckMinutes",
    header: "p95 997 latency",
    align: "end",
    render: (row) => (row.p95AckMinutes === undefined ? dash : `${row.p95AckMinutes.toFixed(1)} min`),
  },
  { key: "ackSlaMinutes", header: "SLA", align: "end", render: (row) => `${row.ackSlaMinutes} min` },
  {
    key: "breaches",
    header: "SLA breaches",
    render: (row) =>
      row.lateAcks + row.overdueAcks === 0 ? (
        <Badge tone="success">None</Badge>
      ) : (
        <Badge tone="danger">{`${row.lateAcks} late, ${row.overdueAcks} overdue`}</Badge>
      ),
  },
];

/** Partner SLA dashboard (PRD §6.35.14): volume, failure rate and 997 latency per partner. */
export function PartnerSlaPage() {
  const navigate = useNavigate();
  const [days, setDays] = useState(30);
  const state = useLoad((signal) => partnerSla(days, signal), `sla:${days}`);
  const rows = state.data?.items ?? [];
  const total = rows.reduce(
    (sum, r) => ({ messages: sum.messages + r.messages, failed: sum.failed + r.failed, overdue: sum.overdue + r.overdueAcks, late: sum.late + r.lateAcks }),
    { messages: 0, failed: 0, overdue: 0, late: 0 },
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Partner SLA"
        description="Traffic per trading partner: transactions in both directions, how many failed, and how fast each partner's 997 came back on the documents we sent, against the partner's acknowledgment SLA."
        actions={
          <Link className={buttonClassName("secondary")} to="/integration/messages?status=FAILED">
            Failed messages
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="sla-window" label="Period">
          {(control) => (
            <Select {...control} value={String(days)} onChange={(event) => setDays(Number(event.target.value))}>
              {SLA_WINDOWS.map((d) => (
                <option key={d} value={d}>
                  {`Last ${d} days`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the SLA dashboard" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          {state.data && rows.length > 0 ? (
            <StatGroup label={`Totals for the last ${state.data.days} days`}>
              <StatTile label="Partners with traffic" value={rows.length} />
              <StatTile label="Transactions" value={total.messages} />
              <StatTile label="Failed" value={total.failed} tone="danger" hint={total.messages ? percent((total.failed * 100) / total.messages) : undefined} />
              <StatTile label="997s late or overdue" value={total.late + total.overdue} tone="warning" />
            </StatGroup>
          ) : null}
          <DataTable
            label="Partner SLA"
            columns={columns}
            rows={rows}
            getRowKey={(row) => row.partnerId}
            loading={state.loading}
            onRowOpen={(row) => navigate(`/integration/messages?partnerId=${row.partnerId}`)}
            empty={{ title: "No partner traffic in this period", description: "Partners appear here once they send or receive an interchange." }}
          />
          <p className={styles.note}>
            Latency runs from when we record an outbound interchange to when the partner's 997 for it is matched. Overdue counts interchanges still waiting past the SLA.
          </p>
        </>
      )}
    </div>
  );
}
