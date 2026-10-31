import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  SPLIT_ALLOCATIONS,
  describeProblem,
  listProductionParameters,
  saveProductionParameters,
  type Problem,
  type ProductionParameters,
  type ProductionParametersInput,
  type SplitAllocation,
} from "./api/client";
import { splitAllocationLabel, yesNo } from "./labels";
import styles from "./pages.module.css";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const percentPattern = /^\d{1,4}(\.\d{1,2})?$/;

/**
 * The form's starting values until the service answers with its defaults: PRD 4.21.13, except trigger points enabled,
 * which is on so that a site keeps evaluating the trigger points it defines (see NOT-IMPLEMENTED.md).
 */
export const blankParameters = (): ProductionParametersInput => ({
  siteId: "All",
  rafTolerancePct: "0",
  overConsumptionPct: "10",
  autoStartOnFeedback: true,
  estimateOnCreate: false,
  splitAllocation: "proportional",
  triggerPointsEnabled: true,
  asBuiltRequired: false,
});

/** Validates the parameters like the service: the site (UUID or All) and the two percentage ranges. */
export function validateParameters(p: ProductionParametersInput): Record<string, string> {
  const errors: Record<string, string> = {};
  if (p.siteId !== "All" && !uuidPattern.test(p.siteId)) {
    errors.siteId = "Enter the site id (a UUID) or All.";
  }
  if (!percentPattern.test(p.rafTolerancePct) || Number(p.rafTolerancePct) > 100) {
    errors.rafTolerancePct = "Enter a percentage from 0 to 100, with up to 2 decimals.";
  }
  if (!percentPattern.test(p.overConsumptionPct) || Number(p.overConsumptionPct) > 1000) {
    errors.overConsumptionPct = "Enter a percentage from 0 to 1000, with up to 2 decimals.";
  }
  if (!SPLIT_ALLOCATIONS.includes(p.splitAllocation)) {
    errors.splitAllocation = "Choose proportional or keep on the original.";
  }
  return errors;
}

const columns: DataTableColumn<ProductionParameters>[] = [
  { key: "siteId", header: "Site", mono: true },
  { key: "rafTolerancePct", header: "RAF tolerance %", align: "end" },
  { key: "overConsumptionPct", header: "Over-consumption %", align: "end" },
  { key: "autoStartOnFeedback", header: "Auto-start", render: (row) => yesNo(row.autoStartOnFeedback) },
  { key: "estimateOnCreate", header: "Estimate on create", render: (row) => yesNo(row.estimateOnCreate) },
  { key: "splitAllocation", header: "Split allocation", render: (row) => (row.splitAllocation === "keep-on-original" ? "Keep on original" : "Proportional") },
  { key: "triggerPointsEnabled", header: "Trigger points", render: (row) => yesNo(row.triggerPointsEnabled) },
  { key: "asBuiltRequired", header: "As-built required", render: (row) => yesNo(row.asBuiltRequired) },
  { key: "version", header: "Version", align: "end" },
];

type Flag = "autoStartOnFeedback" | "estimateOnCreate" | "triggerPointsEnabled" | "asBuiltRequired";

/**
 * Production parameters (PRD 4.21.13): per site, or for All sites, the tolerances on report as finished and picking,
 * auto-start on feedback, estimate on create, the split allocation default, trigger-point evaluation and the as-built
 * requirement at End. A site without parameters runs on the service defaults.
 */
