import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, StatGroup, StatTile } from "../../shell/ui";
import { describeProblem, stackLoadPlan, type LoadPlan, type PlanPallet, type Problem, type Trailer } from "./api/client";
import styles from "./pages.module.css";
import { RowsEditor, type RowField } from "./RowsEditor";
import { ProblemMessage } from "../../shell/errors";

export interface PlanValues {
  lengthMm: string;
  widthMm: string;
  maxHeightMm: string;
  maxWeightKg: string;
  pallets: Record<string, string>[];
}

const palletFields: RowField[] = [
  { key: "id", label: "Pallet id" },
  { key: "lengthMm", label: "Length mm" },
  { key: "widthMm", label: "Width mm" },
  { key: "heightMm", label: "Height mm" },
  { key: "weightKg", label: "Weight kg" },
  { key: "stackable", label: "Stackable", options: ["yes", "no"], hint: "Can carry another pallet." },
  { key: "stopSeq", label: "Stop", hint: "Its stop on the route; later stops load first." },
  { key: "maxStackKg", label: "Max stack kg", hint: "Optional limit for a stack on it." },
];

const blankPallet = () => ({ id: "", lengthMm: "1200", widthMm: "1000", heightMm: "", weightKg: "", stackable: "yes", stopSeq: "1", maxStackKg: "" });

const whole = /^\d{1,6}$/;

/** Checks that mirror LoadPlanRequest.Validate in the service, and the request the form sends. */
export function validatePlan(v: PlanValues): { errors: Record<string, string>; input?: { trailer: Trailer; pallets: PlanPallet[] } } {
  const errors: Record<string, string> = {};
  const positive = (s: string) => whole.test(s.trim()) && Number(s) > 0 && Number(s) <= 30000;
  if (!positive(v.lengthMm)) errors.lengthMm = "Enter the inside length in mm, such as 13600.";
  if (!positive(v.widthMm)) errors.widthMm = "Enter the inside width in mm, such as 2480.";
  if (!positive(v.maxHeightMm)) errors.maxHeightMm = "Enter the inside height in mm, such as 2700.";
  if (!whole.test(v.maxWeightKg.trim()) || Number(v.maxWeightKg) < 1) errors.maxWeightKg = "Enter the payload in kg, at least 1.";
  const seen = new Set<string>();
  const pallets = v.pallets.map((p, i) => {
    const row = `pallets[${i}]`;
    const id = p.id.trim();
    if (id === "" || id.length > 64) errors[row] = "Enter a pallet id of 1 to 64 characters.";
    else if (seen.has(id)) errors[row] = "Give each pallet its own id.";
    else if (![p.lengthMm, p.widthMm, p.heightMm].every(positive)) errors[row] = "Enter length, width and height in mm, each above zero.";
    else if (!whole.test(p.weightKg.trim())) errors[row] = "Enter the weight in whole kg.";
    else if (!whole.test(p.stopSeq.trim())) errors[row] = "Enter the stop as a whole number.";
    else if (p.maxStackKg.trim() !== "" && !whole.test(p.maxStackKg.trim())) errors[row] = "Enter the stack limit in whole kg, or leave it empty.";
    seen.add(id);
    const pallet: PlanPallet = {
      id,
      lengthMm: Number(p.lengthMm),
      widthMm: Number(p.widthMm),
      heightMm: Number(p.heightMm),
      weightKg: Number(p.weightKg),
      stackable: p.stackable === "yes",
      stopSeq: Number(p.stopSeq),
    };
    if (p.maxStackKg.trim() !== "") pallet.maxStackKg = Number(p.maxStackKg);
    return pallet;
  });
  if (pallets.length === 0 || pallets.length > 200) errors.pallets = "Add between 1 and 200 pallets.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      trailer: { lengthMm: Number(v.lengthMm), widthMm: Number(v.widthMm), maxHeightMm: Number(v.maxHeightMm), maxWeightKg: Number(v.maxWeightKg) },
      pallets,
    },
  };
}

/** Top view of the trailer floor, nose at the top: one box per floor position, labelled with its stack. */
function TopView({ plan, trailer }: { plan: LoadPlan; trailer: Trailer }) {
  return (
    <figure className={styles.topView}>
      <svg viewBox={`0 0 ${trailer.widthMm} ${trailer.lengthMm}`} role="img" aria-labelledby="load-plan-top-view">
        <title id="load-plan-top-view">{`Top view: ${plan.floorPositionsUsed} floor positions used, ${plan.lengthUsedMm} of ${trailer.lengthMm} mm`}</title>
        <rect className={styles.floor} x={0} y={0} width={trailer.widthMm} height={trailer.lengthMm} />
        {plan.placements.map((p) => (
          <g key={p.palletIds[0]}>
            <rect className={p.palletIds.length > 1 ? styles.stacked : styles.single} x={p.xMm} y={p.yMm} width={p.widthMm} height={p.lengthMm} />
            <text className={styles.floorLabel} x={p.xMm + p.widthMm / 2} y={p.yMm + p.lengthMm / 2} textAnchor="middle" dominantBaseline="middle">
              {p.palletIds.length > 1 ? `${p.palletIds[0]} +${p.palletIds.length - 1}` : p.palletIds[0]}
            </text>
          </g>
        ))}
      </svg>
      <figcaption className={styles.note}>Nose at the top, rear door at the bottom. Darker boxes hold stacks.</figcaption>
    </figure>
  );
}

