import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createBatchOrder,
  describeProblem,
  getBatchOrder,
  listBatchOrders,
  listFormulas,
  newKey,
  ORDER_STATUSES,
  rescaleBatchOrder,
  type BatchOrder,
  type OrderLine,
  type OrderStatus,
  type Problem,
} from "./api/client";
import { formatDateTime, LINE_TYPE_LABELS, QUANTITY, shortId } from "./format";
import { AllocationCard, OrderLifecycle } from "./OrderLifecycle";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const STATUS_LABELS: Record<OrderStatus, string> = {
  planned: "Planned",
  released: "Released",
  started: "Started",
  reported: "Reported as finished",
  ended: "Ended",
  closed: "Closed",
};

/** BR-24.1 and PRD 4.24.5: only orders that have not started can be rescaled. */
export function canRescale(status: OrderStatus): boolean {
  return status === "planned" || status === "released";
}

function positiveQuantity(value: string): boolean {
  return QUANTITY.test(value) && !/^0+(\.0+)?$/.test(value);
}

const listColumns: DataTableColumn<BatchOrder>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "formulaCode", header: "Formula", mono: true, render: (o) => `${o.formulaCode} v${o.formulaVersion}` },
  { key: "batchSize", header: "Batch size", align: "end", render: (o) => `${o.batchSize} ${o.unit}` },
  { key: "scaleFactor", header: "Scale factor", align: "end" },
  { key: "status", header: "Status", render: (o) => <StatusPill status={o.status} /> },
  { key: "createdAt", header: "Created", render: (o) => formatDateTime(o.createdAt) },
];

