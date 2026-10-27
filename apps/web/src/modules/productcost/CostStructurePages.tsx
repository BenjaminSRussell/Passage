import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  createCostComponent,
  createCostGroup,
  describeProblem,
  listCostComponents,
  listCostGroups,
  type CostBehaviour,
  type CostComponent,
  type CostComponentInput,
  type CostGroup,
  type CostGroupInput,
  type CostGroupType,
  type Problem,
} from "./api/client";
import { ProblemAlert } from "./shared";
import { structureCode, useIdempotencyKey, useLoad } from "./stored";
import styles from "./pages.module.css";

/** FR-39.1 limits per tenant. */
export const MAX_COMPONENTS = 20;
export const MAX_COST_GROUPS = 50;

export const groupTypeLabels: Record<CostGroupType, string> = {
  DirectMaterial: "Direct material",
  DirectManufacturing: "Direct manufacturing",
  Indirect: "Indirect",
  Undefined: "Undefined",
};

const groupTypes = Object.keys(groupTypeLabels) as CostGroupType[];

interface StructureValues {
  code: string;
  name: string;
  type: CostGroupType;
  behaviour: CostBehaviour;
}

/** Checks a code and name the way the service does (FR-39.1). */
export function validateStructure(values: StructureValues): Record<string, string> {
  const errors: Record<string, string> = {};
  const code = values.code.trim();
  if (code === "") errors.code = "Enter a code.";
  else if (!structureCode.test(code)) errors.code = "Use 1 to 20 upper-case letters, digits or hyphens, such as DM or IND-MAT.";
  const name = values.name.trim();
  if (name === "") errors.name = "Enter a name.";
  else if ([...name].length > 120) errors.name = "Use at most 120 characters.";
  return errors;
}

