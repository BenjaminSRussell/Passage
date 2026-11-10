import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  ASSOCIABLE_TYPES,
  ASSOCIATION_ROLES,
  CUSTOMER_REQUIRED,
  describeProblem,
  GENERIC_STATUSES,
  listCaseCategories,
  listGenericCases,
  newKey,
  openGenericCase,
  PRIORITIES,
  SERVICE_ROUTED,
  type AssociableType,
  type AssociationRole,
  type CaseCategory,
  type GenericCase,
  type GenericCaseInput,
  type GenericStatus,
  type Problem,
} from "./api/client";
import { PriorityBadge } from "./badges";
import { dash, formatDateTime, sentence, UUID } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<GenericCase>[] = [
  { key: "number", header: "Case", mono: true },
  { key: "subject", header: "Subject" },
  { key: "categoryCode", header: "Category", render: (row) => `${row.categoryCode} (${sentence(row.categoryType)})` },
  { key: "stageCode", header: "Stage", mono: true, render: (row) => row.stageCode || dash },
  { key: "priority", header: "Priority", render: (row) => <PriorityBadge priority={row.priority} /> },
  { key: "slaResolutionDue", header: "Resolution due", mono: true, render: (row) => formatDateTime(row.slaResolutionDue) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** Cases in any category (PRD 8.45.6 "My cases / case list"), filtered by category, stage, status and associated record. */
export function GenericCasesPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const categories = useLoad((signal) => listCaseCategories(signal), "categories");
  const [categoryId, setCategoryId] = useState(params.get("categoryId") ?? "");
  const [status, setStatus] = useState<GenericStatus | "">("OPEN");
  const [stage, setStage] = useState("");
  const [entityType, setEntityType] = useState<AssociableType | "">("");
  const [entityId, setEntityId] = useState("");
  const record = entityId.trim();
  const recordValid = entityType !== "" && UUID.test(record);
  const stageCode = stage.trim().toUpperCase();
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cases"
        description="HR, collections, product change, audit, project, vendor and internal cases with their stage, owner and associated records. Cases you have no access to are not listed."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to="/service/case-setup">
              Case setup
            </Link>
            <Link className={buttonClassName("primary")} to="/service/case-management/new">
              Open case
            </Link>
          </div>
        }
      />
      {categories.error ? (
        <Alert tone="warning" title="Categories could not be loaded" live="alert">
          <ProblemMessage problem={categories.error} />
        </Alert>
      ) : null}
      <div className={styles.toolbar}>
        <FormField id="gcases-category" label="Category">
          {(control) => (
            <Select {...control} value={categoryId} onChange={(e) => setCategoryId(e.target.value)}>
              <option value="">Any category</option>
              {(categories.data?.items ?? [])
                .filter((c) => !SERVICE_ROUTED.includes(c.categoryType))
                .map((c) => (
                  <option key={c.id} value={c.id}>
                    {`${c.code} — ${c.name}`}
                  </option>
                ))}
            </Select>
          )}
        </FormField>
        <FormField id="gcases-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as GenericStatus | "")}>
              <option value="">Any status</option>
              {GENERIC_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="gcases-stage" label="Stage" hint="Stage code, such as INVESTIGATION.">
          {(control) => <Input {...control} className={styles.mono} value={stage} onChange={(e) => setStage(e.target.value)} />}
        </FormField>
        <FormField id="gcases-entityType" label="Associated record type">
          {(control) => (
            <Select {...control} value={entityType} onChange={(e) => setEntityType(e.target.value as AssociableType | "")}>
              <option value="">Any record</option>
              {ASSOCIABLE_TYPES.map((t) => (
                <option key={t} value={t}>
                  {sentence(t)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="gcases-entityId" label="Associated record" hint="UUID of the record." error={record && !UUID.test(record) ? "Enter a full UUID." : undefined}>
          {(control) => (
            <Input {...control} className={styles.mono} value={entityId} invalid={Boolean(record && !UUID.test(record))} onChange={(e) => setEntityId(e.target.value)} />
          )}
        </FormField>
      </div>
      <PagedTable
        label="Cases"
        columns={columns}
        load={(cursor, signal) =>
          listGenericCases({ categoryId, status, stage: stageCode, ...(recordValid ? { entityType, entityId: record } : {}), cursor }, signal)
        }
        loadKey={`${categoryId}|${status}|${stageCode}|${recordValid ? `${entityType}:${record}` : ""}`}
        empty={{ title: "No cases match", description: "Open a case, or change the filters." }}
        onRowOpen={(row) => navigate(`/service/case-management/${row.id}`)}
      />
    </div>
  );
}

export interface GenericCaseValues {
  categoryId: string;
  legalEntityId: string;
  customerPartyId: string;
  subject: string;
  description: string;
  /** "" takes the category's default priority. */
  priority: string;
  subjectType: AssociableType | "";
  subjectId: string;
  subjectRole: AssociationRole;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (cases.go openGenericCase and the category rules of the store). */
export function validateGenericCase(values: GenericCaseValues, category?: CaseCategory): { errors: Errors; input?: GenericCaseInput } {
  const errors: Errors = {};
  if (!UUID.test(values.categoryId)) errors.categoryId = "Choose a category.";
  if (category && SERVICE_ROUTED.includes(category.categoryType)) errors.categoryId = "Service cases open on the service case screen.";
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  const customer = values.customerPartyId.trim();
  if (customer && !UUID.test(customer)) errors.customerPartyId = "Enter the customer's UUID, or leave it empty.";
  if (!customer && category && CUSTOMER_REQUIRED.includes(category.categoryType)) errors.customerPartyId = "This category needs a customer.";
  const subject = values.subject.trim();
  if (subject.length < 1 || [...subject].length > 200) errors.subject = "Enter a subject of 1 to 200 characters.";
  if ([...values.description].length > 4000) errors.description = "Use at most 4,000 characters.";
  const record = values.subjectId.trim();
  if (values.subjectType && !UUID.test(record)) errors.subjectId = "Enter the record's UUID.";
  if (!values.subjectType && record) errors.subjectType = "Choose the record type.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      categoryId: values.categoryId,
      legalEntityId: values.legalEntityId.trim(),
      subject,
      ...(customer ? { customerPartyId: customer } : {}),
      ...(values.description.trim() ? { description: values.description.trim() } : {}),
      ...(values.priority ? { priority: Number(values.priority) } : {}),
      ...(values.subjectType ? { associations: [{ entityType: values.subjectType, entityId: record, role: values.subjectRole }] } : {}),
    },
  };
}

/** Open a case in any non-service category; it starts at the first stage of the category's process. */
export function OpenGenericCasePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const categories = useLoad((signal) => listCaseCategories(signal), "categories");
  const [values, setValues] = useState<GenericCaseValues>({
    categoryId: params.get("categoryId") ?? "",
    legalEntityId: params.get("legalEntityId") ?? "",
    customerPartyId: params.get("customerPartyId") ?? "",
    subject: "",
    description: "",
    priority: "",
    subjectType: "",
    subjectId: "",
    subjectRole: "SUBJECT",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const usable = (categories.data?.items ?? []).filter((c) => !SERVICE_ROUTED.includes(c.categoryType));
  const category = usable.find((c) => c.id === values.categoryId);

  function update(patch: Partial<GenericCaseValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateGenericCase(values, category);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await openGenericCase(input, key.current);
      navigate(`/service/case-management/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "legalEntityId" | "customerPartyId", label: string, hint: string, optional = false) => (
    <FormField id={`gcase-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <Input {...control} className={styles.mono} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Open case"
        description="You become the case owner. Categories with an access duty accept cases only from holders of that duty, and only they and the owner see the case."
      />
      {categories.error ? (
        <Alert tone="danger" title="Could not load the case categories" live="alert" action={<Button size="sm" onClick={categories.reload}>Try again</Button>}>
          <ProblemMessage problem={categories.error} />
        </Alert>
      ) : null}
      {categories.data && usable.length === 0 ? (
        <Alert tone="info" title="No case categories yet">
          <Link to="/service/case-setup">Set up a case category</Link> before opening a case.
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Open case">
          <FormField
            id="gcase-categoryId"
            label="Category"
            hint={category ? `${sentence(category.categoryType)}; default priority P${category.defaultPriority}.` : "The category decides the process, SLA and access."}
            error={errors.categoryId}
          >
            {(control) => (
              <Select {...control} value={values.categoryId} onChange={(e) => update({ categoryId: e.target.value })} invalid={Boolean(errors.categoryId)}>
                <option value="">{categories.loading ? "Loading…" : "Choose a category"}</option>
                {usable.map((c) => (
                  <option key={c.id} value={c.id}>
                    {`${c.code} — ${c.name}`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("legalEntityId", "Legal entity", "UUID of the legal entity that owns the case.")}
          {text(
            "customerPartyId",
            "Customer",
            "UUID of the customer party; collections cases need one.",
            !(category && CUSTOMER_REQUIRED.includes(category.categoryType)),
          )}
          <FormField id="gcase-priority" label="Priority" hint="1 is the most urgent." optional>
            {(control) => (
              <Select {...control} value={values.priority} onChange={(e) => update({ priority: e.target.value })}>
                <option value="">Category default</option>
                {PRIORITIES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.wide}>
            <FormField id="gcase-subject" label="Subject" error={errors.subject}>
              {(control) => <Input {...control} value={values.subject} onChange={(e) => update({ subject: e.target.value })} invalid={Boolean(errors.subject)} />}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField id="gcase-description" label="Description" error={errors.description} optional>
              {(control) => (
                <Textarea {...control} value={values.description} onChange={(e) => update({ description: e.target.value })} invalid={Boolean(errors.description)} />
              )}
            </FormField>
          </div>
          <FormField id="gcase-subjectType" label="Record type" hint="A record the case is about, such as the worker of an HR case." error={errors.subjectType} optional>
            {(control) => (
              <Select {...control} value={values.subjectType} onChange={(e) => update({ subjectType: e.target.value as AssociableType | "" })}>
                <option value="">None</option>
                {ASSOCIABLE_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="gcase-subjectId" label="Record" hint="UUID of the record." error={errors.subjectId} optional>
            {(control) => (
              <Input {...control} className={styles.mono} value={values.subjectId} onChange={(e) => update({ subjectId: e.target.value })} invalid={Boolean(errors.subjectId)} />
            )}
          </FormField>
          <FormField id="gcase-subjectRole" label="Role">
            {(control) => (
              <Select {...control} value={values.subjectRole} onChange={(e) => update({ subjectRole: e.target.value as AssociationRole })}>
                {ASSOCIATION_ROLES.map((r) => (
                  <option key={r} value={r}>
                    {sentence(r)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "CASE-SEC-001" ? "You cannot open cases in this category" : "Could not open the case"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Open case
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/case-management">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
