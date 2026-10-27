import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  createAppointment,
  createDoor,
  describeProblem,
  listAppointments,
  listDoors,
  newKey,
  type Appointment,
  type AppointmentDirection,
  type AppointmentInput,
  type DockDoor,
  type Problem,
} from "./api/client";
import styles from "./pages.module.css";
import { ChooseWarehouse, sentence, useWarehouse, UUID, WarehouseField } from "./shared";
import { useLoad } from "./useLoad";

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const TIME = /^([01]\d|2[0-3]):([0-5]\d)$/;

/** Today's date in the browser's time zone, as YYYY-MM-DD. */
export function today(now = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

/** The instant of a local date and HH:MM, as a UTC RFC 3339 string. */
export function localInstant(date: string, time: string): string {
  return new Date(`${date}T${time}:00`).toISOString().replace(".000Z", "Z");
}

/** The local wall-clock HH:MM of an RFC 3339 instant. */
export function localTime(iso: string): string {
  const d = new Date(iso);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

function nextDay(date: string): string {
  const d = new Date(`${date}T00:00:00`);
  d.setDate(d.getDate() + 1);
  return today(d);
}

function problemText(p: Problem): string {
  const fields = Object.entries(p.fields);
  return `${p.message} (${p.code})${fields.length > 0 ? `: ${fields.map(([f, m]) => `${f} ${m}`).join("; ")}` : ""}`;
}

const appointmentColumns: DataTableColumn<Appointment>[] = [
  { key: "window", header: "Window", mono: true, render: (a) => `${localTime(a.start)}–${localTime(a.end)}` },
  { key: "doorCode", header: "Door", mono: true },
  { key: "direction", header: "Direction", render: (a) => <Badge tone={a.direction === "INBOUND" ? "info" : "neutral"}>{sentence(a.direction)}</Badge> },
  { key: "reference", header: "Reference", mono: true },
];

/** Dock schedule (FR-31.8): a warehouse's doors and the appointments booked on them for one day. */
export function DockSchedulePage() {
  const [warehouse, setWarehouse] = useWarehouse();
  const [date, setDate] = useState(today);
  const [door, setDoor] = useState("");
  const wh = warehouse.trim();
  const valid = UUID.test(wh);
  const validDate = DATE.test(date);
  const doors = useLoad((signal) => (valid ? listDoors(wh, signal) : Promise.resolve(null)), `${wh}|doors`);
  const schedule = useLoad(
    (signal) =>
      valid && validDate
        ? listAppointments({ warehouseId: wh, doorId: door, from: localInstant(date, "00:00"), to: localInstant(nextDay(date), "00:00") }, signal)
        : Promise.resolve(null),
    `${wh}|${date}|${door}`,
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Dock schedule"
        description="Doors are booked in 15-minute slots; two appointments never overlap on one door. Times are in your time zone."
        actions={
          <Link className={buttonClassName("primary")} to={`/warehouse/docks/new?warehouse=${encodeURIComponent(wh)}`}>
            Book appointment
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <WarehouseField value={warehouse} onChange={setWarehouse} />
        <FormField id="dock-date" label="Day" error={validDate ? undefined : "Choose a day."}>
          {(c) => <Input {...c} type="date" value={date} onChange={(e) => setDate(e.target.value)} invalid={!validDate} />}
        </FormField>
        <FormField id="dock-door" label="Door">
          {(c) => (
            <Select {...c} value={door} onChange={(e) => setDoor(e.target.value)}>
              <option value="">All doors</option>
              {(doors.data?.items ?? []).map((d) => (
                <option key={d.id} value={d.id}>
                  {d.code}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {!valid ? (
        <ChooseWarehouse />
      ) : (
        <>
          {schedule.error ? (
            <Alert tone="danger" title="Could not load the schedule" live="alert" action={<Button size="sm" onClick={schedule.reload}>Try again</Button>}>
              {schedule.error.message} ({schedule.error.code})
            </Alert>
          ) : (
            <DataTable
              label="Appointments"
              columns={appointmentColumns}
              rows={schedule.data?.items ?? []}
              getRowKey={(a) => a.id}
              loading={schedule.loading}
              empty={{ title: "No appointments", description: "Nothing is booked on this day. Book a door for an inbound ASN or an outbound load." }}
            />
          )}
          <DoorsCard warehouseId={wh} doors={doors.data?.items ?? []} loading={doors.loading} error={doors.error} onChange={doors.reload} />
        </>
      )}
    </div>
  );
}

function DoorsCard({ warehouseId, doors, loading, error, onChange }: { warehouseId: string; doors: DockDoor[]; loading: boolean; error: Problem | null; onChange(): void }) {
  const [code, setCode] = useState("");
  const [codeError, setCodeError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function add(event: FormEvent) {
    event.preventDefault();
    const c = code.trim();
    if (c === "" || c.length > 20) {
      setCodeError("Enter the door code, at most 20 characters.");
      return;
    }
    setCodeError(undefined);
    setFailure(null);
    setBusy(true);
    try {
      await createDoor(warehouseId.toLowerCase(), c, key.current);
      key.current = newKey();
      setCode("");
      onChange();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Dock doors">
      <div className={styles.stack}>
        {error ? (
          <Alert tone="danger" title="Could not load the doors" live="alert" action={<Button size="sm" onClick={onChange}>Try again</Button>}>
            {error.message} ({error.code})
          </Alert>
        ) : (
          <DataTable
            label="Dock doors"
            columns={[{ key: "code", header: "Door", mono: true }]}
            rows={doors}
            getRowKey={(d) => d.id}
            loading={loading}
            empty={{ title: "No doors", description: "Add the warehouse's dock doors to book appointments on them." }}
          />
        )}
        <form className={styles.form} onSubmit={add} noValidate>
          <FormField id="door-code" label="New door code" hint="For example D1; unique in the warehouse." error={codeError}>
            {(c) => (
              <Input
                {...c}
                className={styles.mono}
                value={code}
                onChange={(e) => {
                  setCode(e.target.value);
                  key.current = newKey();
                }}
                invalid={Boolean(codeError)}
              />
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" loading={busy}>
              Add door
            </Button>
          </div>
          <div className={styles.formActions}>
            <Alert tone="danger" title={failure ? "The door was not added" : undefined} live="alert">
              {failure ? problemText(failure) : null}
            </Alert>
          </div>
        </form>
      </div>
    </Card>
  );
}

export interface AppointmentDraft {
  warehouseId: string;
  doorId: string;
  direction: AppointmentDirection;
  reference: string;
  date: string;
  start: string;
  end: string;
}

type AppointmentErrors = Partial<Record<keyof AppointmentDraft, string>>;

const onGrid = (t: string) => TIME.test(t) && Number(t.slice(3)) % 15 === 0;

export function validateAppointmentDraft(d: AppointmentDraft): AppointmentErrors {
  const errors: AppointmentErrors = {};
  if (!UUID.test(d.warehouseId.trim())) errors.warehouseId = "Enter the warehouse id, a UUID.";
  if (!UUID.test(d.doorId)) errors.doorId = "Choose a door.";
  const ref = d.reference.trim();
  if (ref === "" || ref.length > 100) errors.reference = "Enter the ASN, shipment or load number.";
  if (!DATE.test(d.date)) errors.date = "Choose a day.";
  if (!onGrid(d.start)) errors.start = "Enter a start on the quarter hour, such as 09:00 or 09:15.";
  if (!onGrid(d.end)) errors.end = "Enter an end on the quarter hour, such as 09:45.";
  else if (onGrid(d.start) && d.end <= d.start) errors.end = "The end must be after the start on the same day.";
  return errors;
}

export function toAppointmentInput(d: AppointmentDraft): AppointmentInput {
  return {
    warehouseId: d.warehouseId.trim().toLowerCase(),
    doorId: d.doorId,
    direction: d.direction,
    reference: d.reference.trim(),
    start: localInstant(d.date, d.start),
    end: localInstant(d.date, d.end),
  };
}

/** Books a door for an inbound ASN or an outbound load (FR-31.8); an overlap with another booking is refused (WMS-0108). */
export function NewAppointmentPage() {
  const navigate = useNavigate();
  const [warehouse] = useWarehouse();
  const [draft, setDraft] = useState<AppointmentDraft>({ warehouseId: warehouse, doorId: "", direction: "INBOUND", reference: "", date: today(), start: "", end: "" });
  const [errors, setErrors] = useState<AppointmentErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const wh = draft.warehouseId.trim();
  const doors = useLoad((signal) => (UUID.test(wh) ? listDoors(wh, signal) : Promise.resolve(null)), wh);

  const set = (patch: Partial<AppointmentDraft>) => {
    setDraft((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAppointmentDraft(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await createAppointment(toAppointmentInput(draft), key.current);
      navigate(`/warehouse/docks?warehouse=${encodeURIComponent(wh.toLowerCase())}`);
    } catch (error) {
      setFailure(describeProblem(error));
      setBusy(false);
    }
  }

  const doorList = doors.data?.items ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader title="Book a dock appointment" description="Times are in your time zone and on the quarter hour. The end may touch the next booking but not overlap it." />
      <Card title="Appointment">
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>
            <WarehouseField id="appt-warehouse" value={draft.warehouseId} onChange={(v) => set({ warehouseId: v, doorId: "" })} error={errors.warehouseId} />
          </div>
          <FormField
            id="appt-door"
            label="Door"
            error={errors.doorId ?? (doors.error ? `Could not load the doors: ${doors.error.message}` : undefined)}
            hint={UUID.test(wh) && !doors.loading && doorList.length === 0 && !doors.error ? "This warehouse has no doors yet; add them on the dock schedule." : undefined}
          >
            {(c) => (
              <Select {...c} value={draft.doorId} onChange={(e) => set({ doorId: e.target.value })} invalid={Boolean(errors.doorId)}>
                <option value="">Choose a door</option>
                {doorList.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.code}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="appt-direction" label="Direction" hint="Inbound needs the receiving duty, outbound the shipping duty.">
            {(c) => (
              <Select {...c} value={draft.direction} onChange={(e) => set({ direction: e.target.value as AppointmentDirection })}>
                <option value="INBOUND">Inbound (unload)</option>
                <option value="OUTBOUND">Outbound (load)</option>
              </Select>
            )}
          </FormField>
          <FormField id="appt-reference" label="Reference" hint="The ASN, shipment or load number." error={errors.reference}>
            {(c) => <Input {...c} value={draft.reference} onChange={(e) => set({ reference: e.target.value })} invalid={Boolean(errors.reference)} />}
          </FormField>
          <FormField id="appt-date" label="Day" error={errors.date}>
            {(c) => <Input {...c} type="date" value={draft.date} onChange={(e) => set({ date: e.target.value })} invalid={Boolean(errors.date)} />}
          </FormField>
          <FormField id="appt-start" label="Start" error={errors.start}>
            {(c) => <Input {...c} type="time" step={900} value={draft.start} onChange={(e) => set({ start: e.target.value })} invalid={Boolean(errors.start)} />}
          </FormField>
          <FormField id="appt-end" label="End" error={errors.end}>
            {(c) => <Input {...c} type="time" step={900} value={draft.end} onChange={(e) => set({ end: e.target.value })} invalid={Boolean(errors.end)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Book appointment
            </Button>
          </div>
          <div className={styles.formActions}>
            <Alert tone="danger" title={failure ? "The door was not booked" : undefined} live="alert">
              {failure ? problemText(failure) : null}
            </Alert>
          </div>
        </form>
      </Card>
    </div>
  );
}
