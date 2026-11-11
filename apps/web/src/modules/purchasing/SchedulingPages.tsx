import { useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  buttonClassName,
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createSchedulingAgreement,
  describeProblem,
  getSchedulingAgreement,
  getSchedulingRelease,
  listSchedulingAgreements,
  listSchedulingReleases,
  newIdempotencyKey,
  replaceSchedule,
  runSchedulingRelease,
  type ReleaseFrequency,
  type ReleaseKind,
  type ReleaseProfile,
  type ScheduleLine,
  type ScheduleLineInput,
  type ScheduleZone,
  type SchedulingAgreement,
  type SchedulingAgreementInput,
  type SchedulingAgreementRow,
  type SchedulingRelease,
  type SchedulingReleaseLine,
} from "./api/client";
import { validity } from "./AgreementPages";
import {
  Details,
  LoadError,
  currencyPattern,
  dash,
  datePattern,
  decimalPattern,
  money,
  todayIso,
  useResource,
  uuidPattern,
  type Failure,
  Named,
} from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

type Errors = Record<string, string>;

const zoneTones: Record<ScheduleZone, BadgeTone> = { FIRM: "warning", TRADEOFF: "info", FORECAST: "neutral" };
const zoneLabels: Record<ScheduleZone, string> = { FIRM: "Firm", TRADEOFF: "Trade-off", FORECAST: "Forecast" };

export function ZoneBadge({ zone }: { zone: ScheduleZone }) {
  return <Badge tone={zoneTones[zone]}>{zoneLabels[zone]}</Badge>;
}

export const frequencyLabels: Record<ReleaseFrequency, string> = { DAILY: "Daily", WEEKLY: "Weekly", MONTHLY: "Monthly" };
export const kindLabels: Record<ReleaseKind, string> = { FRC: "Forecast (FRC)", JIT: "Just-in-time (JIT)" };

const qty = (value: string, unit: string) => `${value} ${unit}`;

// --- Release monitor -------------------------------------------------------

const monitorColumns: DataTableColumn<SchedulingAgreementRow>[] = [
  { key: "number", header: "Number", mono: true },
  { key: "vendorId", header: "Vendor", render: (a) => <Named entity="vendor" id={a.vendorId} /> },
  { key: "productId", header: "Product", render: (a) => <Named entity="product" id={a.productId} /> },
  { key: "validity", header: "Validity", mono: true, render: validity },
  { key: "openQty", header: "Open quantity", align: "end", render: (a) => qty(a.openQty, a.unit) },
  { key: "releaseCount", header: "Releases", align: "end" },
  { key: "lastRelease", header: "Last release", mono: true, render: (a) => (a.lastReleaseNumber ? `${a.lastReleaseNumber} (${a.lastRunDate})` : dash) },
  {
    key: "nextReleaseDue",
    header: "Next release due",
    mono: true,
    render: (a) => (a.nextReleaseDue ? `${a.nextReleaseDue}, ${frequencyLabels[a.releaseFrequency].toLowerCase()}` : "Not due"),
  },
];

