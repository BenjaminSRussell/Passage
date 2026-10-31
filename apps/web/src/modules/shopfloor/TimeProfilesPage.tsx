import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listTimeProfiles, type ProfileDay, type TimeProfile } from "./api/client";
import { dash } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const DAY_LABEL = { weekday: "Weekday", saturday: "Saturday", sunday: "Sunday" } as const;

/** "Weekday 07:00-15:30, 1 break". */
export function describeDay(day: ProfileDay): string {
  const shift = day.shiftStart && day.shiftEnd ? `${day.shiftStart}-${day.shiftEnd}` : "no shift window";
  const breaks = day.breaks.length === 1 ? "1 break" : `${day.breaks.length} breaks`;
  return `${DAY_LABEL[day.dayType]} ${shift}, ${breaks}`;
}

const columns: DataTableColumn<TimeProfile>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  {
    key: "days",
    header: "Day rules",
    render: (row) =>
      row.days.length === 0 ? (
        dash
      ) : (
        <ul className={styles.plainList}>
          {row.days.map((day) => (
            <li key={day.dayType}>{describeDay(day)}</li>
          ))}
        </ul>
      ),
  },
  { key: "active", header: "Status", render: (row) => (row.active ? <Badge tone="success">Active</Badge> : <Badge tone="neutral">Inactive</Badge>) },
];

/** Time profiles: shift windows, breaks, rounding and tolerances per day type (FR-25.7, BR-25.2). */
export function TimeProfilesPage() {
  const state = useLoad((signal) => listTimeProfiles({}, signal), "time-profiles");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Time profiles"
        description="Day rules a worker's clock times are rounded and paid by. A day type without rules uses the defaults: 10 minutes tolerance before, 5 after, 1 minute rounding and no unpaid break."
        actions={
          <Link className={buttonClassName("primary")} to="/shopfloor/time-profiles/new">
            New time profile
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load time profiles" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Time profiles"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No time profiles yet", description: "Create one, then assign it to workers from their page. Until then the default day rules apply." }}
        />
      )}
    </div>
  );
}