/** Layer-based load plan for a trailer or container (FR-34.18): floor positions, layers, load sequence and axle-weight estimate. */
export function LoadPlanPage() {
  const [values, setValues] = useState<PlanValues>({ lengthMm: "13600", widthMm: "2480", maxHeightMm: "2700", maxWeightKg: "20000", pallets: [blankPallet()] });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ plan: LoadPlan; trailer: Trailer } | null>(null);

  const update = (patch: Partial<PlanValues>) => setValues((v) => ({ ...v, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePlan(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult({ plan: await stackLoadPlan(input), trailer: input.trailer });
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const trailerField = (key: "lengthMm" | "widthMm" | "maxHeightMm" | "maxWeightKg", id: string, label: string) => {
    const error = errors[key] ?? errors[`trailer.${key}`] ?? (key !== "maxWeightKg" ? errors.trailer : undefined);
    return (
      <FormField id={id} label={label} error={error}>
        {(control) => <Input {...control} inputMode="numeric" value={values[key]} onChange={(e) => update({ [key]: e.target.value })} invalid={Boolean(error)} />}
      </FormField>
    );
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Load plan"
        description="Stack pallets onto a trailer or container floor, largest footprint first. Nothing goes on a non-stackable pallet, and a pallet for an earlier stop never sits under one for a later stop."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {trailerField("lengthMm", "plan-length", "Inside length (mm)")}
          {trailerField("widthMm", "plan-width", "Inside width (mm)")}
          {trailerField("maxHeightMm", "plan-height", "Inside height (mm)")}
          {trailerField("maxWeightKg", "plan-weight", "Payload (kg)")}
          <RowsEditor field="pallets" legend="Pallets" noun="pallet" fields={palletFields} rows={values.pallets} errors={errors} blank={blankPallet} onChange={(pallets) => update({ pallets })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "TMS-0123" ? "The load planner is unavailable" : "Could not plan the load"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Plan the load
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <div className={styles.stack}>
          {result.plan.unplaced.length > 0 ? (
            <Alert tone="warning" title={`${result.plan.unplaced.length} ${result.plan.unplaced.length === 1 ? "pallet does" : "pallets do"} not fit`} live="status">
              {result.plan.unplaced.join(", ")} did not fit by floor space, height or weight. Plan them on another trailer.
            </Alert>
          ) : (
            <Alert tone="success" title="Every pallet is placed" live="status" />
          )}
          <StatGroup label="Load plan">
            <StatTile label="Floor positions" value={result.plan.floorPositionsUsed} />
            <StatTile label="Layers" value={result.plan.layers} />
            <StatTile label="Length used (mm)" value={result.plan.lengthUsedMm} />
            <StatTile label="Weight (kg)" value={result.plan.totalWeightKg} />
            <StatTile label="Kingpin (kg)" value={result.plan.axle.kingpinKg} />
            <StatTile label="Rear axle (kg)" value={result.plan.axle.axleKg} />
          </StatGroup>
          <Card title="Top view" headingLevel={3}>
            <TopView plan={result.plan} trailer={result.trailer} />
          </Card>
          <Card title="Floor positions" headingLevel={3}>
            <DataTable
              label="Floor positions"
              columns={[
                { key: "palletIds", header: "Pallets, bottom to top", render: (row) => row.palletIds.join(" / ") },
                { key: "position", header: "From nose", align: "end", render: (row) => `${row.yMm} mm` },
                { key: "side", header: "From left wall", align: "end", render: (row) => `${row.xMm} mm` },
                { key: "heightMm", header: "Height", align: "end", render: (row) => `${row.heightMm} mm` },
                { key: "weightKg", header: "Weight", align: "end", render: (row) => `${row.weightKg} kg` },
              ]}
              rows={result.plan.placements}
              getRowKey={(row) => row.palletIds[0]}
              empty={{ title: "Nothing placed" }}
            />
          </Card>
          <Card title="Load sequence" headingLevel={3}>
            <ol className={styles.trace}>
              {result.plan.loadSequence.map((id) => (
                <li key={id}>{id}</li>
              ))}
            </ol>
          </Card>
        </div>
      ) : null}
    </div>
  );
}
