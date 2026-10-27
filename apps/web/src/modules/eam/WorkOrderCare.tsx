import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getFaultCatalog,
  newKey,
  recordDowntime,
  registerFault,
  type DowntimeWindow,
  type Fault,
  type FaultCatalog,
  type FaultCode,
  type NewDowntime,
  type Problem,
  type WorkOrderDetail,
} from "./api/client";
import { dash, minuteOf, UUID, utcFromLocalInput } from "./format";
import { Entry, Failure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const isOpen = (wo: WorkOrderDetail) => !["COMPLETED", "CLOSED", "CANCELED"].includes(wo.status);

/** CORRECTIVE and EMERGENCY orders need a fault before they complete (FR-44.16, EAM-FLT-001). */
export const faultRequired = (type: string) => type === "CORRECTIVE" || type === "EMERGENCY";

const textOf = (codes: FaultCode[], code: string) => codes.find((c) => c.code === code)?.text ?? code;

/** The fault found on the order (FR-44.16), chosen from the asset type's catalog. */
export function FaultCard({ wo, onSaved }: { wo: WorkOrderDetail; onSaved(text: string): void }) {
  const catalog = useLoad((signal) => getFaultCatalog(wo.assetType, signal), `catalog|${wo.assetType}`);
  const required = faultRequired(wo.workOrderType);
  return (
    <Card title="Fault">
      <div className={styles.stack}>
        {required ? <p className={styles.note}>Register the fault before you complete a {wo.workOrderType.toLowerCase()} work order.</p> : null}
        {wo.fault ? (
          <dl className={styles.details} aria-label="Registered fault">
            <Entry term="Symptom">{catalog.data ? textOf(catalog.data.symptoms, wo.fault.symptom) : wo.fault.symptom}</Entry>
            <Entry term="Area">{catalog.data ? textOf(catalog.data.areas, wo.fault.area) : wo.fault.area}</Entry>
            <Entry term="Type">{catalog.data ? textOf(catalog.data.types, wo.fault.type) : wo.fault.type}</Entry>
            <Entry term="Cause">{catalog.data ? textOf(catalog.data.causes, wo.fault.cause) : wo.fault.cause}</Entry>
            <Entry term="Remedy">{catalog.data ? textOf(catalog.data.remedies, wo.fault.remedy) : wo.fault.remedy}</Entry>
            <Entry term="Notes">{wo.fault.notes ?? dash}</Entry>
            <Entry term="Registered" mono>
              {minuteOf(wo.fault.registeredAt)}
            </Entry>
          </dl>
        ) : (
          <p className={styles.note}>No fault registered.</p>
        )}
        {!isOpen(wo) ? null : catalog.error ? (
          <Failure title="Could not load the fault catalog" problem={catalog.error} retry={catalog.reload} />
        ) : catalog.loading && !catalog.data ? (
          <div aria-busy="true">
            <Skeleton height="control" />
          </div>
        ) : catalog.data ? (
          <FaultForm workOrderId={wo.id} catalog={catalog.data} replacing={Boolean(wo.fault)} onSaved={onSaved} />
        ) : (
          <Alert tone="info" title={`No fault catalog for ${wo.assetType}`}>
            Faults are chosen from the asset type's catalog. <Link to={`/eam/fault-catalogs?type=${encodeURIComponent(wo.assetType)}`}>Design the catalog</Link> first.
          </Alert>
        )}
      </div>
    </Card>
  );
}

function FaultForm({ workOrderId, catalog, replacing, onSaved }: { workOrderId: string; catalog: FaultCatalog; replacing: boolean; onSaved(text: string): void }) {
  const combos = catalog.allowedCombinations;
  const [fault, setFault] = useState<Fault>(() => ({
    ...combos[0],
    cause: catalog.causes[0]?.code ?? "",
    remedy: catalog.remedies[0]?.code ?? "",
  }));
  const [notes, setNotes] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  // Only allowed combinations can be chosen: the area follows the symptom, the type follows both.
  const symptoms = catalog.symptoms.filter((s) => combos.some((c) => c.symptom === s.code));
  const areas = catalog.areas.filter((a) => combos.some((c) => c.symptom === fault.symptom && c.area === a.code));
  const types = catalog.types.filter((t) => combos.some((c) => c.symptom === fault.symptom && c.area === fault.area && c.type === t.code));

  function choose(patch: Partial<Fault>) {
    const next = { ...fault, ...patch };
    if (patch.symptom !== undefined) {
      const first = combos.find((c) => c.symptom === next.symptom);
      next.area = first?.area ?? "";
      next.type = first?.type ?? "";
    } else if (patch.area !== undefined) {
      next.type = combos.find((c) => c.symptom === next.symptom && c.area === next.area)?.type ?? "";
    }
    setFault(next);
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setBusy(true);
    try {
      await registerFault(workOrderId, notes.trim() ? { ...fault, notes: notes.trim() } : fault, key.current);
      key.current = newKey();
      setNotes("");
      onSaved("Fault registered.");
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  const select = (id: string, label: string, value: string, options: FaultCode[], onChange: (code: string) => void) => (
    <FormField id={id} label={label}>
      {(control) => (
        <Select {...control} value={value} onChange={(e) => onChange(e.target.value)}>
          {options.map((o) => (
            <option key={o.code} value={o.code}>
              {o.text}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Register fault">
      {select("fault-symptom", "Symptom", fault.symptom, symptoms, (symptom) => choose({ symptom }))}
      {select("fault-area", "Area", fault.area, areas, (area) => choose({ area }))}
      {select("fault-type", "Type", fault.type, types, (type) => choose({ type }))}
      {select("fault-cause", "Cause", fault.cause, catalog.causes, (cause) => choose({ cause }))}
      {select("fault-remedy", "Remedy", fault.remedy, catalog.remedies, (remedy) => choose({ remedy }))}
      <div className={styles.formActions}>
        <FormField id="fault-notes" label="Notes">
          {(control) => <Textarea {...control} value={notes} maxLength={1000} onChange={(e) => { setNotes(e.target.value); key.current = newKey(); }} />}
        </FormField>
      </div>
      {failure ? (
        <div className={styles.formActions}>
          <Failure title={failure.code === "EAM-FLT-002" ? "The catalog does not allow this fault" : "Could not register the fault"} problem={failure} />
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" loading={busy}>
          {replacing ? "Replace fault" : "Register fault"}
        </Button>
      </div>
    </form>
  );
}

const downtimeColumns: DataTableColumn<DowntimeWindow>[] = [
  { key: "from", header: "From (UTC)", mono: true, render: (row) => minuteOf(row.from) },
  { key: "to", header: "To (UTC)", mono: true, render: (row) => minuteOf(row.to) },
  { key: "minutes", header: "Minutes", align: "end" },
  { key: "resourceId", header: "Production resource", mono: true },
  { key: "recordedBy", header: "By", render: (row) => row.recordedBy ?? dash },
];

interface DowntimeValues {
  from: string;
  to: string;
  resourceId: string;
}

/** The checks the service makes on a downtime window, and the window the form sends. */
export function validateDowntime(values: DowntimeValues): { errors: Partial<Record<keyof DowntimeValues, string>>; input?: NewDowntime } {
  const errors: Partial<Record<keyof DowntimeValues, string>> = {};
  const from = utcFromLocalInput(values.from);
  const to = utcFromLocalInput(values.to);
  if (!from) errors.from = "Enter when the stop began.";
  if (!to) errors.to = "Enter when the resource ran again.";
  else if (from && Date.parse(to) - Date.parse(from) < 60_000) errors.to = "The stop must end at least one minute after it began.";
  const resourceId = values.resourceId.trim();
  if (resourceId && !UUID.test(resourceId)) errors.resourceId = "Enter the production resource id (a UUID), or leave it empty.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { from: from!, to: to!, ...(resourceId ? { resourceId } : {}) } };
}

/** Production resource downtime booked on the order (BR-44.5); scheduling blocks the resource for each window. */
export function DowntimeCard({ wo, onSaved }: { wo: WorkOrderDetail; onSaved(text: string): void }) {
  const [values, setValues] = useState<DowntimeValues>({ from: "", to: "", resourceId: "" });
  const [errors, setErrors] = useState<Partial<Record<keyof DowntimeValues, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<DowntimeValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateDowntime(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await recordDowntime(wo.id, input, key.current);
      key.current = newKey();
      setValues({ from: "", to: "", resourceId: "" });
      onSaved("Downtime recorded. Scheduling blocks the resource for that window.");
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Downtime">
      <div className={styles.stack}>
        <DataTable label="Downtime windows" columns={downtimeColumns} rows={wo.downtime} getRowKey={(row) => row.id} empty={{ title: "No downtime windows recorded" }} />
        {isOpen(wo) ? (
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Record downtime">
            <FormField id="dt-from" label="Stopped at (UTC)" error={errors.from}>
              {(control) => <Input {...control} type="datetime-local" value={values.from} invalid={Boolean(errors.from)} onChange={(e) => update({ from: e.target.value })} />}
            </FormField>
            <FormField id="dt-to" label="Running again at (UTC)" error={errors.to}>
              {(control) => <Input {...control} type="datetime-local" value={values.to} invalid={Boolean(errors.to)} onChange={(e) => update({ to: e.target.value })} />}
            </FormField>
            <FormField id="dt-resource" label="Production resource id" hint="Leave empty for the asset's production resource." error={errors.resourceId}>
              {(control) => <Input {...control} value={values.resourceId} invalid={Boolean(errors.resourceId)} onChange={(e) => update({ resourceId: e.target.value })} />}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Failure title={failure.code === "EAM-DT-001" ? "Downtime not recorded" : "Could not record the downtime"} problem={failure} />
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" loading={busy}>
                Record downtime
              </Button>
            </div>
          </form>
        ) : (
          <p className={styles.note}>Downtime can only be recorded on an open work order.</p>
        )}
      </div>
    </Card>
  );
}
