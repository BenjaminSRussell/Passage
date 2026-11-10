import { Link, useParams } from "react-router-dom";
import { Badge, buttonClassName, Card, DataTable, EmptyState, PageHeader, Skeleton, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { getAbcRun, type AbcClass, type AbcItem } from "./api/client";
import { formatDateTime } from "./format";
import { Detail, ProblemAlert } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const classTones: Record<AbcClass, BadgeTone> = { A: "success", B: "info", C: "neutral", N: "neutral" };

const columns: DataTableColumn<AbcItem>[] = [
  { key: "productId", header: "Product", render: (row) => <Named entity="product" id={row.productId} /> },
  { key: "site", header: "Site", mono: true },
  { key: "value", header: "On-hand value", align: "end", mono: true },
  { key: "cumPct", header: "Cumulative %", align: "end", mono: true },
  { key: "class", header: "Class", render: (row) => <Badge tone={classTones[row.class] ?? "neutral"}>{row.class === "N" ? "Not classified" : `Class ${row.class}`}</Badge> },
];

/** ABC classification report (PRD 3.16.14): each product and site with its value, cumulative share and class. */
export function AbcRunPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAbcRun(id, signal), id);
  const back = (
    <Link className={buttonClassName("ghost")} to="/inventory/abc-runs">
      Back to ABC runs
    </Link>
  );

  if (state.error) {
    if (state.error.code === "INV-ABC-002") {
      return <EmptyState icon="xCircle" headingLevel={2} title="ABC run not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <ProblemAlert title="Could not load the ABC run" problem={state.error} onRetry={state.reload} />;
  }
  const run = state.data;
  if (!run) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading ABC run…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="ABC classification run" description={`By on-hand value · ${formatDateTime(run.createdAt)}`} actions={back} />
      <Card title="Setup">
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {run.legalEntityId}
          </Detail>
          <Detail term="Thresholds A / B / C">{`${run.thresholds.a}% / ${run.thresholds.b}% / ${run.thresholds.c}%`}</Detail>
          <Detail term="Products with no value">{run.zeroClass === "N" ? "Not classified" : "Class C"}</Detail>
          <Detail term="Items per class">{`A ${run.classCounts.A ?? 0} · B ${run.classCounts.B ?? 0} · C ${run.classCounts.C ?? 0} · N ${run.classCounts.N ?? 0}`}</Detail>
        </dl>
      </Card>
      <DataTable
        label="Classified items"
        columns={columns}
        rows={run.items ?? []}
        getRowKey={(row) => `${row.productId}|${row.site}`}
        empty={{ title: "Nothing to classify", description: "The legal entity held no stock when the run was made." }}
      />
    </div>
  );
}
