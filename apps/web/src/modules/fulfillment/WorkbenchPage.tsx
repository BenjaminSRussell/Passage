// Picking workbench (FR-28.13, BR-28.14): filter released shipments, group them
// by a key into waves of at most WAVE_MAX_LINES lines, preview the grouping and
// create every wave in one action. One Idempotency-Key is kept per preview, so
// a retried create cannot create the waves twice.
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  createWaves,
  describeProblem,
  previewWaves,
  WAVE_GROUP_KEYS,
  type Problem,
  type Wave,
  type WaveGroupBy,
  type WavePlan,
  type WavesResult,
  type WorkbenchInput,
} from "./api/client";
import { GROUP_LABELS, LOGISTICS_CODE, UUID } from "./format";
import { ProblemAlert, useCommandKey } from "./forms";
import styles from "./pages.module.css";
import { EntityName } from "../../shell/lookup/EntityName";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

export interface WorkbenchValues {
  warehouseId: string;
  customerId: string;
  carrier: string;
  zone: string;
  shipFrom: string;
  shipTo: string;
  /** Empty takes the warehouse's workbench_group_key_default. */
  groupBy: WaveGroupBy | "";
  maxLines: string;
  maxOrders: string;
}

const DATE = /^\d{4}-\d{2}-\d{2}$/;

function whole(value: string, max: number): boolean {
  return /^\d{1,4}$/.test(value) && Number(value) >= 1 && Number(value) <= max;
}

/** Checks that mirror the service (FUL-0050), keyed like its field names, and the request to send. */
export function validateWorkbench(v: WorkbenchValues): { errors: Record<string, string>; input?: WorkbenchInput } {
  const errors: Record<string, string> = {};
  const t = (s: string) => s.trim();
  if (!UUID.test(t(v.warehouseId))) errors.warehouseId = "Choose a warehouse.";
  if (t(v.customerId) !== "" && !UUID.test(t(v.customerId))) errors.customerId = "Choose a customer, or leave it empty.";
  if (t(v.carrier) !== "" && !LOGISTICS_CODE.test(t(v.carrier))) errors.carrier = "Use 1 to 32 letters, digits, '.', '_' or '-'.";
  if (t(v.zone) !== "" && !LOGISTICS_CODE.test(t(v.zone))) errors.zone = "Use 1 to 32 letters, digits, '.', '_' or '-'.";
  if (v.shipFrom !== "" && !DATE.test(v.shipFrom)) errors.shipFrom = "Enter a date.";
  if (v.shipTo !== "" && !DATE.test(v.shipTo)) errors.shipTo = "Enter a date.";
  if (!errors.shipFrom && !errors.shipTo && v.shipFrom && v.shipTo && v.shipFrom > v.shipTo) errors.shipTo = "The end date must not be before the start date.";
  if (t(v.maxLines) !== "" && !whole(t(v.maxLines), 2000)) errors.maxLines = "Enter 1 to 2000 lines.";
  if (t(v.maxOrders) !== "" && !whole(t(v.maxOrders), 500)) errors.maxOrders = "Enter 1 to 500 orders.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: WorkbenchInput = { warehouseId: t(v.warehouseId) };
  if (v.groupBy) input.groupBy = v.groupBy;
  if (t(v.customerId)) input.customerId = t(v.customerId);
  if (t(v.carrier)) input.carrier = t(v.carrier);
  if (t(v.zone)) input.zone = t(v.zone);
  if (v.shipFrom) input.shipFrom = v.shipFrom;
  if (v.shipTo) input.shipTo = v.shipTo;
  if (t(v.maxLines)) input.maxLines = Number(t(v.maxLines));
  if (t(v.maxOrders)) input.maxOrders = Number(t(v.maxOrders));
  return { errors, input };
}

const planColumns: DataTableColumn<WavePlan>[] = [
  { key: "groupKey", header: "Group", render: (row) => (row.groupKey === "UNASSIGNED" ? "Unassigned" : row.groupKey) },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "orderCount", header: "Orders", align: "end" },
  { key: "shipments", header: "Shipments", render: (row) => row.shipments.map((s) => s.shipmentNumber).join(", ") },
  { key: "legalEntityId", header: "Legal entity", render: (row) => <EntityName entity="legalEntity" id={row.legalEntityId} /> },
];

const waveColumns: DataTableColumn<Wave>[] = [
  { key: "waveNumber", header: "Wave", mono: true, render: (row) => <Link to={`/fulfillment/waves/${row.id}`}>{row.waveNumber}</Link> },
  { key: "groupKey", header: "Group", render: (row) => (row.groupKey === "UNASSIGNED" ? "Unassigned" : row.groupKey) },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "orderCount", header: "Orders", align: "end" },
  { key: "shipments", header: "Shipments", align: "end", render: (row) => String(row.shipments.length) },
];

const initial: WorkbenchValues = { warehouseId: "", customerId: "", carrier: "", zone: "", shipFrom: "", shipTo: "", groupBy: "", maxLines: "", maxOrders: "" };

