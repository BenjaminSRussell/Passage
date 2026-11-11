import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader } from "../../shell/ui";
import { BODY_PARTS, INCIDENT_TYPES, reportIncident, type Incident, type IncidentType, type NewIncident } from "./api/safety";
import { UUID, utcFromLocalInput } from "./format";
import styles from "./pages.module.css";
import { CheckField, FormActions, humanize, labelled, LookupField, opt, SelectField, TextAreaField, TextField, useSubmit } from "./parts";

export interface IncidentValues {
  workerId: string;
  legalEntityId: string;
  siteId: string;
  incidentType: string;
  occurredAt: string;
  location: string;
  bodyParts: string[];
  death: boolean;
  lossOfConsciousness: boolean;
  medicalTreatment: boolean;
  daysAway: string;
  daysRestricted: string;
  recordable: "" | "yes" | "no";
  overrideReason: string;
  privacyCase: boolean;
  supersedesId: string;
  description: string;
  witnesses: string;
  treatments: string;
  rootCause: string;
  correctiveAction: string;
}

const blank: IncidentValues = {
  workerId: "",
  legalEntityId: "",
  siteId: "",
  incidentType: "INJURY",
  occurredAt: "",
  location: "",
  bodyParts: [],
  death: false,
  lossOfConsciousness: false,
  medicalTreatment: false,
  daysAway: "0",
  daysRestricted: "0",
  recordable: "",
  overrideReason: "",
  privacyCase: false,
  supersedesId: "",
  description: "",
  witnesses: "",
  treatments: "",
  rootCause: "",
  correctiveAction: "",
};

const lines = (raw: string) => raw.split("\n").map((l) => l.trim()).filter((l) => l !== "");

/** The service's incident rules (BR-42.11), and the request the form sends. Medical text is sealed server-side. */
export function validateIncident(v: IncidentValues, now: Date = new Date()): { errors: Record<string, string>; input?: NewIncident } {
  const errors: Record<string, string> = {};
  for (const [name, label] of [["workerId", "worker"], ["legalEntityId", "legal entity"], ["siteId", "site"]] as const) {
    if (!UUID.test(v[name].trim())) errors[name] = `Choose the ${label}.`;
  }
  const occurredAt = utcFromLocalInput(v.occurredAt);
  if (!occurredAt) errors.occurredAt = "Choose when it happened.";
  else if (Date.parse(occurredAt) > now.getTime()) errors.occurredAt = "It can't have happened in the future.";
  const location = v.location.trim();
  if (location === "" || location.length > 200) errors.location = "Enter where it happened, up to 200 characters.";
  const description = v.description.trim();
  if (description === "" || description.length > 4000) errors.description = "Describe what happened, up to 4000 characters.";
  for (const name of ["daysAway", "daysRestricted"] as const) {
    if (!/^\d{1,3}$/.test(v[name].trim()) || Number(v[name]) > 365) errors[name] = "Enter 0 to 365.";
  }
  if (v.recordable !== "" && v.overrideReason.trim() === "") errors.overrideReason = "Say why you are overriding the recordable decision.";
  if (v.overrideReason.trim().length > 500) errors.overrideReason = "Use up to 500 characters.";
  if (v.supersedesId.trim() !== "" && !UUID.test(v.supersedesId.trim())) errors.supersedesId = "Enter the corrected incident's id.";
  const witnesses = lines(v.witnesses);
  const treatments = lines(v.treatments);
  if (witnesses.length > 20 || witnesses.some((w) => w.length > 120)) errors.witnesses = "Up to 20 names of at most 120 characters, one per line.";
  if (treatments.length > 20 || treatments.some((t) => t.length > 200)) errors.treatments = "Up to 20 treatments of at most 200 characters, one per line.";
  if (v.rootCause.trim().length > 2000) errors.rootCause = "Use up to 2000 characters.";
  if (v.correctiveAction.trim().length > 2000) errors.correctiveAction = "Use up to 2000 characters.";
  if (Object.keys(errors).length > 0 || !occurredAt) return { errors };

  const input: NewIncident = {
    workerId: v.workerId.trim(),
    legalEntityId: v.legalEntityId.trim(),
    siteId: v.siteId.trim(),
    incidentType: v.incidentType as IncidentType,
    occurredAt,
    location,
    bodyParts: v.bodyParts,
    death: v.death,
    lossOfConsciousness: v.lossOfConsciousness,
    medicalTreatment: v.medicalTreatment,
    daysAway: Number(v.daysAway),
    daysRestricted: Number(v.daysRestricted),
    privacyCase: v.privacyCase,
    description,
    witnesses,
    treatments,
    rootCause: opt(v.rootCause),
    correctiveAction: opt(v.correctiveAction),
    supersedesId: opt(v.supersedesId),
  };
  if (v.recordable !== "") {
    input.oshaRecordable = v.recordable === "yes";
    input.overrideReason = v.overrideReason.trim();
  }
  return { errors, input };
}

