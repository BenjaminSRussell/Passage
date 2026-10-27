import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton } from "../../shell/ui";
import { getResource, getResourceCapacity, type Interval, type Resource } from "./api/client";
import { clock, dayStart, formatDateTime, formatMinutes, today, WEEK_ORDER, WEEKDAYS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Detail {
  resource: Resource;
  capacity: Interval[];
}

const CAPACITY_DAYS = 7;

/** A resource, its weekly calendar and its open capacity for the next week (FR-23.1). */
export function ResourceDetailPage() {
  const { id = "" } = useParams();
  const from = dayStart(today());
  const to = dayStart(today(), CAPACITY_DAYS);
  const state = useLoad<Detail>(async (signal) => {
    const [resource, capacity] = await Promise.all([getResource(id, signal), getResourceCapacity(id, from, to, signal)]);
    return { resource, capacity };
  }, `${id}|${from}`);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Resource" />
        <Alert tone="danger" title="Could not load the resource" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Resource" />
        <span className="visually-hidden">Loading the resource…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { resource, capacity } = state.data;
  const open = capacity.reduce((sum, iv) => sum + iv.minutes, 0);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={resource.code}
        description={resource.name}
        meta={<Badge tone={resource.finite ? "info" : "neutral"}>{resource.finite ? "Finite" : "Infinite"}</Badge>}
        actions={
          <Link className={buttonClassName("secondary")} to="/scheduling/load">
            Capacity load
          </Link>
        }
      />
      <Card title="Weekly calendar">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Time zone</dt>
            <dd className={styles.value}>{resource.timeZone}</dd>
          </div>
          {WEEK_ORDER.map((day) => {
            const spans = resource.shifts.filter((s) => s.weekday === day);
            return (
              <div key={day} className={styles.entry}>
                <dt className={styles.term}>{WEEKDAYS[day]}</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{spans.length === 0 ? "Closed" : spans.map((s) => `${clock(s.startMinute)}–${clock(s.endMinute)}`).join(", ")}</dd>
              </div>
            );
          })}
        </dl>
      </Card>
      <Card title="Open capacity, next 7 days">
        <div className={styles.stack}>
          <p className={styles.note}>{`${formatMinutes(open)} open between ${formatDateTime(from)} and ${formatDateTime(to)}.`}</p>
          <DataTable
            label="Open intervals"
            columns={[
              { key: "start", header: "From", render: (iv: Interval) => formatDateTime(iv.start) },
              { key: "end", header: "To", render: (iv: Interval) => formatDateTime(iv.end) },
              { key: "minutes", header: "Minutes", align: "end" },
            ]}
            rows={capacity}
            getRowKey={(iv) => iv.start}
            empty={{ title: "No open capacity in the next 7 days", description: "The calendar has no shifts on these days." }}
          />
        </div>
      </Card>
    </div>
  );
}
