import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  createReworkOrder,
  describeProblem,
  getBatch,
  getReworkOrder,
  listReworkOrders,
  newKey,
  receiveReworkOrder,
  REWORK_OUTPUT_DISPOSITIONS,
  type Problem,
  type ReworkOrder,
  type ReworkOutputDisposition,
} from "./api/client";
import { dash, QUANTITY, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Duty that creates rework orders and receives their output; enforced by the service (FR-24.18). */
export const REWORK_DUTY = "production.batch-order.write";
/** Receiving an output planned as available releases the batch, so it also needs the disposition duty (PRD 4.24.17). */
export const RELEASE_DUTY = "production.batch-disposition.write";

const DISPOSITION_TEXT: Record<ReworkOutputDisposition, string> = { quarantine: "Quarantine", "on-hold": "On hold", available: "Available" };

function StatusBadge({ rework }: { rework: ReworkOrder }) {
  return rework.status === "received" ? <Badge tone="success">Received</Badge> : <Badge tone="warning">Planned</Badge>;
}

function LoadError({ title, error, reload }: { title: string; error: Problem; reload: () => void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
      {error.message} ({error.code})
    </Alert>
  );
}

const columns: DataTableColumn<ReworkOrder>[] = [
  { key: "orderNumber", header: "Batch order", mono: true },
  { key: "sourceBatchNumber", header: "Batch", mono: true },
  { key: "inputQty", header: "Put in", align: "end" },
  { key: "outputQty", header: "Planned out", align: "end" },
  { key: "outputDisposition", header: "Output", render: (r) => DISPOSITION_TEXT[r.outputDisposition] },
  { key: "status", header: "Status", render: (r) => <StatusBadge rework={r} /> },
];

/** FR-24.18: the rework orders, filtered by status. */
export function ReworkOrdersPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), REWORK_DUTY);
  const [status, setStatus] = useState("");
  const state = useLoad((signal) => listReworkOrders({ status }, signal), `rework|${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Rework orders"
        description="Batches sent back through a rework formula. The batch order does the work; when it is reported as finished, the output goes back to the batch register."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/processmfg/rework-orders/new">
              New rework order
            </Link>
          ) : undefined
        }
      />
      <div className={styles.toolbar}>
        <FormField id="rework-filter-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All</option>
              <option value="planned">Planned</option>
              <option value="received">Received</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load the rework orders" error={state.error} reload={state.reload} />
      ) : (
        <DataTable
          label="Rework orders"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={
            status
              ? { title: "No rework orders match", description: "Clear the filter to see every rework order." }
              : { title: "No rework orders yet", description: "Create one from a batch that failed its limits or needs another pass." }
          }
          onRowOpen={(r) => navigate(`/processmfg/rework-orders/${r.id}`)}
        />
      )}
    </div>
  );
}

/** Creates a rework order: reads the batch version, then creates under one key. */
export function NewReworkOrderPage() {
  const navigate = useNavigate();
  const allowed = holdsDuty(usePrincipalDuties(), REWORK_DUTY);
  const [formulaId, setFormulaId] = useState("");
  const [batchSize, setBatchSize] = useState("");
  const [batchId, setBatchId] = useState("");
  const [batchQty, setBatchQty] = useState("");
  const [reworkQty, setReworkQty] = useState("");
  const [outputQty, setOutputQty] = useState("");
  const [keep, setKeep] = useState(true);
  const [quality, setQuality] = useState(true);
  const [output, setOutput] = useState<ReworkOutputDisposition>("quarantine");
  const [capitalize, setCapitalize] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function on<T>(set: (v: T) => void) {
    return (v: T) => {
      set(v);
      key.current = newKey();
    };
  }

  if (!allowed) {
    return (
      <div className={styles.stack}>
        <PageHeader title="New rework order" />
        <Card title="Rework order">
          <p className={styles.note}>Creating rework orders needs the batch order duty. Ask your administrator if you should have it.</p>
        </Card>
      </div>
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(formulaId.trim())) found.formulaId = "Enter the rework formula id as a UUID.";
    if (!UUID.test(batchId.trim())) found.batchId = "Enter the batch id as a UUID.";
    for (const [name, value, text] of [
      ["batchSize", batchSize, "the batch size"],
      ["batchQty", batchQty, "the quantity of the batch"],
      ["reworkQty", reworkQty, "the quantity to rework"],
      ["outputQty", outputQty, "the planned output"],
    ] as const) {
      if (!QUANTITY.test(value.trim()) || Number(value) <= 0) found[name] = `Enter ${text} as a positive number with at most 6 decimals.`;
    }
    if (!found.batchQty && !found.reworkQty && Number(reworkQty) > Number(batchQty)) found.reworkQty = "The rework quantity cannot exceed the batch quantity.";
    if (quality && output === "available") found.output = "The output cannot be available while a quality order is required.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      // The register keeps no version in the form: read it now, so a change since is refused as stale.
      const batch = await getBatch(batchId.trim());
      const rework = await createReworkOrder(
        {
          formulaId: formulaId.trim(),
          batchSize: batchSize.trim(),
          sourceBatchId: batchId.trim(),
          sourceVersion: batch.version,
          sourceQty: batchQty.trim(),
          reworkQty: reworkQty.trim(),
          outputQty: outputQty.trim(),
          params: { keepBatchNumber: keep, requireQualityOrder: quality, outputDisposition: output, capitalize },
        },
        key.current,
      );
      navigate(`/processmfg/rework-orders/${rework.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      // A refused command can be corrected and retried under a new key; an answer lost in transit is retried under the same one.
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New rework order"
        description="The rework formula must make the item of the batch. A batch has one open rework order at a time. The register keeps no quantity, so enter the quantity of the batch."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="rw-formula" label="Rework formula id" error={errors.formulaId}>
            {(c) => <Input {...c} value={formulaId} onChange={(e) => on(setFormulaId)(e.target.value)} invalid={Boolean(errors.formulaId)} />}
          </FormField>
          <FormField id="rw-size" label="Batch size" error={errors.batchSize}>
            {(c) => <Input {...c} inputMode="decimal" value={batchSize} onChange={(e) => on(setBatchSize)(e.target.value)} invalid={Boolean(errors.batchSize)} />}
          </FormField>
          <FormField id="rw-batch" label="Batch id" error={errors.batchId}>
            {(c) => <Input {...c} value={batchId} onChange={(e) => on(setBatchId)(e.target.value)} invalid={Boolean(errors.batchId)} />}
          </FormField>
          <FormField id="rw-batch-qty" label="Quantity of the batch" error={errors.batchQty}>
            {(c) => <Input {...c} inputMode="decimal" value={batchQty} onChange={(e) => on(setBatchQty)(e.target.value)} invalid={Boolean(errors.batchQty)} />}
          </FormField>
          <FormField id="rw-rework-qty" label="Quantity to rework" hint="Put in as the first ingredient; at most the batch quantity." error={errors.reworkQty}>
            {(c) => <Input {...c} inputMode="decimal" value={reworkQty} onChange={(e) => on(setReworkQty)(e.target.value)} invalid={Boolean(errors.reworkQty)} />}
          </FormField>
          <FormField id="rw-output-qty" label="Planned output" error={errors.outputQty}>
            {(c) => <Input {...c} inputMode="decimal" value={outputQty} onChange={(e) => on(setOutputQty)(e.target.value)} invalid={Boolean(errors.outputQty)} />}
          </FormField>
          <FormField id="rw-output" label="Output disposition" error={errors.output}>
            {(c) => (
              <Select {...c} value={output} onChange={(e) => on(setOutput)(e.target.value as ReworkOutputDisposition)} invalid={Boolean(errors.output)}>
                {REWORK_OUTPUT_DISPOSITIONS.map((d) => (
                  <option key={d} value={d}>
                    {DISPOSITION_TEXT[d]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.lineChecks}>
            <label htmlFor="rw-keep">
              <input id="rw-keep" type="checkbox" checked={keep} onChange={(e) => on(setKeep)(e.target.checked)} /> Keep the batch number
            </label>
            <label htmlFor="rw-quality">
              <input id="rw-quality" type="checkbox" checked={quality} onChange={(e) => on(setQuality)(e.target.checked)} /> Require a quality order
            </label>
            <label htmlFor="rw-capitalize">
              <input id="rw-capitalize" type="checkbox" checked={capitalize} onChange={(e) => on(setCapitalize)(e.target.checked)} /> Capitalize the cost (else a rework variance)
            </label>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the rework order" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create rework order
            </Button>
            <Link className={buttonClassName("ghost")} to="/processmfg/rework-orders">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** One rework order, with receive of its output once its batch order is reported. */
export function ReworkOrderDetailPage() {
  const { id = "" } = useParams();
  const duties = usePrincipalDuties();
  const canWrite = holdsDuty(duties, REWORK_DUTY);
  const canRelease = holdsDuty(duties, RELEASE_DUTY);
  const state = useLoad((signal) => getReworkOrder(id, signal), id);
  const [number, setNumber] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Rework order" />
        <LoadError title="Could not load the rework order" error={state.error} reload={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Rework order" />
        <span className="visually-hidden">Loading the rework order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const rework = state.data;
  const ready = rework.orderStatus === "reported" || rework.orderStatus === "ended";
  const releaseBlocked = rework.outputDisposition === "available" && !canRelease;

  async function receive(event: FormEvent) {
    event.preventDefault();
    const text = number.trim();
    if (!rework.keepBatchNumber && text === "") {
      setFailure({ code: "COM-0001", message: "Enter the new batch number.", fields: {} });
      return;
    }
    setBusy(true);
    setFailure(null);
    try {
      await receiveReworkOrder(rework.id, { version: rework.version, ...(rework.keepBatchNumber ? {} : { newBatchNumber: text }) }, key.current);
      key.current = newKey();
      state.reload();
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
      <PageHeader title={`Rework of ${rework.sourceBatchNumber}`} description={`Batch order ${rework.orderNumber}`} meta={<StatusBadge rework={rework} />} />
      <StatGroup label="Rework">
        <StatTile label="Put in" value={rework.inputQty} />
        <StatTile label="Planned out" value={rework.outputQty} />
        <StatTile label="Received" value={rework.receivedQty ?? dash} />
        <StatTile label="Output" value={DISPOSITION_TEXT[rework.outputDisposition]} />
        <StatTile label="Cost posted as" value={rework.varianceType === "capitalized" ? "Capitalized" : "Rework variance"} />
        <StatTile label="Batch order status" value={rework.orderStatus} />
      </StatGroup>
      <Card title="Links">
        <p className={styles.note}>
          Reworked batch: <Link to={`/processmfg/batches/${rework.sourceBatchId}`}>{rework.sourceBatchNumber}</Link>. Batch order:{" "}
          <Link to={`/processmfg/batch-orders/${rework.batchOrderId}`}>{shortId(rework.batchOrderId)}</Link>.
          {rework.outputBatchId ? (
            <>
              {" "}
              Output batch: <Link to={`/processmfg/batches/${rework.outputBatchId}`}>{rework.outputBatchNumber}</Link>.
            </>
          ) : null}{" "}
          {rework.qualityOrderRequired ? "Release waits for a quality order." : "No quality order is required."}
        </p>
      </Card>
      {failure ? (
        <Alert tone="danger" title="Could not receive the output" live="alert" action={failure.code === "PMF-0104" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}>
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {rework.status === "planned" && canWrite ? (
        <Card title="Receive the output">
          {releaseBlocked ? (
            <p className={styles.note}>
              The output is planned as available, so receiving it releases the batch: that needs the batch disposition duty. Ask a QA manager to receive it.
            </p>
          ) : ready ? (
            <form className={styles.form} onSubmit={receive} noValidate>
              {rework.keepBatchNumber ? (
                <p className={styles.note}>The batch number is kept: receiving moves the batch to {DISPOSITION_TEXT[rework.outputDisposition].toLowerCase()}.</p>
              ) : (
                <FormField id="rw-new-number" label="New batch number" hint="The output is registered as a new batch linked to the reworked one.">
                  {(c) => <Input {...c} value={number} onChange={(e) => setNumber(e.target.value)} />}
                </FormField>
              )}
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy}>
                  Receive output
                </Button>
              </div>
            </form>
          ) : (
            <p className={styles.note}>The batch order must be reported as finished first; it is {rework.orderStatus}.</p>
          )}
        </Card>
      ) : null}
      <div className={styles.formActions}>
        <Link className={buttonClassName("ghost")} to="/processmfg/rework-orders">
          Back to the list
        </Link>
      </div>
    </div>
  );
}
