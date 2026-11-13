import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  FormField,
  Input,
  ListPage,
  loadedRows,
  PageHeader,
  Select,
  Skeleton,
  StatusPill,
  type DataTableColumn,
  FormErrorSummary,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
} from "../../shell/ui";
import {
  createScheduleRelease,
  describeProblem,
  etagOf,
  getAgreement,
  listScheduleReleases,
  newKey,
  RELEASE_TYPES,
  recordAgreementShipped,
  type Problem,
  type ReleaseBucket,
  type ReleaseType,
  type ScheduleRelease,
  type ScheduleReleaseInput,
} from "./api/client";
import { DECIMAL, formatDateTime, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface ScheduleReleaseValues {
  releaseNo: string;
  releaseType: ReleaseType;
  cumReceived: string;
  buckets: ReleaseBucket[];
}

/** Checks that mirror the service (api/schedule_releases.go buildScheduleRelease). Keys are the service's field paths. */
export function validateScheduleRelease(values: ScheduleReleaseValues): { errors: Errors; input?: ScheduleReleaseInput } {
  const errors: Errors = {};
  const no = values.releaseNo.trim();
  if (no.length < 1 || no.length > 40) errors.releaseNo = "Enter the customer's release number (up to 40 characters).";
  if (!DECIMAL.test(values.cumReceived)) errors.cumReceived = "Enter the customer's cumulative received quantity (0 or more).";
  if (values.buckets.length === 0) errors.buckets = "Add at least one requirement date.";
  values.buckets.forEach((b, i) => {
    const at = (name: string) => `buckets[${i}].${name}`;
    const prev = values.buckets[i - 1];
    if (!DATE.test(b.date)) errors[at("date")] = "Choose the requirement date.";
    else if (prev && DATE.test(prev.date) && b.date < prev.date) errors[at("date")] = "Use a date on or after the previous one.";
    if (!DECIMAL.test(b.cumRequired)) errors[at("cumRequired")] = "Enter 0 or more.";
    else if (prev && DECIMAL.test(prev.cumRequired) && Number(b.cumRequired) < Number(prev.cumRequired)) errors[at("cumRequired")] = "Cumulative required may not go down.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { releaseNo: no, releaseType: values.releaseType, cumReceived: values.cumReceived, buckets: values.buckets } };
}

const columns: DataTableColumn<ScheduleRelease>[] = [
  { key: "releaseNo", header: "Release", mono: true },
  { key: "releaseType", header: "Type", render: (r) => sentence(r.releaseType) },
  { key: "cumReceived", header: "Cum. received", align: "end" },
  { key: "cumShipped", header: "Cum. shipped", align: "end" },
  { key: "difference", header: "Difference", align: "end" },
  { key: "openQuantity", header: "Open", align: "end" },
  { key: "receivedAt", header: "Received", render: (r) => formatDateTime(r.receivedAt) },
  { key: "status", header: "Status", render: (r) => <StatusPill status={r.status} /> },
];

/** Stored scheduling releases of one agreement, with the cumulative shipped quantity (FR-27.20, BR-27.12). */
export function ScheduleReleasesPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const agreement = useLoad((signal) => getAgreement(id, signal), id);
  // Bumped after the shipped quantity is recorded, so the list reads the recalculated releases.
  const [reloads, setReloads] = useState(0);
  const useReleaseRows = () => loadedRows(useLoad((signal) => listScheduleReleases(id, signal), `${id}-${reloads}`));
  const [shipped, setShipped] = useState("");
  const [reset, setReset] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Errors = {};
    if (!DECIMAL.test(shipped)) found.cumShipped = "Enter the cumulative shipped quantity (0 or more).";
    if (reset && !DATE.test(reset)) found.cumResetDate = "Choose a date.";
    setErrors(found);
    setFailure(null);
    setDone(false);
    if (Object.keys(found).length > 0 || !agreement.data) return;
    setBusy(true);
    try {
      await recordAgreementShipped(id, etagOf(agreement.data.value.version), shipped, reset || undefined);
      setDone(true);
      agreement.reload();
      setReloads((n) => n + 1);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const a = agreement.data?.value;
  return (
    <ListPage
      id="sales-schedule-releases"
      title={a ? `Scheduling releases for ${a.agreementNumber}` : "Scheduling releases"}
      description="Forecast and JIT releases from the customer. A new release replaces the open schedule lines of the previous one; a cumulative quantity that differs from shipped beyond the tolerance is held for review."
      actions={
        <>
          <Link className={buttonClassName("ghost")} to={`/sales/agreements/${id}`}>
            Back to agreement
          </Link>
          {a?.status === "CLOSED" ? null : (
            <Link className={buttonClassName("primary")} to={`/sales/agreements/${id}/schedule-releases/new`}>
              New release
            </Link>
          )}
        </>
      }
      notice={
        <>
      {agreement.error && !a ? (
        <Alert tone="danger" title="Could not load the agreement" live="alert" action={<Button size="sm" onClick={agreement.reload}>Try again</Button>}>
          <ProblemMessage problem={agreement.error} />
        </Alert>
      ) : !a ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading the agreement…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <Card title="Cumulative shipped">
          <dl className={styles.details}>
            {(
              [
                ["Shipped since reset", `${a.cumShipped ?? "0"} ${a.unit}`],
                ["Reset date", a.cumResetDate ?? "Not set"],
                ["Tolerance", `${a.cumTolerance ?? "0"} ${a.unit}`],
              ] as const
            ).map(([term, value]) => (
              <div key={term} className={styles.entry}>
                <dt className={styles.term}>{term}</dt>
                <dd className={styles.value}>{value}</dd>
              </div>
            ))}
          </dl>
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Record shipped quantity">
            <FormField id="shipped-qty" label="New cumulative shipped" hint="Until fulfillment reports shipments, enter the total shipped since the reset date." error={errors.cumShipped}>
              {(control) => <Input {...control} inputMode="decimal" value={shipped} onChange={(e) => setShipped(e.target.value)} invalid={Boolean(errors.cumShipped)} />}
            </FormField>
            <FormField id="shipped-reset" label="New reset date" optional hint="A later date starts a new cumulative period and lets the quantity fall." error={errors.cumResetDate}>
              {(control) => <Input {...control} type="date" value={reset} onChange={(e) => setReset(e.target.value)} invalid={Boolean(errors.cumResetDate)} />}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not record the shipped quantity" live="alert">
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" loading={busy}>
                Record shipped quantity
              </Button>
            </div>
          </form>
          <Alert tone="success" live="status">
            {done ? "Shipped quantity recorded. The open schedule lines of the release in effect, if any, were recalculated." : null}
          </Alert>
        </Card>
      )}
        </>
      }
      noun="releases"
      singular="release"
      columns={columns}
      useRows={useReleaseRows}
      getRowKey={(r) => r.id}
      onRowOpen={(r) => navigate(`/sales/agreements/${id}/schedule-releases/${r.id}`)}
      empty={{ title: "No releases yet", description: "Enter the customer's first forecast or JIT release to create open schedule lines." }}
    />
  );
}

const NEW_SCHEDULE_RELEASE_FIELDS: Record<string, FormFieldTarget> = {
  releaseNo: { id: "release-no", label: "Release number" },
  releaseType: { id: "release-type", label: "Type" },
  cumReceived: { id: "release-received", label: "Cumulative received (customer)" },
};

/** Stores a new release. A held release (cumulative mismatch) is a result to review, not a failure. */
export function NewScheduleReleasePage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [values, setValues] = useState<ScheduleReleaseValues>({ releaseNo: "", releaseType: "FORECAST", cumReceived: "", buckets: [{ date: "", cumRequired: "" }] });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));

  function update(patch: Partial<ScheduleReleaseValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }
  function updateBucket(i: number, patch: Partial<ReleaseBucket>) {
    update({ buckets: values.buckets.map((b, idx) => (idx === i ? { ...b, ...patch } : b)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateScheduleRelease(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await createScheduleRelease(id, input, key.current);
      release();
      navigate(`/sales/agreements/${id}/schedule-releases/${saved.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New scheduling release" description="Enter the customer's release as received. It is reconciled against the shipped quantity when you save." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New scheduling release">
          <FormErrorSummary items={summaryItems(errors, NEW_SCHEDULE_RELEASE_FIELDS)} attempt={attempt} />
          <FormField id="release-no" label="Release number" hint="The customer's number for this release." error={errors.releaseNo}>
            {(control) => <Input {...control} value={values.releaseNo} onChange={(e) => update({ releaseNo: e.target.value })} invalid={Boolean(errors.releaseNo)} />}
          </FormField>
          <FormField id="release-type" label="Type" error={errors.releaseType}>
            {(control) => (
              <Select {...control} value={values.releaseType} onChange={(e) => update({ releaseType: e.target.value as ReleaseType })}>
                {RELEASE_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="release-received" label="Cumulative received (customer)" error={errors.cumReceived}>
            {(control) => <Input {...control} inputMode="decimal" value={values.cumReceived} onChange={(e) => update({ cumReceived: e.target.value })} invalid={Boolean(errors.cumReceived)} />}
          </FormField>
          <div className={`${styles.formActions} ${styles.lines}`}>
            {values.buckets.map((b, i) => (
              <fieldset key={i} className={styles.lineCard}>
                <legend className={styles.lineLegend}>Requirement {i + 1}</legend>
                <FormField id={`release-${i}-date`} label="Date" error={errors[`buckets[${i}].date`]}>
                  {(control) => <Input {...control} type="date" value={b.date} onChange={(e) => updateBucket(i, { date: e.target.value })} invalid={Boolean(errors[`buckets[${i}].date`])} />}
                </FormField>
                <FormField id={`release-${i}-cum`} label="Cumulative required" error={errors[`buckets[${i}].cumRequired`]}>
                  {(control) => <Input {...control} inputMode="decimal" value={b.cumRequired} onChange={(e) => updateBucket(i, { cumRequired: e.target.value })} invalid={Boolean(errors[`buckets[${i}].cumRequired`])} />}
                </FormField>
                {values.buckets.length > 1 ? (
                  <div className={styles.formActions}>
                    <Button size="sm" variant="ghost" onClick={() => update({ buckets: values.buckets.filter((_, idx) => idx !== i) })}>
                      Remove requirement {i + 1}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            ))}
            {errors.buckets ? (
              <p role="alert" className={styles.fieldError}>
                {errors.buckets}
              </p>
            ) : null}
            <div>
              <Button onClick={() => update({ buckets: [...values.buckets, { date: "", cumRequired: "" }] })}>Add requirement</Button>
            </div>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not store the release" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Store release
            </Button>
            <Link className={buttonClassName("ghost")} to={`/sales/agreements/${id}/schedule-releases`}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}
