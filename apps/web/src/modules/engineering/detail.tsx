import { Fragment, useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  ApiError,
  getCalendar,
  getCapability,
  getResourceGroup,
  listCalendars,
  type Capability,
  type ResourceGroup,
  type WorkCalendar,
} from "./api/client";
import { calendarLink, PagedTable } from "./resources";
import { GenerateDays } from "./setup";
import { errorText } from "./screens";
import styles from "./pages.module.css";

const notFoundStatus = 404;
const weekdays = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"];

function Facts({ facts }: { facts: [string, ReactNode][] }) {
  return (
    <dl className={styles.facts}>
      {facts.map(([term, value]) => (
        <Fragment key={term}>
          <dt>{term}</dt>
          <dd>{value}</dd>
        </Fragment>
      ))}
    </dl>
  );
}

function DetailShell<T>({
  title,
  description,
  noun,
  load,
  id,
  onBack,
  backLabel,
  children,
}: {
  title: (item: T | null) => string;
  description: string;
  noun: string;
  load: (id: string) => Promise<T>;
  id: string;
  onBack?: () => void;
  backLabel: string;
  children: (item: T, refresh: () => void) => ReactNode;
}) {
  const [item, setItem] = useState<T | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let current = true;
    setItem(null);
    setError(null);
    load(id)
      .then((loaded) => current && setItem(loaded))
      .catch((e: Error) => current && setError(e));
    return () => {
      current = false;
    };
  }, [id, attempt, load]);

  const notFound = error instanceof ApiError && error.status === notFoundStatus;
  return (
    <>
      <PageHeader title={title(item)} description={description} />
      <div className={styles.toolbar}>{onBack ? <Button size="sm" onClick={onBack}>{backLabel}</Button> : null}</div>
      <div className={styles.alertSlot}>
        <Alert
          tone="danger"
          live="alert"
          action={error && !notFound ? <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Retry</Button> : undefined}
        >
          {error ? (notFound ? `This ${noun} does not exist` : errorText(error)) : null}
        </Alert>
      </div>
      {!item && !error ? (
        <Card>
          <Skeleton width="1/2" />
          <Skeleton />
        </Card>
      ) : null}
      {item ? children(item, () => void load(id).then(setItem, () => undefined)) : null}
    </>
  );
}

export function ResourceGroupDetailPage({ id, onBack }: { id: string; onBack?: () => void }) {
  return (
    <DetailShell<ResourceGroup>
      title={(g) => (g ? `Resource group ${g.code}` : "Resource group")}
      description="A group of resources on one site."
      noun="resource group"
      load={getResourceGroup}
      id={id}
      onBack={onBack}
      backLabel="Back to resource groups"
    >
      {(group) => (
        <Card title="Resource group">
          <Facts
            facts={[
              ["Code", group.code],
              ["Name", group.name],
              ["Site", group.siteId],
              ["Calendar", calendarLink(group.calendarId)],
              ["Members", <Link key="m" to={`/engineering/resources?groupId=${encodeURIComponent(group.id)}`}>Resources in this group</Link>],
            ]}
          />
        </Card>
      )}
    </DetailShell>
  );
}

export function CapabilityDetailPage({ id, onBack }: { id: string; onBack?: () => void }) {
  return (
    <DetailShell<Capability>
      title={(c) => (c ? `Capability ${c.code}` : "Capability")}
      description="A skill or ability resources can hold."
      noun="capability"
      load={getCapability}
      id={id}
      onBack={onBack}
      backLabel="Back to capabilities"
    >
      {(capability) => (
        <Card title="Capability">
          <Facts
            facts={[
              ["Code", capability.code],
              ["Name", capability.name],
              ["Holders", <Link key="h" to={`/engineering/resources?capabilityId=${encodeURIComponent(capability.id)}`}>Resources holding this capability</Link>],
            ]}
          />
        </Card>
      )}
    </DetailShell>
  );
}

