import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, newKey, startProjectRun, type Problem, type ProjectRunRequest } from "./api/client";
import { localToUtc, parseProjectExtract, SOURCE_LABELS, SOURCE_SYSTEMS, type SourceSystem } from "./domain";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface ProjectRunValues {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoff: string;
  freeze: string;
  quarantineMaxPct: string;
  lines: string;
  totals: string;
}

type Field = keyof ProjectRunValues;
type Errors = Partial<Record<Field, string>>;

const PCT = /^\d{1,2}(\.\d{1,4})?$/;

/** The checks the service makes before it loads a project_wip extract, and the request the form sends. */
export function validateProjectRun(values: ProjectRunValues): { errors: Errors; input?: ProjectRunRequest } {
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
  const parsed = parseProjectExtract(values.lines, values.totals);
  if (parsed.lineErrors.length > 0) errors.lines = parsed.lineErrors.slice(0, 3).join(" ");
  else if (!parsed.projects.some((p) => p.lines.length > 0)) errors.lines = "Paste at least one opening line.";
  if (parsed.totalErrors.length > 0) errors.totals = parsed.totalErrors.slice(0, 3).join(" ");
  if (Object.keys(errors).length > 0 || !cutoffTs || !freezeTs) return { errors };
  return {
    errors,
    input: {
      sourceSystem: values.sourceSystem,
      sourceCompany: company,
      cutoffTs,
      freezeTs,
      ...(pct !== "" ? { quarantineMaxPct: pct } : {}),
      projects: parsed.projects,
    },
  };
}

/** Load project beginning balances from a pasted project_wip extract (FR-49.12). */
export function NewProjectRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ProjectRunValues>({
    sourceSystem: "AX2012",
    sourceCompany: "",
    cutoff: "",
    freeze: "",
    quarantineMaxPct: "",
    lines: "",
    totals: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ProjectRunValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateProjectRun(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const run = await startProjectRun(input, key.current);
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
    <FormField id={`project-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<ProjectRunValues>)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Load project beginning balances"
        description="Opening project transactions by category with WIP, reconciled per project to the legacy project ledger. Opening lines never post to the GL."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="project-sourceSystem" label="Source system">
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
          <div className={styles.wide}>
            <FormField
              id="project-lines"
              label="Opening lines"
              hint="One per line: projectId, legacyId, category, transactionDate, qty, costAmount, salesAmount, currency, billingStatus, wipCost, wipSales."
              error={errors.lines}
            >
              {(control) => (
                <Textarea {...control} mono rows={10} value={values.lines} invalid={Boolean(errors.lines)} onChange={(event) => update({ lines: event.target.value })} />
              )}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="project-totals"
              label="Legacy totals"
              hint="From ProjTrans* or COSP/COSS and RA results, one per project and category: projectId, category, cost, sales, wipCost, wipSales."
              error={errors.totals}
            >
              {(control) => (
                <Textarea {...control} mono rows={5} value={values.totals} invalid={Boolean(errors.totals)} onChange={(event) => update({ totals: event.target.value })} />
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
              Run project_wip
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
