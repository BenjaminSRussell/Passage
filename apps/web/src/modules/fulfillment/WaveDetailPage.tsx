import { useState, type FormEvent } from "react";
import { useNames } from "../mfg-shared/names";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import { confirmWavePick, describeProblem, getWave, type Problem, type ShortPickReason, type Wave, type WaveLine, type WavePickResult, type WaveShipment } from "./api/client";
import { ShipmentStatusBadge } from "./badges";
import { AssignWaveCard, CloseWavePanel, PickListCard, WaveStatusBadge } from "./WaveActions";
import { compareDecimal, dash, DECIMAL, formatDate, formatDateTime, GROUP_LABELS, SHORT_PICK_REASONS, shortId } from "./format";
import { explain, ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { OrderNumber } from "../sales/OrderNumber";

const shipmentColumns: DataTableColumn<WaveShipment>[] = [
  { key: "shipmentNumber", header: "Shipment", mono: true },
  { key: "status", header: "Status", render: (row) => <ShipmentStatusBadge status={row.status} /> },
  { key: "orderId", header: "Order", render: (row) => <OrderNumber id={row.orderId} /> },
  { key: "customerId", header: "Customer", render: (row) => <EntityName entity="customer" id={row.customerId} /> },
  { key: "carrierCode", header: "Carrier", render: (row) => row.carrierCode ?? dash },
  { key: "plannedShipDate", header: "Planned ship", render: (row) => formatDate(row.plannedShipDate) },
  { key: "lineCount", header: "Lines", align: "end" },
];

const PICKABLE = new Set<string>(["RELEASED", "PICKING", "PARTIALLY_SHIPPED"]);

/**
 * Confirm the picks of the whole wave (FR-28.13): every line not yet confirmed starts at its
 * planned quantity; a lower quantity is a short pick and takes a reason. One command confirms
 * the lot, and the wave is reloaded from the service afterwards.
 */
export function WavePickCard({ wave, onDone }: { wave: Wave; onDone(): void }) {
  // Only lines of shipments that still take picks: a cancelled or already
  // picked shipment of the wave is left alone by the service.
  const pickable = new Set(wave.shipments.filter((sh) => PICKABLE.has(sh.status)).map((sh) => sh.id));
  const open = (wave.lines ?? []).filter((l) => !l.pickConfirmed && pickable.has(l.shipmentId));
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(open.map((l) => [l.shipmentLineId, l.qtyPlanned])));
  const [reasons, setReasons] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<WavePickResult | null>(null);
  const [busy, setBusy] = useState(false);
  const productNames = useNames("product", open.map((l) => l.productId));
  const key = useCommandKey();

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const picks: { shipmentLineId: string; picked: string; reason?: ShortPickReason }[] = [];
    for (const l of open) {
      const v = (values[l.shipmentLineId] ?? "").trim();
      if (!DECIMAL.test(v)) found[l.shipmentLineId] = "Enter the picked quantity, 0 or more, with at most 6 decimals.";
      else if (compareDecimal(v, l.qtyPlanned) > 0) found[l.shipmentLineId] = `Over-pick: at most ${l.qtyPlanned} is planned (FUL-0004).`;
      else if (compareDecimal(v, l.qtyPlanned) < 0) picks.push({ shipmentLineId: l.shipmentLineId, picked: v, ...(reasons[l.shipmentLineId] ? { reason: reasons[l.shipmentLineId] as ShortPickReason } : {}) });
    }
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const done = await confirmWavePick(wave.id, { picks, pickRest: true }, key.current());
      key.renew();
      setResult(done);
      onDone();
    } catch (error) {
      const problem = explain(describeProblem(error));
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.renew();
    } finally {
      setBusy(false);
    }
  }

  if (result) {
    return (
      <Alert tone="success" title="Wave picks confirmed" live="status">
        {result.linesConfirmed} {result.linesConfirmed === 1 ? "line" : "lines"} on {result.shipments.length} {result.shipments.length === 1 ? "shipment" : "shipments"}
        {result.shortLines > 0 ? `, ${result.shortLines} short` : ""}.
      </Alert>
    );
  }
  // A CLOSED wave takes no picks (FUL-0001); its remainders are re-picked on the shipment.
  if (wave.status !== "OPEN" || open.length === 0) return null;
  return (
    <Card title="Confirm picks for the wave">
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>
          Every line is picked in full unless you enter less. A short line goes to backorder; give the reason so the short picks report can show it.
        </p>
        {open.map((l: WaveLine) => {
          const short = DECIMAL.test((values[l.shipmentLineId] ?? "").trim()) && compareDecimal(values[l.shipmentLineId].trim(), l.qtyPlanned) < 0;
          return (
            <div key={l.shipmentLineId} className={styles.form}>
              <FormField
                id={`wp-${l.shipmentLineId}`}
                label={`${l.shipmentNumber}, product ${productNames[l.productId] ?? shortId(l.productId)} (${l.unit}) picked of ${l.qtyPlanned}`}
                error={errors[l.shipmentLineId]}
              >
                {(control) => (
                  <Input
                    {...control}
                    inputMode="decimal"
                    value={values[l.shipmentLineId] ?? ""}
                    invalid={Boolean(errors[l.shipmentLineId])}
                    onChange={(event) => {
                      setValues({ ...values, [l.shipmentLineId]: event.target.value });
                      key.renew();
                    }}
                  />
                )}
              </FormField>
              {short ? (
                <FormField id={`wr-${l.shipmentLineId}`} label={`Reason for ${l.shipmentNumber}, product ${productNames[l.productId] ?? shortId(l.productId)} short pick`} optional>
                  {(control) => (
                    <Select {...control} value={reasons[l.shipmentLineId] ?? ""} onChange={(event) => {
                        setReasons({ ...reasons, [l.shipmentLineId]: event.target.value });
                        key.renew();
                      }}>
                      <option value="">No reason given</option>
                      {Object.entries(SHORT_PICK_REASONS).map(([code, label]) => (
                        <option key={code} value={code}>
                          {label}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
              ) : null}
            </div>
          );
        })}
        <ProblemAlert title="Could not confirm the wave picks" problem={failure} />
        <Button type="submit" disabled={busy}>
          {busy ? "Confirming…" : `Confirm ${open.length} ${open.length === 1 ? "line" : "lines"}`}
        </Button>
      </form>
    </Card>
  );
}

/** One pick wave: its group and its shipments with their current status. */
export function WaveDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getWave(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Pick wave" />
        <Alert tone="danger" title="Could not load the wave" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Pick wave" />
        <span className="visually-hidden">Loading the wave…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const wave = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Wave ${wave.waveNumber}`}
        description={`${wave.lineCount} ${wave.lineCount === 1 ? "line" : "lines"} of ${wave.orderCount} ${wave.orderCount === 1 ? "order" : "orders"}, grouped by ${(GROUP_LABELS[wave.groupBy] ?? wave.groupBy).toLowerCase()}.`}
        actions={
          <span className={`${styles.actions} ${styles.noPrint}`}>
            <WaveStatusBadge status={wave.status} />
            <Link className={buttonClassName("secondary")} to="/fulfillment/waves">
              All waves
            </Link>
          </span>
        }
      />
      <Card>
        <dl className={styles.details}>
          {[
            ["Group", wave.groupKey === "UNASSIGNED" ? "Unassigned" : wave.groupKey],
            ["Picker", wave.picker ?? "Not assigned"],
            ["Warehouse", <EntityName entity="warehouse" id={wave.warehouseId} />],
            ["Legal entity", <EntityName entity="legalEntity" id={wave.legalEntityId} />],
            ["Created", formatDateTime(wave.createdAt)],
            ...(wave.closedAt ? [["Closed", formatDateTime(wave.closedAt)]] : []),
          ].map(([term, value]) => (
            <div key={String(term)} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <div className={styles.noPrint}>
        <AssignWaveCard wave={wave} onDone={state.reload} />
      </div>
      <div className={styles.noPrint}>
        <WavePickCard wave={wave} onDone={state.reload} />
      </div>
      <PickListCard wave={wave} />
      <div className={styles.noPrint}>
        <CloseWavePanel key={`close-${wave.version}`} wave={wave} onDone={state.reload} />
      </div>
      <Card title="Shipments">
        <DataTable
          label="Wave shipments"
          columns={shipmentColumns}
          rows={wave.shipments}
          getRowKey={(row) => row.id}
          onRowOpen={(row) => navigate(`/fulfillment/shipments/${row.id}`)}
          empty={{ title: "No shipments", description: "This wave holds no shipment you can see." }}
        />
      </Card>
    </div>
  );
}