function TypeAndBehaviour({
  prefix,
  label,
  values,
  onChange,
}: {
  prefix: string;
  label: string;
  values: StructureValues;
  onChange(values: StructureValues): void;
}) {
  return (
    <>
      <FormField id={`${prefix}-type`} label={label}>
        {(control) => (
          <Select {...control} value={values.type} onChange={(e) => onChange({ ...values, type: e.target.value as CostGroupType })}>
            {groupTypes.map((t) => (
              <option key={t} value={t}>
                {groupTypeLabels[t]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id={`${prefix}-behaviour`} label="Behaviour" hint="Fixed costs do not change with the quantity produced.">
        {(control) => (
          <Select {...control} value={values.behaviour} onChange={(e) => onChange({ ...values, behaviour: e.target.value as CostBehaviour })}>
            <option value="Variable">Variable</option>
            <option value="Fixed">Fixed</option>
          </Select>
        )}
      </FormField>
    </>
  );
}

function CodeAndName({
  prefix,
  values,
  errors,
  onChange,
}: {
  prefix: string;
  values: StructureValues;
  errors: Record<string, string>;
  onChange(values: StructureValues): void;
}) {
  return (
    <>
      <FormField id={`${prefix}-code`} label="Code" error={errors.code}>
        {(control) => (
          <Input
            {...control}
            className={styles.mono}
            value={values.code}
            onChange={(e) => onChange({ ...values, code: e.target.value.toUpperCase() })}
            invalid={Boolean(errors.code)}
          />
        )}
      </FormField>
      <FormField id={`${prefix}-name`} label="Name" error={errors.name}>
        {(control) => (
          <Input {...control} value={values.name} onChange={(e) => onChange({ ...values, name: e.target.value })} invalid={Boolean(errors.name)} />
        )}
      </FormField>
    </>
  );
}

function LoadFailure({ what, problem }: { what: string; problem: Problem }) {
  return (
    <Alert tone="danger" title={`Could not load ${what}`} live="alert">
      {problem.message} ({problem.code})
    </Alert>
  );
}

const emptyComponent: StructureValues = { code: "", name: "", type: "DirectMaterial", behaviour: "Variable" };

const componentColumns: DataTableColumn<CostComponent>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "costGroupType", header: "Cost group type", render: (c) => groupTypeLabels[c.costGroupType] },
  { key: "behaviour", header: "Behaviour" },
];

/** The cost components of the split (FR-39.1): list and create, at most 20 per tenant. */
export function CostComponentsPage() {
  const list = useLoad((signal) => listCostComponents(signal));
  const [values, setValues] = useState<StructureValues>(emptyComponent);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [created, setCreated] = useState<string | null>(null);
  const idempotency = useIdempotencyKey();
  const rows = list.data?.items ?? [];
  const full = rows.length >= MAX_COMPONENTS;

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateStructure(values);
    setErrors(found);
    setFailure(null);
    setCreated(null);
    if (Object.keys(found).length > 0) return;
    const input: CostComponentInput = { code: values.code.trim(), name: values.name.trim(), costGroupType: values.type, behaviour: values.behaviour };
    setSaving(true);
    try {
      const c = await createCostComponent(input, idempotency.keyFor(input));
      idempotency.forget();
      setCreated(c.code);
      setValues(emptyComponent);
      list.reload();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost components"
        description="The columns of every cost component split, such as direct material, direct labor and the overheads. Cost groups map item costs, cost categories and costing sheet nodes to one of them."
        meta={list.data ? <Badge tone={full ? "warning" : "neutral"}>{`${rows.length} of ${MAX_COMPONENTS}`}</Badge> : undefined}
      />
      <Card title="New cost component" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="New cost component">
          <CodeAndName prefix="component" values={values} errors={errors} onChange={setValues} />
          <TypeAndBehaviour prefix="component" label="Cost group type" values={values} onChange={setValues} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={saving} disabled={full}>
              Add component
            </Button>
            {full ? <p className={styles.note}>A tenant holds at most {MAX_COMPONENTS} cost components.</p> : null}
          </div>
        </form>
      </Card>
      {created ? <Alert tone="success" live="status" title={`Component ${created} added`} /> : null}
      {failure ? <ProblemAlert title="The component was not added" problem={failure} /> : null}
      {list.error ? <LoadFailure what="cost components" problem={list.error} /> : null}
      <DataTable
        label="Cost components"
        columns={componentColumns}
        rows={rows}
        getRowKey={(c) => c.id}
        loading={list.loading && !list.data}
        empty={{ title: "No cost components yet", description: "Add the components of your split, for example DM, DL, IND-MAT, IND-LAB and SUB." }}
      />
    </div>
  );
}

interface GroupValues extends StructureValues {
  componentId: string;
}

const emptyGroup: GroupValues = { code: "", name: "", type: "DirectManufacturing", behaviour: "Variable", componentId: "" };

const groupColumns: DataTableColumn<CostGroup>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "type", header: "Type", render: (g) => groupTypeLabels[g.type] },
  { key: "behaviour", header: "Behaviour" },
  { key: "componentCode", header: "Cost component", mono: true },
];

/** Cost groups mapped to cost components (FR-39.1): list and create, at most 50 per tenant. */
export function CostGroupsPage() {
  const groups = useLoad((signal) => listCostGroups(signal));
  const components = useLoad((signal) => listCostComponents(signal));
  const [values, setValues] = useState<GroupValues>(emptyGroup);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [created, setCreated] = useState<string | null>(null);
  const idempotency = useIdempotencyKey();
  const rows = groups.data?.items ?? [];
  const options = components.data?.items ?? [];
  const full = rows.length >= MAX_COST_GROUPS;

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateStructure(values);
    if (values.componentId === "") found.componentId = "Choose the cost component the group maps to.";
    setErrors(found);
    setFailure(null);
    setCreated(null);
    if (Object.keys(found).length > 0) return;
    const input: CostGroupInput = {
      code: values.code.trim(),
      name: values.name.trim(),
      type: values.type,
      behaviour: values.behaviour,
      componentId: values.componentId,
    };
    setSaving(true);
    try {
      const g = await createCostGroup(input, idempotency.keyFor(input));
      idempotency.forget();
      setCreated(g.code);
      setValues({ ...emptyGroup, componentId: values.componentId });
      groups.reload();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setSaving(false);
    }
  }

  const noComponents = components.data !== null && options.length === 0;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost groups"
        description="Cost groups classify item costs, labor and machine cost categories and indirect costing sheet nodes, and map each to a cost component of the split."
        meta={groups.data ? <Badge tone={full ? "warning" : "neutral"}>{`${rows.length} of ${MAX_COST_GROUPS}`}</Badge> : undefined}
      />
      <Card title="New cost group" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="New cost group">
          <CodeAndName prefix="group" values={values} errors={errors} onChange={(v) => setValues({ ...values, ...v })} />
          <TypeAndBehaviour prefix="group" label="Type" values={values} onChange={(v) => setValues({ ...values, ...v })} />
          <FormField
            id="group-component"
            label="Cost component"
            error={errors.componentId}
            hint={noComponents ? "Add a cost component first." : undefined}
          >
            {(control) => (
              <Select
                {...control}
                value={values.componentId}
                disabled={options.length === 0}
                invalid={Boolean(errors.componentId)}
                onChange={(e) => setValues({ ...values, componentId: e.target.value })}
              >
                <option value="">Choose a component</option>
                {options.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.code} {c.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={saving} disabled={full || options.length === 0}>
              Add cost group
            </Button>
          </div>
        </form>
      </Card>
      {created ? <Alert tone="success" live="status" title={`Cost group ${created} added`} /> : null}
      {failure ? <ProblemAlert title="The cost group was not added" problem={failure} /> : null}
      {components.error ? <LoadFailure what="cost components" problem={components.error} /> : null}
      {groups.error ? <LoadFailure what="cost groups" problem={groups.error} /> : null}
      <DataTable
        label="Cost groups"
        columns={groupColumns}
        rows={rows}
        getRowKey={(g) => g.id}
        loading={groups.loading && !groups.data}
        empty={{ title: "No cost groups yet", description: "Add a group for each kind of cost, such as MACH for machining, and map it to a component." }}
      />
    </div>
  );
}
