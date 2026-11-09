import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, newKey, startGLRun, type GLRunRequest, type Problem } from "./api/client";
import { glImbalances, localToUtc, parseGLBalances, SOURCE_LABELS, SOURCE_SYSTEMS, splitList, type SourceSystem } from "./domain";
import styles from "./pages.module.css";

export interface GLRunValues {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoff: string;
  freeze: string;
  quarantineMaxPct: string;
  accounts: string;
  openItemsRunId: string;
  balances: string;
}

type Field = keyof GLRunValues;
type Errors = Partial<Record<Field, string>>;

const PCT = /^\d{1,2}(\.\d{1,4})?$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** The checks the service makes before it loads a gl_balances extract, and the request the form sends. */
export function validateGLRun(values: GLRunValues): { errors: Errors; input?: GLRunRequest } {
  const errors: Errors = {};
  const company = values.sourceCompany.trim();
  if (company === "" || company.length > 40) errors.sourceCompany = "Enter the legacy company (DataAreaId or BUKRS).";
  const cutoffTs = localToUtc(values.cutoff);
  const freezeTs = localToUtc(values.freeze);
  if (!cutoffTs) errors.cutoff = "Enter the extract cutoff.";
  if (!freezeTs) errors.freeze = "Enter the legacy transaction freeze.";
  if (cutoffTs && freezeTs && cutoffTs > freezeTs) errors.cutoff = "The cutoff can't be after the legacy freeze (MIG-1501).";
  const pct = values.quarantineMaxPct.trim();
  if (pct !== "" && (!PCT.test(pct) || Number(pct) > 10)) errors.quarantineMaxPct = "Enter a percentage from 0 to 10.";
  const accounts = splitList(values.accounts);
  if (accounts.length === 0) errors.accounts = "List the chart of accounts the balances are checked against.";
  const openItemsRunId = values.openItemsRunId.trim();
  if (openItemsRunId !== "" && !UUID.test(openItemsRunId)) errors.openItemsRunId = "Enter the id of an open_items run, or leave it blank.";
  const parsed = parseGLBalances(values.balances);
  if (parsed.errors.length > 0) errors.balances = parsed.errors.slice(0, 3).join(" ");
  else if (parsed.records.length === 0) errors.balances = "Paste at least one balance.";
  else {
    const off = glImbalances(parsed.records);
    if (off.length > 0)
      errors.balances = `Debits and credits must agree in every currency: ${off.map((o) => `${o.currency} is off by ${o.net}`).join(", ")}.`;
  }
  if (Object.keys(errors).length > 0 || !cutoffTs || !freezeTs) return { errors };
  return {
    errors,
    input: {
      sourceSystem: values.sourceSystem,
      sourceCompany: company,
      cutoffTs,
      freezeTs,
      ...(pct !== "" ? { quarantineMaxPct: pct } : {}),
      accounts,
      ...(openItemsRunId !== "" ? { openItemsRunId } : {}),
      records: parsed.records,
    },
  };
}

/** Load legacy GL balances as opening balances from a pasted gl_balances extract (FR-49.1, FR-49.2, BR-49.2). */
export function NewGLRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<GLRunValues>({
    sourceSystem: "AX2012",
    sourceCompany: "",
    cutoff: "",
    freeze: "",
    quarantineMaxPct: "",
    accounts: "",
    openItemsRunId: "",
    balances: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<GLRunValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateGLRun(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const run = await startGLRun(input, key.current);
      navigate(`/migration-factory/runs/${run.runId}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "MIG-0007") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Field, label: string, hint?: string, extra?: Record<string, unknown>, optional?: boolean) => (
    <FormField id={`gl-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<GLRunValues>)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Load GL balances"
        description="Legacy balances per account, dimension combination and currency at the cutoff, loaded as opening balances and reconciled to the extract. Nothing posts to the GL here."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="gl-sourceSystem" label="Source system">
            {(control) => (
              <Select {...control} value={values.sourceSystem} onChange={(event) => update({ sourceSystem: event.target.value as SourceSystem })}>
                {SOURCE_SYSTEMS.map((s) => (
                  <option key={s} value={s}>
                    {SOURCE_LABELS[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("sourceCompany", "Legacy company", "DataAreaId in AX, BUKRS in SAP.", { maxLength: 40 })}
          {text("cutoff", "Extract cutoff (UTC)", undefined, { type: "datetime-local" })}
          {text("freeze", "Legacy freeze (UTC)", "The cutoff must not be after it.", { type: "datetime-local" })}
          {text("quarantineMaxPct", "Quarantine limit (%)", "The load stops above this share. Default 1.", { inputMode: "decimal" }, true)}
          {text(
            "openItemsRunId",
            "Open items run",
            "An open_items run of this company. Its open items per control account must equal the GL balance of that account.",
            { spellCheck: false },
            true,
          )}
          <div className={styles.wide}>
            <FormField id="gl-accounts" label="Chart of accounts" hint="Main accounts in Passage, separated by commas, spaces or lines." error={errors.accounts}>
              {(control) => (
                <Textarea {...control} mono rows={3} value={values.accounts} invalid={Boolean(errors.accounts)} onChange={(event) => update({ accounts: event.target.value })} />
              )}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="gl-balances"
              label="Balances"
              hint="One per line: legacyId, account, dimensions, currency, balance. Debit positive, credit negative; dimensions may be blank and use - or | between values."
              error={errors.balances}
            >
              {(control) => (
                <Textarea {...control} mono rows={10} value={values.balances} invalid={Boolean(errors.balances)} onChange={(event) => update({ balances: event.target.value })} />
              )}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not load the balances" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run gl_balances
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration-factory">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
