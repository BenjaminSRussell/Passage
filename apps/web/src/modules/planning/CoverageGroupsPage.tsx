import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  COVERAGE_DEFAULTS,
  createCoverageGroup,
  getCoverageGroup,
  listCoverageGroups,
  updateCoverageGroup,
  type CoverageCode,
  type CoverageGroup,
  type CoverageSettings,
} from "./api/client";
import { describeError, PLAN_CODE, useLoad, type LoadError } from "./hooks";
import styles from "./pages.module.css";

const LIST = "/planning/coverage-groups";

export const coverageLabel: Record<CoverageCode, string> = {
  requirement: "Requirement (lot-for-lot)",
  period: "Period",
  minmax: "Min/Max",
  manual: "Manual (messages only)",
};

/** Day fields of a coverage group with their label, range and help (FR-22.2, BR-22.4). */
const DAY_FIELDS = [
  { key: "coverageTimeFenceDays", label: "Coverage time fence (days)", min: 1, max: 3650, hint: "Surplus supply at or beyond it raises cancel or decrease." },
  { key: "periodDays", label: "Period (days)", min: 0, max: 365, hint: "Orders grouped per period; 1 to 365 for period coverage." },
  { key: "negativeDays", label: "Negative days", min: 0, max: 365, hint: "Wait this long for a late receipt before planning a new order." },
  { key: "positiveDays", label: "Positive days", min: 0, max: 365, hint: "Earlier receipts beyond this get postpone; 0 is unlimited." },
  { key: "freezeFenceDays", label: "Freeze fence (days)", min: 0, max: 3650, hint: "No planned orders inside it; 0 freezes nothing." },
  { key: "firmingFenceDays", label: "Firming fence (days)", min: 0, max: 3650, hint: "With auto-firm, orders placed inside it are firmed by the run." },
  { key: "actionFenceDays", label: "Action fence (days)", min: 0, max: 3650, hint: "Action messages only inside it; 0 is unlimited." },
  { key: "futuresFenceDays", label: "Futures fence (days)", min: 0, max: 3650, hint: "Delay messages only inside it; 0 is unlimited." },
] as const;

type DayKey = (typeof DAY_FIELDS)[number]["key"];

/** The form as typed: day fields stay strings until validated. */
export type CoverageDraft = { code: string; coverageCode: CoverageCode; autoFirm: boolean; generateActions: boolean; generateFutures: boolean } & Record<
  DayKey,
  string
>;

type DraftErrors = Partial<Record<keyof CoverageDraft, string>>;

export function draftOf(code: string, s: CoverageSettings): CoverageDraft {
  const days = Object.fromEntries(DAY_FIELDS.map((f) => [f.key, String(s[f.key])])) as Record<DayKey, string>;
  return { code, coverageCode: s.coverageCode, autoFirm: s.autoFirm, generateActions: s.generateActions, generateFutures: s.generateFutures, ...days };
}

/** Checks a coverage group draft with the service's rules and returns the settings or the field errors. */
export function validateCoverage(d: CoverageDraft, checkCode: boolean): { errors: DraftErrors; settings: CoverageSettings | null } {
  const errors: DraftErrors = {};
  if (checkCode && !PLAN_CODE.test(d.code.trim().toUpperCase())) errors.code = "Use 2 to 20 capital letters, digits, _ or -.";
  const days = {} as Record<DayKey, number>;
  for (const f of DAY_FIELDS) {
    const raw = d[f.key].trim();
    const n = Number(raw);
    if (!/^\d+$/.test(raw) || n < f.min || n > f.max) errors[f.key] = `Enter a whole number from ${f.min} to ${f.max}.`;
    else days[f.key] = n;
  }
  if (d.coverageCode === "period" && !errors.periodDays && days.periodDays < 1) errors.periodDays = "Period coverage needs a period of 1 to 365 days.";
  if (d.autoFirm && !errors.firmingFenceDays && days.firmingFenceDays < 1) errors.autoFirm = "Auto-firm needs a firming fence of at least 1 day.";
  if (Object.keys(errors).length > 0) return { errors, settings: null };
  return {
    errors,
    settings: {
      coverageCode: d.coverageCode,
      autoFirm: d.autoFirm,
      generateActions: d.generateActions,
      generateFutures: d.generateFutures,
      ...days,
    },
  };
}

function newKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  return c?.randomUUID ? c.randomUUID() : `cg-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

const onOff = (on: boolean) => (on ? "On" : "Off");
const fence = (days: number, zero: string) => (days === 0 ? zero : `${days} d`);

const columns: DataTableColumn<CoverageGroup>[] = [
  { key: "code", header: "Code", mono: true, render: (g) => g.code },
  { key: "coverage", header: "Coverage", render: (g) => coverageLabel[g.coverageCode] + (g.coverageCode === "period" ? `, ${g.periodDays} d` : "") },
  { key: "ctf", header: "Coverage fence", align: "end", render: (g) => `${g.coverageTimeFenceDays} d` },
  { key: "freeze", header: "Freeze", align: "end", render: (g) => fence(g.freezeFenceDays, "None") },
  {
    key: "firming",
    header: "Firming",
    render: (g) =>
      g.autoFirm ? <Badge tone="info">{`Auto-firm, ${g.firmingFenceDays} d`}</Badge> : fence(g.firmingFenceDays, "None"),
  },
  { key: "messages", header: "Actions / futures", render: (g) => `${onOff(g.generateActions)} / ${onOff(g.generateFutures)}` },
];

/** Coverage group setup (FR-22.2): the list of the tenant's groups. */
export function CoverageGroupsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listCoverageGroups({ signal }), []);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Coverage groups"
        description="How items are covered: lot-for-lot, per period, min/max or manual, with the fences that decide planned orders, firming and messages."
        actions={
          <Link className={buttonClassName("primary")} to={`${LIST}/new`}>
            New coverage group
          </Link>
        }
      />
      <Alert tone="danger" title={state.error ? "Could not load coverage groups" : undefined} live="alert">
        {state.error ? `${state.error.message} (${state.error.code})` : null}
      </Alert>
      {state.error ? null : (
        <DataTable
          label="Coverage groups"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(g) => g.id}
          loading={state.loading}
          onRowOpen={(g) => navigate(`${LIST}/${encodeURIComponent(g.code)}`)}
          empty={{ title: "No coverage groups yet", description: "Create one to set how items are covered and fenced." }}
        />
      )}
    </div>
  );
}

interface FormProps {
  /** The group being edited; absent when creating. */
  group?: CoverageGroup;
  onSaved: (g: CoverageGroup) => void;
  onReload?: () => void;
}

function CoverageGroupForm({ group, onSaved, onReload }: FormProps) {
  const creating = !group;
  const [draft, setDraft] = useState<CoverageDraft>(() => draftOf(group?.code ?? "", group ?? COVERAGE_DEFAULTS));
  const [errors, setErrors] = useState<DraftErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per create attempt, kept across retries of the same draft.
  const key = useRef(newKey());

  useEffect(() => {
    if (group) {
      setDraft(draftOf(group.code, group));
      setErrors({});
    }
  }, [group?.id, group?.version]);

  const set = <K extends keyof CoverageDraft>(field: K, value: CoverageDraft[K]) => {
    setDraft((d) => ({ ...d, [field]: value }));
    key.current = newKey();
  };

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, settings } = validateCoverage(draft, creating);
    setErrors(found);
    if (!settings) return;
    setBusy(true);
    setFailure(null);
    try {
      const saved = group
        ? await updateCoverageGroup(group.code, group.version, settings)
        : await createCoverageGroup(draft.code.trim().toUpperCase(), settings, key.current);
      onSaved(saved);
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.status === 412;
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={creating ? "New coverage group" : `Edit coverage group ${group.code}`}>
      <div className={styles.wide}>
        <Alert
          tone="danger"
          title={failure ? (stale ? "Someone else changed this coverage group" : "The coverage group was not saved") : undefined}
          live="alert"
          action={
            stale && onReload ? (
              <Button size="sm" onClick={onReload}>
                Reload group
              </Button>
            ) : undefined
          }
        >
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
      </div>
      {creating ? (
        <FormField id="cg-code" label="Code" hint="2 to 20 capital letters, digits, _ or -." error={errors.code}>
          {(a11y) => <Input {...a11y} className={styles.mono} value={draft.code} onChange={(e) => set("code", e.target.value)} />}
        </FormField>
      ) : null}
      <FormField id="cg-coverage" label="Coverage code">
        {(a11y) => (
          <Select {...a11y} value={draft.coverageCode} onChange={(e) => set("coverageCode", e.target.value as CoverageCode)}>
            {(Object.keys(coverageLabel) as CoverageCode[]).map((c) => (
              <option key={c} value={c}>
                {coverageLabel[c]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {DAY_FIELDS.map((f) => (
        <FormField key={f.key} id={`cg-${f.key}`} label={f.label} hint={f.hint} error={errors[f.key]}>
          {(a11y) => <Input {...a11y} inputMode="numeric" value={draft[f.key]} onChange={(e) => set(f.key, e.target.value)} />}
        </FormField>
      ))}
      <fieldset className={`${styles.fieldset} ${styles.wide}`}>
        <legend className={styles.legend}>Firming and messages</legend>
        <div className={styles.checks}>
          <span className={styles.check}>
            <Input
              id="cg-autoFirm"
              type="checkbox"
              checked={draft.autoFirm}
              aria-invalid={errors.autoFirm ? true : undefined}
              aria-describedby={errors.autoFirm ? "cg-autoFirm-error" : undefined}
              onChange={(e) => set("autoFirm", e.target.checked)}
            />
            <label htmlFor="cg-autoFirm">Auto-firm inside the firming fence</label>
          </span>
          <span className={styles.check}>
            <Input id="cg-actions" type="checkbox" checked={draft.generateActions} onChange={(e) => set("generateActions", e.target.checked)} />
            <label htmlFor="cg-actions">Action messages</label>
          </span>
          <span className={styles.check}>
            <Input id="cg-futures" type="checkbox" checked={draft.generateFutures} onChange={(e) => set("generateFutures", e.target.checked)} />
            <label htmlFor="cg-futures">Futures messages</label>
          </span>
        </div>
        {errors.autoFirm ? (
          <p id="cg-autoFirm-error" className={styles.formError} role="alert">
            {errors.autoFirm}
          </p>
        ) : null}
      </fieldset>
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {creating ? "Create coverage group" : "Save changes"}
        </Button>
        <Link className={buttonClassName("secondary")} to={LIST}>
          Cancel
        </Link>
      </div>
    </form>
  );
}

/** Creates a coverage group; the form starts from the FR-22.2 defaults. */
export function NewCoverageGroupPage() {
  const navigate = useNavigate();
  return (
    <div className={styles.stack}>
      <PageHeader title="New coverage group" description="Omitted settings keep the defaults: coverage fence 100 days, negative days 1, no freeze or firming fence." />
      <Card>
        <CoverageGroupForm onSaved={(g) => navigate(`${LIST}/${encodeURIComponent(g.code)}`)} />
      </Card>
    </div>
  );
}

/** Shows and edits one coverage group under If-Match on its version. */
export function CoverageGroupPage() {
  const { code = "" } = useParams();
  const [reload, setReload] = useState(0);
  const state = useLoad((signal) => getCoverageGroup(code, { signal }), [code, reload]);
  const [saved, setSaved] = useState<CoverageGroup | null>(null);
  const group = saved && saved.code === code ? saved : state.data;

  useEffect(() => setSaved(null), [state.data]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Coverage group ${code}`}
        description="Changes apply to plans that use this group; the run reads coverage from its input until items reference groups."
        actions={
          <Link className={buttonClassName("secondary")} to={LIST}>
            All coverage groups
          </Link>
        }
      />
      <Alert tone="danger" title={state.error ? `Could not load coverage group ${code}` : undefined} live="alert">
        {state.error ? `${state.error.message} (${state.error.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {saved ? `Saved. Version ${saved.version}.` : null}
      </Alert>
      {state.loading ? (
        <Card>
          <div className={styles.stack} aria-busy="true" aria-label="Loading coverage group">
            <Skeleton width="1/2" />
            <Skeleton height="control" />
            <Skeleton height="control" />
            <Skeleton width="3/4" />
          </div>
        </Card>
      ) : null}
      {group ? (
        <Card title="Settings">
          <CoverageGroupForm group={group} onSaved={setSaved} onReload={() => setReload((n) => n + 1)} />
        </Card>
      ) : null}
    </div>
  );
}