export function ParametersPage() {
  const [state, setState] = useState<{ rows: ProductionParameters[]; defaults: ProductionParametersInput | null; loading: boolean; error: Problem | null }>({
    rows: [],
    defaults: null,
    loading: true,
    error: null,
  });
  const [form, setForm] = useState<ProductionParametersInput>(blankParameters);
  const [editing, setEditing] = useState<ProductionParameters | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback((signal?: AbortSignal) => {
    return listProductionParameters(signal).then(
      ({ items, defaults }) => setState({ rows: items, defaults, loading: false, error: null }),
      (err) => !signal?.aborted && setState({ rows: [], defaults: null, loading: false, error: describeProblem(err) }),
    );
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const edit = (row: ProductionParameters) => {
    setEditing(row);
    setForm({
      siteId: row.siteId,
      rafTolerancePct: row.rafTolerancePct,
      overConsumptionPct: row.overConsumptionPct,
      autoStartOnFeedback: row.autoStartOnFeedback,
      estimateOnCreate: row.estimateOnCreate,
      splitAllocation: row.splitAllocation,
      triggerPointsEnabled: row.triggerPointsEnabled,
      asBuiltRequired: row.asBuiltRequired,
    });
    setErrors({});
    setFailure(null);
    setNotice("");
  };

  const startNew = () => {
    setEditing(null);
    setForm({ ...blankParameters(), ...(state.defaults ?? {}), siteId: "All" });
    setErrors({});
    setFailure(null);
    setNotice("");
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateParameters(form);
    if (!editing && !found.siteId && state.rows.some((row) => row.siteId === form.siteId)) {
      found.siteId = "Parameters for this site are already saved: open the row in the table to change them.";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) {
      return;
    }
    setSaving(true);
    setFailure(null);
    setNotice("");
    try {
      const saved = await saveProductionParameters(form, editing?.version);
      setNotice(`Production parameters for ${saved.siteId} saved (version ${saved.version}).`);
      setEditing(saved);
      await load();
    } catch (err) {
      const problem = describeProblem(err);
      setFailure(problem);
      setErrors(problem.errors ?? {});
    } finally {
      setSaving(false);
    }
  };

  const percent = (key: "rafTolerancePct" | "overConsumptionPct", label: string, hint: string) => (
    <FormField key={key} id={`prm-${key}`} label={label} hint={hint} error={errors[key]}>
      {(control) => <Input {...control} inputMode="decimal" value={form[key]} onChange={(e) => setForm({ ...form, [key]: e.target.value })} />}
    </FormField>
  );

  const flag = (key: Flag, label: string, hint: string) => (
    <FormField key={key} id={`prm-${key}`} label={label} hint={hint} error={errors[key]}>
      {(control) => (
        <Select {...control} value={form[key] ? "yes" : "no"} onChange={(e) => setForm({ ...form, [key]: e.target.value === "yes" })}>
          <option value="yes">Yes</option>
          <option value="no">No</option>
        </Select>
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Production parameters"
        description="Tolerances, auto-start, estimate on create, split allocation, trigger points and the as-built requirement, per site. A site without parameters uses the service defaults."
        actions={
          <Button variant="secondary" onClick={startNew}>
            New parameters
          </Button>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="Could not load the parameters" live="alert">
          {state.error.status === 403 ? "You need the View production setup duty to see the parameters." : `${state.error.message} (${state.error.code})`}
        </Alert>
      ) : (
        <DataTable
          label="Production parameters"
          columns={columns}
          rows={state.rows}
          loading={state.loading}
          getRowKey={(row) => row.siteId}
          onRowOpen={edit}
          empty={{ title: "No parameters yet", description: "Every site runs on the service defaults until parameters are saved for it or for All sites." }}
        />
      )}
      <Card title={editing ? `Edit ${editing.siteId}` : "New parameters"}>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Production parameters">
          {failure ? (
            <Alert tone="danger" live="alert" title="The parameters were not saved">
              {failure.message} ({failure.code})
            </Alert>
          ) : null}
          <div className={styles.fields}>
            <FormField id="prm-siteId" label="Site" hint="A site id, or All. A site's own row wins over All." error={errors.siteId}>
              {(control) => <Input {...control} value={form.siteId} readOnly={editing !== null} onChange={(e) => setForm({ ...form, siteId: e.target.value })} />}
            </FormField>
            {percent("rafTolerancePct", "RAF tolerance %", "Over-delivery allowed on report as finished, 0 to 100.")}
            {percent("overConsumptionPct", "Over-consumption tolerance %", "Picking beyond the requirement without the over-consumption duty, 0 to 1000.")}
            {flag("autoStartOnFeedback", "Auto-start on feedback", "A route card on a Released order starts it.")}
            {flag("estimateOnCreate", "Estimate on create", "New orders are created Estimated.")}
            <FormField id="prm-splitAllocation" label="Split allocation default" hint="Used when a split names no allocation." error={errors.splitAllocation}>
              {(control) => (
                <Select {...control} value={form.splitAllocation} onChange={(e) => setForm({ ...form, splitAllocation: e.target.value as SplitAllocation })}>
                  {SPLIT_ALLOCATIONS.map((a) => (
                    <option key={a} value={a}>
                      {splitAllocationLabel[a]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {flag("triggerPointsEnabled", "Trigger points enabled", "Route-card feedback evaluates the order's trigger points.")}
            {flag("asBuiltRequired", "As-built required", "End is refused for an order that reported output without an as-built record.")}
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={saving}>
              Save parameters
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
