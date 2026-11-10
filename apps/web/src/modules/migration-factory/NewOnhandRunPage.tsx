import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, newKey, startOnhandRun, type OnhandRunRequest, type Problem } from "./api/client";
import { localToUtc, parseOnhandLines, SOURCE_LABELS, SOURCE_SYSTEMS, splitList, type SourceSystem } from "./domain";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface OnhandRunValues {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoff: string;
  freeze: string;
  quarantineMaxPct: string;
  items: string;
  glRunId: string;
  inventoryAccount: string;
  lines: string;
}

type Field = keyof OnhandRunValues;
type Errors = Partial<Record<Field, string>>;

const PCT = /^\d{1,2}(\.\d{1,4})?$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** The checks the service makes before it loads a onhand extract, and the request the form sends. */
export function validateOnhandRun(values: OnhandRunValues): { errors: Errors; input?: OnhandRunRequest } {
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
  const items = splitList(values.items);
  if (items.length === 0) errors.items = "List the item master the lines are checked against.";
  const glRunId = values.glRunId.trim();
  const inventoryAccount = values.inventoryAccount.trim();
  if (glRunId !== "" && !UUID.test(glRunId)) errors.glRunId = "Enter the id of a gl_balances run, or leave it blank.";
  if (glRunId === "" && inventoryAccount !== "") errors.glRunId = "Name the gl_balances run that holds the inventory account.";
  if (glRunId !== "" && inventoryAccount === "") errors.inventoryAccount = "Enter the inventory GL account to compare.";
  const parsed = parseOnhandLines(values.lines);
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
      items,
      ...(glRunId !== "" ? { glRunId, inventoryAccount } : {}),
      records: parsed.records,
    },
  };
}

/** Load legacy on-hand stock as opening stock at legacy cost from a pasted onhand extract (FR-49.1, FR-49.2, BR-49.3). */
export function NewOnhandRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<OnhandRunValues>({
    sourceSystem: "AX2012",
    sourceCompany: "",
    cutoff: "",
    freeze: "",
    quarantineMaxPct: "",
    items: "",
    glRunId: "",
    inventoryAccount: "",
    lines: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<OnhandRunValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateOnhandRun(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const run = await startOnhandRun(input, key.current);
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
    <FormField id={`oh-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<OnhandRunValues>)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Load on-hand stock"
        description="Legacy stock per item, site, warehouse, location, batch and serial at the cutoff, with its value at legacy cost, loaded as opening stock and reconciled to the extract. Nothing posts to the inventory ledger here."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="oh-sourceSystem" label="Source system">
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
          {text(
            "glRunId",
            "GL balances run",
            "A gl_balances run of this company. Its inventory account must equal the loaded stock value per currency.",
            { spellCheck: false },
            true,
          )}
          {text("inventoryAccount", "Inventory GL account", "Required with a GL balances run.", { spellCheck: false }, true)}
          <div className={styles.wide}>
            <FormField id="oh-items" label="Item master" hint="Item numbers in Passage, separated by commas, spaces or lines." error={errors.items}>
              {(control) => (
                <Textarea {...control} mono rows={3} value={values.items} invalid={Boolean(errors.items)} onChange={(event) => update({ items: event.target.value })} />
              )}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="oh-lines"
              label="On-hand lines"
              hint="One per line: legacyId, item, site, warehouse, location, batch, serial, unit, currency, quantity, value. Location, batch and serial may be blank. Value is the total at legacy cost; a serial holds quantity 1."
              error={errors.lines}
            >
              {(control) => (
                <Textarea {...control} mono rows={10} value={values.lines} invalid={Boolean(errors.lines)} onChange={(event) => update({ lines: event.target.value })} />
              )}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not load the stock" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run onhand
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
