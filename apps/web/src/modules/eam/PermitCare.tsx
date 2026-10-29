import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Button, buttonClassName, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  createIsolationPoint,
  describeProblem,
  getJobPermits,
  listIsolationPoints,
  listPermits,
  newKey,
  type EnergyType,
  type IsolationPoint,
  type NewIsolationPoint,
  type Permit,
  type Problem,
  type WorkOrder,
} from "./api/client";
import { minuteOf, sentence } from "./format";
import { Failure, State } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const ENERGY: EnergyType[] = ["ELECTRICAL", "PNEUMATIC", "HYDRAULIC", "MECHANICAL", "THERMAL", "CHEMICAL"];

const pointColumns: DataTableColumn<IsolationPoint>[] = [
  { key: "tagCode", header: "Tag", mono: true },
  { key: "energyType", header: "Energy", render: (row) => sentence(row.energyType) },
  { key: "location", header: "Location" },
  { key: "method", header: "Isolation method" },
  { key: "verificationStep", header: "Verification step" },
];

type PointValues = NewIsolationPoint;

/** The checks the service makes on an isolation point, and the point the form sends. */
export function validateIsolationPoint(values: PointValues): { errors: Partial<Record<keyof PointValues, string>>; input?: NewIsolationPoint } {
  const errors: Partial<Record<keyof PointValues, string>> = {};
  const trimmed = {
    energyType: values.energyType,
    location: values.location.trim(),
    method: values.method.trim(),
    verificationStep: values.verificationStep.trim(),
    tagCode: values.tagCode.trim(),
  };
  if (trimmed.tagCode.length < 1 || trimmed.tagCode.length > 40) errors.tagCode = "Enter the tag code on the point, up to 40 characters.";
  if (trimmed.location.length < 1 || trimmed.location.length > 200) errors.location = "Say where the point is, up to 200 characters.";
  if (trimmed.method.length < 1 || trimmed.method.length > 200) errors.method = "Say how it is isolated, up to 200 characters.";
  if (trimmed.verificationStep.length < 1 || trimmed.verificationStep.length > 200) errors.verificationStep = "Say how zero energy is verified, up to 200 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: trimmed };
}

const blankPoint: PointValues = { energyType: "ELECTRICAL", location: "", method: "", verificationStep: "", tagCode: "" };

/** The asset's energy isolation points (FR-44.19), which isolation permits lock before work starts. */
export function IsolationPointsCard({ assetId, onSaved }: { assetId: string; onSaved(text: string): void }) {
  const [round, setRound] = useState(0);
  const points = useLoad((signal) => listIsolationPoints(assetId, signal), `${assetId}|iso|${round}`);
  const [values, setValues] = useState<PointValues>(blankPoint);
  const [errors, setErrors] = useState<Partial<Record<keyof PointValues, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PointValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateIsolationPoint(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await createIsolationPoint(assetId, input, key.current);
      key.current = newKey();
      setValues(blankPoint);
      setRound((n) => n + 1);
      onSaved(`Isolation point ${saved.tagCode} added.`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Isolation points">
      <div className={styles.stack}>
        <p className={styles.note}>An isolation permit on this asset is issued only when every point below carries a verified personal lock.</p>
        {points.error ? (
          <Failure title="Could not load the isolation points" problem={points.error} retry={points.reload} />
        ) : (
          <DataTable
            label="Isolation points"
            columns={pointColumns}
            rows={points.data ?? []}
            getRowKey={(row) => row.id}
            loading={points.loading}
            empty={{ title: "No isolation points yet", description: "Add each energy source that must be locked out before work." }}
          />
        )}
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Add isolation point">
          <FormField id="iso-energy" label="Energy type">
            {(control) => (
              <Select {...control} value={values.energyType} onChange={(e) => update({ energyType: e.target.value as EnergyType })}>
                {ENERGY.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="iso-tag" label="Tag code" error={errors.tagCode}>
            {(control) => <Input {...control} value={values.tagCode} maxLength={40} invalid={Boolean(errors.tagCode)} onChange={(e) => update({ tagCode: e.target.value })} />}
          </FormField>
          <FormField id="iso-location" label="Location" error={errors.location}>
            {(control) => <Input {...control} value={values.location} maxLength={200} invalid={Boolean(errors.location)} onChange={(e) => update({ location: e.target.value })} />}
          </FormField>
          <FormField id="iso-method" label="Isolation method" hint="For example breaker padlock." error={errors.method}>
            {(control) => <Input {...control} value={values.method} maxLength={200} invalid={Boolean(errors.method)} onChange={(e) => update({ method: e.target.value })} />}
          </FormField>
          <FormField id="iso-verify" label="Verification step" hint="How zero energy is proven, for example try start." error={errors.verificationStep}>
            {(control) => (
              <Input {...control} value={values.verificationStep} maxLength={200} invalid={Boolean(errors.verificationStep)} onChange={(e) => update({ verificationStep: e.target.value })} />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Failure title="Could not add the isolation point" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" loading={busy}>
              Add isolation point
            </Button>
          </div>
        </form>
      </div>
    </Card>
  );
}

const permitColumns: DataTableColumn<Permit>[] = [
  { key: "number", header: "Permit", mono: true, render: (row) => <Link to={`/eam/permits/${row.id}`}>{row.number}</Link> },
  { key: "permitTypeCode", header: "Type", mono: true },
  { key: "validTo", header: "Valid to (UTC)", mono: true, render: (row) => minuteOf(row.validTo) },
  { key: "status", header: "Status", render: (row) => <State value={row.status} /> },
  { key: "locksOpen", header: "Locks open", align: "end" },
];

const isOpen = (wo: WorkOrder) => !["COMPLETED", "CLOSED", "CANCELED"].includes(wo.status);

/** The work order's permits and the permit types its job type requires before it may start (BR-44.11). */
export function WorkOrderPermitsCard({ wo }: { wo: WorkOrder }) {
  const permits = useLoad((signal) => listPermits({ workOrder: wo.id, limit: 50 }, signal), `${wo.id}|permits|${wo.version}`);
  const jobType = wo.jobType ?? "";
  const required = useLoad(
    async (signal) => (jobType ? (await getJobPermits(jobType, signal)).permitTypeCodes : []),
    `${jobType}|job-permits`,
  );
  const codes = required.data ?? [];

  return (
    <Card
      title="Permits to work"
      actions={
        isOpen(wo) ? (
          <Link className={buttonClassName("secondary", "sm")} to={`/eam/permits/new?workOrder=${wo.id}`}>
            Request permit
          </Link>
        ) : null
      }
    >
      <div className={styles.stack}>
        <p className={styles.note}>
          {!jobType
            ? "This order has no job type, so no permit is required before it starts."
            : required.loading
              ? "Checking which permits this job type requires."
              : codes.length === 0
                ? `Job type ${jobType} requires no permit.`
                : `Job type ${jobType} requires an issued, valid ${codes.join(", ")} permit before the order can move to In progress.`}
        </p>
        {required.error ? <Failure title="Could not load the job type's permit requirement" problem={required.error} retry={required.reload} /> : null}
        {permits.error ? (
          <Failure title="Could not load the permits" problem={permits.error} retry={permits.reload} />
        ) : (
          <DataTable
            label="Permits for this work order"
            columns={permitColumns}
            rows={permits.data?.items ?? []}
            getRowKey={(row) => row.id}
            loading={permits.loading}
            empty={{ title: "No permits yet" }}
          />
        )}
      </div>
    </Card>
  );
}
