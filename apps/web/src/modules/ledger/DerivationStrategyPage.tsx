import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import {
  activateDerivationStrategy,
  CONDITION_OPS,
  DERIVATION_KINDS,
  fetchDerivationStrategies,
  fetchDerivationStrategy,
  fetchLookupTables,
  LedgerApiError,
  putDerivationStrategy,
  type DerivationStep,
  type DerivationStrategy,
  type DerivationStrategyInput,
  type Violation,
} from "./api/client";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";
import { splitFields } from "./LookupTableFormPage";
import { ProblemMessage } from "../../shell/errors";

const MAX_STEPS = 50;
const ATTRIBUTE = /^[A-Z][A-Z0-9_]{0,19}$/;
const DIM_VALUE = /^[A-Za-z0-9._-]{1,20}$/;

export interface StepDraft {
  key: number;
  sequence: string;
  kind: DerivationStep["kind"];
  sourceFields: string;
  target: string;
  lookupTableId: string;
  value: string;
  overwrite: boolean;
  condField: string;
  condOp: "" | (typeof CONDITION_OPS)[number];
  condValues: string;
}

export interface StrategyDraft {
  name: string;
  steps: StepDraft[];
}

type Errors = Record<string, string>;

const KIND_LABELS: Record<DerivationStep["kind"], string> = {
  table_lookup: "Look up in a table",
  move: "Copy a value",
  clear: "Clear a value",
  constant: "Set a fixed value",
};
const OP_LABELS: Record<(typeof CONDITION_OPS)[number], string> = {
  eq: "is",
  ne: "is not",
  in: "is one of",
  blank: "is blank",
  notblank: "is not blank",
};

export const emptyStep = (key: number, sequence: number): StepDraft => ({
  key,
  sequence: String(sequence),
  kind: "table_lookup",
  sourceFields: "",
  target: "",
  lookupTableId: "",
  value: "",
  overwrite: false,
  condField: "",
  condOp: "",
  condValues: "",
});

export const emptyStrategy: StrategyDraft = { name: "", steps: [] };

/** Client checks that mirror PUT .../derivation-strategies/{version}; the service checks the names against the ledger. */
export function validateStrategy(d: StrategyDraft): Errors {
  const errors: Errors = {};
  const name = d.name.trim();
  if (name === "" || name.length > 80) errors.name = "Enter a name of up to 80 characters.";
  if (d.steps.length < 1) errors.steps = "Add at least one step.";
  if (d.steps.length > MAX_STEPS) errors.steps = `A strategy has at most ${MAX_STEPS} steps.`;
  let last = 0;
  d.steps.forEach((s, i) => {
    const at = `Step ${i + 1}: `;
    const seq = Number(s.sequence);
    const sources = splitFields(s.sourceFields);
    let problem = "";
    if (!Number.isInteger(seq) || seq <= last) problem = "the sequence must be a whole number above the previous step's.";
    else if (!ATTRIBUTE.test(s.target.trim())) problem = "name the dimension it writes, in capitals.";
    else if (sources.includes(s.target.trim())) problem = "a step can't read the dimension it writes.";
    else if (s.kind === "table_lookup" && s.lookupTableId === "") problem = "choose a lookup table.";
    else if (s.kind === "table_lookup" && sources.length === 0) problem = "list the values it reads, as in the lookup table.";
    else if (s.kind === "move" && sources.length !== 1) problem = "name the one value to copy.";
    else if (s.kind === "clear" && !s.overwrite) problem = "clearing only works when it may overwrite.";
    else if (s.kind === "constant" && !DIM_VALUE.test(s.value.trim())) problem = "enter the fixed value, up to 20 letters, digits, dots, underscores or dashes.";
    else if (s.condOp !== "" && s.condField.trim() === "") problem = "name the value the condition checks.";
    else if (s.condOp === "eq" || s.condOp === "ne") {
      if (splitFields(s.condValues).length !== 1) problem = "the condition needs exactly one value.";
    } else if (s.condOp === "in" && (splitFields(s.condValues).length < 1 || splitFields(s.condValues).length > 20)) problem = "the condition needs 1 to 20 values.";
    if (problem) errors[`steps.${i}`] = at + problem;
    if (Number.isInteger(seq)) last = seq;
  });
  return errors;
}

