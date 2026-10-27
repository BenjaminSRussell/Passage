import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listSchedules, type Schedule } from "./api/client";
import { CURRENCY_TYPE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Schedule>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "definition", header: "Definition", mono: true },
  { key: "cron", header: "Runs at", mono: true, render: (s) => `${s.cron} (${s.timezone})` },
  { key: "ledgerIds", header: "Ledgers", mono: true, align: "end", render: (s) => String(s.ledgerIds.length) },
  { key: "currencyType", header: "Currency type", render: (s) => CURRENCY_TYPE_LABELS[s.currencyType] ?? s.currencyType },
  { key: "formats", header: "Formats", render: (s) => s.formats.map((f) => f.toUpperCase()).join(", ") },
  { key: "recipients", header: "Recipients", mono: true, align: "end", render: (s) => String(s.recipients.length) },
  {
    key: "nextRunAt",
    header: "Next run",
    mono: true,
    render: (s) => (s.enabled ? s.nextRunAt ? <time dateTime={s.nextRunAt}>{s.nextRunAt}</time> : "—" : <Badge tone="neutral">Paused</Badge>),
  },
];

/** Distribution schedules (FR-15.8): statements that run on a timetable and go to their recipients. */
export function SchedulesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listSchedules(signal), "schedules");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Distribution schedules"
        description="Statements that run on a timetable for a set of ledgers and go out to their recipients by email."
        actions={
          <Link className={buttonClassName("primary")} to="/finreport/schedules/new">
            New schedule
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the schedules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Distribution schedules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(s) => s.code}
          loading={state.loading}
          empty={{ title: "No schedules yet", description: "Create a schedule to send a statement pack on a timetable, such as every night at 23:00." }}
          onRowOpen={(s) => navigate(`/finreport/schedules/${encodeURIComponent(s.code)}`)}
        />
      )}
      <p className={styles.note}>
        Schedules are stored and checked now. Running them and sending the email follow in a later release; until then, run the statement and download it as CSV.
      </p>
    </div>
  );
}
