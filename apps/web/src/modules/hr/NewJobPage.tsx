import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Card, PageHeader } from "../../shell/ui";
import { SKILL_CODE } from "./api/client";
import { createJob, type NewJob } from "./api/personnel";
import { compareDecimal, validRate } from "./format";
import styles from "./pages.module.css";
import { FormActions, TextField, useSubmit } from "./parts";

export interface JobValues {
  code: string;
  title: string;
  jobType: string;
  jobFunction: string;
  payCurrency: string;
  payMin: string;
  payMax: string;
}

type Errors = Record<string, string>;
const LONG_CODE = /^[A-Z0-9][A-Z0-9_-]{0,39}$/;
const FIELDS: Array<[keyof JobValues, string, string?, boolean?]> = [
  ["code", "Code", "Unique in your tenant, for example FORKLIFT-OP."],
  ["title", "Title"],
  ["jobType", "Job type", "For example HOURLY or SALARIED."],
  ["jobFunction", "Function", "Optional, for example Warehouse.", true],
  ["payCurrency", "Pay currency", "Give the currency, minimum and maximum together, or leave all three empty.", true],
  ["payMin", "Pay minimum", undefined, true],
  ["payMax", "Pay maximum", undefined, true],
];

/** The service's job rules (FR-42.2), and the request the form sends. The pay range is all three fields or none. */
export function validateJob(v: JobValues): { errors: Errors; input?: NewJob } {
  const errors: Errors = {};
  const code = v.code.trim().toUpperCase();
  if (!SKILL_CODE.test(code)) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const title = v.title.trim();
  if (title === "" || title.length > 120) errors.title = "Enter a title of up to 120 characters.";
  const jobType = v.jobType.trim().toUpperCase();
  if (!LONG_CODE.test(jobType)) errors.jobType = "Use 1 to 40 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const fn = v.jobFunction.trim();
  if (fn.length > 60) errors.jobFunction = "Use up to 60 characters.";

  const currency = v.payCurrency.trim().toUpperCase();
  const min = v.payMin.trim();
  const max = v.payMax.trim();
  const paid = currency !== "" || min !== "" || max !== "";
  if (paid) {
    if (!/^[A-Z]{3}$/.test(currency)) errors.payCurrency = "Use a three-letter currency code, for example USD.";
    else if (!validRate(min, currency)) errors.payMin = "Enter an amount within the currency's decimals.";
    else if (!validRate(max, currency)) errors.payMax = "Enter an amount within the currency's decimals.";
    else if (compareDecimal(max, min) < 0) errors.payMax = "The maximum can't be below the minimum.";
  }
  if (Object.keys(errors).length > 0) return { errors };

  const input: NewJob = { code, title, jobType };
  if (fn) input.jobFunction = fn;
  if (paid) Object.assign(input, { payCurrency: currency, payMin: min, payMax: max });
  return { errors, input };
}

export function NewJobPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<JobValues>({ code: "", title: "", jobType: "", jobFunction: "", payCurrency: "", payMin: "", payMax: "" });
  const form = useSubmit({ "HCM-JOB-DUPLICATE": "code" });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateJob(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => createJob(input, key), (job) => navigate(`/hr/jobs/${job.id}`));
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New job" description="Setting a pay range needs the compensation administrator duty." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {FIELDS.map(([name, label, hint, optional]) => (
            <TextField
              key={name}
              id={`job-${name}`}
              label={label}
              hint={hint}
              optional={optional}
              value={values[name]}
              error={form.errors[name]}
              onChange={(v) => {
                setValues({ ...values, [name]: v });
                form.touch();
              }}
            />
          ))}
          <FormActions busy={form.busy} submit="Add job" cancelTo="/hr/jobs" failure={form.failure} failureTitle="Could not add the job" />
        </form>
      </Card>
    </div>
  );
}
