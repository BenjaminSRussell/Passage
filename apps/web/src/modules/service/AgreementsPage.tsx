import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  AGREEMENT_STATUSES,
  BILLING_MODELS,
  createAgreement,
  describeProblem,
  expireAgreements,
  listAgreements,
  listSlaProfiles,
  newKey,
  type Agreement,
  type AgreementInput,
  type AgreementLine,
  type AgreementStatus,
  type BillingModel,
  type Problem,
} from "./api/client";
import { DATE, DECIMAL, sentence, shortId, today, UUID } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<Agreement>[] = [
  { key: "number", header: "Agreement", mono: true },
  { key: "customerPartyId", header: "Customer", mono: true, render: (row) => shortId(row.customerPartyId) },
  { key: "validity", header: "Valid", mono: true, render: (row) => `${row.validFrom} – ${row.validTo ?? "open"}` },
  { key: "billingModel", header: "Billing", render: (row) => sentence(row.billingModel) },
  { key: "priorityRank", header: "Rank", align: "end" },
  { key: "hoursConsumed", header: "Hours used", align: "end" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** Service agreement list (PRD 8.45.14 "Agreement entitlement usage"). */
export function AgreementsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<AgreementStatus | "">("");
  const [reloads, setReloads] = useState(0);
  const [expiring, setExpiring] = useState(false);
  const [notice, setNotice] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);

  async function expire() {
    setExpiring(true);
    setNotice("");
    setFailure(null);
    try {
      const res = await expireAgreements();
      setNotice(res.expired === 0 ? "No agreement has passed its last day." : `${res.expired} agreement${res.expired === 1 ? "" : "s"} expired.`);
      setReloads((n) => n + 1);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setExpiring(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Service agreements"
        description="Agreements decide which cases are entitled, which SLA applies and what a work order bills."
        actions={
          <div className={styles.actions}>
            <Button loading={expiring} onClick={expire}>
              Expire ended agreements
            </Button>
            <Link className={buttonClassName("primary")} to="/service/agreements/new">
              New agreement
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="The expiry pass failed" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <div className={styles.toolbar}>
        <FormField id="agreements-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as AgreementStatus | "")}>
              <option value="">Any status</option>
              {AGREEMENT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <PagedTable
        label="Service agreements"
        columns={columns}
        load={(cursor, signal) => listAgreements({ status, cursor }, signal)}
        loadKey={`${status}:${reloads}`}
        empty={{ title: "No service agreements yet", description: "Create an agreement to entitle a customer's cases." }}
        onRowOpen={(row) => navigate(`/service/agreements/${row.id}`)}
      />
    </div>
  );
}

export interface AgreementLineValues {
  serviceObjectId: string;
  includedVisits: string;
  includedHours: string;
  partsCovered: boolean;
  laborCovered: boolean;
  travelCovered: boolean;
}

export interface AgreementValues {
  customerPartyId: string;
  legalEntityId: string;
  validFrom: string;
  validTo: string;
  slaProfileId: string;
  billingModel: BillingModel;
  priorityRank: string;
  status: "DRAFT" | "ACTIVE";
  lines: AgreementLineValues[];
}

export const emptyAgreementLine = (): AgreementLineValues => ({
  serviceObjectId: "",
  includedVisits: "0",
  includedHours: "0",
  partsCovered: false,
  laborCovered: true,
  travelCovered: false,
});

type Errors = Record<string, string>;

/** Checks that mirror the service (api.go createAgreement). Line errors use the service's lines[i] keys. */
export function validateAgreement(values: AgreementValues): { errors: Errors; input?: AgreementInput } {
  const errors: Errors = {};
  if (!UUID.test(values.customerPartyId.trim())) errors.customerPartyId = "Enter the customer's UUID.";
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!DATE.test(values.validFrom)) errors.validFrom = "Choose the first day the agreement applies.";
  if (values.validTo && (!DATE.test(values.validTo) || values.validTo < values.validFrom)) errors.validTo = "Choose a last day on or after the first day, or leave it empty.";
  if (!UUID.test(values.slaProfileId)) errors.slaProfileId = "Choose the SLA profile.";
  const rank = Number(values.priorityRank);
  if (!Number.isInteger(rank) || rank < 1 || rank > 100) errors.priorityRank = "Enter a whole number from 1 to 100.";
  if (values.lines.length < 1 || values.lines.length > 200) errors.lines = "Add between 1 and 200 lines.";
  const lines: AgreementLine[] = values.lines.map((l, i) => {
    const object = l.serviceObjectId.trim();
    const visits = Number(l.includedVisits);
    if (object && !UUID.test(object)) errors[`lines[${i}]`] = "Enter the service object's UUID, or leave it empty to cover any object.";
    else if (!Number.isInteger(visits) || visits < 0) errors[`lines[${i}]`] = "Included visits must be a whole number of at least 0.";
    else if (!DECIMAL.test(l.includedHours)) errors[`lines[${i}]`] = "Included hours must be a decimal of at least 0, such as 1.5.";
    return {
      ...(object ? { serviceObjectId: object } : {}),
      includedVisits: visits,
      includedHours: l.includedHours,
      partsCovered: l.partsCovered,
      laborCovered: l.laborCovered,
      travelCovered: l.travelCovered,
    };
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      customerPartyId: values.customerPartyId.trim(),
      legalEntityId: values.legalEntityId.trim(),
      validFrom: values.validFrom,
      ...(values.validTo ? { validTo: values.validTo } : {}),
      slaProfileId: values.slaProfileId,
      billingModel: values.billingModel,
      priorityRank: rank,
      status: values.status,
      lines,
    },
  };
}

/** Create a service agreement with its coverage lines (FR-45.3). */
export function NewAgreementPage() {
  const navigate = useNavigate();
  const profiles = useLoad((signal) => listSlaProfiles(signal), "profiles");
  const [values, setValues] = useState<AgreementValues>({
    customerPartyId: "",
    legalEntityId: "",
    validFrom: today(),
    validTo: "",
    slaProfileId: "",
    billingModel: "PREPAID",
    priorityRank: "1",
    status: "ACTIVE",
    lines: [emptyAgreementLine()],
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<AgreementValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }
  const updateLine = (i: number, patch: Partial<AgreementLineValues>) => update({ lines: values.lines.map((l, idx) => (idx === i ? { ...l, ...patch } : l)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAgreement(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createAgreement(input, key.current);
      navigate(`/service/agreements/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "customerPartyId" | "legalEntityId" | "validFrom" | "validTo" | "priorityRank", label: string, hint?: string, extra: { type?: string; mono?: boolean; optional?: boolean } = {}) => (
    <FormField id={`agreement-${name}`} label={label} hint={hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New service agreement"
        description="Only ACTIVE agreements entitle cases. When several match, the most specific line wins, then the higher rank, then the later start."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New service agreement">
          {text("customerPartyId", "Customer", "UUID of the customer party.", { mono: true })}
          {text("legalEntityId", "Legal entity", "UUID of the servicing legal entity.", { mono: true })}
          {text("validFrom", "Valid from", undefined, { type: "date" })}
          {text("validTo", "Valid to", "Leave empty for an open-ended agreement.", { type: "date", optional: true })}
          <FormField
            id="agreement-slaProfileId"
            label="SLA profile"
            hint={profiles.error ? `Profiles could not be loaded (${profiles.error.code}).` : profiles.data?.items.length === 0 ? "Create an SLA profile first." : undefined}
            error={errors.slaProfileId}
          >
            {(control) => (
              <Select {...control} value={values.slaProfileId} onChange={(e) => update({ slaProfileId: e.target.value })} invalid={Boolean(errors.slaProfileId)}>
                <option value="">Choose a profile</option>
                {(profiles.data?.items ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {`${p.code} — ${p.name}`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="agreement-billingModel" label="Billing model">
            {(control) => (
              <Select {...control} value={values.billingModel} onChange={(e) => update({ billingModel: e.target.value as BillingModel })}>
                {BILLING_MODELS.map((m) => (
                  <option key={m} value={m}>
                    {sentence(m)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("priorityRank", "Priority rank", "1 to 100; the higher rank wins a tie between equally specific agreements.")}
          <FormField id="agreement-status" label="Status" hint="A draft agreement entitles nothing.">
            {(control) => (
              <Select {...control} value={values.status} onChange={(e) => update({ status: e.target.value as "DRAFT" | "ACTIVE" })}>
                <option value="ACTIVE">Active</option>
                <option value="DRAFT">Draft</option>
              </Select>
            )}
          </FormField>
          <div className={`${styles.wide} ${styles.lines}`}>
            {values.lines.map((line, i) => (
              <fieldset key={i} className={styles.lineCard}>
                <legend className={styles.lineLegend}>Line {i + 1}</legend>
                <FormField id={`aline-${i}-object`} label="Service object" hint="Empty covers any object." optional>
                  {(control) => (
                    <Input {...control} className={styles.mono} value={line.serviceObjectId} onChange={(e) => updateLine(i, { serviceObjectId: e.target.value })} />
                  )}
                </FormField>
                <FormField id={`aline-${i}-visits`} label="Included visits">
                  {(control) => <Input {...control} inputMode="numeric" value={line.includedVisits} onChange={(e) => updateLine(i, { includedVisits: e.target.value })} />}
                </FormField>
                <FormField id={`aline-${i}-hours`} label="Included hours">
                  {(control) => <Input {...control} inputMode="decimal" value={line.includedHours} onChange={(e) => updateLine(i, { includedHours: e.target.value })} />}
                </FormField>
                <fieldset className={styles.groups}>
                  <legend className={styles.groupsLegend}>Covered</legend>
                  {(["partsCovered", "laborCovered", "travelCovered"] as const).map((flag) => (
                    <span key={flag} className={styles.check}>
                      <Input id={`aline-${i}-${flag}`} type="checkbox" checked={line[flag]} onChange={(e) => updateLine(i, { [flag]: e.target.checked })} />
                      <label htmlFor={`aline-${i}-${flag}`}>{flag === "partsCovered" ? "Parts" : flag === "laborCovered" ? "Labor" : "Travel"}</label>
                    </span>
                  ))}
                </fieldset>
                {errors[`lines[${i}]`] ? (
                  <p role="alert" className={styles.fieldError}>
                    {errors[`lines[${i}]`]}
                  </p>
                ) : null}
                {values.lines.length > 1 ? (
                  <div className={styles.formActions}>
                    <Button size="sm" variant="ghost" onClick={() => update({ lines: values.lines.filter((_, idx) => idx !== i) })}>
                      Remove line {i + 1}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            ))}
            {errors.lines ? (
              <p role="alert" className={styles.fieldError}>
                {errors.lines}
              </p>
            ) : null}
            <div>
              <Button onClick={() => update({ lines: [...values.lines, emptyAgreementLine()] })}>Add line</Button>
            </div>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the agreement" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create agreement
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/agreements">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
