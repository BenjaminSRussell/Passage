import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { BILLABLE_TYPES, createProject, describeProblem, newKey, PROJECT_TYPES, type NewProject, type Problem, type ProjectType } from "./api/client";
import { typeLabel } from "./labels";
import styles from "./pages.module.css";

export interface ProjectValues {
  id: string;
  legalEntityId: string;
  parentProjectId: string;
  projectGroupId: string;
  projectType: ProjectType;
  customerPartyId: string;
  contractId: string;
  currency: string;
  startDate: string;
  endDate: string;
}

type Errors = Partial<Record<keyof ProjectValues, string>>;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const PROJECT_ID = /^[A-Z0-9][A-Z0-9-]{0,19}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

/**
 * Checks that mirror the service (domain.ValidateCreate and api.validateCreate).
 * A sub-project may leave group and customer empty: the service inherits them
 * from the parent (FR-41.1). Keys are the service's field names.
 */
export function validateProject(values: ProjectValues): { errors: Errors; input?: NewProject } {
  const errors: Errors = {};
  const v = {
    id: values.id.trim(),
    legalEntityId: values.legalEntityId.trim(),
    parentProjectId: values.parentProjectId.trim(),
    projectGroupId: values.projectGroupId.trim(),
    customerPartyId: values.customerPartyId.trim(),
    contractId: values.contractId.trim(),
  };
  const sub = v.parentProjectId !== "";
  if (!PROJECT_ID.test(v.id)) errors.id = "Use 1 to 20 capital letters, digits or hyphens, starting with a letter or digit.";
  if (!UUID.test(v.legalEntityId)) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (sub && !PROJECT_ID.test(v.parentProjectId)) errors.parentProjectId = "Enter an existing project id, or leave it empty.";
  if ((!sub && v.projectGroupId === "") || v.projectGroupId.length > 10) {
    errors.projectGroupId = sub ? "Use at most 10 characters." : "Enter a project group of up to 10 characters.";
  }
  if (v.customerPartyId !== "" && !UUID.test(v.customerPartyId)) errors.customerPartyId = "Enter the customer's UUID.";
  else if (v.customerPartyId === "" && !sub && BILLABLE_TYPES.includes(values.projectType)) {
    errors.customerPartyId = `A ${typeLabel[values.projectType].toLowerCase()} project bills a customer. Enter the customer's UUID.`;
  }
  if (v.contractId !== "" && !UUID.test(v.contractId)) errors.contractId = "Enter the contract's UUID, or leave it empty.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (!DATE.test(values.startDate)) errors.startDate = "Choose the start date.";
  if (values.endDate !== "" && !DATE.test(values.endDate)) errors.endDate = "Choose a valid end date, or leave it empty.";
  else if (values.endDate !== "" && DATE.test(values.startDate) && values.endDate < values.startDate) {
    errors.endDate = "The end date cannot be before the start date.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      id: v.id,
      legalEntityId: v.legalEntityId,
      projectType: values.projectType,
      currency: values.currency,
      startDate: values.startDate,
      ...(sub ? { parentProjectId: v.parentProjectId } : {}),
      ...(v.projectGroupId ? { projectGroupId: v.projectGroupId } : {}),
      ...(v.customerPartyId ? { customerPartyId: v.customerPartyId } : {}),
      ...(v.contractId ? { contractId: v.contractId } : {}),
      ...(values.endDate ? { endDate: values.endDate } : {}),
    },
  };
}

const failureTitle: Record<string, string> = {
  "PROJ-CREATE-001": "A project with this id already exists",
  "PROJ-PARENT-001": "The parent project was not found",
  "PROJ-PARENT-002": "The sub-project conflicts with its parent",
  "AUTH-0003": "You can't create projects here",
};

/** Project entry (FR-41.1): saved in stage CREATED. Opened with ?parent= it creates a sub-project. */
export function NewProjectPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<ProjectValues>({
    id: "",
    legalEntityId: params.get("legalEntity") ?? "",
    parentProjectId: params.get("parent") ?? "",
    projectGroupId: "",
    projectType: "TIME_MATERIAL",
    customerPartyId: "",
    contractId: "",
    currency: "USD",
    startDate: new Date().toISOString().slice(0, 10),
    endDate: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per distinct request: a retry of the same values replays, an edit starts a new request.
  const key = useRef(newKey());
  const sub = values.parentProjectId.trim() !== "";

  function update(patch: Partial<ProjectValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateProject(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createProject(input, key.current);
      navigate(`/projects/${encodeURIComponent(created.id)}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields as Errors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: Exclude<keyof ProjectValues, "projectType">, label: string, hint?: string, extra: { type?: string; mono?: boolean; optional?: boolean } = {}) => (
    <FormField id={`project-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: name === "currency" || name === "id" || name === "parentProjectId" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={sub ? "New sub-project" : "New project"}
        description="The project is saved in stage created. Start work from the project page when it is ready."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New project">
          {text("id", "Project id", "Up to 20 capital letters, digits or hyphens, for example ES-1001.", { mono: true })}
          {text("legalEntityId", "Legal entity", "UUID of the legal entity that runs the project.", { mono: true })}
          {text("parentProjectId", "Parent project", "Set it to create a sub-project. Group, customer and contract then default to the parent's.", { mono: true, optional: true })}
          <FormField id="project-projectType" label="Project type" error={errors.projectType}>
            {(control) => (
              <Select {...control} value={values.projectType} onChange={(e) => update({ projectType: e.target.value as ProjectType })}>
                {PROJECT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {typeLabel[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("projectGroupId", "Project group", sub ? "Leave empty to use the parent's group." : "Up to 10 characters, for example ENG.", { mono: true, optional: sub })}
          {text(
            "customerPartyId",
            "Customer",
            sub ? "Leave empty to use the parent's customer. A sub-project cannot change it." : "UUID of the customer party. Needed for time and material and fixed price projects.",
            { mono: true, optional: sub || !BILLABLE_TYPES.includes(values.projectType) },
          )}
          {text("contractId", "Contract", "UUID of the project contract.", { mono: true, optional: true })}
          {text("currency", "Currency")}
          {text("startDate", "Start date", undefined, { type: "date" })}
          {text("endDate", "End date", undefined, { type: "date", optional: true })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failureTitle[failure.code] ?? "Could not create the project"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create project
            </Button>
            <Link className={buttonClassName("ghost")} to={sub ? `/projects/${encodeURIComponent(values.parentProjectId.trim())}` : "/projects/list"}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
