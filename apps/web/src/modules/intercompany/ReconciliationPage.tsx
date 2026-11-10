import { useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  StatGroup,
  StatTile,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import { classifyItems, describeProblem, type ClassifiedItem, type ClassifyResult, type Problem, type ReconItem, type ReconStatus } from "./api/client";
import { amount, isCurrency, isDecimal, isNonZero, isSignedDecimal } from "./format";
import { newKey, runReconciliation } from "./api/postings";
import { RowGrid, type RowField } from "./RowGrid";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type ItemKey = "id" | "entityA" | "entityB" | "period" | "currency" | "amountA" | "amountB" | "nextPeriodA" | "nextPeriodB" | "fxApplies";
type ItemValues = Record<ItemKey, string>;

export interface ReconValues {
  fxTolerancePct: string;
  items: ItemValues[];
}

const itemFields: RowField<ItemKey>[] = [
  { name: "id", label: "Reference", optional: true, mono: true },
  { name: "entityA", label: "Entity A", mono: true },
  { name: "entityB", label: "Entity B", mono: true },
  { name: "period", label: "Period", mono: true },
  { name: "currency", label: "Currency", mono: true },
  { name: "amountA", label: "Due from, booked in A", decimal: true },
  { name: "amountB", label: "Due to, booked in B", decimal: true },
  { name: "nextPeriodA", label: "A in next period", optional: true, decimal: true },
  { name: "nextPeriodB", label: "B in next period", optional: true, decimal: true },
  {
    name: "fxApplies",
    label: "Converted between currencies",
    options: [
      { value: "no", label: "No" },
      { value: "yes", label: "Yes, FX tolerance applies" },
    ],
  },
];

const emptyItem = (): ItemValues => ({
  id: "",
  entityA: "",
  entityB: "",
  period: "",
  currency: "",
  amountA: "",
  amountB: "",
  nextPeriodA: "",
  nextPeriodB: "",
  fxApplies: "no",
});

const pair = (id: string, a: string, b: string, extra: Partial<ItemValues> = {}): ItemValues => ({
  ...emptyItem(),
  id,
  entityA: "P",
  entityB: "S",
  period: "2026-11",
  currency: "USD",
  amountA: a,
  amountB: b,
  ...extra,
});

/** One pair of each kind: matched, within FX tolerance, a timing difference and a mismatch. */
export const sample: ReconValues = {
  fxTolerancePct: "0.5",
  items: [
    pair("IC-1001", "1000.00", "1000.00"),
    pair("IC-1002", "1000.00", "996.00", { fxApplies: "yes" }),
    pair("IC-1003", "250.00", "0", { nextPeriodB: "250.00" }),
    pair("IC-1004", "100.00", "105.00"),
  ],
};

/** Checks that mirror the service, and the request the form sends. */
export function validateRecon(values: ReconValues): { errors: Record<string, string>; request?: { fxTolerancePct?: string; items: ReconItem[] } } {
  const errors: Record<string, string> = {};
  const tolerance = values.fxTolerancePct.trim();
  if (tolerance !== "" && !(isDecimal(tolerance) && /^0*([0-4](\.\d+)?|5(\.0+)?)$/.test(tolerance))) errors.fxTolerancePct = "Enter a percentage from 0 to 5, or leave it empty for 0.5.";
  if (values.items.length === 0) errors.items = "Add at least one pair.";
  const items = values.items.map((it, i): ReconItem => {
    const at = (k: ItemKey) => `items[${i}].${k}`;
    const entityA = it.entityA.trim();
    const entityB = it.entityB.trim();
    const currency = it.currency.trim().toUpperCase();
    if (entityA === "") errors[at("entityA")] = "Enter entity A.";
    if (entityB === "") errors[at("entityB")] = "Enter entity B.";
    else if (entityA === entityB) errors[at("entityB")] = "Entity B must differ from entity A.";
    if (!/^\d{4}-\d{2}$/.test(it.period.trim())) errors[at("period")] = "Enter a period such as 2026-11.";
    if (!isCurrency(currency)) errors[at("currency")] = "Enter a three-letter currency code.";
    for (const k of ["amountA", "amountB"] as const) {
      if (!isSignedDecimal(it[k])) errors[at(k)] = "Enter an amount with at most 6 decimals.";
    }
    for (const k of ["nextPeriodA", "nextPeriodB"] as const) {
      if (it[k].trim() !== "" && !isSignedDecimal(it[k])) errors[at(k)] = "Enter an amount, or leave it empty.";
    }
    const optional = (v: string) => (v.trim() === "" ? undefined : v.trim());
    return {
      id: optional(it.id),
      entityA,
      entityB,
      period: it.period.trim(),
      currency,
      amountA: it.amountA.trim(),
      amountB: it.amountB.trim(),
      nextPeriodA: optional(it.nextPeriodA),
      nextPeriodB: optional(it.nextPeriodB),
      fxApplies: it.fxApplies === "yes",
    };
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { fxTolerancePct: tolerance || undefined, items } };
}

const statusTone: Record<ReconStatus, BadgeTone> = { Matched: "success", Timing: "warning", Mismatch: "danger" };

const resultColumns: DataTableColumn<ClassifiedItem>[] = [
  { key: "id", header: "Reference", mono: true },
  { key: "pair", header: "Pair", render: (r) => `${r.entityA} / ${r.entityB}` },
  { key: "period", header: "Period", mono: true },
  { key: "amountA", header: "Amount A", align: "end", render: (r) => amount(r.amountA) },
  { key: "amountB", header: "Amount B", align: "end", render: (r) => amount(r.amountB) },
  {
    key: "difference",
    header: "Difference",
    align: "end",
    render: (r) => <span className={isNonZero(r.difference) ? styles.danger : undefined}>{amount(r.difference)}</span>,
  },
  { key: "currency", header: "Currency", mono: true },
  {
    key: "status",
    header: "Status",
    render: (r) => (
      <>
        <Badge tone={statusTone[r.status] ?? "neutral"}>{r.status}</Badge>
        {r.toleranceApplied ? <span className={styles.note}> within FX tolerance</span> : null}
      </>
    ),
  },
];

/** Classify intercompany balance pairs as Matched, Timing or Mismatch (FR-40.6, BR-40.5). Classifying alone stores nothing. */
export function ReconciliationPage() {
  const [values, setValues] = useState<ReconValues>(sample);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ClassifyResult | null>(null);
  const [saving, setSaving] = useState(false);
  const navigate = useNavigate();
  const key = useRef(newKey());

  /** Store the same pairs as a run. A run has one period and currency, so every pair must share them. */
  async function saveRun() {
    const { errors: found, request } = validateRecon(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    const { period, currency } = request.items[0];
    const other = request.items.findIndex((it) => it.period !== period || it.currency !== currency);
    if (other >= 0) {
      setErrors({ [`items[${other}].period`]: "A run covers one period and currency. Use the same as the first pair." });
      return;
    }
    setSaving(true);
    try {
      const run = await runReconciliation(
        { period, currency, fxTolerancePct: request.fxTolerancePct, items: request.items.map(({ period: _p, currency: _c, ...rest }) => rest) },
        key.current,
      );
      key.current = newKey();
      navigate(`/intercompany/reconciliation-runs/${run.id}`);
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setSaving(false);
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateRecon(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setResult(await classifyItems(request));
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Intercompany reconciliation"
        description="A pair matches when both sides agree within 0.01, or within the FX tolerance when they were converted between currencies. It is a timing difference when the next period's bookings close it, and a mismatch otherwise. Save the classification as a run to keep it and resolve the pairs that need action."
      />
      <Card title="Balance pairs" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <FormField
            id="recon-tolerance"
            label="FX tolerance (%)"
            optional
            hint="Percentage of the larger side, from 0 to 5. Applies only to pairs converted between currencies."
            error={errors.fxTolerancePct}
          >
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values.fxTolerancePct}
                onChange={(e) => setValues({ ...values, fxTolerancePct: e.target.value })}
                invalid={Boolean(errors.fxTolerancePct)}
              />
            )}
          </FormField>
          <RowGrid
            idPrefix="recon-item"
            path="items"
            noun="Pair"
            fields={itemFields}
            rows={values.items}
            errors={errors}
            newRow={emptyItem}
            onChange={(items) => setValues({ ...values, items })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The pairs were not classified" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Classify pairs
            </Button>
            <Button loading={saving} onClick={() => void saveRun()}>
              Classify and save as a run
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Reconciliation items" columns={resultColumns} rows={[]} getRowKey={(r) => r.id} loading />
      ) : result ? (
        <>
          <StatGroup label="Reconciliation summary">
            <StatTile label="Matched" value={result.summary.matched} />
            <StatTile label="Timing" value={result.summary.timing} tone="warning" />
            <StatTile label="Mismatch" value={result.summary.mismatch} tone="danger" />
          </StatGroup>
          {result.summary.blocking ? (
            <Alert tone="warning" title="Mismatches block consolidation">
              Resolve every mismatch before the period's consolidation runs, unless a group approver overrides it (CNS-0901).
            </Alert>
          ) : null}
          <DataTable
            label="Reconciliation items"
            columns={resultColumns}
            rows={result.items}
            getRowKey={(r, i) => `${r.id}-${i}`}
            empty={{ title: "No items", description: "The service returned no classified items." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="checkCircle"
            headingLevel={3}
            title="Nothing classified yet"
            description="Enter what each side booked for an entity pair, period and currency to see whether it matches."
          />
        </Card>
      )}
    </div>
  );
}
