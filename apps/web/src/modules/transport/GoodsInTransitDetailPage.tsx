import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getGoodsInTransit, shipGoodsInTransit, type GITLine, type GoodsInTransit, type Problem } from "./api/client";
import { Details } from "./Details";
import { fixed, formatDateTime, money, stateTone, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** One column per cost type, then the line's estimate: what moves into transit per receipt line. */
function lineColumns(g: GoodsInTransit): DataTableColumn<GITLine>[] {
  const costTypes = g.lines[0]?.costs.map((c) => c.costType) ?? [];
  return [
    { key: "line", header: "Receipt line", align: "end" },
    { key: "quantity", header: "Quantity", align: "end" },
    { key: "value", header: "Goods value", align: "end", render: (row) => fixed(row.value, 2) },
    ...costTypes.map((type, i) => ({
      key: `cost-${i}`,
      header: type,
      align: "end" as const,
      render: (row: GITLine) => fixed(row.costs[i]?.amount ?? "0", 2),
    })),
    { key: "estimated", header: "Estimated landed cost", align: "end", render: (row) => money(row.estimated, g.currency) },
  ];
}

/** A goods-in-transit order; "Ship" posts it at the transfer event and moves ownership (BR-34.11, TMS-0121). */
export function GoodsInTransitDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getGoodsInTransit(id, signal), `git:${id}`);
  const [shipped, setShipped] = useState<GoodsInTransit | null>(null);
  const [shippedAt, setShippedAt] = useState("");
  const [error, setError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const order = shipped && shipped.id === state.data?.id ? shipped : state.data;

  async function ship(event: FormEvent) {
    event.preventDefault();
    if (!order) return;
    setFailure(null);
    let at: string | undefined;
    if (shippedAt !== "") {
      const d = new Date(shippedAt);
      if (Number.isNaN(d.getTime())) {
        setError("Enter the departure date and time.");
        return;
      }
      at = d.toISOString();
    }
    setError("");
    setBusy(true);
    try {
      setShipped(await shipGoodsInTransit(order.id, at));
    } catch (e) {
      const problem = describeProblem(e);
      if (problem.fields.shippedAt) setError(problem.fields.shippedAt);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Goods in transit" />
        <Alert tone="danger" title="Could not load the order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link to="/transport/goods-in-transit">Back to goods in transit</Link>
      </div>
    );
  }
  if (!order) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Goods in transit" />
        <Card>
          <p className={styles.note}>Loading the order…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={`Goods in transit, voyage ${order.voyageReference}`} meta={<Badge tone={stateTone(order.status)}>{statusLabel(order.status)}</Badge>} />
      <Alert tone="success" live="status">
        {shipped ? `Ownership moved to ${shipped.inTransitWarehouse} at origin shipment (${shipped.incoterm}, ${shipped.code ?? "TMS-0121"}).` : null}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="Could not ship the order" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card>
        <Details
          entries={[
            { term: "Order", value: order.id, mono: true },
            { term: "Voyage", value: <Link to={`/transport/voyages/${order.voyageId}`}>{order.voyageReference}</Link> },
            { term: "Incoterm", value: order.incoterm, mono: true },
            { term: "Transfer event", value: "Origin shipment" },
            { term: "In-transit warehouse", value: order.inTransitWarehouse, mono: true },
            { term: "Goods value", value: money(order.goodsValue, order.currency) },
            { term: "Estimated landed cost", value: money(order.estimatedTotal, order.currency) },
            { term: "Created", value: formatDateTime(order.createdAt) },
            { term: "Shipped", value: formatDateTime(order.shippedAt) },
          ]}
        />
      </Card>
      {order.status === "PENDING" ? (
        <Card title="Ship" headingLevel={3}>
          <div className={styles.stack}>
            <p className={styles.note}>Ship when the forwarder reports the departure. The estimated landed cost goes with the goods to the in-transit warehouse.</p>
            <form className={styles.inlineForm} onSubmit={ship} noValidate>
              <FormField id="git-shipped-at" label="Departed" hint="Leave empty for now." optional error={error || undefined}>
                {(control) => <Input {...control} type="datetime-local" value={shippedAt} onChange={(e) => setShippedAt(e.target.value)} invalid={Boolean(error)} />}
              </FormField>
              <Button type="submit" variant="primary" loading={busy}>
                Ship
              </Button>
            </form>
          </div>
        </Card>
      ) : null}
      <Card title="Receipt lines in transit" headingLevel={3}>
        <DataTable label="Receipt lines in transit" columns={lineColumns(order)} rows={order.lines} getRowKey={(row) => String(row.line)} />
      </Card>
      <Link to="/transport/goods-in-transit">Back to goods in transit</Link>
    </div>
  );
}
