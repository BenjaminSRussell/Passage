import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  createFiscalCalendar,
  fiscalPatterns,
  generateFiscalYear,
  getFiscalCalendar,
  renameFiscalCalendar,
  setFiscalCalendarStatus,
  withFreshRowOnConflict,
  listFiscalCalendars,
  listFiscalPeriods,
  type FiscalCalendar,
  type FiscalPattern,
  type FiscalPeriod,
} from "./api/structure";
import { dayNumber } from "./hierarchyTree";
import { useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";
import { StatusAction, useRows } from "./referenceParts";

const codePattern = /^[A-Za-z0-9][A-Za-z0-9_-]{0,9}$/;
type Errors = Partial<Record<"code" | "name", string>>;

export function validateCalendar(code: string, name: string): Errors {
  const errors: Errors = {};
  if (!codePattern.test(code)) {
    errors.code = "Use 1 to 10 letters, digits, - or _.";
  }
  if (name.trim() === "" || name.trim().length > 120) {
    errors.name = "Enter a name of 1 to 120 characters.";
  }
  return errors;
}

export function validateYear(value: string): string | undefined {
  const year = Number(value);
  return /^\d{4}$/.test(value) && year >= 1900 && year <= 2999 ? undefined : "Enter a year from 1900 to 2999.";
}

// parsePeriods reads one period per line as "YYYY-MM-DD YYYY-MM-DD" (a comma also separates the dates).
export function parsePeriods(text: string): { periods: { start: string; end: string }[]; error?: string } {
  const periods: { start: string; end: string }[] = [];
  const lines = text.split("\n").map((line) => line.trim()).filter((line) => line !== "");
  for (const [index, line] of lines.entries()) {
    const [start, end, ...rest] = line.split(/[\s,]+/);
    const valid = (value?: string) => value !== undefined && dayNumber(value) !== null;
    if (rest.length > 0 || !valid(start) || !valid(end) || (start as string) > (end as string)) {
      return { periods: [], error: `Line ${index + 1}: enter a start and an end date as YYYY-MM-DD, start first.` };
    }
    periods.push({ start: start as string, end: end as string });
  }
  return { periods };
}

const codeProblems: Record<string, string> = { "ORG-CAL-CODE-DUPLICATE": "A fiscal calendar with this code already exists." };

function CreateCalendarForm({ onCreated }: { onCreated(): void }) {
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [pattern, setPattern] = useState<FiscalPattern>("calendar");
  const [startMonth, setStartMonth] = useState("1");
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateCalendar(code, name);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      await createFiscalCalendar({ code: code.toUpperCase(), name: name.trim(), pattern, startMonth: Number(startMonth) });
      setCode("");
      setName("");
      onCreated();
    } catch (error) {
      const problem = describeProblem(error);
      if (codeProblems[problem.code]) {
        setErrors({ code: codeProblems[problem.code] });
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Add fiscal calendar">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="cal-code" label="Code" error={errors.code}>
          {(control) => <Input {...control} value={code} maxLength={10} onChange={(event) => setCode(event.target.value)} />}
        </FormField>
        <FormField id="cal-name" label="Name" error={errors.name}>
          {(control) => <Input {...control} value={name} maxLength={120} onChange={(event) => setName(event.target.value)} />}
        </FormField>
        <FormField id="cal-pattern" label="Pattern">
          {(control) => (
            <Select {...control} value={pattern} onChange={(event) => setPattern(event.target.value as FiscalPattern)}>
              {fiscalPatterns.map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="cal-start-month" label="First month of the fiscal year">
          {(control) => (
            <Select {...control} value={startMonth} onChange={(event) => setStartMonth(event.target.value)}>
              {Array.from({ length: 12 }, (_, index) => (
                <option key={index + 1} value={String(index + 1)}>
                  {index + 1}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {failure ? (
          <Alert tone="danger" title="Could not add the fiscal calendar" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Add fiscal calendar
          </Button>
        </div>
      </form>
    </Card>
  );
}

const periodColumns: DataTableColumn<FiscalPeriod>[] = [
  { key: "periodNo", header: "Period", align: "end" },
  { key: "name", header: "Name" },
  { key: "type", header: "Type" },
  { key: "startDate", header: "Start", mono: true },
  { key: "endDate", header: "End", mono: true },
  { key: "week53", header: "53rd week", render: (row) => (row.week53 ? <Badge tone="info">Yes</Badge> : "") },
];

function CalendarDetail({ calendar, canWrite, onGenerated, onChanged }: { calendar: FiscalCalendar; canWrite: boolean; onGenerated(year: number): void; onChanged(fresh: FiscalCalendar): void }) {
  const custom = calendar.pattern === "custom";
  const [year, setYear] = useState(String(calendar.generatedYears.at(-1) ?? new Date().getFullYear()));
  const [yearError, setYearError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [periodText, setPeriodText] = useState("");
  const [force, setForce] = useState(false);
  const [name, setName] = useState(calendar.name);
  const [nameError, setNameError] = useState<string | undefined>();
  const [renameFailure, setRenameFailure] = useState<Problem | null>(null);
  const [renaming, setRenaming] = useState(false);
  const [shownYear, setShownYear] = useState<number | null>(calendar.generatedYears.at(-1) ?? null);
  const periods = useRows(
    (signal) => (shownYear === null ? Promise.resolve([]) : listFiscalPeriods(calendar.id, shownYear, { signal })),
    `${calendar.id}|${shownYear}|${calendar.generatedYears.length}`,
  );

  const generate = async (event: FormEvent) => {
    event.preventDefault();
    const problem = validateYear(year);
    setYearError(problem);
    setFailure(null);
    if (problem) {
      return;
    }
    const parsed = custom ? parsePeriods(periodText) : { periods: [] };
    if (parsed.error || (custom && parsed.periods.length === 0)) {
      setFailure({ code: "ORG-WEB-PERIODS", message: parsed.error ?? "A custom calendar needs its periods: enter one per line." });
      return;
    }
    setBusy(true);
    try {
      const generated = await generateFiscalYear(calendar.id, Number(year), { periods: parsed.periods, force: force && calendar.generatedYears.includes(Number(year)) });
      setShownYear(generated.fiscalYear);
      onGenerated(generated.fiscalYear);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  };

  const rename = async (event: FormEvent) => {
    event.preventDefault();
    const problem = validateCalendar(calendar.code, name).name;
    setNameError(problem);
    setRenameFailure(null);
    if (problem || name.trim() === calendar.name) {
      return;
    }
    setRenaming(true);
    try {
      await withFreshRowOnConflict(
        () => renameFiscalCalendar(calendar.id, calendar.version, name.trim()),
        () => getFiscalCalendar(calendar.id),
        (fresh) => onChanged(fresh),
      );
    } catch (error) {
      setRenameFailure(describeProblem(error));
    } finally {
      setRenaming(false);
    }
  };

  return (
    <Card title={`${calendar.code} - ${calendar.name}`}>
      <div className={styles.panel}>
        {canWrite ? (
          <form onSubmit={rename} noValidate className={styles.form} aria-label={`Rename ${calendar.code}`}>
            <FormField id="rename-calendar" label="Calendar name" error={nameError}>
              {(control) => <Input {...control} value={name} maxLength={120} onChange={(event) => setName(event.target.value)} />}
            </FormField>
            {renameFailure ? (
              <Alert tone="danger" title="Could not rename the fiscal calendar" live="alert">
                {renameFailure.message} ({renameFailure.code})
              </Alert>
            ) : null}
            <div>
              <Button type="submit" loading={renaming}>
                Save name
              </Button>
            </div>
          </form>
        ) : null}
        <div className={styles.filters}>
          <div className={styles.filter}>
            <label htmlFor="period-year" className={styles.filterLabel}>
              Fiscal year
            </label>
            <Select id="period-year" value={shownYear === null ? "" : String(shownYear)} onChange={(event) => setShownYear(Number(event.target.value))} disabled={calendar.generatedYears.length === 0}>
              {calendar.generatedYears.length === 0 ? <option value="">None generated</option> : null}
              {calendar.generatedYears.map((value) => (
                <option key={value} value={String(value)}>
                  {value}
                </option>
              ))}
            </Select>
          </div>
        </div>
        {canWrite ? (
          <form onSubmit={generate} noValidate className={styles.form}>
            <FormField id="generate-year" label="Generate fiscal year" hint="Generating an existing year changes nothing." error={yearError}>
              {(control) => <Input {...control} value={year} inputMode="numeric" maxLength={4} onChange={(event) => setYear(event.target.value)} />}
            </FormField>
            {custom ? (
              <FormField id="generate-periods" label="Periods" hint="One period per line: start date, then end date (YYYY-MM-DD). Periods must be contiguous.">
                {(control) => <Textarea {...control} value={periodText} rows={6} onChange={(event) => setPeriodText(event.target.value)} />}
              </FormField>
            ) : null}
            {calendar.generatedYears.includes(Number(year)) ? (
              <label className={styles.checkRow}>
                <input type="checkbox" checked={force} onChange={(event) => setForce(event.target.checked)} />
                Regenerate this year (replaces its periods; refused once the ledger has postings)
              </label>
            ) : null}
            <div>
              <Button type="submit" loading={busy}>
                {force && calendar.generatedYears.includes(Number(year)) ? "Regenerate year" : "Generate year"}
              </Button>
            </div>
          </form>
        ) : null}
        {failure ? (
          <Alert tone="danger" title="Could not generate the fiscal year" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {periods.error ? (
          <Alert tone="danger" title="Could not load the periods" live="alert">
            {periods.error.message} ({periods.error.code})
          </Alert>
        ) : null}
        <DataTable
          label={`Periods of ${calendar.code}`}
          columns={periodColumns}
          rows={periods.rows}
          getRowKey={(row) => `${row.fiscalYear}-${row.periodNo}`}
          loading={periods.loading}
          empty={{ title: "No periods generated", description: "Generate a fiscal year to see its periods." }}
        />
      </div>
    </Card>
  );
}

export function FiscalCalendarsPage() {
  const { canWriteFiscalCalendar, canAdministerTenant } = useLegalEntity();
  // Calendar writes are tenant-level: the service also requires tenant.administer and no legal-entity claim.
  const canWrite = canWriteFiscalCalendar && canAdministerTenant;
  const state = useRows((signal) => listFiscalCalendars({ signal }), "fiscal-calendars");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selected = state.rows.find((row) => row.id === selectedId) ?? null;
  const columns: DataTableColumn<FiscalCalendar>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name" },
    { key: "pattern", header: "Pattern" },
    { key: "generatedYears", header: "Generated years", render: (row) => row.generatedYears.join(", ") },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    {
      key: "actions",
      header: "Actions",
      render: (row) => (
        <div className={styles.rowActions}>
          <Button size="sm" aria-label={`Open periods of ${row.code}`} onClick={() => setSelectedId(row.id)}>
            Periods
          </Button>
          {canWrite ? (
            <StatusAction
              subject={`fiscal calendar ${row.code}`}
              status={row.status}
              apply={(next) =>
                withFreshRowOnConflict(
                  () => setFiscalCalendarStatus(row.id, row.version, next),
                  () => getFiscalCalendar(row.id),
                  (fresh) => state.update((rows) => rows.map((item) => (item.id === row.id ? { ...item, ...fresh } : item))),
                )
              }
            />
          ) : null}
        </div>
      ),
    },
  ];
  return (
    <>
      <PageHeader title="Fiscal calendars" description="Calendars shared by the tenant's legal entities and sites." />
      {canWrite ? <CreateCalendarForm onCreated={state.reload} /> : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load fiscal calendars" live="alert">
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      <DataTable label="Fiscal calendars" columns={columns} rows={state.rows} getRowKey={(row) => row.id} loading={state.loading} empty={{ title: "No fiscal calendars" }} />
      {selected ? (
        <CalendarDetail
          key={selected.id}
          calendar={selected}
          canWrite={canWrite}
          onChanged={(fresh) => state.update((rows) => rows.map((item) => (item.id === fresh.id ? { ...item, ...fresh } : item)))}
          onGenerated={(fiscalYear) =>
            state.update((rows) =>
              rows.map((row) =>
                row.id === selected.id && !row.generatedYears.includes(fiscalYear)
                  ? { ...row, generatedYears: [...row.generatedYears, fiscalYear].sort((a, b) => a - b) }
                  : row,
              ),
            )
          }
        />
      ) : null}
    </>
  );
}