/** Report an injury or illness. The service decides whether it is OSHA recordable unless you override it with a reason. */
export function NewIncidentPage() {
  const [values, setValues] = useState<IncidentValues>(blank);
  const [reported, setReported] = useState<Incident | null>(null);
  const form = useSubmit({ "HCM-INC-001": "overrideReason", "HCM-WRK-NOT-FOUND": "workerId" });
  const set = (patch: Partial<IncidentValues>) => {
    setValues((prev) => ({ ...prev, ...patch }));
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateIncident(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => reportIncident(input, key), setReported);
  }

  if (reported) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Incident reported" />
        <Alert tone="success" title={`Case #${reported.caseNo} was recorded`} live="status">
          {reported.oshaRecordable ? "It is OSHA recordable." : "It is not OSHA recordable."} An incident administrator will review it.
        </Alert>
        <div className={styles.rowActions}>
          <Button
            onClick={() => {
              setValues(blank);
              setReported(null);
              form.touch();
            }}
          >
            Report another incident
          </Button>
          <Link className={buttonClassName("ghost")} to="/hr/incidents">
            Incident register
          </Link>
        </div>
      </div>
    );
  }

  const text = (name: keyof IncidentValues, label: string, extra?: { hint?: string; optional?: boolean; type?: string; inputMode?: "numeric" }) => (
    <TextField id={`incident-${name}`} label={label} value={String(values[name])} error={form.errors[name]} onChange={(v) => set({ [name]: v })} {...extra} />
  );
  const area = (name: "description" | "witnesses" | "treatments" | "rootCause" | "correctiveAction", label: string, optional: boolean, hint?: string) => (
    <div className={styles.wide}>
      <TextAreaField id={`incident-${name}`} label={label} optional={optional} hint={hint} value={values[name]} error={form.errors[name]} onChange={(v) => set({ [name]: v })} />
    </div>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Report an incident" description="What happened, to whom and where. Descriptions, witnesses and treatments are stored sealed." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupField entity="worker" id="incident-workerId" label="Worker" value={values.workerId} error={form.errors.workerId} onChange={(workerId) => set({ workerId })} />
          <LookupField entity="legalEntity" id="incident-legalEntityId" label="Legal entity" value={values.legalEntityId} error={form.errors.legalEntityId} onChange={(legalEntityId) => set({ legalEntityId })} />
          <LookupField entity="site" id="incident-siteId" label="Site" value={values.siteId} error={form.errors.siteId} onChange={(siteId) => set({ siteId })} />
          <SelectField id="incident-incidentType" label="Type" value={values.incidentType} error={form.errors.incidentType} onChange={(v) => set({ incidentType: v })} options={labelled(INCIDENT_TYPES)} />
          {text("occurredAt", "When it happened", { type: "datetime-local", hint: "Read as UTC." })}
          {text("location", "Location")}
          <fieldset className={styles.fieldset}>
            <legend className={styles.legend}>Body parts</legend>
            <div className={styles.optionRow}>
              {BODY_PARTS.map((part) => (
                <CheckField
                  key={part}
                  id={`incident-body-${part}`}
                  label={humanize(part)}
                  checked={values.bodyParts.includes(part)}
                  onChange={(on) => set({ bodyParts: on ? [...values.bodyParts, part] : values.bodyParts.filter((p) => p !== part) })}
                />
              ))}
            </div>
          </fieldset>
          <CheckField id="incident-death" label="Death" checked={values.death} onChange={(death) => set({ death })} />
          <CheckField id="incident-lossOfConsciousness" label="Loss of consciousness" checked={values.lossOfConsciousness} onChange={(lossOfConsciousness) => set({ lossOfConsciousness })} />
          <CheckField id="incident-medicalTreatment" label="Medical treatment beyond first aid" checked={values.medicalTreatment} onChange={(medicalTreatment) => set({ medicalTreatment })} />
          <CheckField id="incident-privacyCase" label="Privacy case: withhold the worker's name on OSHA logs" checked={values.privacyCase} onChange={(privacyCase) => set({ privacyCase })} />
          {text("daysAway", "Days away from work", { inputMode: "numeric" })}
          {text("daysRestricted", "Days of restricted work", { inputMode: "numeric" })}
          <SelectField
            id="incident-recordable"
            label="OSHA recordable"
            value={values.recordable}
            error={form.errors.recordable}
            onChange={(v) => set({ recordable: v as IncidentValues["recordable"] })}
            options={[
              ["yes", "Override: recordable"],
              ["no", "Override: not recordable"],
            ]}
            placeholder="Let the service decide"
          />
          {values.recordable !== "" ? text("overrideReason", "Reason for the override") : null}
          {text("supersedesId", "Corrects incident id", { optional: true, hint: "Only for correcting a closed incident." })}
          {area("description", "What happened", false)}
          {area("witnesses", "Witnesses", true, "One name per line.")}
          {area("treatments", "Treatments", true, "One per line.")}
          {area("rootCause", "Root cause", true)}
          {area("correctiveAction", "Corrective action", true)}
          <FormActions errors={form.errors} busy={form.busy} submit="Report incident" cancelTo="/hr/incidents" failure={form.failure} failureTitle="Could not report the incident" />
        </form>
      </Card>
    </div>
  );
}