/** Monitor scheduling agreement releases (FR-17.15, SAP ME85): open quantity, last release, next due. */
export function SchedulingAgreementsPage() {
  const navigate = useNavigate();
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<SchedulingAgreementRow[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listSchedulingAgreements({}, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [cursor],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Scheduling agreements"
        description="Dated delivery schedules with a vendor for one product and site. Release runs send the schedule as forecast (FRC) or just-in-time (JIT) releases with cumulative quantities."
        actions={
          <Link className={buttonClassName("primary")} to="/purchasing/scheduling-agreements/new">
            New scheduling agreement
          </Link>
        }
      />
      {page.error ? <LoadError what="scheduling agreements" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Scheduling agreements"
        columns={monitorColumns}
        rows={page.error ? [] : rows}
        getRowKey={(a) => a.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No scheduling agreements yet", description: "Agree a delivery schedule with a vendor, then release it to them on the agreed rhythm." }}
        onRowOpen={(a) => navigate(`/purchasing/scheduling-agreements/${a.id}`)}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

// --- Create ----------------------------------------------------------------

export interface SAHeaderDraft {
  legalEntityId: string;
  vendorId: string;
  productId: string;
  siteId: string;
  currency: string;
  unit: string;
  unitPrice: string;
  targetQty: string;
  validFrom: string;
  validTo: string;
}

export interface ProfileDraft {
  frcHorizonDays: string;
  jitHorizonDays: string;
  firmZoneDays: string;
  tradeoffZoneDays: string;
  releaseFrequency: ReleaseFrequency;
}

export const defaultProfileDraft: ProfileDraft = {
  frcHorizonDays: "90",
  jitHorizonDays: "14",
  firmZoneDays: "14",
  tradeoffZoneDays: "28",
  releaseFrequency: "WEEKLY",
};

/** One row of a schedule editor. lineNo is set for a saved line; removed marks a saved line to drop. */
export interface RowDraft {
  key: string;
  lineNo?: number;
  deliveryDate: string;
  qty: string;
  reason: string;
  removed: boolean;
  /** The saved line as loaded, to tell which changes reduce it. */
  saved?: ScheduleLine;
}

const newRow = (deliveryDate = ""): RowDraft => ({ key: newIdempotencyKey(), deliveryDate, qty: "", reason: "", removed: false });

const dayPattern = /^\d+$/;

/** Mirrors the service's shape rules so most mistakes are caught before the round trip. */
export function validateSchedulingAgreement(h: SAHeaderDraft, p: ProfileDraft, rows: RowDraft[]): Errors {
  const errors: Errors = {};
  for (const [field, label] of [
    ["legalEntityId", "legal entity"],
    ["vendorId", "vendor"],
    ["productId", "product"],
    ["siteId", "site"],
  ] as const) {
    if (!uuidPattern.test(h[field].trim())) errors[field] = `Choose a ${label}.`;
  }
  if (!currencyPattern.test(h.currency)) errors.currency = "Use a three-letter ISO code such as USD.";
  if (h.unit.trim() === "") errors.unit = "Enter a unit.";
  if (!decimalPattern.test(h.unitPrice)) errors.unitPrice = "Enter the agreed price per unit.";
  if (h.targetQty !== "" && !decimalPattern.test(h.targetQty)) errors.targetQty = "Enter a quantity.";
  if (!datePattern.test(h.validFrom)) errors.validFrom = "Enter a date.";
  if (h.validTo !== "" && (!datePattern.test(h.validTo) || h.validTo < h.validFrom)) errors.validTo = "Enter a date on or after valid from.";
  for (const k of ["frcHorizonDays", "jitHorizonDays", "firmZoneDays", "tradeoffZoneDays"] as const) {
    if (!dayPattern.test(p[k])) errors[`profile.${k}`] = "Enter a whole number of days.";
  }
  if (!errors["profile.tradeoffZoneDays"] && !errors["profile.firmZoneDays"] && Number(p.tradeoffZoneDays) < Number(p.firmZoneDays)) {
    errors["profile.tradeoffZoneDays"] = "The trade-off zone cannot end before the firm zone.";
  }
  if (rows.length === 0) errors.lines = "Add at least one delivery.";
  rows.forEach((r, i) => {
    if (!datePattern.test(r.deliveryDate)) errors[`lines[${i}].deliveryDate`] = "Enter a date.";
    else if (r.deliveryDate < h.validFrom || (h.validTo !== "" && r.deliveryDate > h.validTo)) {
      errors[`lines[${i}].deliveryDate`] = "Keep the date inside the agreement validity.";
    }
    if (!decimalPattern.test(r.qty) || Number(r.qty) <= 0) errors[`lines[${i}].qty`] = "Enter a positive quantity.";
  });
  return errors;
}

function toCreateInput(h: SAHeaderDraft, p: ProfileDraft, rows: RowDraft[]): SchedulingAgreementInput {
  const profile: Partial<ReleaseProfile> = {
    frcHorizonDays: Number(p.frcHorizonDays),
    jitHorizonDays: Number(p.jitHorizonDays),
    firmZoneDays: Number(p.firmZoneDays),
    tradeoffZoneDays: Number(p.tradeoffZoneDays),
    releaseFrequency: p.releaseFrequency,
  };
  return {
    legalEntityId: h.legalEntityId.trim(),
    vendorId: h.vendorId.trim(),
    productId: h.productId.trim(),
    siteId: h.siteId.trim(),
    currency: h.currency,
    unit: h.unit.trim(),
    unitPrice: h.unitPrice.trim(),
    targetQty: h.targetQty.trim() === "" ? undefined : h.targetQty.trim(),
    validFrom: h.validFrom,
    validTo: h.validTo === "" ? undefined : h.validTo,
    profile,
    lines: rows.map((r) => ({ deliveryDate: r.deliveryDate, qty: r.qty.trim() })),
  };
}

/** New scheduling agreement: header, price term, release profile and the first schedule (FR-17.15). */
export function SchedulingAgreementCreatePage() {
  const navigate = useNavigate();
  const [header, setHeader] = useState<SAHeaderDraft>({
    legalEntityId: "",
    vendorId: "",
    productId: "",
    siteId: "",
    currency: "USD",
    unit: "EA",
    unitPrice: "",
    targetQty: "",
    validFrom: todayIso(),
    validTo: "",
  });
  const [profile, setProfile] = useState<ProfileDraft>(defaultProfileDraft);
  const [rows, setRows] = useState<RowDraft[]>(() => [newRow()]);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [header, profile, rows]);
  const setH = (patch: Partial<SAHeaderDraft>) => setHeader((h) => ({ ...h, ...patch }));
  const setP = (patch: Partial<ProfileDraft>) => setProfile((p) => ({ ...p, ...patch }));
  const update = (index: number, patch: Partial<RowDraft>) => setRows((rs) => rs.map((r, i) => (i === index ? { ...r, ...patch } : r)));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateSchedulingAgreement(header, profile, rows);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await createSchedulingAgreement(toCreateInput(header, profile, rows), key);
      navigate(`/purchasing/scheduling-agreements/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: string, label: string, field: keyof SAHeaderDraft, opts: { optional?: boolean; hint?: string; decimal?: boolean } = {}) => (
    <FormField id={id} label={label} optional={opts.optional} hint={opts.hint} error={errors[field]}>
      {(c) => <Input {...c} inputMode={opts.decimal ? "decimal" : undefined} value={header[field]} onChange={(e) => setH({ [field]: e.target.value })} />}
    </FormField>
  );
  const days = (id: string, label: string, field: keyof Omit<ProfileDraft, "releaseFrequency">) => (
    <FormField id={id} label={label} error={errors[`profile.${field}`]}>
      {(c) => <Input {...c} inputMode="numeric" value={profile[field]} onChange={(e) => setP({ [field]: e.target.value })} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New scheduling agreement"
        description="One vendor, product and site with an agreed price, a release profile and the dated quantities to deliver."
      />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Agreement">
          <div className={styles.grid}>
            <LookupFormField id="sa-legal-entity" label="Legal entity" entity="legalEntity" value={header.legalEntityId} error={errors.legalEntityId} onChange={(legalEntityId) => setH({ legalEntityId })} />
            <LookupFormField
              id="sa-vendor"
              label="Vendor"
              entity="vendor"
              scope={{ legalEntityId: header.legalEntityId || undefined }}
              value={header.vendorId}
              error={errors.vendorId}
              onChange={(vendorId) => setH({ vendorId })}
            />
            <LookupFormField
              id="sa-product"
              label="Product"
              entity="product"
              value={header.productId}
              error={errors.productId}
              onChange={(productId, option) => setHeader((h) => ({ ...h, productId, ...(option?.unit && (h.unit.trim() === "" || h.unit === "EA") ? { unit: option.unit } : {}) }))}
            />
            <LookupFormField
              id="sa-site"
              label="Site"
              entity="site"
              scope={{ legalEntityId: header.legalEntityId || undefined }}
              value={header.siteId}
              error={errors.siteId}
              onChange={(siteId) => setH({ siteId })}
            />
            <FormField id="sa-currency" label="Currency" error={errors.currency}>
              {(c) => <Input {...c} value={header.currency} maxLength={3} onChange={(e) => setH({ currency: e.target.value.toUpperCase() })} />}
            </FormField>
            {text("sa-unit", "Unit", "unit")}
            {text("sa-price", "Unit price", "unitPrice", { decimal: true })}
            {text("sa-target", "Target quantity", "targetQty", { optional: true, decimal: true })}
            <FormField id="sa-valid-from" label="Valid from" error={errors.validFrom}>
              {(c) => <Input {...c} type="date" value={header.validFrom} onChange={(e) => setH({ validFrom: e.target.value })} />}
            </FormField>
            <FormField id="sa-valid-to" label="Valid to" optional hint="Blank is open-ended" error={errors.validTo}>
              {(c) => <Input {...c} type="date" value={header.validTo} onChange={(e) => setH({ validTo: e.target.value })} />}
            </FormField>
          </div>
        </Card>
        <Card title="Release profile">
          <p className={styles.muted}>
            Lines up to the firm zone are firm: reducing them needs a reason code. Lines up to the trade-off zone warn when reduced. Later lines are forecast.
          </p>
          <div className={styles.grid}>
            {days("sa-firm", "Firm zone (days)", "firmZoneDays")}
            {days("sa-tradeoff", "Trade-off zone (days)", "tradeoffZoneDays")}
            {days("sa-jit", "JIT horizon (days)", "jitHorizonDays")}
            {days("sa-frc", "FRC horizon (days)", "frcHorizonDays")}
            <FormField id="sa-frequency" label="Release frequency">
              {(c) => (
                <Select {...c} value={profile.releaseFrequency} onChange={(e) => setP({ releaseFrequency: e.target.value as ReleaseFrequency })}>
                  {(Object.keys(frequencyLabels) as ReleaseFrequency[]).map((f) => (
                    <option key={f} value={f}>
                      {frequencyLabels[f]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </Card>
        <Card
          title="Schedule"
          actions={
            <Button size="sm" onClick={() => setRows((rs) => [...rs, newRow()])}>
              Add delivery
            </Button>
          }
        >
          {errors.lines ? <p className={styles.muted}>{errors.lines}</p> : null}
          {rows.map((row, i) => (
            <fieldset key={row.key} className={styles.lineCard} aria-label={`Delivery ${i + 1}`}>
              <div className={styles.lineHead}>
                <p className={styles.lineTitle}>Delivery {i + 1}</p>
                {rows.length > 1 ? (
                  <Button size="sm" variant="ghost" onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}>
                    Remove delivery {i + 1}
                  </Button>
                ) : null}
              </div>
              <div className={styles.grid}>
                <FormField id={`sa-line-${i}-date`} label="Delivery date" error={errors[`lines[${i}].deliveryDate`]}>
                  {(c) => <Input {...c} type="date" value={row.deliveryDate} onChange={(e) => update(i, { deliveryDate: e.target.value })} />}
                </FormField>
                <FormField id={`sa-line-${i}-qty`} label={`Quantity (${header.unit || "unit"})`} error={errors[`lines[${i}].qty`]}>
                  {(c) => <Input {...c} inputMode="decimal" value={row.qty} onChange={(e) => update(i, { qty: e.target.value })} />}
                </FormField>
              </div>
            </fieldset>
          ))}
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save scheduling agreement
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/scheduling-agreements">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

// --- Detail: schedule, editor, release run, releases ------------------------

/** True when the draft takes quantity off the saved line: less, a new date, or removed. */
export function reduces(row: RowDraft): boolean {
  if (!row.saved) return false;
  if (row.removed) return true;
  return row.deliveryDate !== row.saved.deliveryDate || (decimalPattern.test(row.qty) && Number(row.qty) < Number(row.saved.qty));
}

/** Shape rules plus BR-17.13 as the page knows it: a firm line needs a reason to be reduced, moved or removed. */
export function validateSchedule(rows: RowDraft[]): Errors {
  const errors: Errors = {};
  if (rows.filter((r) => !r.removed).length === 0) errors.lines = "Keep at least one delivery.";
  rows.forEach((r, i) => {
    if (!r.removed) {
      if (!datePattern.test(r.deliveryDate)) errors[`lines[${i}].deliveryDate`] = "Enter a date.";
      if (!decimalPattern.test(r.qty) || Number(r.qty) <= 0) errors[`lines[${i}].qty`] = "Enter a positive quantity.";
      else if (r.saved && Number(r.qty) < Number(r.saved.receivedQty)) {
        errors[`lines[${i}].qty`] = `${r.saved.receivedQty} were already received against this line.`;
      }
    } else if (r.saved && Number(r.saved.receivedQty) > 0) {
      errors[`lines[${i}].qty`] = "This line has receipts and cannot be removed.";
    }
    if (r.saved?.zone === "FIRM" && reduces(r) && r.reason.trim() === "") {
      errors[`lines[${i}].reason`] = "This line is in the firm zone: give a reason code to reduce, move or remove it.";
    }
  });
  return errors;
}

export function toScheduleInput(rows: RowDraft[]): ScheduleLineInput[] {
  return rows
    .filter((r) => r.saved || !r.removed)
    .map((r) => ({
      lineNo: r.lineNo,
      deliveryDate: r.removed && r.saved ? r.saved.deliveryDate : r.deliveryDate,
      qty: r.removed ? "0" : r.qty.trim(),
      reason: r.reason.trim() === "" ? undefined : r.reason.trim(),
    }));
}

const draftsOf = (a: SchedulingAgreement): RowDraft[] =>
  a.lines.map((l) => ({ key: `line-${l.lineNo}`, lineNo: l.lineNo, deliveryDate: l.deliveryDate, qty: l.qty, reason: "", removed: false, saved: l }));

const scheduleColumns = (unit: string): DataTableColumn<ScheduleLine>[] => [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "deliveryDate", header: "Delivery", mono: true },
  { key: "zone", header: "Zone", render: (l) => <ZoneBadge zone={l.zone} /> },
  { key: "qty", header: "Scheduled", align: "end", render: (l) => qty(l.qty, unit) },
  { key: "receivedQty", header: "Received", align: "end", render: (l) => qty(l.receivedQty, unit) },
  { key: "openQty", header: "Open", align: "end", render: (l) => qty(l.openQty, unit) },
  { key: "reductionReason", header: "Last reduction reason", render: (l) => l.reductionReason ?? dash },
];

function ScheduleEditor({ agreement, onSaved, onCancel }: { agreement: SchedulingAgreement; onSaved(a: SchedulingAgreement): void; onCancel(): void }) {
  const [rows, setRows] = useState<RowDraft[]>(() => draftsOf(agreement));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const update = (index: number, patch: Partial<RowDraft>) => setRows((rs) => rs.map((r, i) => (i === index ? { ...r, ...patch } : r)));

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const found = validateSchedule(rows);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const { body } = await replaceSchedule(agreement.id, toScheduleInput(rows), agreement.version);
      onSaved(body);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={onSubmit} noValidate aria-label="Edit schedule">
      <Card
        title="Edit schedule"
        actions={
          <Button size="sm" onClick={() => setRows((rs) => [...rs, newRow()])}>
            Add delivery
          </Button>
        }
      >
        <p className={styles.muted}>Zones as of {agreement.zonesAsOf}. Reducing, moving or removing a firm line needs a reason code; trade-off reductions warn.</p>
        {errors.lines ? <p className={styles.muted}>{errors.lines}</p> : null}
        {rows.map((row, i) => {
          const title = row.lineNo ? `Line ${row.lineNo}` : `New delivery ${i + 1}`;
          const err = (n: string) => errors[`lines[${i}].${n}`];
          return (
            <fieldset key={row.key} className={styles.lineCard} aria-label={title}>
              <div className={styles.lineHead}>
                <p className={styles.lineTitle}>
                  {title} {row.saved ? <ZoneBadge zone={row.saved.zone} /> : null} {row.removed ? <Badge tone="danger">Removed</Badge> : null}
                </p>
                {row.saved ? (
                  <Button size="sm" variant="ghost" onClick={() => update(i, { removed: !row.removed })}>
                    {row.removed ? `Keep ${title.toLowerCase()}` : `Remove ${title.toLowerCase()}`}
                  </Button>
                ) : (
                  <Button size="sm" variant="ghost" onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}>
                    Discard {title.toLowerCase()}
                  </Button>
                )}
              </div>
              <div className={styles.grid}>
                {!row.removed ? (
                  <>
                    <FormField id={`sched-${row.key}-date`} label="Delivery date" error={err("deliveryDate")}>
                      {(c) => <Input {...c} type="date" value={row.deliveryDate} onChange={(e) => update(i, { deliveryDate: e.target.value })} />}
                    </FormField>
                    <FormField
                      id={`sched-${row.key}-qty`}
                      label={`Quantity (${agreement.unit})`}
                      hint={row.saved && row.saved.receivedQty !== "0" ? `${row.saved.receivedQty} received` : undefined}
                      error={err("qty")}
                    >
                      {(c) => <Input {...c} inputMode="decimal" value={row.qty} onChange={(e) => update(i, { qty: e.target.value })} />}
                    </FormField>
                  </>
                ) : err("qty") ? (
                  <p className={styles.muted}>{err("qty")}</p>
                ) : null}
                {row.saved ? (
                  <FormField
                    id={`sched-${row.key}-reason`}
                    label="Reason code"
                    optional={row.saved.zone !== "FIRM"}
                    hint={row.saved.zone === "FIRM" ? "Required to reduce, move or remove" : undefined}
                    error={err("reason")}
                  >
                    {(c) => <Input {...c} maxLength={64} value={row.reason} onChange={(e) => update(i, { reason: e.target.value })} />}
                  </FormField>
                ) : null}
              </div>
            </fieldset>
          );
        })}
      </Card>
      <Alert tone="danger" live="alert">
        {failure ? <ProblemMessage problem={failure} /> : null}
      </Alert>
      <div className={styles.actions}>
        <Button type="submit" variant="primary" loading={busy}>
          Save schedule
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function ReleaseRun({ agreement, onReleased }: { agreement: SchedulingAgreement; onReleased(r: SchedulingRelease): void }) {
  const [kind, setKind] = useState<ReleaseKind>("JIT");
  const [runDate, setRunDate] = useState(todayIso());
  const [reset, setReset] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [kind, runDate, reset, agreement.version]);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!datePattern.test(runDate)) {
      setError("Enter the run date.");
      return;
    }
    setError(undefined);
    setBusy(true);
    try {
      const { body } = await runSchedulingRelease(agreement.id, { kind, runDate, cumulativeReset: reset || undefined }, key);
      onReleased(body);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={onSubmit} noValidate aria-label="Run a release">
      <Card title="Run a release">
        <p className={styles.muted}>
          Sends the whole schedule as the next release with cumulative released and received quantities. Running again on the same day with an unchanged schedule
          returns the same release.
        </p>
        <div className={styles.grid}>
          <FormField id="rel-kind" label="Release kind">
            {(c) => (
              <Select {...c} value={kind} onChange={(e) => setKind(e.target.value as ReleaseKind)}>
                {(Object.keys(kindLabels) as ReleaseKind[]).map((k) => (
                  <option key={k} value={k}>
                    {kindLabels[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rel-date" label="Run date" error={error}>
            {(c) => <Input {...c} type="date" value={runDate} onChange={(e) => setRunDate(e.target.value)} />}
          </FormField>
        </div>
        <div className={styles.checkboxRow}>
          <Input id="rel-reset" type="checkbox" checked={reset} onChange={(e) => setReset(e.target.checked)} />
          <label htmlFor="rel-reset">Record a cumulative reset (new agreement year): cumulative released may go below the last release</label>
        </div>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Run release
          </Button>
        </div>
      </Card>
    </form>
  );
}

const releaseColumns = (agreement: SchedulingAgreement): DataTableColumn<SchedulingRelease>[] => [
  {
    key: "number",
    header: "Release",
    render: (r) => (
      <Link className={styles.link} to={`/purchasing/scheduling-agreements/${agreement.id}/releases/${r.releaseNo}`}>
        {r.number}
      </Link>
    ),
  },
  { key: "kind", header: "Kind", render: (r) => kindLabels[r.kind] },
  { key: "runDate", header: "Run date", mono: true },
  { key: "cumulativeReleased", header: "Cumulative released", align: "end", render: (r) => qty(r.cumulativeReleased, agreement.unit) },
  { key: "cumulativeReceived", header: "Cumulative received", align: "end", render: (r) => qty(r.cumulativeReceived, agreement.unit) },
  { key: "cumulativeReset", header: "Reset", render: (r) => (r.cumulativeReset ? <Badge tone="warning">Cumulative reset</Badge> : dash) },
];

/** One scheduling agreement: summary, schedule with zones, schedule editor, release run and releases. */
export function SchedulingAgreementDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource((signal) => getSchedulingAgreement(id, signal), [id]);
  const [releaseTick, setReleaseTick] = useState(0);
  const releases = useResource((signal) => listSchedulingReleases(id, signal), [id, releaseTick]);
  const [changed, setChanged] = useState<SchedulingAgreement | null>(null);
  const [editing, setEditing] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const agreement = changed ?? resource.data?.body ?? null;

  if (resource.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Scheduling agreement" />
        {resource.error.code === "PRC-SA-NOT-FOUND" ? (
          <Card>
            <EmptyState
              icon="receipt"
              title="Scheduling agreement not found"
              description="It may belong to another legal entity, or the link is wrong."
              action={
                <Link className={buttonClassName("secondary")} to="/purchasing/scheduling-agreements">
                  Back to scheduling agreements
                </Link>
              }
            />
          </Card>
        ) : (
          <LoadError what="the scheduling agreement" error={resource.error} onRetry={resource.reload} />
        )}
      </div>
    );
  }
  if (!agreement) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Scheduling agreement" />
        <p className={styles.muted} role="status">
          Loading scheduling agreement…
        </p>
      </div>
    );
  }

  const p = agreement.profile;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Scheduling agreement ${agreement.number}`}
        meta={<Badge tone="neutral">Version {agreement.version}</Badge>}
        actions={
          editing ? undefined : (
            <Button
              onClick={() => {
                setNotice(null);
                setEditing(true);
              }}
            >
              Edit schedule
            </Button>
          )
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {agreement.warnings && agreement.warnings.length > 0 ? (
        <Alert tone="warning" title="Trade-off zone reductions">
          {agreement.warnings.join(" ")}
        </Alert>
      ) : null}
      <Card title="Summary">
        <Details
          items={[
            { label: "Vendor", value: <Named entity="vendor" id={agreement.vendorId} /> },
            { label: "Product", value: <Named entity="product" id={agreement.productId} /> },
            { label: "Site", value: <Named entity="site" id={agreement.siteId} /> },
            { label: "Legal entity", value: <Named entity="legalEntity" id={agreement.legalEntityId} /> },
            { label: "Price", value: `${money(agreement.unitPrice, agreement.currency)} per ${agreement.unit}`, mono: true },
            { label: "Validity", value: validity(agreement), mono: true },
            { label: "Target quantity", value: qty(agreement.targetQty, agreement.unit), mono: true },
            { label: "Scheduled", value: qty(agreement.scheduledQty, agreement.unit), mono: true },
            { label: "Received", value: qty(agreement.receivedQty, agreement.unit), mono: true },
            { label: "Zones", value: `Firm ${p.firmZoneDays} days, trade-off ${p.tradeoffZoneDays} days`, mono: true },
            { label: "Horizons", value: `JIT ${p.jitHorizonDays} days, FRC ${p.frcHorizonDays} days`, mono: true },
            { label: "Release frequency", value: frequencyLabels[p.releaseFrequency] },
          ]}
        />
      </Card>
      {editing ? (
        <ScheduleEditor
          agreement={agreement}
          onCancel={() => setEditing(false)}
          onSaved={(a) => {
            setChanged(a);
            setEditing(false);
            setNotice(`Schedule saved as version ${a.version}.`);
          }}
        />
      ) : (
        <DataTable label={`Schedule (zones as of ${agreement.zonesAsOf})`} columns={scheduleColumns(agreement.unit)} rows={agreement.lines} getRowKey={(l) => String(l.lineNo)} />
      )}
      <ReleaseRun
        agreement={agreement}
        onReleased={(r) => {
          const existed = releases.data?.some((x) => x.id === r.id) ?? false;
          setNotice(
            existed
              ? `The schedule has not changed since ${r.number}; no new release was created.`
              : `Release ${r.number} created: cumulative released ${qty(r.cumulativeReleased, agreement.unit)}.`,
          );
          setReleaseTick((n) => n + 1);
        }}
      />
      {releases.error ? <LoadError what="the releases" error={releases.error} onRetry={releases.reload} /> : null}
      <DataTable
        label="Releases"
        columns={releaseColumns(agreement)}
        rows={releases.data ?? []}
        getRowKey={(r) => r.id}
        loading={releases.loading && !releases.data}
        empty={{ title: "No releases yet", description: "Run a release to send the schedule to the vendor." }}
      />
    </div>
  );
}

// --- Release documentation ---------------------------------------------------

const releaseLineColumns = (unit: string): DataTableColumn<SchedulingReleaseLine>[] => [
  { key: "lineNo", header: "Line", align: "end" },
  { key: "deliveryDate", header: "Delivery", mono: true },
  { key: "zone", header: "Zone", render: (l) => <ZoneBadge zone={l.zone} /> },
  { key: "inHorizon", header: "In horizon", render: (l) => (l.inHorizon ? "Yes" : "No") },
  { key: "qty", header: "Quantity", align: "end", render: (l) => qty(l.qty, unit) },
  { key: "cumulativeReleased", header: "Cumulative released", align: "end", render: (l) => qty(l.cumulativeReleased, unit) },
  { key: "cumulativeReceived", header: "Cumulative received", align: "end", render: (l) => qty(l.cumulativeReceived, unit) },
];

/** Release documentation for one release number (SAP ME86). */
export function SchedulingReleasePage() {
  const { id = "", releaseNo = "" } = useParams();
  const agreement = useResource((signal) => getSchedulingAgreement(id, signal), [id]);
  const release = useResource((signal) => getSchedulingRelease(id, Number(releaseNo), signal), [id, releaseNo]);
  const unit = agreement.data?.body.unit ?? "";
  const back = (
    <Link className={buttonClassName("secondary")} to={`/purchasing/scheduling-agreements/${id}`}>
      Back to the agreement
    </Link>
  );

  if (release.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Scheduling agreement release" />
        {release.error.code === "PRC-SA-REL-NOT-FOUND" || release.error.code === "PRC-SA-NOT-FOUND" ? (
          <Card>
            <EmptyState icon="receipt" title="Release not found" description="The agreement has no release with this number." action={back} />
          </Card>
        ) : (
          <LoadError what="the release" error={release.error} onRetry={release.reload} />
        )}
      </div>
    );
  }
  const r = release.data?.body;
  if (!r) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Scheduling agreement release" />
        <p className={styles.muted} role="status">
          Loading release…
        </p>
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title={`Release ${r.number}`} meta={<Badge tone="info">{kindLabels[r.kind]}</Badge>} actions={back} />
      <Card title="Release">
        <Details
          items={[
            { label: "Agreement", value: agreement.data?.body.number ?? r.agreementId, mono: true },
            { label: "Run date", value: r.runDate, mono: true },
            { label: "Schedule version", value: String(r.agreementVersion), mono: true },
            { label: "Cumulative released", value: qty(r.cumulativeReleased, unit).trim(), mono: true },
            { label: "Cumulative received", value: qty(r.cumulativeReceived, unit).trim(), mono: true },
            { label: "Cumulative reset", value: r.cumulativeReset ? "Yes" : "No" },
          ]}
        />
      </Card>
      <DataTable label="Release lines" columns={releaseLineColumns(unit)} rows={r.lines ?? []} getRowKey={(l) => String(l.lineNo)} />
    </div>
  );
}
