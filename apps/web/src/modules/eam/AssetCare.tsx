import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  assessCriticality,
  createWarranty,
  describeProblem,
  listAssessments,
  listWarranties,
  newKey,
  type Consequences,
  type CriticalityAssessment,
  type NewWarranty,
  type Problem,
  type Warranty,
} from "./api/client";
import { CODE, DATE, dash, minuteOf, UUID } from "./format";
import { Failure, State } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const CATEGORIES: { key: keyof Consequences; label: string }[] = [
  { key: "safety", label: "Safety" },
  { key: "environment", label: "Environment" },
  { key: "production", label: "Production" },
  { key: "quality", label: "Quality" },
  { key: "cost", label: "Cost" },
];

const SCORES = [1, 2, 3, 4, 5];

/** BR-44.15, mirrored for the preview: risk = worst consequence × likelihood; the service stays the authority. */
export function previewCriticality(c: Consequences, likelihood: number): { risk: number; criticality: number } {
  const risk = Math.max(c.safety, c.environment, c.production, c.quality, c.cost) * likelihood;
  const criticality = risk >= 20 || c.safety === 5 ? 5 : risk >= 15 ? 4 : risk >= 10 ? 3 : risk >= 5 ? 2 : 1;
  return { risk, criticality };
}

const assessmentColumns: DataTableColumn<CriticalityAssessment>[] = [
  { key: "version", header: "Version", align: "end" },
  {
    key: "consequence",
    header: "Consequences (S/E/P/Q/C)",
    mono: true,
    render: (row) => CATEGORIES.map((c) => row.consequence[c.key]).join(" / "),
  },
  { key: "likelihood", header: "Likelihood", align: "end" },
  { key: "riskScore", header: "Risk score", align: "end" },
  { key: "criticality", header: "Criticality", align: "end" },
  { key: "assessedAt", header: "Assessed", mono: true, render: (row) => minuteOf(row.assessedAt) },
  { key: "assessedBy", header: "By", render: (row) => row.assessedBy ?? dash },
];