interface TemplateRow {
  day: string;
  shifts: string;
  hours: string;
}

function minutes(clock: string): number {
  const [h, m] = clock.split(":").map(Number);
  return h * 60 + (m || 0);
}

function shiftLabel(s: { start: string; end: string; efficiencyPct?: string }): string {
  return s.efficiencyPct ? `${s.start}-${s.end} at ${s.efficiencyPct}%` : `${s.start}-${s.end}`;
}

function shiftMinutes(s: { start: string; end: string }): number {
  const span = minutes(s.end) - minutes(s.start);
  return span > 0 ? span : span + 24 * 60;
}

export function templateRows(calendar: WorkCalendar): TemplateRow[] {
  return weekdays.map((day, i) => {
    const shifts = calendar.template[i] ?? [];
    const total = shifts.reduce((sum, s) => sum + shiftMinutes(s), 0) / 60;
    return { day, shifts: shifts.map(shiftLabel).join(", ") || "Closed", hours: String(Math.round(total * 100) / 100) };
  });
}

const templateColumns: DataTableColumn<TemplateRow>[] = [
  { key: "day", header: "Weekday" },
  { key: "shifts", header: "Shifts" },
  { key: "hours", header: "Hours", align: "end" },
];

export function CalendarDetailPage({ id, onBack, canGenerate = false }: { id: string; onBack?: () => void; canGenerate?: boolean }) {
  return (
    <DetailShell<WorkCalendar>
      title={(c) => (c ? `Calendar ${c.code}` : "Calendar")}
      description="Weekly shift template, closed dates and generated range."
      noun="calendar"
      load={getCalendar}
      id={id}
      onBack={onBack}
      backLabel="Back to calendars"
    >
      {(calendar, reload) => (
        <>
          <Card title="Calendar">
            <Facts
              facts={[
                ["Code", calendar.code],
                ["Name", calendar.name],
                ["Version", String(calendar.version)],
                ["Generated", calendar.generatedFrom ? `${calendar.generatedFrom} to ${calendar.generatedTo}` : "Not generated yet"],
                ["Closed dates", calendar.closedDates.length ? calendar.closedDates.join(", ") : "None"],
                ["Days", <Link key="d" to={`/engineering/calendars/${encodeURIComponent(calendar.id)}/days`}>Calendar days</Link>],
              ]}
            />
          </Card>
          {canGenerate ? <GenerateDays calendarId={calendar.id} onGenerated={reload} /> : null}
          <div className={styles.results}>
            <DataTable
              label="Weekly template"
              columns={templateColumns}
              rows={templateRows(calendar)}
              getRowKey={(row) => row.day}
              empty={{ title: "No template" }}
            />
          </div>
        </>
      )}
    </DetailShell>
  );
}

const calendarColumns: DataTableColumn<WorkCalendar>[] = [
  {
    key: "code",
    header: "Code",
    mono: true,
    render: (row) => <Link to={`/engineering/calendars/${encodeURIComponent(row.id)}`}>{row.code}</Link>,
  },
  { key: "name", header: "Name" },
  { key: "version", header: "Version", align: "end" },
  { key: "generatedTo", header: "Generated through", render: (row) => row.generatedTo ?? "Not generated" },
];

const noFilters = {};
const fetchCalendars = (_filters: object, cursor?: string) => listCalendars(cursor);

export function CalendarsPage({ onCreate }: { onCreate?: () => void }) {
  return (
    <>
      <PageHeader title="Work calendars" description="Shift calendars that drive resource availability." actions={onCreate ? <Button variant="primary" onClick={onCreate}>New calendar</Button> : undefined} />
      <PagedTable
        label="Work calendars"
        emptyTitle="No work calendars yet"
        columns={calendarColumns}
        getRowKey={(row: WorkCalendar) => row.id}
        fetchPage={fetchCalendars}
        filters={noFilters}
      />
    </>
  );
}
