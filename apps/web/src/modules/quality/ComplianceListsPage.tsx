import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { createComplianceList, describeProblem, listComplianceLists, newKey, type BlockMode, type ComplianceList, type LimitLine, type ListKind, type NewComplianceList, type Problem } from "./api/client";
import { CODE, DECIMAL, qty, REGION, validCAS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const KIND_LABELS: Record<ListKind, string> = { RESTRICTED: "Restricted", REPORTING: "Reporting" };

const columns: DataTableColumn<ComplianceList>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "version", header: "Version", align: "end", render: (row) => `v${row.version}` },
  { key: "name", header: "Name", render: (row) => row.name || "—" },
  { key: "kind", header: "Kind", render: (row) => <Badge tone={row.kind === "RESTRICTED" ? "danger" : "info"}>{KIND_LABELS[row.kind]}</Badge> },
  {
    key: "blockMode",
    header: "On a hit",
    render: (row) => (row.kind === "REPORTING" ? "Warn (QM-0121)" : row.blockMode === "BLOCK" ? "Block (QM-0122)" : "Warn (QM-0122)"),
  },
  { key: "regions", header: "Regions", render: (row) => row.regions.join(", ") },
  {
    key: "limits",
    header: "Substances",
    render: (row) => {
      const shown = row.limits.slice(0, 3).map((l) => `${l.name || l.cas} > ${qty(l.thresholdPct)} %`);
      return row.limits.length > 3 ? `${shown.join("; ")} and ${row.limits.length - 3} more` : shown.join("; ");
    },
  },
];

/** Regulated, restricted and reporting lists (FR-33.13); every version is kept, the highest is evaluated. */
export function ComplianceListsPage() {
  const state = useLoad((signal) => listComplianceLists(signal), "compliance-lists");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Compliance lists"
        description="Restricted-substance lists such as RoHS Annex II and reporting lists such as the REACH Candidate List, with a threshold per substance. Saving a list code again creates its next version; items are evaluated against the newest."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/quality/compliance/items">
              Evaluate an item
            </Link>
            <Link className={buttonClassName("primary")} to="/quality/compliance/lists/new">
              New list version
            </Link>
          </>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load compliance lists" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Compliance lists"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No compliance lists yet", description: "Add the lists that apply to the regions you sell into, then evaluate your items against them." }}
        />
      )}
    </div>
  );
}

interface LimitValues {
  cas: string;
  name: string;
  thresholdPct: string;
}

export interface ListValues {
  code: string;
  name: string;
  kind: ListKind;
  blockMode: BlockMode;
  regions: string;
  limits: LimitValues[];
}

const blankLimit: LimitValues = { cas: "", name: "", thresholdPct: "0.1" };

/** Checks that mirror the service's ValidateComplianceList, and the request the form sends. */
export function validateComplianceList(values: ListValues): { errors: Record<string, string>; input?: NewComplianceList } {
  const errors: Record<string, string> = {};
  const code = values.code.trim();
  if (!CODE.test(code)) errors.code = "Use 2 to 32 capital letters, digits, _ or -, such as REACH-SVHC.";
  if (values.name.trim().length > 120) errors.name = "Use at most 120 characters.";
  const regions = values.regions.split(/[\s,]+/).filter(Boolean);
  if (regions.length === 0 || regions.some((r) => !REGION.test(r)) || new Set(regions).size !== regions.length) {
    errors.regions = "Enter one or more region codes in capitals, such as EU, CN or US-CA, each once.";
  }
  if (values.limits.length === 0) errors.code = "Add at least one substance.";
  const seen = new Set<string>();
  const limits: LimitLine[] = [];
  values.limits.forEach((l, i) => {
    const cas = l.cas.trim();
    if (!validCAS(cas)) errors[`${i}.cas`] = "Enter a CAS number with a valid check digit, such as 7439-92-1.";
    else if (seen.has(cas)) errors[`${i}.cas`] = "This substance is already on the list.";
    seen.add(cas);
    const pct = l.thresholdPct.trim();
    if (!DECIMAL.test(pct) || Number(pct) <= 0 || Number(pct) > 100) errors[`${i}.thresholdPct`] = "Enter a percentage above 0 and at most 100.";
    if (l.name.trim().length > 120) errors[`${i}.name`] = "Use at most 120 characters.";
    limits.push(l.name.trim() ? { cas, name: l.name.trim(), thresholdPct: pct } : { cas, thresholdPct: pct });
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name: values.name.trim(), kind: values.kind, blockMode: values.blockMode, regions, limits } };
}

