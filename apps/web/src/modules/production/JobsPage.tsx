import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  JOB_TYPES,
  STATUSES,
  describeProblem,
  getJobs,
  type JobPage,
  type JobType,
  type JobsQuery,
  type Problem,
  type ProductionJob,
  type ProductionStatus,
} from "./api/client";
import { instant, jobTypeLabel, statusLabel, statusTone } from "./labels";
import styles from "./pages.module.css";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface JobsPageProps {
  /** Opens a production order from its row. */
  onOpenOrder?(orderId: string): void;
}

const columns: DataTableColumn<ProductionJob>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "orderStatus", header: "Order status", render: (row) => <Badge tone={statusTone[row.orderStatus]}>{statusLabel[row.orderStatus] ?? row.orderStatus}</Badge> },
  { key: "operNumber", header: "Operation", align: "end" },
  { key: "jobType", header: "Job", render: (row) => jobTypeLabel[row.jobType] ?? row.jobType },
  { key: "resourceId", header: "Resource", mono: true },
  { key: "plannedStart", header: "Start", mono: true, render: (row) => instant(row.plannedStart) },
  { key: "plannedEnd", header: "End", mono: true, render: (row) => instant(row.plannedEnd) },
  { key: "plannedHours", header: "Hours", align: "end" },
];

interface Filter {
  resourceId: string;
  siteId: string;
  jobType: JobType | "";
  orderStatus: ProductionStatus | "";
  from: string;
  to: string;
}

interface FilterErrors {
  resourceId?: string;
  siteId?: string;
  to?: string;
}

interface State {
  page: JobPage | null;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
  /** A failed "load more" keeps the rows already shown. */
  moreError: Problem | null;
}

const initial: State = { page: null, loading: true, loadingMore: false, error: null, moreError: null };

const iso = (local: string) => (local === "" ? undefined : new Date(local).toISOString());

const query = (f: Filter): JobsQuery => ({
  resourceId: f.resourceId.trim() || undefined,
  siteId: f.siteId.trim() || undefined,
  jobType: f.jobType,
  orderStatus: f.orderStatus,
  from: iso(f.from),
  to: iso(f.to),
});

/** Checks the filter as the service does: the resource and site are ids and the window is in order. */
export function validateJobsFilter(f: Filter): FilterErrors {
  const errors: FilterErrors = {};
  const resource = f.resourceId.trim();
  if (resource !== "" && !uuidPattern.test(resource)) {
    errors.resourceId = "Enter the resource's id (a UUID), or leave it empty.";
  }
  const site = f.siteId.trim();
  if (site !== "" && !uuidPattern.test(site)) {
    errors.siteId = "Enter the site's id (a UUID), or leave it empty.";
  }
  if (f.from !== "" && f.to !== "" && new Date(f.to).getTime() <= new Date(f.from).getTime()) {
    errors.to = "The end of the window must be after its start.";
  }
  return errors;
}

/**
 * Scheduled jobs across orders (PRD 4.21 ProductionJob): what each resource is asked to do and when,
 * so a planner reads the load of a resource over a window. Dates come from infinite-capacity scheduling.
 */
export function JobsPage({ onOpenOrder }: JobsPageProps) {
  const [draft, setDraft] = useState<Filter>({ resourceId: "", siteId: "", jobType: "", orderStatus: "", from: "", to: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<FilterErrors>({});
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    getJobs(query(filter), controller.signal).then(
      (page) => token === latest.current && setState({ ...initial, page, loading: false }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    const problems = validateJobsFilter(draft);
    setErrors(problems);
    if (Object.keys(problems).length === 0) {
      setFilter({ ...draft });
    }
  };

  const loadMore = async () => {
    const cursor = state.page?.nextCursor;
    if (!cursor) {
      return;
    }
    const token = latest.current;
    setState((s) => ({ ...s, loadingMore: true, moreError: null }));
    try {
      const next = await getJobs({ ...query(filter), cursor });
      if (token !== latest.current) {
        return;
      }
      setState((s) => ({
        ...s,
        page: s.page ? { ...s.page, items: [...s.page.items, ...next.items], nextCursor: next.nextCursor } : next,
        loadingMore: false,
      }));
    } catch (error) {
      if (token === latest.current) {
        setState((s) => ({ ...s, loadingMore: false, moreError: describeProblem(error) }));
      }
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Scheduled jobs"
        description="The dated jobs of the scheduled production orders: queue, setup, process and transport, by resource and time."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the jobs">
          <FormField id="jobs-resource" label="Resource" optional error={errors.resourceId}>
            {(control) => <Input {...control} value={draft.resourceId} onChange={(e) => setDraft({ ...draft, resourceId: e.target.value })} />}
          </FormField>
          <FormField id="jobs-site" label="Site" optional error={errors.siteId}>
            {(control) => <Input {...control} value={draft.siteId} onChange={(e) => setDraft({ ...draft, siteId: e.target.value })} />}
          </FormField>
          <FormField id="jobs-type" label="Job">
            {(control) => (
              <Select {...control} value={draft.jobType} onChange={(e) => setDraft({ ...draft, jobType: e.target.value as JobType | "" })}>
                <option value="">All jobs</option>
                {JOB_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {jobTypeLabel[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="jobs-order-status" label="Order status">
            {(control) => (
              <Select {...control} value={draft.orderStatus} onChange={(e) => setDraft({ ...draft, orderStatus: e.target.value as ProductionStatus | "" })}>
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="jobs-from" label="From (your local time)" optional>
            {(control) => <Input {...control} type="datetime-local" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />}
          </FormField>
          <FormField id="jobs-to" label="To (your local time)" optional error={errors.to}>
            {(control) => <Input {...control} type="datetime-local" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The jobs are not available" live="alert">
          {state.error.status === 403 ? "You need the View production orders duty to see the jobs." : `${state.error.message} (${state.error.code})`}
        </Alert>
      ) : (
        <>
          {state.page ? (
            <StatGroup label="Job totals">
              <StatTile label="Jobs" value={String(state.page.total)} />
              <StatTile label="Planned hours" value={state.page.plannedHours} />
            </StatGroup>
          ) : null}
          <DataTable
            label="Jobs"
            columns={columns}
            rows={state.page?.items ?? []}
            loading={state.loading}
            getRowKey={(row) => row.id}
            onRowOpen={onOpenOrder ? (row) => onOpenOrder(row.orderId) : undefined}
            empty={{
              title: "No jobs",
              description: "Jobs appear here once an estimated production order is scheduled. Times are shown in UTC.",
            }}
          />
          {state.moreError ? (
            <Alert tone="danger" title="More jobs could not be loaded" live="alert">
              {`${state.moreError.message} (${state.moreError.code})`}
            </Alert>
          ) : null}
          {state.page?.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more jobs
              </Button>
            </div>
          ) : null}
          <p className={styles.hint}>Jobs are dated with infinite capacity; resource calendars and finite capacity come with capacity planning.</p>
        </>
      )}
    </div>
  );
}
