import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, newKey, startPackRun, type PackRunRequest, type Problem } from "./api/client";
import { localToUtc, parseCustomerMap, parseRecords, SOURCE_LABELS, SOURCE_SYSTEMS, splitList, TARGET_COLUMNS, type SourceSystem } from "./domain";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

export interface PackRunValues {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  cutoff: string;
  freeze: string;
  quarantineMaxPct: string;
  parties: string;
  records: string;
  /** Stage the accepted records for loading into receivables. */
  loadIntoReceivables: boolean;
  legalEntityId: string;
  importSource: string;
  /** "legacyParty=customer UUID" entries. */
  customers: string;
}

type Field = Exclude<keyof PackRunValues, "loadIntoReceivables">;
type Errors = Partial<Record<Field, string>>;

const PCT = /^\d{1,2}(\.\d{1,4})?$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const IMPORT_SOURCE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$/;

/** The checks the service makes before it loads, and the request the form sends. */
export function validatePackRun(values: PackRunValues): { errors: Errors; input?: PackRunRequest } {
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
  const parties = splitList(values.parties);
  if (parties.length === 0) errors.parties = "List the vendor and customer ids in master data.";
  const parsed = parseRecords(values.records, { forTarget: values.loadIntoReceivables });
  if (parsed.errors.length > 0) errors.records = parsed.errors.slice(0, 3).join(" ");
  else if (parsed.records.length === 0) errors.records = "Paste at least one open item.";
  const legalEntityId = values.legalEntityId.trim();
  const importSource = values.importSource.trim();
  const customers = parseCustomerMap(values.customers);
  if (values.loadIntoReceivables) {
    if (!UUID.test(legalEntityId)) errors.legalEntityId = "Choose the legal entity the open items belong to.";
    if (importSource !== "" && !IMPORT_SOURCE.test(importSource)) errors.importSource = "Use up to 40 letters, digits, dots, dashes or underscores.";
    if (customers.errors.length > 0) errors.customers = customers.errors.slice(0, 2).join(" ");
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
      parties,
      records: parsed.records,
      ...(values.loadIntoReceivables
        ? {
            load: {
              target: "receivables" as const,
              legalEntityId: legalEntityId.toLowerCase(),
              ...(importSource !== "" ? { importSource } : {}),
              ...(Object.keys(customers.map).length > 0 ? { customerIds: customers.map } : {}),
            },
          }
        : {}),
    },
  };
}

/** Start an open_items pack run from a pasted extract (FR-49.1, FR-49.11). */
export function NewPackRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<PackRunValues>({
    sourceSystem: "AX2012",
    sourceCompany: "",
    cutoff: "",
    freeze: "",
    quarantineMaxPct: "",
    parties: "",
    records: "",
    loadIntoReceivables: false,
    legalEntityId: "",
    importSource: "",
    customers: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PackRunValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePackRun(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const run = await startPackRun("open_items", input, key.current);
      navigate(`/migration-factory/runs/${run.runId}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "MIG-0007") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const target = values.loadIntoReceivables;
  const text = (id: Field, label: string, hint?: string, extra?: Record<string, unknown>, optional?: boolean) => (
    <FormField id={`run-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<PackRunValues>)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Start pack run"
        description="Open AP and AR items from the legacy extract. Records that fail a rule are quarantined; the rest load once by their legacy id."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="run-sourceSystem" label="Source system">
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
            <FormField id="run-parties" label="Parties in master data" hint="Vendor and customer ids, separated by commas or new lines." error={errors.parties}>
              {(control) => (
                <Textarea {...control} mono rows={3} value={values.parties} invalid={Boolean(errors.parties)} onChange={(event) => update({ parties: event.target.value })} />
              )}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="run-records"
              label="Open items"
              hint={target ? `One per line: ${TARGET_COLUMNS}. A negative amount is a credit.` : "One per line: legacyId, party, account, currency, amount."}
              error={errors.records}
            >
              {(control) => (
                <Textarea {...control} mono rows={10} value={values.records} invalid={Boolean(errors.records)} onChange={(event) => update({ records: event.target.value })} />
              )}
            </FormField>
          </div>
          <div className={styles.wide}>
            <div className={styles.check}>
              <Input id="run-load" type="checkbox" checked={target} onChange={(event) => update({ loadIntoReceivables: event.target.checked })} />
              <label htmlFor="run-load">Load the accepted open items into receivables (open AR). Nothing is sent until you press Load on the run.</label>
            </div>
          </div>
          {target ? (
            <>
              <LookupFormField
                id="run-legalEntityId"
                label="Legal entity"
                entity="legalEntity"
                hint="The Passage legal entity the open items belong to. You must be scoped to it."
                value={values.legalEntityId}
                error={errors.legalEntityId}
                onChange={(legalEntityId) => update({ legalEntityId })}
              />
              {text("importSource", "Import source", "How receivables keys the items with the legacy id. Default: the system and company, such as ax2012.usmf.", { maxLength: 40 }, true)}
              <div className={styles.wide}>
                <FormField id="run-customers" label="Customers" hint="One per line: legacyParty=Passage customer id. A party that is already a UUID needs no line." error={errors.customers} optional>
                  {(control) => (
                    <Textarea {...control} mono rows={3} value={values.customers} invalid={Boolean(errors.customers)} onChange={(event) => update({ customers: event.target.value })} />
                  )}
                </FormField>
              </div>
            </>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not run the pack" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run open_items
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
