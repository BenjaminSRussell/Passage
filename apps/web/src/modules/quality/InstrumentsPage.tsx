import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { calibrateInstrument, createInstrument, describeProblem, listInstruments, newKey, type Instrument, type NewInstrument, type Problem } from "./api/client";
import { CALIBRATION_LABELS, CODE, formatDate, pickFields } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const baseColumns: DataTableColumn<Instrument>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "calibrationIntervalDays", header: "Interval", align: "end", render: (row) => `${row.calibrationIntervalDays} days` },
  { key: "lastCalibratedAt", header: "Last calibrated", render: (row) => formatDate(row.lastCalibratedAt) },
  { key: "dueAt", header: "Due", render: (row) => formatDate(row.dueAt) },
  {
    key: "calibrationStatus",
    header: "Calibration",
    render: (row) => {
      const s = row.calibrationStatus ? CALIBRATION_LABELS[row.calibrationStatus] : undefined;
      return s ? <Badge tone={s.tone}>{s.label}</Badge> : null;
    },
  },
];

/** Horizons offered on the calibration due list (section 6.33.14), in days. */
export const DUE_WINDOWS = [14, 30, 90] as const;

/**
 * Instruments and their calibration due dates (BR-33.8); results from an overdue
 * instrument are refused. "Due within" turns the register into the calibration due list.
 */
export function InstrumentsPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("due") ?? "";
  const due = /^\d{1,4}$/.test(raw) && Number(raw) <= 3650 ? Number(raw) : undefined;
  const state = useLoad((signal) => listInstruments(signal, due), `instruments|${due ?? ""}`);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function calibrated(row: Instrument) {
    setBusy(row.id);
    setNotice(null);
    setFailure(null);
    try {
      const next = await calibrateInstrument(row.id, new Date().toISOString());
      setNotice(`${next.code} calibrated; next due ${formatDate(next.dueAt)}.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<Instrument>[] = [
    ...baseColumns,
    {
      key: "calibrate",
      header: "Calibration done",
      render: (row) => (
        <Button size="sm" loading={busy === row.id} aria-label={`Calibrated today: ${row.code}`} onClick={() => calibrated(row)}>
          Calibrated today
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Instruments"
        description="Measuring devices with their calibration interval. An instrument is due on its last calibration plus the interval; results from an overdue instrument are refused."
        actions={
          <Link className={buttonClassName("primary")} to="/quality/instruments/new">
            Register instrument
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="instruments-due" label="Due within">
          {(control) => (
            <Select
              {...control}
              value={due === undefined ? "" : String(due)}
              onChange={(event) => {
                const next = new URLSearchParams(params);
                if (event.target.value) next.set("due", event.target.value);
                else next.delete("due");
                setParams(next);
              }}
            >
              <option value="">All instruments</option>
              {DUE_WINDOWS.map((d) => (
                <option key={d} value={String(d)}>{`${d} days, overdue included`}</option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not record the calibration" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load instruments" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Instruments"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            due === undefined
              ? { title: "No instruments yet", description: "Register the gauges and meters your inspectors record results with." }
              : { title: `No instrument is due within ${due} days`, description: "Every registered instrument is calibrated beyond this horizon." }
          }
        />
      )}
    </div>
  );
}

interface Values {
  code: string;
  name: string;
  calibrationIntervalDays: string;
  lastCalibratedAt: string;
}

type Errors = Partial<Record<keyof Values, string>>;

/** Checks that mirror the service, and the request the form sends. */
export function validateInstrument(values: Values): { errors: Errors; input?: NewInstrument } {
  const errors: Errors = {};
  const code = values.code.trim();
  if (!CODE.test(code)) errors.code = "Use 2 to 32 capital letters, digits, _ or -, such as PH-01.";
  const name = values.name.trim();
  if (name === "" || name.length > 120) errors.name = "Enter a name, up to 120 characters.";
  const days = Number(values.calibrationIntervalDays);
  if (!/^\d{1,4}$/.test(values.calibrationIntervalDays.trim()) || days < 1 || days > 3650) errors.calibrationIntervalDays = "Enter whole days from 1 to 3650.";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(values.lastCalibratedAt)) errors.lastCalibratedAt = "Enter the date of the last calibration.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name, calibrationIntervalDays: days, lastCalibratedAt: `${values.lastCalibratedAt}T00:00:00Z` } };
}

/** Register an instrument with its calibration interval. */
export function NewInstrumentPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ code: "", name: "", calibrationIntervalDays: "365", lastCalibratedAt: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateInstrument(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createInstrument(input, key.current);
      navigate("/quality/instruments");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = pickFields(problem.fields, ["code", "name", "calibrationIntervalDays", "lastCalibratedAt"] as const);
      if (problem.code === "QM-0151") fieldErrors.code = "An instrument with this code already exists.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (id: keyof Values, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`instrument-${id}`} label={label} hint={hint} error={errors[id]}>
      {(control) => <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value })} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Register instrument" description="The due date is the last calibration plus the interval. You are warned 14 days ahead." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Register instrument">
          {field("code", "Code", "Such as PH-01.", { maxLength: 32 })}
          {field("name", "Name", undefined, { maxLength: 120 })}
          {field("calibrationIntervalDays", "Calibration interval (days)", undefined, { inputMode: "numeric" })}
          {field("lastCalibratedAt", "Last calibrated", undefined, { type: "date" })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not register the instrument" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Register instrument
            </Button>
            <Link className={buttonClassName("ghost")} to="/quality/instruments">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