/** Create the next version of a compliance list with its substances and thresholds. */
export function NewComplianceListPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ListValues>({ code: "", name: "", kind: "RESTRICTED", blockMode: "BLOCK", regions: "", limits: [blankLimit] });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ListValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function updateLimit(i: number, patch: Partial<LimitValues>) {
    update({ limits: values.limits.map((l, j) => (j === i ? { ...l, ...patch } : l)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const checked = validateComplianceList(values);
    setErrors(checked.errors);
    setFailure(null);
    if (!checked.input) return;
    setBusy(true);
    try {
      await createComplianceList(checked.input, key.current);
      navigate("/quality/compliance/lists");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Record<string, string> = {};
      for (const name of ["code", "name", "regions", "kind", "blockMode"]) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      for (const [name, message] of Object.entries(problem.fields)) {
        const m = /^limits\[(\d+)\]\.(cas|thresholdPct|name)$/.exec(name);
        if (m) fieldErrors[`${m[1]}.${m[2]}`] = message;
      }
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New compliance list version"
        description="A substance is over the list when its rolled-up concentration is strictly above its threshold (% w/w). A restricted list blocks or warns on sales, shipments and receipts in its regions; a reporting list only warns and feeds the reporting extract."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New compliance list version">
          <FormField id="list-code" label="List code" hint="An existing code gets its next version." error={errors.code}>
            {(control) => <Input {...control} maxLength={32} value={values.code} invalid={Boolean(errors.code)} onChange={(event) => update({ code: event.target.value })} />}
          </FormField>
          <FormField id="list-name" label="Name" optional error={errors.name}>
            {(control) => <Input {...control} maxLength={120} value={values.name} invalid={Boolean(errors.name)} onChange={(event) => update({ name: event.target.value })} />}
          </FormField>
          <FormField id="list-kind" label="Kind">
            {(control) => (
              <Select {...control} value={values.kind} onChange={(event) => update({ kind: event.target.value as ListKind })}>
                <option value="RESTRICTED">Restricted (substance limits)</option>
                <option value="REPORTING">Reporting (communication duty)</option>
              </Select>
            )}
          </FormField>
          <FormField id="list-block" label="On a restricted hit" hint="compliance_block_mode">
            {(control) => (
              <Select {...control} value={values.blockMode} onChange={(event) => update({ blockMode: event.target.value as BlockMode })}>
                <option value="BLOCK">Block the line</option>
                <option value="WARN">Warn only</option>
              </Select>
            )}
          </FormField>
          <FormField id="list-regions" label="Regions" hint="Comma-separated, such as EU, CN." error={errors.regions}>
            {(control) => <Input {...control} value={values.regions} invalid={Boolean(errors.regions)} onChange={(event) => update({ regions: event.target.value })} />}
          </FormField>
          {values.limits.map((limit, i) => (
            <fieldset key={i} className={styles.valueGroup}>
              <legend className={styles.legend}>{`Substance ${i + 1}`}</legend>
              <FormField id={`limit-${i}-cas`} label="CAS number" error={errors[`${i}.cas`]}>
                {(control) => <Input {...control} className={styles.mono} value={limit.cas} invalid={Boolean(errors[`${i}.cas`])} onChange={(event) => updateLimit(i, { cas: event.target.value })} />}
              </FormField>
              <FormField id={`limit-${i}-name`} label="Substance name" optional error={errors[`${i}.name`]}>
                {(control) => <Input {...control} value={limit.name} invalid={Boolean(errors[`${i}.name`])} onChange={(event) => updateLimit(i, { name: event.target.value })} />}
              </FormField>
              <FormField id={`limit-${i}-threshold`} label="Threshold (% w/w)" error={errors[`${i}.thresholdPct`]}>
                {(control) => (
                  <Input {...control} inputMode="decimal" value={limit.thresholdPct} invalid={Boolean(errors[`${i}.thresholdPct`])} onChange={(event) => updateLimit(i, { thresholdPct: event.target.value })} />
                )}
              </FormField>
              {values.limits.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => update({ limits: values.limits.filter((_, j) => j !== i) })}>
                  {`Remove substance ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the list" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button onClick={() => update({ limits: [...values.limits, blankLimit] })}>Add substance</Button>
            <Button type="submit" variant="primary" loading={busy}>
              Save list version
            </Button>
            <Link className={buttonClassName("ghost")} to="/quality/compliance/lists">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
