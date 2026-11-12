import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  CardGrid,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  DASHBOARD_ID,
  describeProblem,
  getDashboard,
  getDashboardValues,
  listDashboards,
  listMetrics,
  MAX_REFRESH_SECONDS,
  MAX_TILES,
  MIN_REFRESH_SECONDS,
  putDashboard,
  TILE_PERIODS,
  type Dashboard,
  type DashboardValues,
  type Metric,
  type Problem,
  type Tile,
} from "./api/client";
import { dateTime, tileStatusHint, tileValueText } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const periodLabels: Record<Tile["period"], string> = {
  MTD: "Month to date",
  QTD: "Quarter to date",
  YTD: "Year to date",
  TRAILING_12M: "Trailing 12 months",
};

const columns: DataTableColumn<Dashboard>[] = [
  { key: "title", header: "Title" },
  { key: "dashboardId", header: "Id", mono: true },
  { key: "tiles", header: "Tiles", align: "end", render: (d) => d.tiles.length },
  { key: "refreshSeconds", header: "Refresh", align: "end", render: (d) => `${d.refreshSeconds} s` },
  { key: "version", header: "Version", align: "end" },
  { key: "updatedAt", header: "Updated", render: (d) => dateTime(d.updatedAt) },
];

/** Embedded dashboards of the tenant (FR-46.8, PRD §9.46.6 "Workspace dashboard host"). */
export function DashboardsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listDashboards(undefined, signal), "dashboards");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Dashboards"
        description="Dashboards embedded in the workspaces. Every tile is bound to a published governed metric, so a dashboard shows only governed numbers."
        actions={
          <Link className={buttonClassName("primary")} to="/analytics/dashboards/new">
            New dashboard
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load dashboards" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Dashboards"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(d) => d.dashboardId}
          loading={state.loading}
          empty={{ title: "No dashboards yet", description: "Create a dashboard and add up to 12 metric tiles." }}
          onRowOpen={(d) => navigate(`/analytics/dashboards/${encodeURIComponent(d.dashboardId)}`)}
        />
      )}
    </div>
  );
}

export interface DashboardDraft {
  dashboardId: string;
  title: string;
  refreshSeconds: string;
  tiles: Tile[];
}
/** Field errors keyed like the service's validation problem (dashboardId, tiles[0].metric, ...). */
export type DashboardErrors = Record<string, string>;

const blankTile = (metric = ""): Tile => ({ title: "", metric, period: "MTD" });

/** Checks that mirror domain.PrepareDashboard in services/analytics. */
export function validateDashboard(d: DashboardDraft, catalog: Metric[]): DashboardErrors {
  const errors: DashboardErrors = {};
  if (!DASHBOARD_ID.test(d.dashboardId)) errors.dashboardId = "Use 2 to 63 lowercase letters, digits, _ or -, starting with a letter.";
  const title = d.title.trim();
  if (title === "" || title.length > 120) errors.title = "Enter a title of up to 120 characters.";
  const refresh = Number(d.refreshSeconds);
  if (!/^\d+$/.test(d.refreshSeconds) || refresh < MIN_REFRESH_SECONDS || refresh > MAX_REFRESH_SECONDS) {
    errors.refreshSeconds = `Enter ${MIN_REFRESH_SECONDS} to ${MAX_REFRESH_SECONDS} seconds.`;
  }
  if (d.tiles.length === 0 || d.tiles.length > MAX_TILES) errors.tiles = `Add between 1 and ${MAX_TILES} tiles.`;
  const published = new Set(catalog.filter((m) => m.status === "PUBLISHED").map((m) => m.metricId));
  d.tiles.forEach((t, i) => {
    const tileTitle = t.title.trim();
    if (tileTitle === "" || tileTitle.length > 120) errors[`tiles[${i}].title`] = "Enter a tile title of up to 120 characters.";
    if (!published.has(t.metric)) errors[`tiles[${i}].metric`] = "Choose a published governed metric.";
  });
  return errors;
}

