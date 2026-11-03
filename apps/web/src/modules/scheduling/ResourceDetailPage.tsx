import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton } from "../../shell/ui";
import { getBlockUtilization, getResource, getResourceCapacity, type BlockWindow, type Interval, type Resource } from "./api/client";
import { describeCondition } from "./CapacityBlocksPage";
import { clock, dayStart, formatDateTime, formatMinutes, today, WEEK_ORDER, WEEKDAYS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Detail {
  resource: Resource;
  capacity: Interval[];
}

const CAPACITY_DAYS = 7;

/**
 * The capacity blocks of a resource over the next week with what each window
 * holds and how much of it matching jobs use (FR-23.17, PRD 4.23.14 Block
 * utilization). It loads on its own so a failure here leaves the rest of the
 * page usable.
 */
export function BlockUtilizationCard({ resourceId, from, to }: { resourceId: string; from: string; to: string }) {
  const state = useLoad<BlockWindow[]>((signal) => getBlockUtilization(resourceId, from, to, signal), `${resourceId}|${from}|${to}`);
  const rows = state.data ?? [];
  const held = rows.reduce((sum, w) => sum + w.blockMinutes, 0);
  const matched = rows.reduce((sum, w) => sum + w.matchedMinutes, 0);
  return (
    <Card
      title="Capacity blocks, next 7 days"
      actions={
        <Link className={buttonClassName("secondary")} to="/scheduling/capacity-blocks/new">
          New block
        </Link>
      }
    >
      {state.error ? (
        <Alert tone="danger" title="Could not load the block windows" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <div className={styles.stack}>
          {rows.length > 0 && <p className={styles.note}>{`Blocks hold ${formatMinutes(held)} of open time; matching jobs use ${formatMinutes(matched)}.`}</p>}
          <DataTable
            label="Block windows"
            columns={[
              { key: "code", header: "Block", mono: true },
              { key: "start", header: "From", render: (w: BlockWindow) => formatDateTime(w.start) },
              { key: "end", header: "To", render: (w: BlockWindow) => formatDateTime(w.end) },
              { key: "condition", header: "Condition", mono: true, render: (w: BlockWindow) => describeCondition(w.condition) },
              { key: "blockMinutes", header: "Held min", align: "end" },
              { key: "matchedMinutes", header: "Used min", align: "end" },
              { key: "unusedMinutes", header: "Unused min", align: "end" },
            ]}
            rows={rows}
            getRowKey={(w) => `${w.blockId}|${w.start}`}
            loading={state.loading}
            empty={{
              title: "No block windows in the next 7 days",
              description: "Every job may use this resource's open capacity. Add a capacity block to reserve time for matching jobs.",
            }}
          />
        </div>
      )}
    </Card>
  );
}

/** A resource, its weekly calendar, its open capacity and its block windows for the next week (FR-23.1, FR-23.17). */
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
          <>
            <Link className={buttonClassName("secondary")} to={`/scheduling/resources/${resource.id}/edit`}>
              Edit calendar
            </Link>
            <Link className={buttonClassName("secondary")} to={`/scheduling/capacity-overrides?resourceId=${resource.id}`}>
              Downtimes and extra shifts
            </Link>
            <Link className={buttonClassName("secondary")} to="/scheduling/load">
              Capacity load
            </Link>
          </>
        }
      />
      <Card title="Weekly calendar">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Time zone</dt>
            <dd className={styles.value}>{resource.timeZone}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Efficiency</dt>
            <dd className={styles.value}>{`${resource.efficiencyPct ?? 100} %`}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Engineering resource</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{resource.engineeringResourceId ?? "Not mapped"}</dd>
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
      <BlockUtilizationCard resourceId={resource.id} from={from} to={to} />
    </div>
  );
}
