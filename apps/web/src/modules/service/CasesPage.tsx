import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  CASE_STATUSES,
  CHANNELS,
  describeProblem,
  listCases,
  listSlaProfiles,
  newKey,
  openCase,
  PRIORITIES,
  SLA_STATES,
  type CaseChannel,
  type CaseStatus,
  type OpenCaseInput,
  type Priority,
  type Problem,
  type ServiceCase,
  type SlaState,
} from "./api/client";
import { PriorityBadge, SlaBadge } from "./badges";
import { formatDateTime, sentence, shortId, UUID } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<ServiceCase>[] = [
  { key: "number", header: "Case", mono: true },
  { key: "customerPartyId", header: "Customer", mono: true, render: (row) => shortId(row.customerPartyId) },
  { key: "channel", header: "Channel", render: (row) => sentence(row.channel) },
  { key: "priority", header: "Priority", render: (row) => <PriorityBadge priority={row.priority} /> },
  { key: "agreementId", header: "Agreement", mono: true, render: (row) => shortId(row.agreementId) },
  { key: "slaState", header: "SLA", render: (row) => <SlaBadge state={row.slaState} /> },
  { key: "slaResolutionDue", header: "Resolution due", mono: true, render: (row) => formatDateTime(row.slaResolutionDue) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** Service case list (PRD 8.45.14 "Case list"), newest first, filtered by status and SLA state. */
export function CasesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<CaseStatus | "">("");
  const [slaState, setSlaState] = useState<SlaState | "">("");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Service cases"
        description="Cases with the agreement each one resolved to and its SLA clocks. Open a case to see why it was entitled and when it is due."
        actions={
          <Link className={buttonClassName("primary")} to="/service/cases/new">
            Open case
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="cases-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as CaseStatus | "")}>
              <option value="">Any status</option>
              {CASE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="cases-sla" label="SLA state">
          {(control) => (
            <Select {...control} value={slaState} onChange={(e) => setSlaState(e.target.value as SlaState | "")}>
              <option value="">Any SLA state</option>
              {SLA_STATES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <PagedTable
        label="Service cases"
        columns={columns}
        load={(cursor, signal) => listCases({ status, slaState, cursor }, signal)}
        loadKey={`${status}|${slaState}`}
        empty={{ title: "No service cases yet", description: "Cases opened by phone, email, portal or IoT appear here." }}
        onRowOpen={(row) => navigate(`/service/cases/${row.id}`)}
      />
    </div>
  );
}

export interface CaseValues {
  customerPartyId: string;
  legalEntityId: string;
  serviceObjectId: string;
  channel: CaseChannel;
  priority: Priority;
  description: string;
  fallbackSlaProfileId: string;
}

type Errors = Record<string, string>;

/** Checks that mirror the service (api.go openCase). Keys are the service's field names. */
export function validateCase(values: CaseValues): { errors: Errors; input?: OpenCaseInput } {
  const errors: Errors = {};
  if (!UUID.test(values.customerPartyId.trim())) errors.customerPartyId = "Enter the customer's UUID.";
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  const object = values.serviceObjectId.trim();
  if (object && !UUID.test(object)) errors.serviceObjectId = "Enter the service object's UUID, or leave it empty.";
  if ([...values.description].length > 4000) errors.description = "Use at most 4,000 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      customerPartyId: values.customerPartyId.trim(),
      legalEntityId: values.legalEntityId.trim(),
      channel: values.channel,
      priority: values.priority,
      ...(object ? { serviceObjectId: object } : {}),
      ...(values.description.trim() ? { description: values.description.trim() } : {}),
      ...(values.fallbackSlaProfileId ? { fallbackSlaProfileId: values.fallbackSlaProfileId } : {}),
    },
  };
}

/** Open a service case: the service picks the agreement (BR-45.1) and starts the SLA clocks (BR-45.2). */
export function OpenCasePage() {
  const navigate = useNavigate();
  const profiles = useLoad((signal) => listSlaProfiles(signal), "profiles");
  const [values, setValues] = useState<CaseValues>({
    customerPartyId: "",
    legalEntityId: "",
    serviceObjectId: "",
    channel: "PHONE",
    priority: 3,
    description: "",
    fallbackSlaProfileId: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CaseValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCase(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await openCase(input, key.current);
      navigate(`/service/cases/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "customerPartyId" | "legalEntityId" | "serviceObjectId", label: string, hint: string, optional = false) => (
    <FormField id={`case-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <Input {...control} className={styles.mono} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Open service case"
        description="The case resolves to the customer's best matching active agreement in the legal entity, and its response and resolution clocks start in business time."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Open service case">
          {text("customerPartyId", "Customer", "UUID of the customer party.")}
          {text("legalEntityId", "Legal entity", "UUID of the servicing legal entity.")}
          {text("serviceObjectId", "Service object", "UUID of the installed unit; an object-specific agreement line wins over a general one.", true)}
          <FormField id="case-channel" label="Channel">
            {(control) => (
              <Select {...control} value={values.channel} onChange={(e) => update({ channel: e.target.value as CaseChannel })}>
                {CHANNELS.map((c) => (
                  <option key={c} value={c}>
                    {sentence(c)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="case-priority" label="Priority" hint="1 is the most urgent.">
            {(control) => (
              <Select {...control} value={values.priority} onChange={(e) => update({ priority: Number(e.target.value) as Priority })}>
                {PRIORITIES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField
            id="case-fallbackSlaProfileId"
            label="Fallback SLA profile"
            hint={profiles.error ? `Profiles could not be loaded (${profiles.error.code}).` : "Applies when no agreement matches; without one such a case is refused (SVC-ENT-002)."}
            error={errors.fallbackSlaProfileId}
            optional
          >
            {(control) => (
              <Select {...control} value={values.fallbackSlaProfileId} onChange={(e) => update({ fallbackSlaProfileId: e.target.value })}>
                <option value="">None</option>
                {(profiles.data?.items ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {`${p.code} — ${p.name}`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.wide}>
            <FormField id="case-description" label="Description" error={errors.description} optional>
              {(control) => (
                <Textarea {...control} value={values.description} onChange={(e) => update({ description: e.target.value })} invalid={Boolean(errors.description)} />
              )}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SVC-ENT-002" ? "No agreement covers this case" : "Could not open the case"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Open case
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/cases">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