/** Criticality assessments (FR-44.22): the matrix form and the versions so far. */
export function CriticalityCard({ assetId, onSaved }: { assetId: string; onSaved(text: string): void }) {
  const [round, setRound] = useState(0);
  const history = useLoad((signal) => listAssessments(assetId, signal), `${assetId}|crit|${round}`);
  const [scores, setScores] = useState<Consequences>({ safety: 1, environment: 1, production: 1, quality: 1, cost: 1 });
  const [likelihood, setLikelihood] = useState(1);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const preview = previewCriticality(scores, likelihood);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setBusy(true);
    try {
      const saved = await assessCriticality(assetId, scores, likelihood, key.current);
      key.current = newKey();
      setRound((n) => n + 1);
      onSaved(`Assessment version ${saved.version} recorded: risk ${saved.riskScore}, criticality ${saved.criticality}.`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Criticality assessment">
      <div className={styles.stack}>
        <p className={styles.note}>
          Score each consequence of a failure and how likely it is, from 1 to 5. The risk score is the worst consequence times the likelihood. The
          asset takes the derived criticality, and new corrective orders default to priority 6 minus criticality.
        </p>
        {history.error ? (
          <Failure title="Could not load the assessments" problem={history.error} retry={history.reload} />
        ) : (
          <DataTable
            label="Criticality assessments"
            columns={assessmentColumns}
            rows={history.data ?? []}
            getRowKey={(row) => row.id}
            loading={history.loading}
            empty={{ title: "Not assessed yet", description: "The criticality was set by hand when the asset was created." }}
          />
        )}
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Assess criticality">
          {CATEGORIES.map((c) => (
            <FormField key={c.key} id={`crit-${c.key}`} label={c.label}>
              {(control) => (
                <Select
                  {...control}
                  value={String(scores[c.key])}
                  onChange={(e) => {
                    setScores({ ...scores, [c.key]: Number(e.target.value) });
                    key.current = newKey();
                  }}
                >
                  {SCORES.map((n) => (
                    <option key={n} value={String(n)}>
                      {n}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ))}
          <FormField id="crit-likelihood" label="Likelihood of failure">
            {(control) => (
              <Select
                {...control}
                value={String(likelihood)}
                onChange={(e) => {
                  setLikelihood(Number(e.target.value));
                  key.current = newKey();
                }}
              >
                {SCORES.map((n) => (
                  <option key={n} value={String(n)}>
                    {n}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <p className={styles.note}>
              Risk score {preview.risk}, criticality {preview.criticality}.
            </p>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Failure title="Could not record the assessment" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" loading={busy}>
              Record assessment
            </Button>
          </div>
        </form>
      </div>
    </Card>
  );
}

interface WarrantyValues {
  warrantyType: string;
  vendorPartyId: string;
  startBasis: "PURCHASE" | "INSTALL";
  startsOn: string;
  months: string;
  partsCovered: boolean;
  laborCovered: boolean;
  exclusions: string;
}

type WarrantyErrors = Partial<Record<keyof WarrantyValues, string>>;

/** The checks the service makes on a new warranty, and the warranty the form sends. */
export function validateWarranty(values: WarrantyValues): { errors: WarrantyErrors; input?: NewWarranty } {
  const errors: WarrantyErrors = {};
  const type = values.warrantyType.trim().toUpperCase();
  if (!CODE.test(type)) errors.warrantyType = "Use 2 to 20 capital letters, digits or underscores, for example OEM.";
  if (!UUID.test(values.vendorPartyId.trim())) errors.vendorPartyId = "Enter the vendor's party id (a UUID).";
  if (!DATE.test(values.startsOn)) errors.startsOn = "Enter the start date.";
  const months = Number(values.months);
  if (!/^\d{1,3}$/.test(values.months.trim()) || months < 1 || months > 240) errors.months = "Enter 1 to 240 months.";
  if (!values.partsCovered && !values.laborCovered) errors.partsCovered = "A warranty covers parts, labor or both.";
  const exclusions = values.exclusions
    .split(",")
    .map((e) => e.trim().toUpperCase())
    .filter(Boolean);
  if (exclusions.length > 20 || exclusions.some((e) => !CODE.test(e))) errors.exclusions = "List up to 20 job type codes, separated by commas.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      warrantyType: type,
      vendorPartyId: values.vendorPartyId.trim(),
      startBasis: values.startBasis,
      startsOn: values.startsOn,
      months,
      partsCovered: values.partsCovered,
      laborCovered: values.laborCovered,
      ...(exclusions.length ? { exclusions } : {}),
    },
  };
}

const covers = (w: Warranty) => [w.partsCovered ? "Parts" : "", w.laborCovered ? "Labor" : ""].filter(Boolean).join(" and ");

const warrantyColumns: DataTableColumn<Warranty>[] = [
  { key: "warrantyType", header: "Warranty", mono: true },
  { key: "vendorPartyId", header: "Vendor", mono: true },
  { key: "startsOn", header: "Starts", mono: true },
  { key: "endsOn", header: "Ends before", mono: true },
  { key: "partsCovered", header: "Covers", render: covers },
  { key: "exclusions", header: "Excluded job types", mono: true, render: (row) => (row.exclusions.length ? row.exclusions.join(", ") : dash) },
  { key: "status", header: "Status", render: (row) => <State value={row.status} /> },
];

const emptyWarranty: WarrantyValues = {
  warrantyType: "",
  vendorPartyId: "",
  startBasis: "PURCHASE",
  startsOn: "",
  months: "24",
  partsCovered: true,
  laborCovered: false,
  exclusions: "",
};

/** Vendor warranties (FR-44.17). New work orders on the asset are checked against them (BR-44.14). */
export function WarrantyCard({ assetId, onSaved }: { assetId: string; onSaved(text: string): void }) {
  const [round, setRound] = useState(0);
  const list = useLoad((signal) => listWarranties(assetId, signal), `${assetId}|war|${round}`);
  const [values, setValues] = useState<WarrantyValues>(emptyWarranty);
  const [errors, setErrors] = useState<WarrantyErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<WarrantyValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateWarranty(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createWarranty(assetId, input, key.current);
      key.current = newKey();
      setValues(emptyWarranty);
      setRound((n) => n + 1);
      onSaved(`Warranty ${input.warrantyType} recorded.`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Warranties">
      <div className={styles.stack}>
        <p className={styles.note}>A work order created while a warranty is in force, for a job type it does not exclude, is flagged as covered. Completing it raises a claim for the covered parts or labor.</p>
        {list.error ? (
          <Failure title="Could not load the warranties" problem={list.error} retry={list.reload} />
        ) : (
          <DataTable label="Warranties" columns={warrantyColumns} rows={list.data ?? []} getRowKey={(row) => row.id} loading={list.loading} empty={{ title: "No warranties recorded" }} />
        )}
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Add warranty">
          <FormField id="war-type" label="Warranty type" error={errors.warrantyType}>
            {(control) => <Input {...control} value={values.warrantyType} maxLength={20} invalid={Boolean(errors.warrantyType)} onChange={(e) => update({ warrantyType: e.target.value })} />}
          </FormField>
          <FormField id="war-vendor" label="Vendor party" error={errors.vendorPartyId}>
            {(control) => <EntityLookup entity="party" {...control} value={values.vendorPartyId} invalid={Boolean(errors.vendorPartyId)} onChange={(e) => update({ vendorPartyId: e.target.value })} />}
          </FormField>
          <FormField id="war-basis" label="Starts from">
            {(control) => (
              <Select {...control} value={values.startBasis} onChange={(e) => update({ startBasis: e.target.value as WarrantyValues["startBasis"] })}>
                <option value="PURCHASE">Purchase</option>
                <option value="INSTALL">Installation</option>
              </Select>
            )}
          </FormField>
          <FormField id="war-start" label="Start date" error={errors.startsOn}>
            {(control) => <Input {...control} type="date" value={values.startsOn} invalid={Boolean(errors.startsOn)} onChange={(e) => update({ startsOn: e.target.value })} />}
          </FormField>
          <FormField id="war-months" label="Months" error={errors.months}>
            {(control) => <Input {...control} inputMode="numeric" value={values.months} invalid={Boolean(errors.months)} onChange={(e) => update({ months: e.target.value })} />}
          </FormField>
          <FormField id="war-exclusions" label="Excluded job types" hint="Comma separated, for example PAINT, CLEAN." error={errors.exclusions}>
            {(control) => <Input {...control} value={values.exclusions} invalid={Boolean(errors.exclusions)} onChange={(e) => update({ exclusions: e.target.value })} />}
          </FormField>
          <div className={styles.check}>
            <Input id="war-parts" type="checkbox" checked={values.partsCovered} onChange={(e) => update({ partsCovered: e.target.checked })} />
            <label htmlFor="war-parts">Covers parts</label>
          </div>
          <div className={styles.check}>
            <Input id="war-labor" type="checkbox" checked={values.laborCovered} onChange={(e) => update({ laborCovered: e.target.checked })} />
            <label htmlFor="war-labor">Covers labor</label>
          </div>
          {errors.partsCovered ? (
            <div className={styles.formActions}>
              <p className={styles.fieldError}>{errors.partsCovered}</p>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Failure title="Could not record the warranty" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" loading={busy}>
              Add warranty
            </Button>
          </div>
        </form>
      </div>
    </Card>
  );
}
