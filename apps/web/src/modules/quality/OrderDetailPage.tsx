import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { getOrder, listInstruments, type Instrument, type OrderView, type Result } from "./api/client";
import { CertificatesPanel } from "./CertificatesPanel";
import { CoAPanel } from "./CoAPanel";
import { CERT_LABELS, DECISION_LABELS, dash, formatDateTime, qty, shortId, TRIGGER_LABELS } from "./format";
import { OrderStatusBadge } from "./OrdersPage";
import styles from "./pages.module.css";
import { ResultsEntry } from "./ResultsEntry";
import { SamplesPanel } from "./SamplesPage";
import { UsageDecisionForm } from "./UsageDecisionForm";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { SourceDocument } from "../mfg-shared/orders";

interface Detail {
  order: OrderView;
  instruments: Instrument[];
}

function resultColumns(order: OrderView, instruments: Instrument[]): DataTableColumn<Result>[] {
  const names = new Map(order.testGroup.tests.map((t) => [t.testId, t.name || shortId(t.testId)]));
  const codes = new Map(instruments.map((i) => [i.id, i.code]));
  return [
    { key: "sampleNo", header: "Sample", align: "end", render: (row) => String(row.sampleNo) },
    { key: "testId", header: "Test", render: (row) => names.get(row.testId) ?? shortId(row.testId) },
    { key: "retestNo", header: "Retest", align: "end", render: (row) => (row.retestNo > 0 ? String(row.retestNo) : dash) },
    { key: "value", header: "Value or outcome", mono: true, render: (row) => (row.value !== undefined ? qty(row.value) : shortId(row.outcomeId)) },
    {
      key: "passed",
      header: "Result",
      render: (row) => <span className={row.passed ? styles.pass : styles.fail}>{row.passed ? "Pass" : "Fail"}</span>,
    },
    { key: "instrumentId", header: "Instrument", mono: true, render: (row) => (row.instrumentId ? (codes.get(row.instrumentId) ?? shortId(row.instrumentId)) : dash) },
    { key: "recordedBy", header: "Recorded by", render: (row) => row.recordedBy || dash },
    { key: "recordedAt", header: "Recorded", render: (row) => formatDateTime(row.recordedAt) },
  ];
}