/** Batch orders, newest first, with a status filter (PRD 4.24.14 "Batch orders"). */
export function BatchOrdersPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<OrderStatus | "">("");
  const state = useLoad((signal) => listBatchOrders({ status }, signal), `orders|${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Batch orders"
        description="Orders created from a formula at a batch size. Lines are scaled from the formula as it was when the order was created."
        actions={
          <Link className={buttonClassName("primary")} to="/processmfg/batch-orders/new">
            New batch order
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="order-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as OrderStatus | "")}>
              <option value="">All statuses</option>
              {ORDER_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {STATUS_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load batch orders" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Batch orders"
          columns={listColumns}
          rows={state.data ?? []}
          getRowKey={(o) => o.id}
          loading={state.loading}
          empty={
            status
              ? { title: "No batch orders with this status", description: "Choose another status, or all statuses." }
              : { title: "No batch orders yet", description: "Create a batch order from a formula." }
          }
          onRowOpen={(o) => navigate(`/processmfg/batch-orders/${o.id}`)}
        />
      )}
    </div>
  );
}

/** Create a batch order from a formula at a batch size (FR-24.2). */
export function NewBatchOrderPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const formulas = useLoad((signal) => listFormulas(signal), "formulas");
  const [formulaId, setFormulaId] = useState(params.get("formulaId") ?? "");
  const [batchSize, setBatchSize] = useState("");
  const [errors, setErrors] = useState<{ formulaId?: string; batchSize?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const chosen = formulas.data?.find((f) => f.id === formulaId);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: typeof errors = {};
    if (!chosen) found.formulaId = "Choose a formula.";
    if (!positiveQuantity(batchSize)) found.batchSize = "Enter a positive batch size with at most 6 decimals.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const order = await createBatchOrder({ formulaId, batchSize }, key.current);
      navigate(`/processmfg/batch-orders/${order.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.fields.batchSize) setErrors({ batchSize: problem.fields.batchSize });
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New batch order" description="The order takes a snapshot of the formula and scales its lines to the batch size." />
      {formulas.error ? (
        <Alert tone="danger" title="Could not load formulas" live="alert" action={<Button size="sm" onClick={formulas.reload}>Try again</Button>}>
          {formulas.error.message} ({formulas.error.code})
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="order-formula" label="Formula" error={errors.formulaId} hint={formulas.loading ? "Loading formulas…" : undefined}>
            {(c) => (
              <Select
                {...c}
                value={formulaId}
                onChange={(e) => {
                  setFormulaId(e.target.value);
                  key.current = newKey();
                }}
                invalid={Boolean(errors.formulaId)}
              >
                <option value="">Choose a formula</option>
                {(formulas.data ?? []).map((f) => (
                  <option key={f.id} value={f.id}>
                    {`${f.code} — ${f.name} (${f.size} ${f.unit})`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="order-size" label={`Batch size${chosen ? ` (${chosen.unit})` : ""}`} error={errors.batchSize}>
            {(c) => (
              <Input
                {...c}
                inputMode="decimal"
                value={batchSize}
                onChange={(e) => {
                  setBatchSize(e.target.value);
                  key.current = newKey();
                }}
                invalid={Boolean(errors.batchSize)}
              />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the batch order" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create batch order
            </Button>
            <Link className={buttonClassName("ghost")} to="/processmfg/batch-orders">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

const lineColumns: DataTableColumn<OrderLine>[] = [
  { key: "lineNumber", header: "Line", align: "end" },
  { key: "itemId", header: "Item", mono: true, render: (l) => shortId(l.itemId) },
  { key: "type", header: "Type", render: (l) => LINE_TYPE_LABELS[l.type] ?? l.type },
  { key: "formulaQty", header: "Formula qty", align: "end" },
  { key: "qty", header: "Order qty", align: "end" },
  { key: "rafQty", header: "Reported qty", align: "end", render: (l) => l.rafQty ?? "—" },
  { key: "scalable", header: "Scaling", render: (l) => (l.percentControlled ? `${l.percent}% of batch` : l.scalable ? "Scalable" : "Fixed") },
];

/** One batch order: header facts, scaled lines and rescale while not started. */
export function BatchOrderDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getBatchOrder(id, signal), id);
  const [current, setCurrent] = useState<BatchOrder | null>(null);
  const [size, setSize] = useState("");
  const [sizeError, setSizeError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Batch order" />
        <Alert tone="danger" title="Could not load the batch order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Batch order" />
        <span className="visually-hidden">Loading the batch order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const order = current && current.id === state.data.id && current.version >= state.data.version ? current : state.data;

  async function rescale(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice(null);
    if (!positiveQuantity(size)) {
      setSizeError("Enter a positive batch size with at most 6 decimals.");
      return;
    }
    setSizeError(null);
    setBusy(true);
    try {
      const updated = await rescaleBatchOrder(order.id, { batchSize: size, version: order.version }, key.current);
      setCurrent(updated);
      setSize("");
      setNotice(`Rescaled to ${updated.batchSize} ${updated.unit}.`);
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={order.orderNumber} description={`Formula ${order.formulaCode} version ${order.formulaVersion}`} meta={<StatusPill status={order.status} />} />
      <StatGroup label="Batch order facts">
        <StatTile label="Batch size" value={`${order.batchSize} ${order.unit}`} />
        <StatTile label="Formula size" value={`${order.formulaSize} ${order.unit}`} />
        <StatTile label="Scale factor" value={order.scaleFactor} />
        {order.actualQty ? <StatTile label="Finished quantity" value={`${order.actualQty} ${order.unit}`} /> : null}
      </StatGroup>
      <OrderLifecycle
        order={order}
        reload={() => {
          setCurrent(null);
          state.reload();
        }}
        onChanged={(updated) => {
          setCurrent(updated);
          setNotice(null);
          setFailure(null);
        }}
      />
      <AllocationCard order={order} />
      <Card title="Lines">
        <DataTable
          label="Batch order lines"
          columns={lineColumns}
          rows={order.lines ?? []}
          getRowKey={(l) => String(l.lineNumber)}
          empty={{ title: "No lines", description: "The formula had no lines when the order was created." }}
        />
      </Card>
      <Card title="Rescale">
        {canRescale(order.status) ? (
          <div className={styles.stack}>
            <form className={styles.toolbar} onSubmit={rescale} noValidate>
              <FormField id="rescale-size" label={`New batch size (${order.unit})`} error={sizeError ?? undefined}>
                {(c) => (
                  <Input
                    {...c}
                    inputMode="decimal"
                    value={size}
                    onChange={(e) => {
                      setSize(e.target.value);
                      key.current = newKey();
                    }}
                    invalid={Boolean(sizeError)}
                  />
                )}
              </FormField>
              <Button type="submit" loading={busy}>
                Rescale batch
              </Button>
            </form>
            {notice ? <Alert tone="success" title={notice} live="status" /> : null}
            {failure ? (
              <Alert
                tone="danger"
                title="Could not rescale the batch order"
                live="alert"
                action={
                  failure.code === "PMF-0104" ? (
                    <Button size="sm" onClick={state.reload}>
                      Reload order
                    </Button>
                  ) : undefined
                }
              >
                {failure.message} ({failure.code})
              </Alert>
            ) : null}
          </div>
        ) : (
          <p className={styles.note}>{`The order is ${STATUS_LABELS[order.status].toLowerCase()}; only planned or released orders can be rescaled.`}</p>
        )}
      </Card>
      <p className={styles.muted}>{`Created ${formatDateTime(order.createdAt)} by ${order.createdBy || "unknown"} · updated ${formatDateTime(order.updatedAt)} · version ${order.version}`}</p>
    </div>
  );
}