export function toStrategyInput(d: StrategyDraft): DerivationStrategyInput {
  return {
    name: d.name.trim(),
    steps: d.steps.map((s) => {
      const base: DerivationStep = {
        sequence: Number(s.sequence),
        kind: s.kind,
        sourceFields: s.kind === "clear" || s.kind === "constant" ? [] : splitFields(s.sourceFields),
        target: s.target.trim(),
        overwrite: s.overwrite,
      };
      if (s.kind === "table_lookup") base.lookupTableId = s.lookupTableId;
      if (s.kind === "constant") base.value = s.value.trim();
      if (s.condOp !== "") base.condition = { field: s.condField.trim(), op: s.condOp, ...(s.condOp === "blank" || s.condOp === "notblank" ? {} : { values: splitFields(s.condValues) }) };
      return base;
    }),
  };
}

export function toStrategyDraft(s: DerivationStrategy): StrategyDraft {
  return {
    name: s.name,
    steps: s.steps.map((st, i) => ({
      key: i,
      sequence: String(st.sequence),
      kind: st.kind,
      sourceFields: st.sourceFields.join(", "),
      target: st.target,
      lookupTableId: st.lookupTableId ?? "",
      value: st.value ?? "",
      overwrite: st.overwrite,
      condField: st.condition?.field ?? "",
      condOp: st.condition?.op ?? "",
      condValues: (st.condition?.values ?? []).join(", "),
    })),
  };
}

function failureOf(error: unknown): { problem: unknown; violations: Violation[] } {
  if (error instanceof LedgerApiError) return { problem: error, violations: error.violations };
  return { problem: error, violations: [] };
}

interface FormProps {
  ledgerId: string;
  version: number;
  initial: StrategyDraft;
  existing?: DerivationStrategy;
}