/** One quality order: lot and sample, results per test and sample, the computed verdict and the usage decision. */
export function OrderDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<Detail>(async (signal) => {
    const [order, instruments] = await Promise.all([getOrder(id, signal), listInstruments(signal).catch(() => [] as Instrument[])]);
    return { order, instruments };
  }, id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Quality order" />
        <Alert tone="danger" title="Could not load the quality order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Quality order" />
        <span className="visually-hidden">Loading the quality order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { order, instruments } = state.data;
  const ev = order.evaluation;
  const complete = ev.missingRequired.length === 0;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Quality order ${order.number ?? shortId(order.id)}`}
        description={
          <>
            {TRIGGER_LABELS[order.trigger] ?? order.trigger}, {order.sourceRef.type} <SourceDocument type={order.sourceRef.type} id={order.sourceRef.id} />
          </>
        }
        meta={<OrderStatusBadge status={order.status} />}
        actions={
          <Link className={buttonClassName("secondary")} to="/quality/orders">
            All quality orders
          </Link>
        }
      />
      <StatGroup label="Inspection summary">
        <StatTile label="Lot" value={`${qty(order.lotQty)} ${order.uom}`} />
        <StatTile label="Sample" value={qty(order.sampleQty)} />
        <StatTile label="Accept / reject" value={`${order.acceptNo} / ${order.rejectNo}`} />
        <StatTile label="Defective samples" value={ev.defectiveSamples} tone={ev.passed ? "neutral" : "danger"} />
      </StatGroup>
      {complete ? (
        <Alert tone={ev.passed ? "success" : "warning"} title={ev.passed ? "The lot passes" : "The lot fails"}>
          {ev.passed
            ? `${ev.defectiveSamples} defective ${ev.defectiveSamples === 1 ? "sample" : "samples"}, within the acceptance number ${order.acceptNo}.`
            : `${ev.defectiveSamples} defective samples reach the rejection number ${order.rejectNo}. Retest, or decide with a reject, rework or deviation.`}
        </Alert>
      ) : (
        <Alert tone="info" title="Results still needed">
          {`${ev.missingRequired.length} required ${ev.missingRequired.length === 1 ? "test lacks" : "tests lack"} a result for every sample. The usage decision opens once they are recorded.`}
        </Alert>
      )}
      <Card title="Order">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Item</dt>
            <dd className={styles.value}><EntityName entity="product" id={order.itemId} fallback={order.itemId} /></dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Site / warehouse</dt>
            <dd className={styles.value}>
              <EntityName entity="site" id={order.siteId} fallback={order.siteId} />
              {" / "}
              <EntityName entity="warehouse" id={order.warehouseId} fallback={order.warehouseId} />
            </dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Batch</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{order.dims?.batch || dash}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Test group</dt>
            <dd className={styles.value}>{`${order.testGroup.code} v${order.testGroupVersion}`}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Sampling</dt>
            <dd className={styles.value}>
              {order.samplingMethod === "AQL" ? `AQL code ${order.codeLetter}, ${order.severity.toLowerCase()}` : order.samplingMethod.toLowerCase()}
            </dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Created</dt>
            <dd className={styles.value}>{formatDateTime(order.createdAt)}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Vendor certificate</dt>
            <dd className={styles.value}>{CERT_LABELS[order.certificateRequired ?? "NONE"]}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Inventory hold</dt>
            <dd className={styles.value}>
              <Badge tone="neutral">Placed by the inventory ledger (handoff)</Badge>
            </dd>
          </div>
        </dl>
      </Card>
      <Card title="Results">
        <DataTable
          label="Results"
          columns={resultColumns(order, instruments)}
          rows={order.results}
          getRowKey={(row) => `${row.testId}|${row.sampleNo}|${row.retestNo}`}
          empty={{ title: "No results yet", description: "Record a result for each required test on each sample." }}
        />
      </Card>
      {order.status !== "CLOSED" && order.status !== "CANCELLED" ? <ResultsEntry order={order} instruments={instruments} onRecorded={state.reload} /> : null}
      <CertificatesPanel order={order} onChanged={state.reload} />
      <SamplesPanel order={order} />
      {order.decision ? (
        <Card title="Usage decision">
          <dl className={styles.details}>
            <div className={styles.entry}>
              <dt className={styles.term}>Accepted</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{qty(order.decision.split.acceptQty)}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Rejected</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{qty(order.decision.split.rejectQty)}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>With deviation</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{qty(order.decision.split.deviationQty)}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Decided</dt>
              <dd className={styles.value}>{`${order.decision.decidedBy || dash}, ${formatDateTime(order.decision.decidedAt)}`}</dd>
            </div>
          </dl>
          <ul className={styles.plainList} aria-label="Decision lines">
            {order.decision.lines.map((line, i) => (
              <li key={i}>{`${DECISION_LABELS[line.decision] ?? line.decision}: ${qty(line.quantity)} ${order.uom}${line.reason ? ` (${line.reason})` : ""}`}</li>
            ))}
          </ul>
          <div className={`${styles.actions} ${styles.spaced}`}>
            {order.decision.nonconformanceId ? (
              <Link className={buttonClassName("secondary", "sm")} to={`/quality/nonconformances/${order.decision.nonconformanceId}`}>
                {`Open NCR ${shortId(order.decision.nonconformanceId)} for the rejected quantity`}
              </Link>
            ) : null}
            <Link className={buttonClassName("ghost", "sm")} to={`/quality/nonconformances/new?qualityOrderId=${order.id}`}>
              Amend through a nonconformance
            </Link>
          </div>
        </Card>
      ) : order.status === "PENDING_DECISION" ? (
        <UsageDecisionForm order={order} onPosted={state.reload} />
      ) : null}
      {order.status === "CLOSED" ? <CoAPanel order={order} /> : null}
    </div>
  );
}