/** Picking workbench: filter, group, preview, then create every wave in one action. */
export function WorkbenchPage() {
  const [values, setValues] = useState<WorkbenchValues>(initial);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [preview, setPreview] = useState<{ input: WorkbenchInput; result: WavesResult<WavePlan> } | null>(null);
  const [created, setCreated] = useState<WavesResult<Wave> | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [busy, setBusy] = useState<"preview" | "create" | null>(null);
  const key = useCommandKey();

  function update(patch: Partial<WorkbenchValues>) {
    setValues({ ...values, ...patch });
    // A changed filter makes the preview stale: it must be previewed again before creating.
    setPreview(null);
  }

  async function runPreview(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateWorkbench(values);
    setErrors(found);
    setFailure(null);
    setCreated(null);
    if (!input) return;
    setBusy("preview");
    try {
      setPreview({ input, result: await previewWaves(input) });
      key.renew();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure({ title: "Could not preview the waves", problem });
    } finally {
      setBusy(null);
    }
  }

  async function create() {
    if (!preview) return;
    setFailure(null);
    setBusy("create");
    try {
      const result = await createWaves(preview.input, key.current());
      key.renew();
      setCreated(result);
      setPreview(null);
    } catch (error) {
      const problem = describeProblem(error);
      const message = problem.code === "FUL-0053" ? `${problem.message}. Preview again to see the current grouping.` : problem.message;
      setFailure({ title: "Could not create the waves", problem: { ...problem, message } });
      if (problem.code === "FUL-0053" || problem.code === "idempotency_key_reuse") {
        key.renew();
        setPreview(null);
      }
    } finally {
      setBusy(null);
    }
  }

  const field = (name: keyof WorkbenchValues, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`workbench-${name}`} label={label} hint={hint} error={errors[name]} optional={name !== "warehouseId"}>
      {(control) => <Input {...control} {...extra} value={values[name]} invalid={Boolean(errors[name])} onChange={(event) => update({ [name]: event.target.value })} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Picking workbench"
        description="Group released shipments into pick waves. A shipment is never split across waves, and a wave never mixes legal entities."
        actions={
          <Link className={buttonClassName("secondary")} to="/fulfillment/waves">
            Pick waves
          </Link>
        }
      />
      <Card title="Filter and group">
        <form className={styles.form} onSubmit={runPreview} noValidate>
          <LookupFormField
            id="workbench-warehouseId"
            label="Warehouse"
            entity="warehouse"
            hint="Released shipments of this warehouse."
            value={values.warehouseId}
            error={errors.warehouseId}
            onChange={(warehouseId) => update({ warehouseId })}
          />
          <LookupFormField id="workbench-customerId" label="Customer" entity="customer" optional value={values.customerId} error={errors.customerId} onChange={(customerId) => update({ customerId })} />
          {field("carrier", "Carrier", "Carrier code given at release, such as UPS.", { maxLength: 32 })}
          {field("zone", "Pick zone", "Shipments with a line in this zone.", { maxLength: 32 })}
          {field("shipFrom", "Ship from", undefined, { type: "date" })}
          {field("shipTo", "Ship to", undefined, { type: "date" })}
          <FormField id="workbench-groupBy" label="Group by">
            {(control) => (
              <Select {...control} value={values.groupBy} onChange={(event) => update({ groupBy: event.target.value as WaveGroupBy | "" })}>
                <option value="">Warehouse default</option>
                {WAVE_GROUP_KEYS.map((g) => (
                  <option key={g} value={g}>
                    {GROUP_LABELS[g]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {field("maxLines", "Max lines per wave", "Empty: 200 (WAVE_MAX_LINES).", { inputMode: "numeric" })}
          {field("maxOrders", "Max orders per wave", "Empty: 50 (WAVE_MAX_ORDERS).", { inputMode: "numeric" })}
          <div className={styles.formActions}>
            <Button type="submit" variant={preview ? "secondary" : "primary"} loading={busy === "preview"}>
              Preview waves
            </Button>
          </div>
        </form>
      </Card>
      {failure ? <ProblemAlert title={failure.title} problem={failure.problem} /> : null}
      {preview ? (
        <Card
          title="Preview"
          actions={
            preview.result.waveCount > 0 ? (
              <Button variant="primary" loading={busy === "create"} onClick={create}>
                {`Create ${preview.result.waveCount} ${preview.result.waveCount === 1 ? "wave" : "waves"}`}
              </Button>
            ) : null
          }
        >
          <div className={styles.stack}>
            <p role="status" className={styles.note}>
              {`${preview.result.waveCount} ${preview.result.waveCount === 1 ? "wave" : "waves"} from ${preview.result.lineCount} ${preview.result.lineCount === 1 ? "line" : "lines"}, grouped by ${GROUP_LABELS[preview.result.groupBy].toLowerCase()}.`}
            </p>
            <DataTable
              label="Planned waves"
              columns={planColumns}
              rows={preview.result.waves}
              getRowKey={(row, i) => `${row.groupKey}-${i}`}
              empty={{ title: "No released shipments match", description: "Release lines to this warehouse, or widen the filter." }}
            />
          </div>
        </Card>
      ) : null}
      {created ? (
        <Card title="Waves created">
          <div className={styles.stack}>
            <Alert tone="success" title={created.message.replace(/^FUL-0016: /, "")}>
              The shipments of each wave are now Picking. Open a wave to confirm its picks.
            </Alert>
            <DataTable
              label="Created waves"
              columns={waveColumns}
              rows={created.waves}
              getRowKey={(row) => row.id}
              empty={{ title: "No waves created", description: "No released shipment matched when the waves were created." }}
            />
          </div>
        </Card>
      ) : null}
    </div>
  );
}
