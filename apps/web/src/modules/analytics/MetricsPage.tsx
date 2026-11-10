import type { ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  PageHeader,
  Skeleton,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import { listMetrics, type Metric } from "./api/client";
import { dimensionLabel, grainLabel, reconciledAgainst } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<Metric>[] = [
  { key: "metricId", header: "Metric", mono: true },
  { key: "name", header: "Name" },
  { key: "version", header: "Version", align: "end" },
  { key: "grain", header: "Grain", render: (m) => grainLabel(m.grain) },
  { key: "reconciliationSource", header: "Reconciled against", render: (m) => reconciledAgainst(m) },
  { key: "status", header: "Status", render: (m) => <StatusPill status={m.status} /> },
];

/** Governed metric registry (PRD §9.46.6 "Metric registry"): every published metric. */
export function MetricsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listMetrics(undefined, signal), "metrics");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Governed metrics"
        description="The published metric definitions every dashboard and explorer query uses. A published version never changes; a revision becomes the next version."
        actions={
          <Link className={buttonClassName("primary")} to="/analytics/explorer">
            Open explorer
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the metric catalog" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Governed metrics"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(m) => `${m.metricId}@${m.version}`}
          loading={state.loading}
          empty={{ title: "No published metrics yet", description: "Metrics appear here once a version is published." }}
          onRowOpen={(m) => navigate(`/analytics/metrics/${encodeURIComponent(m.metricId)}`)}
        />
      )}
    </div>
  );
}

function Entry({ term, wide, children }: { term: string; wide?: boolean; children: ReactNode }) {
  return (
    <div className={wide ? `${styles.entry} ${styles.wide}` : styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

/** One governed metric: formula, grain, allowed dimensions and reconciliation source. */
export function MetricDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => listMetrics(undefined, signal), "metrics");
  const metric = state.data?.find((m) => m.metricId === id);

  if (state.loading) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Metric" />
        <Card>
          <div className={styles.loading}>
            <Skeleton width="1/2" />
            <Skeleton width="3/4" />
            <Skeleton width="full" />
          </div>
        </Card>
        <p role="status" className={styles.muted}>
          Loading metric {id}…
        </p>
      </div>
    );
  }
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Metric" />
        <Alert tone="danger" title="Could not load the metric" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!metric) {
    return (
      <Card>
        <EmptyState
          headingLevel={2}
          icon="database"
          title="We couldn't find that metric"
          description={`No published metric has the id ${id}.`}
          action={
            <Link className={buttonClassName("secondary")} to="/analytics">
              Back to governed metrics
            </Link>
          }
        />
      </Card>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={metric.name}
        description={`Version ${metric.version} of ${metric.metricId}.`}
        meta={<StatusPill status={metric.status} />}
        actions={
          <Link className={buttonClassName("primary")} to={`/analytics/explorer?measure=${encodeURIComponent(metric.metricId)}`}>
            Explore {metric.metricId}
          </Link>
        }
      />
      <Card title="Definition">
        <dl className={styles.details}>
          <Entry term="Metric id">
            <span className={styles.mono}>{metric.metricId}</span>
          </Entry>
          <Entry term="Grain">{grainLabel(metric.grain)}</Entry>
          <Entry term="Reconciled against">
            {metric.nonFinancial ? <Badge tone="neutral">Non-financial</Badge> : <span className={styles.mono}>{metric.reconciliationSource}</span>}
          </Entry>
          <Entry term="Formula" wide>
            <code className={styles.formula}>{metric.formula}</code>
          </Entry>
          <Entry term="Allowed dimensions" wide>
            <span className={styles.tags}>
              {metric.allowedDimensions.map((d) => (
                <Badge key={d} tone="info">
                  {dimensionLabel(d)}
                </Badge>
              ))}
            </span>
          </Entry>
        </dl>
      </Card>
    </div>
  );
}
