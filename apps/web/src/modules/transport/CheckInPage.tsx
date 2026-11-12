import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { checkIn, describeProblem, newKey, VISIT_DIRECTIONS, type NewCheckIn, type Problem, type VisitDirection } from "./api/client";
import { statusLabel, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

export interface CheckInValues {
  siteId: string;
  direction: VisitDirection;
  loadId: string;
  carrierScac: string;
  trailerNo: string;
  sealNo: string;
  driverName: string;
  driverLicenseId: string;
  appointmentId: string;
  scheduledAt: string;
  arrivedAt: string;
  freeTimeMin: string;
  toleranceMin: string;
}

const blank: CheckInValues = {
  siteId: "",
  direction: "INBOUND",
  loadId: "",
  carrierScac: "",
  trailerNo: "",
  sealNo: "",
  driverName: "",
  driverLicenseId: "",
  appointmentId: "",
  scheduledAt: "",
  arrivedAt: "",
  freeTimeMin: "120",
  toleranceMin: "60",
};

/** A datetime-local value (the guard's local time) as an RFC 3339 UTC instant, or null when unreadable. */
function instant(local: string): string | null {
  const d = new Date(local);
  return Number.isNaN(d.getTime()) ? null : d.toISOString();
}

function minutes(value: string, max: number): number | null {
  const n = Number(value.trim());
  return value.trim() !== "" && Number.isInteger(n) && n >= 0 && n <= max ? n : null;
}

/** Checks that mirror the service's check-in validation (FR-34.14), and the request the form sends. */
export function validateCheckIn(v: CheckInValues): { errors: Record<string, string>; input?: NewCheckIn } {
  const errors: Record<string, string> = {};
  const siteId = v.siteId.trim();
  if (!UUID.test(siteId)) errors.siteId = "Choose the site.";
  const loadId = v.loadId.trim();
  if (loadId !== "" && !UUID.test(loadId)) errors.loadId = "Choose a load, or clear it.";
  const scac = v.carrierScac.trim().toUpperCase();
  if (!/^[A-Z]{2,4}$/.test(scac)) errors.carrierScac = "Enter the carrier's 2 to 4 letter SCAC.";
  const trailer = v.trailerNo.trim().toUpperCase();
  if (trailer === "" || trailer.length > 20) errors.trailerNo = "Enter the trailer or container number, up to 20 characters.";
  const seal = v.sealNo.trim();
  if (seal.length > 20) errors.sealNo = "Enter at most 20 characters.";
  const driver = v.driverName.trim();
  if (driver === "" || driver.length > 100) errors.driverName = "Enter the driver's name.";
  const license = v.driverLicenseId.trim();
  if (license === "" || license.length > 32) errors.driverLicenseId = "Enter the driver's license id, up to 32 characters.";
  const appointmentId = v.appointmentId.trim();
  const scheduled = v.scheduledAt.trim() === "" ? undefined : instant(v.scheduledAt);
  if (appointmentId !== "" && !UUID.test(appointmentId)) errors.appointmentId = "Choose the dock appointment.";
  else if ((appointmentId === "") !== (scheduled === undefined)) errors.appointmentId = "Choose the appointment and its time together, or neither for a walk-in.";
  if (scheduled === null) errors.scheduledAt = "Enter the appointment date and time.";
  const arrived = v.arrivedAt.trim() === "" ? undefined : instant(v.arrivedAt);
  if (arrived === null) errors.arrivedAt = "Enter the arrival date and time, or leave it empty for now.";
  else if (arrived && Date.parse(arrived) > Date.now() + 5 * 60_000) errors.arrivedAt = "The arrival cannot be in the future.";
  const freeTime = minutes(v.freeTimeMin, 1440);
  if (freeTime === null) errors.freeTimeMin = "Enter whole minutes from 0 to 1440.";
  const tolerance = minutes(v.toleranceMin, 480);
  if (tolerance === null) errors.toleranceMin = "Enter whole minutes from 0 to 480.";
  if (Object.keys(errors).length > 0 || freeTime === null || tolerance === null) return { errors };
  return {
    errors,
    input: {
      siteId,
      direction: v.direction,
      ...(loadId ? { loadId } : {}),
      carrierScac: scac,
      trailerNo: trailer,
      ...(seal ? { sealNo: seal } : {}),
      driverName: driver,
      driverLicenseId: license,
      ...(appointmentId && scheduled ? { appointmentId, scheduledAt: scheduled } : {}),
      ...(arrived ? { arrivedAt: arrived } : {}),
      freeTimeMin: freeTime,
      toleranceMin: tolerance,
    },
  };
}

/** An ISO instant as the value of a datetime-local input, in the browser's time zone. */
export function localInput(iso: string): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}`;
}

/** Driver check-in at the gate or kiosk (FR-34.14): matched to the dock appointment, or a walk-in. */
export function CheckInPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CheckInValues>(blank);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CheckInValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCheckIn(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const visit = await checkIn(input, key.current);
      navigate(`/transport/gate/${visit.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof CheckInValues, id: string, label: string, hint?: string, opts: { mono?: boolean; upper?: boolean; type?: string; max?: number; optional?: boolean } = {}) => (
    <FormField id={id} label={label} hint={hint} error={errors[name]} optional={opts.optional}>
      {(control) => (
        <Input
          {...control}
          type={opts.type}
          className={opts.mono ? styles.mono : undefined}
          maxLength={opts.max}
          value={values[name]}
          onChange={(e) => update({ [name]: opts.upper ? e.target.value.toUpperCase() : e.target.value } as Partial<CheckInValues>)}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Check in"
        description="Record the truck at the gate. With an appointment the arrival is marked on time, early or late; without one it is a walk-in. Driver name and license are personal data, kept 90 days."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField id="checkin-site" label="Site" entity="site" hint="The site whose gate this is." value={values.siteId} error={errors.siteId} onChange={(siteId) => update({ siteId })} />
          <FormField id="checkin-direction" label="Direction" hint="Inbound delivers to the site; outbound collects a load.">
            {(control) => (
              <Select {...control} value={values.direction} onChange={(e) => update({ direction: e.target.value as VisitDirection })}>
                {VISIT_DIRECTIONS.map((d) => (
                  <option key={d} value={d}>
                    {statusLabel(d)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {field("carrierScac", "checkin-scac", "Carrier SCAC", "Such as ABCD.", { mono: true, upper: true, max: 4 })}
          {field("trailerNo", "checkin-trailer", "Trailer or container", undefined, { mono: true, upper: true, max: 20 })}
          {field("sealNo", "checkin-seal", "Seal number", "The seal of an inbound sealed trailer.", { mono: true, max: 20, optional: true })}
          <LookupFormField id="checkin-load" label="Load" entity="load" optional hint="The load being collected or delivered." value={values.loadId} error={errors.loadId} onChange={(loadId) => update({ loadId })} />
          {field("driverName", "checkin-driver", "Driver name")}
          {field("driverLicenseId", "checkin-license", "Driver licence number", undefined, { max: 32 })}
          <LookupFormField
            id="checkin-appointment"
            label="Dock appointment"
            entity="dockAppointment"
            optional
            hint="Appointments at the site's warehouses from 12 hours ago to 36 hours ahead. Leave empty for a walk-in."
            scope={{ siteId: values.siteId || undefined }}
            value={values.appointmentId}
            error={errors.appointmentId}
            onChange={(appointmentId, option) => update({ appointmentId, ...(option?.at ? { scheduledAt: localInput(option.at) } : appointmentId === "" ? { scheduledAt: "" } : {}) })}
          />
          {field("scheduledAt", "checkin-scheduled", "Appointment time", "Filled in from the appointment; required with it.", { type: "datetime-local", optional: true })}
          {field("arrivedAt", "checkin-arrived", "Arrived", "Leave empty for now; set it when entering a paper log later.", { type: "datetime-local", optional: true })}
          {field("toleranceMin", "checkin-tolerance", "On-time window (minutes)", "Either side of the appointment; default 60.")}
          {field("freeTimeMin", "checkin-free", "Free time (minutes)", "Before the carrier's detention starts; default 120.")}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not check in" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check in
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/gate">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
