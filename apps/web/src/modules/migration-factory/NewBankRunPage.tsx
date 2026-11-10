import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, newKey, startBankRun, type BankRunRequest, type Problem } from "./api/client";
import { localToUtc, parseBankBalances, SOURCE_LABELS, SOURCE_SYSTEMS, splitList, type SourceSystem } from "./domain";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface BankRunValues {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoff: string;
  freeze: string;
  quarantineMaxPct: string;
  bankAccounts: string;
  lines: string;
}

type Field = keyof BankRunValues;
type Errors = Partial<Record<Field, string>>;

const PCT = /^\d{1,2}(\.\d{1,4})?$/;

/** The checks the service makes before it loads a bank_balances extract, and the request the form sends. */
export function validateBankRun(values: BankRunValues): { errors: Errors; input?: BankRunRequest } {
  const errors: Errors = {};
  const company = values.sourceCompany.trim();
  if (company === "" || company.length > 40) errors.sourceCompany = "Enter the legacy company (DataAreaId or company code).";
  const cutoffTs = localToUtc(values.cutoff);
  const freezeTs = localToUtc(values.freeze);
  if (!cutoffTs) errors.cutoff = "Enter the extract cutoff.";
  if (!freezeTs) errors.freeze = "Enter the legacy transaction freeze.";
  if (cutoffTs && freezeTs && cutoffTs > freezeTs) errors.cutoff = "The cutoff can't be after the legacy freeze (MIG-1501).";
  const pct = values.quarantineMaxPct.trim();
  if (pct !== "" && (!PCT.test(pct) || Number(pct) > 10)) errors.quarantineMaxPct = "Enter a percentage from 0 to 10.";
  const bankAccounts = splitList(values.bankAccounts);
  if (bankAccounts.length === 0) errors.bankAccounts = "List the bank account master the lines are checked against.";
  const parsed = parseBankBalances(values.lines);
  if (parsed.errors.length > 0) errors.lines = parsed.errors.slice(0, 3).join(" ");
  else if (parsed.records.length === 0) errors.lines = "Paste at least one line.";
  if (Object.keys(errors).length > 0 || !cutoffTs || !freezeTs) return { errors };
  return {
    errors,
    input: {
      sourceSystem: values.sourceSystem,
      sourceCompany: company,
      cutoffTs,
      freezeTs,
      ...(pct !== "" ? { quarantineMaxPct: pct } : {}),
      bankAccounts,
      records: parsed.records,
    },
  };
}

/** Load legacy bank account balances as opening balances from a pasted extract (FR-49.1, FR-49.2). */
export function NewBankRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<BankRunValues>({
    sourceSystem: "AX2012",
    sourceCompany: "",
    cutoff: "",
    freeze: "",
    quarantineMaxPct: "",
    bankAccounts: "",
    lines: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<BankRunValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateBankRun(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const run = await startBankRun(input, key.current);
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
    <FormField id={`bk-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<BankRunValues>)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Load bank balances"
        description="Legacy book balance per bank account at the cutoff, loaded as opening balances and reconciled to the extract. Nothing posts to treasury here."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="bk-sourceSystem" label="Source system">
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
          {text("sourceCompany", "Legacy company", "DataAreaId in AX, company code in SAP.", { maxLength: 40 })}
          {text("cutoff", "Extract cutoff (UTC)", undefined, { type: "datetime-local" })}
          {text("freeze", "Legacy freeze (UTC)", "The cutoff must not be after it.", { type: "datetime-local" })}
          {text("quarantineMaxPct", "Quarantine limit (%)", "The load stops above this share. Default 1.", { inputMode: "decimal" }, true)}
          <div className={styles.wide}>
            <FormField id="bk-bankAccounts" label="Bank account master" hint="Bank account ids in Passage, separated by commas, spaces or lines." error={errors.bankAccounts}>
              {(control) => (
                <Textarea {...control} mono rows={3} value={values.bankAccounts} invalid={Boolean(errors.bankAccounts)} onChange={(event) => update({ bankAccounts: event.target.value })} />
              )}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="bk-lines"
              label="Bank balances"
              hint="One per line: legacyId, bankAccount, currency, balance. The balance is signed; an overdrawn account is negative."
              error={errors.lines}
            >
              {(control) => (
                <Textarea {...control} mono rows={10} value={values.lines} invalid={Boolean(errors.lines)} onChange={(event) => update({ lines: event.target.value })} />
              )}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not load the balances" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run bank_balances
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
