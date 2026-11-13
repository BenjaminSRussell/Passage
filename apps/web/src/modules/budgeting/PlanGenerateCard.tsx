import { useEffect, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Button, Card, FormField, Input, Select } from "../../shell/ui";
import { listProjectForecasts, type ProjectForecastModel } from "./api/plans";
import {
  emptyDepartment,
  emptyProject,
  emptyRate,
  emptyUnitPrice,
  GENERATE_SOURCES,
  isDriverSource,
  usesWeights,
  type GenerateForm,
  type ProjectRow,
} from "./planLogic";
import styles from "./pages.module.css";

interface Props {
  form: GenerateForm;
  onChange(next: GenerateForm): void;
  errors: Record<string, string>;
  busy: boolean;
  legalEntityId: string;
  onSubmit(event: FormEvent): void;
}

const HINTS: Record<GenerateForm["source"], string> = {
  "prior-plan": "Copies last year's approved scenario into this year's months.",
  "gl-actuals": "Reads what each account posted last year from the ledger.",
  "fixed-assets": "Reads the depreciation schedule of every open fixed asset of the legal entity for the plan year.",
  positions: "Reads the HR positions open in each month and costs each one at the monthly cost of its job, times its FTE.",
  projects: "Reads the cost or sales lines of a forecast of each project.",
  demand: "Reads the quantities of a planning forecast and values them at the unit price of each item.",
};

/**
 * The form that fills a Draft scenario from another source (FR-38.14). The driver sources name records of other services
 * (departments, projects, items), so those are picked by name; the accounts and cost centers are the plan's own codes.
 */
