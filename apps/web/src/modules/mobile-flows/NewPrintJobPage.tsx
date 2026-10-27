import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { CODE, createJob, describeProblem, newKey, TEMPLATES, type DocumentType, type NewPrintJob, type Problem, type TemplateCode } from "./api/print";
import { inSet82, mod10Valid } from "./domain/gs1";
import { DOCUMENT_LABELS } from "./format";
import styles from "./pages.module.css";

export type Target = "queue" | "rule";

export interface JobValues {
  templateCode: TemplateCode;
  target: Target;
  queueCode: string;
  zone: string;
  warehouseId: string;
  data: Record<string, string>;
}

type Errors = Partial<Record<string, string>>;

const FIELD_LABELS: Record<string, string> = {
  lp: "LP number",
  item: "Item",
  lot: "Lot",
  expiry: "Expiry",
  sscc: "SSCC",
  location: "Location",
  gtin: "GTIN",
  serial: "Serial number",
  shipToName: "Ship-to name",
  poNumber: "PO number",
  shipToPostal: "Ship-to postal code",
};

const TEMPLATE_LABELS: Record<TemplateCode, string> = {
  LP_LABEL: "LP label (SSCC)",
  LOCATION_LABEL: "Location label",
  ITEM_LABEL: "Item label (GTIN, lot, expiry, serial)",
  GS1_CARTON_LABEL: "GS1 carton shipping label (4 x 6 in)",
};

/** GS1 data rules the service applies to the item and carton labels (BR-32.1, BR-32.10). */
const GS1_RULES: Partial<Record<TemplateCode, Record<string, (v: string) => string | null>>> = {
  ITEM_LABEL: {
    gtin: (v) => (/^\d{14}$/.test(v) && mod10Valid(v) ? null : "Enter the 14-digit GTIN with a valid check digit."),
    lot: (v) => set82(v, 20),
    expiry: (v) => (validYYMMDD(v) ? null : "Enter the expiry as YYMMDD, such as 261231 (DD 00 means the month end)."),
    serial: (v) => set82(v, 20),
  },
  GS1_CARTON_LABEL: {
    sscc: (v) => (/^\d{18}$/.test(v) && mod10Valid(v) ? null : "Enter the 18-digit SSCC with a valid check digit."),
    poNumber: (v) => set82(v, 30),
    shipToPostal: (v) => set82(v, 20),
  },
};

function set82(v: string, max: number): string | null {
  if (v.length > max) return `Use at most ${max} characters.`;
  return inSet82(v) && !/[\s>]/.test(v) ? null : "Use letters, digits and GS1 punctuation only, without spaces or >.";
}

/** A field label inside a sentence: lower case, except an acronym such as GTIN or SSCC. */
function inSentence(label: string): string {
  return /^[A-Z]{2,}\b/.test(label) ? label : label.charAt(0).toLowerCase() + label.slice(1);
}

/** GS1 YYMMDD: month 01-12, day up to the month end, 00 for the month end itself. */
export function validYYMMDD(v: string): boolean {
  if (!/^\d{6}$/.test(v)) return false;
  const yy = Number(v.slice(0, 2));
  const mm = Number(v.slice(2, 4));
  const dd = Number(v.slice(4, 6));
  if (mm < 1 || mm > 12) return false;
  return dd <= new Date(Date.UTC(2000 + yy, mm, 0)).getUTCDate();
}

/** Checks that mirror the service's template rendering, and the request the form sends. */
export function validateJob(values: JobValues): { errors: Errors; input?: NewPrintJob } {
  const errors: Errors = {};
  const data: Record<string, string> = {};
  const rules = GS1_RULES[values.templateCode] ?? {};
  for (const { name, optional } of TEMPLATES[values.templateCode] as readonly { name: string; optional?: boolean }[]) {
    const v = (values.data[name] ?? "").trim();
    if (v === "") {
      if (!optional) errors[name] = `Enter the ${inSentence(FIELD_LABELS[name])}.`;
      continue;
    }
    if (/[\^~]/.test(v) || /[\u0000-\u001f]/.test(v)) errors[name] = "Remove ^, ~ and control characters; they are ZPL commands.";
    else if (name === "sscc" && !(/^\d{18}$/.test(v) && mod10Valid(v))) errors[name] = "Enter the 18-digit SSCC with a valid check digit.";
    else {
      const problem = rules[name]?.(v);
      if (problem) errors[name] = problem;
    }
    data[name] = v;
  }
  const input: NewPrintJob = { templateCode: values.templateCode, data };
  if (values.target === "queue") {
    if (!CODE.test(values.queueCode.trim())) errors.queueCode = "Use the queue code, such as DOCK.";
    input.queueCode = values.queueCode.trim();
  } else {
    // Built-in templates are labels; the routing rule for the same document type picks the queue.
    input.documentType = values.templateCode as DocumentType;
    if (values.zone.trim()) input.zone = values.zone.trim();
    if (values.warehouseId.trim()) input.warehouseId = values.warehouseId.trim();
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input };
}

function fieldHint(template: TemplateCode, field: string): string | undefined {
  if (field !== "expiry") return undefined;
  return template === "LP_LABEL" ? "As printed, e.g. 2027-06-30." : "YYMMDD as encoded in the barcode, e.g. 261231.";
}

/** Print a label from a built-in ZPL template (FR-32.7), on a named queue or by routing rule. */
export function NewPrintJobPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<JobValues>({ templateCode: "LP_LABEL", target: "queue", queueCode: "", zone: "", warehouseId: "", data: {} });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<JobValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateJob(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const job = await createJob(input, key.current);
      navigate(`/mobile-flows/print-jobs/${job.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const input = (id: string, label: string, value: string, onChange: (v: string) => void, hint?: string, optional?: boolean) => (
    <FormField id={`job-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => <Input {...control} value={value} onChange={(event) => onChange(event.target.value)} invalid={Boolean(errors[id])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Print label"
        description="Renders the template and sends it to the first label printer of the queue that answers. The job is recorded as printed or failed."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="job-template" label="Template">
            {(control) => (
              <Select {...control} value={values.templateCode} onChange={(event) => update({ templateCode: event.target.value as TemplateCode, data: {} })}>
                {(Object.keys(TEMPLATES) as TemplateCode[]).map((t) => (
                  <option key={t} value={t}>
                    {TEMPLATE_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="job-target" label="Printer selection">
            {(control) => (
              <Select {...control} value={values.target} onChange={(event) => update({ target: event.target.value as Target })}>
                <option value="queue">A named queue</option>
                <option value="rule">{`Routing rule for ${DOCUMENT_LABELS[values.templateCode as DocumentType].toLowerCase()}`}</option>
              </Select>
            )}
          </FormField>
          {values.target === "queue"
            ? input("queueCode", "Queue", values.queueCode, (v) => update({ queueCode: v }))
            : [
                <div key="zone">{input("zone", "Zone", values.zone, (v) => update({ zone: v }), "Matches zone rules.", true)}</div>,
                <div key="warehouse">{input("warehouseId", "Warehouse", values.warehouseId, (v) => update({ warehouseId: v }), "Matches warehouse rules.", true)}</div>,
              ]}
          {(TEMPLATES[values.templateCode] as readonly { name: string; optional?: boolean }[]).map(({ name, optional }) => (
            <div key={name}>
              {input(name, FIELD_LABELS[name], values.data[name] ?? "", (v) => update({ data: { ...values.data, [name]: v } }), fieldHint(values.templateCode, name), optional)}
            </div>
          ))}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not print the label" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Print label
            </Button>
            <Link className={buttonClassName("ghost")} to="/mobile-flows/print-jobs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
