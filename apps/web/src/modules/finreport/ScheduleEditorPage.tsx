import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, FormField, Input, PageHeader, Select, Skeleton, Textarea } from "../../shell/ui";
import {
  CURRENCY_TYPES,
  describeProblem,
  getSchedule,
  listDefinitions,
  OUTPUT_FORMATS,
  saveSchedule,
  type CurrencyType,
  type DefinitionSummary,
  type OutputFormat,
  type Problem,
  type Schedule,
  type ScheduleInput,
} from "./api/client";
import { CURRENCY_TYPE_LABELS, DEFINITION_CODE, MONTH_NAMES, parseReferences, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

/** Form state: lists stay text while the user types. */
export interface ScheduleValues {
  code: string;
  name: string;
  definition: string;
  cron: string;
  timezone: string;
  ledgerIds: string;
  currencyType: CurrencyType;
  formats: OutputFormat[];
  recipients: string;
  attachmentLimitMb: string;
  enabled: boolean;
  fiscalYearStartMonth: string;
}

export function blankSchedule(): ScheduleValues {
  return {
    code: "",
    name: "",
    definition: "",
    cron: "0 23 * * *",
    timezone: "UTC",
    ledgerIds: "",
    currencyType: "acct",
    formats: ["pdf", "xlsx"],
    recipients: "",
    attachmentLimitMb: "20",
    enabled: true,
    fiscalYearStartMonth: "1",
  };
}

export function toValues(s: Schedule): ScheduleValues {
  return {
    code: s.code,
    name: s.name,
    definition: s.definition,
    cron: s.cron,
    timezone: s.timezone,
    ledgerIds: s.ledgerIds.join("\n"),
    currencyType: s.currencyType,
    formats: s.formats,
    recipients: s.recipients.join("\n"),
    attachmentLimitMb: String(s.attachmentLimitMb),
    enabled: s.enabled,
    fiscalYearStartMonth: String(s.fiscalYearStartMonth ?? 1),
  };
}

export function toSchedule(v: ScheduleValues): ScheduleInput {
  return {
    code: v.code.trim(),
    name: v.name.trim(),
    definition: v.definition.trim(),
    cron: v.cron.trim().replace(/\s+/g, " "),
    timezone: v.timezone.trim(),
    ledgerIds: parseReferences(v.ledgerIds),
    currencyType: v.currencyType,
    formats: OUTPUT_FORMATS.filter((f) => v.formats.includes(f)),
    recipients: parseReferences(v.recipients),
    attachmentLimitMb: Number(v.attachmentLimitMb),
    enabled: v.enabled,
    fiscalYearStartMonth: Number(v.fiscalYearStartMonth),
  };
}

export type ScheduleErrors = Partial<Record<keyof ScheduleValues, string>>;

const EMAIL = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/;

/** The checks the service makes that need no server; the service checks cron and time zone itself (REPORT-0018). */
export function validateSchedule(v: ScheduleValues): ScheduleErrors {
  const s = toSchedule(v);
  const errors: ScheduleErrors = {};
  if (!DEFINITION_CODE.test(s.code)) errors.code = "Use 2 to 30 upper-case letters, digits or hyphens, starting with a letter.";
  if (s.name === "") errors.name = "Enter a name.";
  if (s.definition === "") errors.definition = "Choose a published definition.";
  if (s.cron.split(" ").length !== 5) errors.cron = "Enter five fields: minute, hour, day of month, month and weekday.";
  if (s.timezone === "") errors.timezone = "Enter a time zone such as Europe/Berlin.";
  if (s.ledgerIds.length === 0 || s.ledgerIds.length > 50) errors.ledgerIds = "List 1 to 50 ledger ids.";
  else if (s.ledgerIds.some((id) => !UUID.test(id))) errors.ledgerIds = "Each ledger id is a UUID.";
  else if (new Set(s.ledgerIds).size !== s.ledgerIds.length) errors.ledgerIds = "List each ledger once.";
  if (s.formats.length === 0) errors.formats = "Choose at least one format.";
  if (s.recipients.length === 0 || s.recipients.length > 50) errors.recipients = "List 1 to 50 email addresses.";
  else if (s.recipients.some((r) => !EMAIL.test(r))) errors.recipients = "Each recipient is a plain email address, such as controller@example.com.";
  if (!Number.isInteger(s.attachmentLimitMb) || s.attachmentLimitMb < 1 || s.attachmentLimitMb > 25) errors.attachmentLimitMb = "Enter a whole number from 1 to 25.";
  if (!Number.isInteger(s.fiscalYearStartMonth) || s.fiscalYearStartMonth < 1 || s.fiscalYearStartMonth > 12) errors.fiscalYearStartMonth = "Choose a month.";
  return errors;
}

/** Create or edit a distribution schedule (FR-15.8). Edits send the ETag read as If-Match. */
export function ScheduleEditorPage() {
  const { code: routeCode } = useParams();
  const isNew = routeCode === undefined;
  const navigate = useNavigate();
  const [values, setValues] = useState<ScheduleValues | null>(isNew ? blankSchedule() : null);
  const [saved, setSaved] = useState<Schedule | null>(null);
  const [etag, setEtag] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<Problem | null>(null);
  const [attempt, setAttempt] = useState(0);
  // null while loading; "failed" falls back to typing the code.
  const [definitions, setDefinitions] = useState<DefinitionSummary[] | null | "failed">(null);
  const [errors, setErrors] = useState<ScheduleErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    listDefinitions(controller.signal).then(
      (list) => setDefinitions(list.filter((d) => d.status === "published")),
      () => {
        if (!controller.signal.aborted) setDefinitions("failed");
      },
    );
    return () => controller.abort();
  }, []);

  useEffect(() => {
    if (isNew) {
      setValues(blankSchedule());
      setSaved(null);
      setEtag(null);
      return;
    }
    const controller = new AbortController();
    setValues(null);
    setLoadError(null);
    getSchedule(routeCode ?? "", controller.signal).then(
      (res) => {
        setValues(toValues(res.schedule));
        setSaved(res.schedule);
        setEtag(res.etag);
      },
      (error: unknown) => {
        if (!controller.signal.aborted) setLoadError(describeProblem(error));
      },
    );
    return () => controller.abort();
  }, [routeCode, isNew, attempt]);

  const title = isNew ? "New schedule" : `Schedule ${routeCode}`;
  if (loadError) {
    const missing = loadError.status === 404;
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Alert
          tone="danger"
          title={missing ? "This schedule does not exist" : "This schedule could not be loaded"}
          live="alert"
          action={missing ? undefined : <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>Try again</Button>}
        >
          <ProblemMessage problem={loadError} />
        </Alert>
        <Link to="/finreport/schedules">Back to schedules</Link>
      </div>
    );
  }
  if (!values) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <p className={styles.note}>Loading schedule…</p>
        <Skeleton width="full" height="tile" />
      </div>
    );
  }
  const v = values;

  function change(patch: Partial<ScheduleValues>) {
    setValues((prev) => (prev ? { ...prev, ...patch } : prev));
    setNotice(null);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateSchedule(v);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    setFailure(null);
    try {
      const res = await saveSchedule(toSchedule(v), etag);
      setSaved(res.schedule);
      setEtag(res.etag);
      setValues(toValues(res.schedule));
      setNotice("Schedule saved.");
      if (isNew) navigate(`/finreport/schedules/${encodeURIComponent(res.schedule.code)}`, { replace: true });
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "conflict_version" || failure?.code === "precondition_required";
  // A schedule whose definition is no longer published keeps it selectable, so the form still shows it.
  const published = Array.isArray(definitions) ? definitions.map((d) => ({ code: d.code, name: d.name })) : [];
  const choices = v.definition !== "" && !published.some((d) => d.code === v.definition) ? [...published, { code: v.definition, name: "not published" }] : published;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description="Runs the latest published version of a definition for each ledger on its timetable, stores the CSV, HTML and XLSX files in the report library and hands them to the recipients. Times use the schedule's time zone."
        meta={saved ? saved.enabled ? <Badge tone="success">Enabled</Badge> : <Badge tone="neutral">Paused</Badge> : undefined}
      />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="What and when">
          <div className={styles.form}>
            <FormField id="fr-s-code" label="Schedule code" hint={isNew ? "Upper-case letters, digits and hyphens, such as NIGHTLY-PACK." : undefined} error={errors.code}>
              {(c) => <Input {...c} className={styles.mono} value={v.code} readOnly={!isNew} invalid={Boolean(errors.code)} onChange={(e) => change({ code: e.target.value.toUpperCase() })} />}
            </FormField>
            <FormField id="fr-s-name" label="Name" error={errors.name}>
              {(c) => <Input {...c} value={v.name} invalid={Boolean(errors.name)} onChange={(e) => change({ name: e.target.value })} />}
            </FormField>
            <FormField id="fr-s-definition" label="Definition" hint="Only published definitions can be scheduled." error={errors.definition}>
              {(c) =>
                definitions !== "failed" ? (
                  <Select {...c} value={v.definition} invalid={Boolean(errors.definition)} onChange={(e) => change({ definition: e.target.value })}>
                    <option value="">{definitions === null ? "Loading definitions…" : "Choose a definition"}</option>
                    {choices.map((d) => (
                      <option key={d.code} value={d.code}>
                        {`${d.code}: ${d.name}`}
                      </option>
                    ))}
                  </Select>
                ) : (
                  <Input {...c} className={styles.mono} value={v.definition} invalid={Boolean(errors.definition)} onChange={(e) => change({ definition: e.target.value.toUpperCase() })} />
                )
              }
            </FormField>
            <FormField id="fr-s-currency" label="Currency type">
              {(c) => (
                <Select {...c} value={v.currencyType} onChange={(e) => change({ currencyType: e.target.value as CurrencyType })}>
                  {CURRENCY_TYPES.map((t) => (
                    <option key={t} value={t}>
                      {CURRENCY_TYPE_LABELS[t]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="fr-s-cron" label="Cron" hint="Minute, hour, day of month, month, weekday. 0 23 * * * is every night at 23:00." error={errors.cron}>
              {(c) => <Input {...c} className={styles.mono} value={v.cron} invalid={Boolean(errors.cron)} onChange={(e) => change({ cron: e.target.value })} />}
            </FormField>
            <FormField id="fr-s-tz" label="Time zone" hint="An IANA name, such as Europe/Berlin or UTC." error={errors.timezone}>
              {(c) => <Input {...c} className={styles.mono} value={v.timezone} invalid={Boolean(errors.timezone)} onChange={(e) => change({ timezone: e.target.value })} />}
            </FormField>
            <FormField id="fr-s-ledgers" label="Ledgers" hint="One ledger id per line. The statement runs once per ledger." error={errors.ledgerIds}>
              {(c) => <Textarea {...c} className={styles.mono} rows={3} value={v.ledgerIds} invalid={Boolean(errors.ledgerIds)} onChange={(e) => change({ ledgerIds: e.target.value })} />}
            </FormField>
            <FormField
              id="fr-s-fy"
              label="Fiscal year starts in"
              hint="Each firing runs for the month before it fires, so a run on 1 April is March's statement. This month places it in the fiscal year until the ledger calendar is available."
              error={errors.fiscalYearStartMonth}
            >
              {(c) => (
                <Select {...c} value={v.fiscalYearStartMonth} invalid={Boolean(errors.fiscalYearStartMonth)} onChange={(e) => change({ fiscalYearStartMonth: e.target.value })}>
                  {MONTH_NAMES.map((name, i) => (
                    <option key={name} value={String(i + 1)}>
                      {name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <span className={styles.check}>
              <Input id="fr-s-enabled" type="checkbox" checked={v.enabled} onChange={(e) => change({ enabled: e.target.checked })} />
              <label htmlFor="fr-s-enabled">Enabled: run on the timetable</label>
            </span>
          </div>
        </Card>

        <Card title="Distribution">
          <div className={styles.form}>
            <fieldset className={styles.fieldset} aria-describedby={errors.formats ? "fr-s-formats-error" : undefined}>
              <legend className={styles.legend}>Formats</legend>
              <div className={styles.checkList}>
                {OUTPUT_FORMATS.map((f) => (
                  <span key={f} className={styles.check}>
                    <Input
                      id={`fr-s-format-${f}`}
                      type="checkbox"
                      checked={v.formats.includes(f)}
                      onChange={(e) => change({ formats: e.target.checked ? [...v.formats, f] : v.formats.filter((x) => x !== f) })}
                    />
                    <label htmlFor={`fr-s-format-${f}`}>{f.toUpperCase()}</label>
                  </span>
                ))}
              </div>
              <p className={styles.note}>CSV, HTML and XLSX are stored in the report library. PDF needs the renderer, which is not deployed yet, so it is skipped; a schedule with only PDF fails.</p>
              {errors.formats ? (
                <p id="fr-s-formats-error" className={styles.error}>
                  {errors.formats}
                </p>
              ) : null}
            </fieldset>
            <FormField id="fr-s-recipients" label="Recipients" hint="One email address per line. Each recipient gets the copy their own dimension access allows." error={errors.recipients}>
              {(c) => <Textarea {...c} className={styles.mono} rows={4} value={v.recipients} invalid={Boolean(errors.recipients)} onChange={(e) => change({ recipients: e.target.value })} />}
            </FormField>
            <FormField id="fr-s-limit" label="Attachment limit (MB)" hint="Larger outputs are sent as a link. 1 to 25." error={errors.attachmentLimitMb}>
              {(c) => (
                <Input {...c} type="number" min={1} max={25} value={v.attachmentLimitMb} invalid={Boolean(errors.attachmentLimitMb)} onChange={(e) => change({ attachmentLimitMb: e.target.value })} />
              )}
            </FormField>
          </div>
        </Card>

        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save schedule
          </Button>
          <Link to="/finreport/schedules">Back to schedules</Link>
        </div>
      </form>

      {notice ? (
        <Alert tone="success" live="status">
          {notice}
          {saved?.nextRunAt ? ` Next run: ${saved.nextRunAt}.` : ""}
        </Alert>
      ) : null}
      {failure ? (
        <Alert
          tone="danger"
          title={stale ? "Someone else changed this schedule" : failure.code === "REPORT-0001" ? "That definition has no published version" : "The schedule was not saved"}
          live="alert"
          action={
            stale ? (
              <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>
                Reload the schedule
              </Button>
            ) : undefined
          }
        >
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
    </div>
  );
}