export function PlanGenerateCard({ form, onChange, errors, busy, legalEntityId, onSubmit }: Props) {
  const set = (patch: Partial<GenerateForm>) => onChange({ ...form, ...patch });
  const scope = legalEntityId ? { legalEntityId } : undefined;
  const driver = isDriverSource(form.source);
  return (
    <Card title="Fill the grid from a source" headingLevel={3}>
      <form className={styles.form} onSubmit={onSubmit} noValidate aria-label="Fill the grid from a source">
        <FormField id="gn-source" label="Start from" hint={HINTS[form.source]}>
          {(c) => (
            <Select {...c} value={form.source} onChange={(e) => set({ source: e.target.value as GenerateForm["source"] })}>
              {GENERATE_SOURCES.map((s) => (
                <option key={s.value} value={s.value}>
                  {s.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="gn-adjust" label="Change every amount by percent" optional hint="5 adds five percent, -10 takes ten off." error={errors.adjustment}>
          {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={form.adjustment} onChange={(e) => set({ adjustment: e.target.value })} invalid={Boolean(errors.adjustment)} />}
        </FormField>
        {driver ? (
          <FormField id="gn-account" label="Plan on account" hint="Every amount of this source is planned on this account." error={errors.account}>
            {(c) => <Input {...c} className={styles.mono} value={form.account} onChange={(e) => set({ account: e.target.value })} invalid={Boolean(errors.account)} />}
          </FormField>
        ) : (
          <FormField
            id="gn-accounts"
            label="Accounts"
            optional={form.source === "prior-plan"}
            hint={form.source === "prior-plan" ? "Leave empty for every account of last year's plan. Separate accounts with commas." : "The accounts to read from the ledger, separated by commas."}
            error={errors.accounts}
          >
            {(c) => <Input {...c} className={styles.mono} value={form.accounts} onChange={(e) => set({ accounts: e.target.value })} invalid={Boolean(errors.accounts)} />}
          </FormField>
        )}
        {form.source === "fixed-assets" ? (
          <FormField id="gn-book" label="Depreciation book" optional hint="Leave empty for the first open book of each asset." error={errors.book}>
            {(c) => <Input {...c} className={styles.mono} value={form.book} onChange={(e) => set({ book: e.target.value })} invalid={Boolean(errors.book)} />}
          </FormField>
        ) : null}
        {form.source === "demand" ? (
          <FormField id="gn-model" label="Planning forecast" hint="The code of the demand forecast in planning, such as FC-2027." error={errors.modelCode}>
            {(c) => <Input {...c} className={styles.mono} value={form.modelCode} onChange={(e) => set({ modelCode: e.target.value })} invalid={Boolean(errors.modelCode)} />}
          </FormField>
        ) : null}
        {usesWeights(form.source) ? (
          <FormField
            id="gn-centers"
            label="Cost centers and their weights"
            hint={form.source === "gl-actuals" ? "The ledger holds each account as one figure, so it is shared over these cost centers. Write CC1=2, CC2=1." : "Each month's amount is shared over these cost centers. Write CC1=2, CC2=1."}
            error={errors.costCenters}
          >
            {(c) => <Input {...c} className={styles.mono} value={form.costCenters} onChange={(e) => set({ costCenters: e.target.value })} invalid={Boolean(errors.costCenters)} />}
          </FormField>
        ) : null}

        {form.source === "positions" ? (
          <>
            <fieldset className={styles.lineSet}>
              <legend className={styles.legend}>Monthly cost of a full-time position, per job{errors.rates ? ` — ${errors.rates}` : ""}</legend>
              {form.rates.map((r, i) => (
                <div key={i} className={styles.lineRow} role="group" aria-label={`Job ${i + 1}`}>
                  <FormField id={`gn-rate-${i}-job`} label="Job code" error={errors[`rates.${i}.jobCode`]}>
                    {(c) => <Input {...c} className={styles.mono} value={r.jobCode} onChange={(e) => set({ rates: form.rates.map((x, j) => (j === i ? { ...x, jobCode: e.target.value } : x)) })} invalid={Boolean(errors[`rates.${i}.jobCode`])} />}
                  </FormField>
                  <FormField id={`gn-rate-${i}-cost`} label="Monthly cost" error={errors[`rates.${i}.monthlyCost`]}>
                    {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={r.monthlyCost} onChange={(e) => set({ rates: form.rates.map((x, j) => (j === i ? { ...x, monthlyCost: e.target.value } : x)) })} invalid={Boolean(errors[`rates.${i}.monthlyCost`])} />}
                  </FormField>
                  {form.rates.length > 1 ? (
                    <Button type="button" variant="secondary" size="sm" onClick={() => set({ rates: form.rates.filter((_, j) => j !== i) })}>
                      Remove job {i + 1}
                    </Button>
                  ) : null}
                </div>
              ))}
              <div>
                <Button type="button" variant="secondary" size="sm" onClick={() => set({ rates: [...form.rates, emptyRate()] })}>
                  Add a job
                </Button>
              </div>
            </fieldset>
            <fieldset className={styles.lineSet}>
              <legend className={styles.legend}>Cost center of each HR department{errors.departments ? ` — ${errors.departments}` : ""}</legend>
              {form.departments.map((d, i) => (
                <div key={i} className={styles.lineRow} role="group" aria-label={`Department ${i + 1}`}>
                  <FormField id={`gn-dept-${i}`} label="Department" error={errors[`departments.${i}.departmentId`]}>
                    {(c) => (
                      <EntityLookup
                        {...c}
                        entity="operatingUnit"
                        value={d.departmentId}
                        invalid={Boolean(errors[`departments.${i}.departmentId`])}
                        onChange={(e) => set({ departments: form.departments.map((x, j) => (j === i ? { ...x, departmentId: e.target.value } : x)) })}
                      />
                    )}
                  </FormField>
                  <FormField id={`gn-dept-${i}-cc`} label="Cost center" error={errors[`departments.${i}.costCenter`]}>
                    {(c) => <Input {...c} className={styles.mono} value={d.costCenter} onChange={(e) => set({ departments: form.departments.map((x, j) => (j === i ? { ...x, costCenter: e.target.value } : x)) })} invalid={Boolean(errors[`departments.${i}.costCenter`])} />}
                  </FormField>
                  {form.departments.length > 1 ? (
                    <Button type="button" variant="secondary" size="sm" onClick={() => set({ departments: form.departments.filter((_, j) => j !== i) })}>
                      Remove department {i + 1}
                    </Button>
                  ) : null}
                </div>
              ))}
              <div>
                <Button type="button" variant="secondary" size="sm" onClick={() => set({ departments: [...form.departments, emptyDepartment()] })}>
                  Add a department
                </Button>
              </div>
            </fieldset>
          </>
        ) : null}

        {form.source === "projects" ? (
          <fieldset className={styles.lineSet}>
            <legend className={styles.legend}>Projects and the forecast each is planned from{errors.projects ? ` — ${errors.projects}` : ""}</legend>
            {form.projects.map((p, i) => (
              <ProjectLine
                key={i}
                index={i}
                row={p}
                errors={errors}
                scope={scope}
                canRemove={form.projects.length > 1}
                onChange={(row) => set({ projects: form.projects.map((x, j) => (j === i ? row : x)) })}
                onRemove={() => set({ projects: form.projects.filter((_, j) => j !== i) })}
              />
            ))}
            <div>
              <Button type="button" variant="secondary" size="sm" onClick={() => set({ projects: [...form.projects, emptyProject()] })}>
                Add a project
              </Button>
            </div>
          </fieldset>
        ) : null}

        {form.source === "demand" ? (
          <fieldset className={styles.lineSet}>
            <legend className={styles.legend}>Price of one unit of each item{errors.unitPrices ? ` — ${errors.unitPrices}` : ""}</legend>
            {form.unitPrices.map((u, i) => (
              <div key={i} className={styles.lineRow} role="group" aria-label={`Item ${i + 1}`}>
                <FormField id={`gn-item-${i}`} label="Item" error={errors[`unitPrices.${i}.itemId`]}>
                  {(c) => (
                    <EntityLookup
                      {...c}
                      entity="product"
                      value={u.itemId}
                      invalid={Boolean(errors[`unitPrices.${i}.itemId`])}
                      onChange={(e) => set({ unitPrices: form.unitPrices.map((x, j) => (j === i ? { ...x, itemId: e.target.value } : x)) })}
                    />
                  )}
                </FormField>
                <FormField id={`gn-item-${i}-price`} label="Unit price" error={errors[`unitPrices.${i}.price`]}>
                  {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={u.price} onChange={(e) => set({ unitPrices: form.unitPrices.map((x, j) => (j === i ? { ...x, price: e.target.value } : x)) })} invalid={Boolean(errors[`unitPrices.${i}.price`])} />}
                </FormField>
                {form.unitPrices.length > 1 ? (
                  <Button type="button" variant="secondary" size="sm" onClick={() => set({ unitPrices: form.unitPrices.filter((_, j) => j !== i) })}>
                    Remove item {i + 1}
                  </Button>
                ) : null}
              </div>
            ))}
            <div>
              <Button type="button" variant="secondary" size="sm" onClick={() => set({ unitPrices: [...form.unitPrices, emptyUnitPrice()] })}>
                Add an item
              </Button>
            </div>
          </fieldset>
        ) : null}

        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Fill the grid
          </Button>
        </div>
        <p className={`${styles.note} ${styles.wide}`}>The amounts replace those of the same account, cost center and month. Save any grid edits first.</p>
      </form>
    </Card>
  );
}

/** One project row: the project picked by name, then one of its forecast models, read from the projects service. */
function ProjectLine({ index: i, row, errors, scope, canRemove, onChange, onRemove }: {
  index: number;
  row: ProjectRow;
  errors: Record<string, string>;
  scope?: { legalEntityId: string };
  canRemove: boolean;
  onChange(row: ProjectRow): void;
  onRemove(): void;
}) {
  const [models, setModels] = useState<ProjectForecastModel[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (row.projectId === "") {
      setModels(null);
      return;
    }
    const controller = new AbortController();
    setFailed(false);
    listProjectForecasts(row.projectId, controller.signal)
      .then((out) => setModels(out.items))
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setModels([]);
          setFailed(error instanceof Error);
        }
      });
    return () => controller.abort();
  }, [row.projectId]);
  const modelError = errors[`projects.${i}.modelId`] ?? (failed ? "The forecasts of this project could not be read." : models && models.length === 0 ? "This project has no forecast yet." : undefined);
  return (
    <div className={styles.lineRow} role="group" aria-label={`Project ${i + 1}`}>
      <FormField id={`gn-project-${i}`} label="Project" error={errors[`projects.${i}.projectId`]}>
        {(c) => <EntityLookup {...c} entity="project" scope={scope} value={row.projectId} invalid={Boolean(errors[`projects.${i}.projectId`])} onChange={(e) => onChange({ ...row, projectId: e.target.value, modelId: "" })} />}
      </FormField>
      <FormField id={`gn-project-${i}-model`} label="Forecast" error={modelError}>
        {(c) => (
          <Select {...c} value={row.modelId} disabled={!models || models.length === 0} onChange={(e) => onChange({ ...row, modelId: e.target.value })}>
            <option value="">{row.projectId === "" ? "Choose a project first" : "Choose a forecast"}</option>
            {(models ?? []).map((m) => (
              <option key={m.id} value={m.id}>
                {m.name} ({m.currency})
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id={`gn-project-${i}-amount`} label="Plan the">
        {(c) => (
          <Select {...c} value={row.amount} onChange={(e) => onChange({ ...row, amount: e.target.value as ProjectRow["amount"] })}>
            <option value="cost">Cost</option>
            <option value="sales">Sales</option>
          </Select>
        )}
      </FormField>
      <FormField id={`gn-project-${i}-cc`} label="Cost center" error={errors[`projects.${i}.costCenter`]}>
        {(c) => <Input {...c} className={styles.mono} value={row.costCenter} onChange={(e) => onChange({ ...row, costCenter: e.target.value })} invalid={Boolean(errors[`projects.${i}.costCenter`])} />}
      </FormField>
      {canRemove ? (
        <Button type="button" variant="secondary" size="sm" onClick={onRemove}>
          Remove project {i + 1}
        </Button>
      ) : null}
    </div>
  );
}