function StrategyForm({ ledgerId, version, initial, existing }: FormProps) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const tables = useQuery({ queryKey: ["ledger", "lookup-tables", ledgerId], queryFn: () => fetchLookupTables(ledgerId) });
  const [values, setValues] = useState(initial);
  const [nextKey, setNextKey] = useState(initial.steps.length);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<{ problem: unknown; violations: Violation[] } | null>(null);
  const [busy, setBusy] = useState<"draft" | "active" | null>(null);
  // The lockVersion of the stored draft: moves on after a save, so a retry after a failed activation is not stale.
  const [lockVersion, setLockVersion] = useState(existing?.lockVersion);
  const locked = existing !== undefined && existing.status !== "draft";
  const query = ledgerQuery(ledgerId);

  const updateStep = (key: number, patch: Partial<StepDraft>) =>
    setValues((prev) => ({ ...prev, steps: prev.steps.map((s) => (s.key === key ? { ...s, ...patch } : s)) }));
  const addStep = () => {
    const last = Number(values.steps[values.steps.length - 1]?.sequence ?? 0);
    setValues((prev) => ({ ...prev, steps: [...prev.steps, emptyStep(nextKey, (Number.isInteger(last) ? last : 0) + 10)] }));
    setNextKey((k) => k + 1);
  };
  const removeStep = (key: number) => setValues((prev) => ({ ...prev, steps: prev.steps.filter((s) => s.key !== key) }));

  async function save(activate: boolean) {
    const found = validateStrategy(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(activate ? "active" : "draft");
    try {
      const saved = await putDerivationStrategy(ledgerId, version, toStrategyInput(values), lockVersion);
      setLockVersion(saved.lockVersion);
      if (activate) await activateDerivationStrategy(ledgerId, version, saved.lockVersion);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "derivation-strategies"] });
      navigate(`/ledger/derivation-strategies${query}`);
    } catch (error) {
      setFailure(failureOf(error));
    } finally {
      setBusy(null);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    void save(false);
  }

  const stepErrors = Object.entries(errors).filter(([k]) => k.startsWith("steps."));

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={existing ? `Derivation strategy version ${version}` : "New derivation strategy version"}>
        <p className={`${styles.note} ${styles.wide}`}>
          Version {version}
          {existing ? <> <Badge tone={existing.status === "active" ? "success" : "neutral"}>{existing.status}</Badge></> : null}
          {locked ? " This version is locked. Create a new version to change it." : null}
        </p>
        <div className={styles.wide}>
          <FormField id="ds-name" label="Name" error={errors.name}>
            {(c) => <Input {...c} value={values.name} readOnly={locked} maxLength={80} onChange={(e) => setValues((p) => ({ ...p, name: e.target.value }))} invalid={Boolean(errors.name)} />}
          </FormField>
        </div>
        {values.steps.map((s, i) => {
          const n = i + 1;
          const id = (field: string) => `ds-${s.key}-${field}`;
          return (
            <fieldset key={s.key} className={styles.group}>
              <legend className={styles.legend}>Step {n}</legend>
              <FormField id={id("seq")} label={`Step ${n} sequence`}>
                {(c) => <Input {...c} inputMode="numeric" value={s.sequence} readOnly={locked} onChange={(e) => updateStep(s.key, { sequence: e.target.value })} />}
              </FormField>
              <FormField id={id("kind")} label={`Step ${n} action`}>
                {(c) => (
                  <Select {...c} value={s.kind} disabled={locked} onChange={(e) => updateStep(s.key, { kind: e.target.value as StepDraft["kind"], overwrite: e.target.value === "clear" ? true : s.overwrite })}>
                    {DERIVATION_KINDS.map((k) => (
                      <option key={k} value={k}>{KIND_LABELS[k]}</option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id={id("target")} label={`Step ${n} dimension to write`}>
                {(c) => <Input {...c} className={styles.mono} placeholder="FUNC_AREA" value={s.target} readOnly={locked} onChange={(e) => updateStep(s.key, { target: e.target.value.toUpperCase() })} />}
              </FormField>
              {s.kind === "table_lookup" || s.kind === "move" ? (
                <FormField id={id("src")} label={`Step ${n} values to read`} hint="ACCOUNT, ACCOUNT_TYPE or dimensions; for a lookup, as in the table.">
                  {(c) => <Input {...c} className={styles.mono} placeholder="ACCOUNT, CC" value={s.sourceFields} readOnly={locked} onChange={(e) => updateStep(s.key, { sourceFields: e.target.value.toUpperCase() })} />}
                </FormField>
              ) : null}
              {s.kind === "table_lookup" ? (
                <FormField id={id("table")} label={`Step ${n} lookup table`}>
                  {(c) => (
                    <Select {...c} value={s.lookupTableId} disabled={locked} onChange={(e) => updateStep(s.key, { lookupTableId: e.target.value })}>
                      <option value="">Choose a table</option>
                      {(tables.data ?? []).map((t) => (
                        <option key={t.id} value={t.id}>{t.name}</option>
                      ))}
                    </Select>
                  )}
                </FormField>
              ) : null}
              {s.kind === "constant" ? (
                <FormField id={id("value")} label={`Step ${n} fixed value`}>
                  {(c) => <Input {...c} className={styles.mono} value={s.value} readOnly={locked} onChange={(e) => updateStep(s.key, { value: e.target.value })} />}
                </FormField>
              ) : null}
              <FormField id={id("condfield")} label={`Step ${n} only when`} optional hint="The value to check, such as ACCOUNT_TYPE.">
                {(c) => <Input {...c} className={styles.mono} value={s.condField} readOnly={locked} onChange={(e) => updateStep(s.key, { condField: e.target.value.toUpperCase() })} />}
              </FormField>
              <FormField id={id("condop")} label={`Step ${n} condition`} optional>
                {(c) => (
                  <Select {...c} value={s.condOp} disabled={locked} onChange={(e) => updateStep(s.key, { condOp: e.target.value as StepDraft["condOp"] })}>
                    <option value="">No condition</option>
                    {CONDITION_OPS.map((op) => (
                      <option key={op} value={op}>{OP_LABELS[op]}</option>
                    ))}
                  </Select>
                )}
              </FormField>
              {s.condOp === "eq" || s.condOp === "ne" || s.condOp === "in" ? (
                <FormField id={id("condvalues")} label={`Step ${n} condition values`} hint="Separated by commas.">
                  {(c) => <Input {...c} className={styles.mono} value={s.condValues} readOnly={locked} onChange={(e) => updateStep(s.key, { condValues: e.target.value })} />}
                </FormField>
              ) : null}
              <span className={styles.check}>
                <Input id={id("overwrite")} type="checkbox" checked={s.overwrite} disabled={locked || s.kind === "clear"} onChange={(e) => updateStep(s.key, { overwrite: e.target.checked })} />
                <label htmlFor={id("overwrite")}>Step {n} may overwrite a value already set</label>
              </span>
              {!locked ? (
                <Button size="sm" variant="ghost" onClick={() => removeStep(s.key)}>
                  Remove step {n}
                </Button>
              ) : null}
            </fieldset>
          );
        })}
        {values.steps.length === 0 ? <p className={`${styles.note} ${styles.wide}`}>No steps yet.</p> : null}
        {!locked ? (
          <div className={styles.wide}>
            <Button onClick={addStep}>Add step</Button>
            <p className={styles.note}>Steps run in sequence. A value typed on the posting is never changed by a step. A step fills a blank value; it replaces a value an earlier step set only when it may overwrite.</p>
          </div>
        ) : null}
        {stepErrors.length > 0 ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Check the steps" live="alert">
              <ul className={styles.problems}>
                {stepErrors.map(([k, message]) => <li key={k}>{message}</li>)}
              </ul>
            </Alert>
          </div>
        ) : null}
        {errors.steps ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Add steps" live="alert">{errors.steps}</Alert>
          </div>
        ) : null}
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not save the strategy" live="alert">
              <ProblemMessage problem={failure.problem} />
              {failure.violations.length > 0 ? (
                <ul className={styles.problems}>
                  {failure.violations.map((v, i) => <li key={i}>{v.message}</li>)}
                </ul>
              ) : null}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          {!locked ? (
            <>
              <Button type="submit" variant="secondary" loading={busy === "draft"} disabled={busy !== null}>Save draft</Button>
              <Button variant="primary" loading={busy === "active"} disabled={busy !== null} onClick={() => void save(true)}>Save and activate</Button>
            </>
          ) : (
            <Link to={`/ledger/derivation-strategies/new?ledger=${encodeURIComponent(ledgerId)}&from=${version}`}>Create a new version from this one</Link>
          )}
          <Link to={`/ledger/derivation-strategies${query}`}>Back to the list</Link>
        </div>
      </form>
    </Card>
  );
}

function NeedsLedger() {
  return (
    <Alert tone="warning" title="No ledger chosen">
      Open this page from <Link to="/ledger/derivation-strategies">Derivation strategies</Link> after choosing a ledger.
    </Alert>
  );
}

/** Create the next draft version; `?from=<version>` starts from that version's steps. */
export function NewDerivationStrategyPage() {
  const [ledgerId] = useLedgerParam();
  const [params] = useSearchParams();
  const from = Number(params.get("from") ?? "0");
  const list = useQuery({ queryKey: ["ledger", "derivation-strategies", ledgerId], queryFn: () => fetchDerivationStrategies(ledgerId), enabled: ledgerId !== "" });
  let body;
  if (ledgerId === "") body = <NeedsLedger />;
  else if (list.isError)
    body = (
      <Alert tone="danger" title="Could not load the existing versions" live="alert" action={<Button size="sm" onClick={() => void list.refetch()}>Try again</Button>}>
        {errorText(list.error)}
      </Alert>
    );
  else if (list.isPending) body = <Skeleton height="tile" />;
  else {
    const next = Math.max(0, ...list.data.map((s) => s.version)) + 1;
    const source = list.data.find((s) => s.version === from);
    body = <StrategyForm key={next} ledgerId={ledgerId} version={next} initial={source ? toStrategyDraft(source) : emptyStrategy} />;
  }
  return (
    <div className={styles.stack}>
      <PageHeader title="New derivation strategy version" description="Save it as a draft to keep working, or activate it so postings from now on run its steps." />
      {body}
    </div>
  );
}

/** View or edit one version: a draft is editable and can be activated. */
export function DerivationStrategyPage() {
  const { version = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const number = Number(version);
  const result = useQuery({ queryKey: ["ledger", "derivation-strategies", ledgerId, number], queryFn: () => fetchDerivationStrategy(ledgerId, number), enabled: ledgerId !== "" });
  return (
    <div className={styles.stack}>
      <PageHeader title={`Derivation strategy version ${version}`} />
      {ledgerId === "" ? (
        <NeedsLedger />
      ) : result.isPending ? (
        <Skeleton height="tile" />
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the derivation strategy" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <StrategyForm key={result.data.lockVersion} ledgerId={ledgerId} version={number} existing={result.data} initial={toStrategyDraft(result.data)} />
      )}
    </div>
  );
}