function fromDashboard(d: Dashboard): DashboardDraft {
  return { dashboardId: d.dashboardId, title: d.title, refreshSeconds: String(d.refreshSeconds), tiles: d.tiles.map((t) => ({ ...t })) };
}

function DashboardForm({
  initial,
  existing,
  metrics,
  onSaved,
}: {
  initial: DashboardDraft;
  existing: Dashboard | null;
  metrics: Metric[];
  onSaved(d: Dashboard): void;
}) {
  const [draft, setDraft] = useState<DashboardDraft>(initial);
  const [errors, setErrors] = useState<DashboardErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const published = metrics.filter((m) => m.status === "PUBLISHED");

  const setTile = (i: number, patch: Partial<Tile>) =>
    setDraft((d) => ({ ...d, tiles: d.tiles.map((t, j) => (j === i ? { ...t, ...patch } : t)) }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateDashboard(draft, metrics);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const input = {
        title: draft.title.trim(),
        refreshSeconds: Number(draft.refreshSeconds),
        tiles: draft.tiles.map((t) => ({ ...t, title: t.title.trim() })),
      };
      onSaved(await putDashboard(draft.dashboardId, input, existing ? existing.version : null));
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      setErrors(problem.fields);
    } finally {
      setBusy(false);
    }
  }

  const conflict = failure?.code === "ANA-0113";
  return (
    <Card title={existing ? "Dashboard definition" : "New dashboard"}>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Dashboard">
        <FormField id="dashboard-id" label="Dashboard key" error={errors.dashboardId} hint={existing ? "A dashboard key cannot change." : "Used in the workspace URL, for example sales_overview."}>
          {(control) => (
            <Input
              {...control}
              className={styles.mono}
              invalid={Boolean(errors.dashboardId)}
              value={draft.dashboardId}
              readOnly={existing !== null}
              onChange={(e) => setDraft((d) => ({ ...d, dashboardId: e.target.value }))}
            />
          )}
        </FormField>
        <FormField id="dashboard-title" label="Title" error={errors.title}>
          {(control) => <Input {...control} invalid={Boolean(errors.title)} value={draft.title} onChange={(e) => setDraft((d) => ({ ...d, title: e.target.value }))} />}
        </FormField>
        <FormField id="dashboard-refresh" label="Refresh every (seconds)" hint={`${MIN_REFRESH_SECONDS} to ${MAX_REFRESH_SECONDS}; 60 by default.`} error={errors.refreshSeconds}>
          {(control) => (
            <Input
              {...control}
              inputMode="numeric"
              invalid={Boolean(errors.refreshSeconds)}
              value={draft.refreshSeconds}
              onChange={(e) => setDraft((d) => ({ ...d, refreshSeconds: e.target.value }))}
            />
          )}
        </FormField>
        {draft.tiles.map((tile, i) => (
          <fieldset key={i} className={styles.tileRow}>
            <legend className={styles.choicesLegend}>Tile {i + 1}</legend>
            <FormField id={`tile-${i}-title`} label="Tile title" error={errors[`tiles[${i}].title`]}>
              {(control) => <Input {...control} invalid={Boolean(errors[`tiles[${i}].title`])} value={tile.title} onChange={(e) => setTile(i, { title: e.target.value })} />}
            </FormField>
            <FormField id={`tile-${i}-metric`} label="Metric" error={errors[`tiles[${i}].metric`]}>
              {(control) => (
                <Select {...control} value={tile.metric} onChange={(e) => setTile(i, { metric: e.target.value })}>
                  <option value="">Choose a metric</option>
                  {published.map((m) => (
                    <option key={m.metricId} value={m.metricId}>
                      {m.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id={`tile-${i}-period`} label="Period" error={errors[`tiles[${i}].period`]}>
              {(control) => (
                <Select {...control} value={tile.period} onChange={(e) => setTile(i, { period: e.target.value as Tile["period"] })}>
                  {TILE_PERIODS.map((p) => (
                    <option key={p} value={p}>
                      {periodLabels[p]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button size="sm" variant="ghost" onClick={() => setDraft((d) => ({ ...d, tiles: d.tiles.filter((_, j) => j !== i) }))}>
                Remove tile {i + 1}
              </Button>
            </div>
          </fieldset>
        ))}
        <div className={styles.formActions}>
          <Button size="sm" disabled={draft.tiles.length >= MAX_TILES} onClick={() => setDraft((d) => ({ ...d, tiles: [...d.tiles, blankTile()] }))}>
            Add tile
          </Button>
          {errors.tiles ? (
            <p id="dashboard-tiles-error" role="alert" className={styles.fieldError}>
              {errors.tiles}
            </p>
          ) : null}
        </div>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title={conflict ? "Someone else changed this dashboard" : "The dashboard was not saved"} live="alert">
              <ProblemMessage problem={failure} message={conflict ? "Reload the page to see the current version, then make your change again." : undefined} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            {existing ? "Save dashboard" : "Create dashboard"}
          </Button>
          <Link className={buttonClassName("secondary")} to="/analytics/dashboards">
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

function Loading({ title, text }: { title: string; text: string }) {
  return (
    <div className={styles.stack} aria-busy="true">
      <PageHeader title={title} />
      <Card>
        <div className={styles.loading}>
          <Skeleton width="1/2" height="control" />
          <Skeleton width="3/4" height="control" />
        </div>
      </Card>
      <p role="status" className={styles.muted}>
        {text}
      </p>
    </div>
  );
}

function LoadFailed({ title, problem, retry }: { title: string; problem: Problem | null; retry(): void }) {
  return (
    <div className={styles.stack}>
      <PageHeader title={title} />
      <Alert tone="danger" title={`Could not load the ${title.toLowerCase()}`} live="alert" action={<Button size="sm" onClick={retry}>Try again</Button>}>
        <ProblemMessage problem={problem} />
      </Alert>
    </div>
  );
}

/** Creates a dashboard. Tiles can only name published governed metrics. */
export function DashboardCreatePage() {
  const navigate = useNavigate();
  const catalog = useLoad((signal) => listMetrics(undefined, signal), "metrics");
  if (catalog.loading && !catalog.data) return <Loading title="New dashboard" text="Loading governed metrics…" />;
  if (catalog.error || !catalog.data) return <LoadFailed title="Metric catalog" problem={catalog.error} retry={catalog.reload} />;
  return (
    <div className={styles.stack}>
      <PageHeader title="New dashboard" description="Add between 1 and 12 tiles, each showing one governed metric over a period." />
      {catalog.data.length === 0 ? (
        <Card>
          <EmptyState icon="database" title="No published metrics yet" description="A dashboard tile needs a published governed metric." />
        </Card>
      ) : (
        <DashboardForm
          initial={{ dashboardId: "", title: "", refreshSeconds: "60", tiles: [blankTile()] }}
          existing={null}
          metrics={catalog.data}
          onSaved={(d) => navigate(`/analytics/dashboards/${encodeURIComponent(d.dashboardId)}`)}
        />
      )}
    </div>
  );
}

/** One dashboard: its tiles with the governed metric behind each, and the editor. */
export function DashboardDetailPage() {
  const { id = "" } = useParams();
  const [saved, setSaved] = useState<Dashboard | null>(null);
  const state = useLoad(
    async (signal) => {
      const [dashboard, metrics] = await Promise.all([getDashboard(id, undefined, signal), listMetrics(undefined, signal)]);
      return { dashboard, metrics };
    },
    `dashboard/${id}`,
  );
  // Tile values refresh on the dashboard's own interval (FR-46.8); a failed refresh keeps the last values.
  const values = useLoad((signal) => getDashboardValues(id, undefined, signal), `dashboard-values/${id}`);
  // A refresh that fails keeps the last values on screen (useLoad drops its data on a failure).
  const lastValues = useRef<DashboardValues | null>(null);
  if (values.data) lastValues.current = values.data;
  const shownValues = values.data ?? lastValues.current;
  const refreshMs = (state.data?.dashboard.refreshSeconds ?? 60) * 1000;
  const reloadValues = values.reload;
  useEffect(() => {
    const timer = setInterval(reloadValues, refreshMs);
    return () => clearInterval(timer);
  }, [reloadValues, refreshMs]);

  if (state.loading && !state.data) return <Loading title="Dashboard" text={`Loading dashboard ${id}…`} />;
  if (state.error?.code === "ANA-0112") {
    return (
      <Card>
        <EmptyState
          headingLevel={2}
          icon="database"
          title="We couldn't find that dashboard"
          description={`No dashboard has the id ${id} in your tenant.`}
          action={
            <Link className={buttonClassName("secondary")} to="/analytics/dashboards">
              Back to dashboards
            </Link>
          }
        />
      </Card>
    );
  }
  if (state.error || !state.data) return <LoadFailed title="Dashboard" problem={state.error} retry={state.reload} />;

  const dashboard = saved ?? state.data.dashboard;
  const metrics = state.data.metrics;
  const metricOf = (metricId: string) => metrics.find((m) => m.metricId === metricId);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={dashboard.title}
        description={`${dashboard.tiles.length} tiles, refreshed every ${dashboard.refreshSeconds} s. Version ${dashboard.version}.`}
      />
      <p role="status" className={styles.muted}>
        {saved ? `Dashboard saved as version ${saved.version}.` : ""}
      </p>
      {values.error ? (
        <Alert tone="warning" title="Could not load the tile values" live="status" action={<Button size="sm" onClick={values.reload}>Try again</Button>}>
          <ProblemMessage problem={values.error} />
          {shownValues ? ` The values below are from ${dateTime(shownValues.computedAt)}.` : null}
        </Alert>
      ) : (
        <div className={styles.formActions}>
          <Button size="sm" onClick={values.reload} loading={values.loading && shownValues !== null}>
            Refresh values
          </Button>
          {shownValues ? <span className={styles.muted}>Values from {dateTime(shownValues.computedAt)}.</span> : null}
        </div>
      )}
      <CardGrid label="Tiles">
        {dashboard.tiles.map((t, i) => {
          const metric = metricOf(t.metric);
          const tv = shownValues?.tiles[i]?.metric === t.metric ? shownValues.tiles[i] : undefined;
          return (
            <Card key={`${i}-${t.metric}`} title={t.title}>
              <p className={styles.tileValue} aria-live="off">
                {tv ? tileValueText(tv) : values.loading ? "Loading…" : "Unavailable"}
              </p>
              {tv ? <p className={styles.muted}>{tileStatusHint(tv)}{tv.status === "OK" ? `, ${tv.from} to ${tv.to}` : ""}</p> : null}
              <dl className={styles.details}>
                <div className={styles.entry}>
                  <dt className={styles.term}>Metric</dt>
                  <dd className={styles.value}>
                    <Link to={`/analytics/metrics/${encodeURIComponent(t.metric)}`}>{metric?.name ?? t.metric}</Link>
                  </dd>
                </div>
                <div className={styles.entry}>
                  <dt className={styles.term}>Period</dt>
                  <dd className={styles.value}>{periodLabels[t.period]}</dd>
                </div>
              </dl>
              {metric ? <code className={styles.formula}>{metric.formula}</code> : null}
            </Card>
          );
        })}
      </CardGrid>
      <DashboardForm key={dashboard.version} initial={fromDashboard(dashboard)} existing={dashboard} metrics={metrics} onSaved={setSaved} />
    </div>
  );
}
