import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import { createStrategy, getStrategy, listStrategies, updateStrategy, type ConsumptionMode, type StrategySettings, type StrategySetup } from "./api/client";
import { describeError, useLoad, type LoadError } from "./hooks";
import styles from "./pages.module.css";

const LIST = "/planning/strategies";

export const strategyLabel: Record<StrategySetup["code"], string> = {
  "mts-net": "Make to stock, net",
  "mts-gross": "Make to stock, gross",
  mto: "Make to order",
  "assemble-to-order-final": "Assemble to order (final assembly)",
  "plan-without-final-assembly": "Plan without final assembly",
  subassembly: "Subassembly",
};

/** What a plan run does with the forecast under each strategy (FR-22.19, BR-22.13). */
export const strategyEffect: Record<StrategySetup["code"], string> = {
  "mts-net": "Plans the forecast reduced by sales orders per the model's reduction principle.",
  "mts-gross": "Plans the forecast as entered; sales orders do not reduce it.",
  mto: "Plans no forecast; supply follows the sales lines only.",
  "assemble-to-order-final": "Sales lines consume the forecast inside the window; unconsumed sales add on top of what is left.",
  "plan-without-final-assembly": "Plans the reduced forecast for now; the finished item is not yet held back until sales demand.",
  subassembly: "Plans the reduced forecast for now; dependent-demand consumption is not built yet.",
};

const modeLabel: Record<ConsumptionMode, string> = {
  backward: "Backward only",
  forward: "Forward only",
  "backward-forward": "Backward, then forward",
};

/** The form as typed: window days stay strings until validated. */
export interface StrategyDraft {
  consumptionMode: ConsumptionMode;
  backwardDays: string;
  forwardDays: string;
  usesMarking: boolean;
  segregatedStock: boolean;
}

type DraftErrors = Partial<Record<keyof StrategyDraft, string>>;

export function draftOf(s: StrategySettings): StrategyDraft {
  return { consumptionMode: s.consumptionMode, backwardDays: String(s.backwardDays), forwardDays: String(s.forwardDays), usesMarking: s.usesMarking, segregatedStock: s.segregatedStock };
}

/** Checks a strategy draft with the service's rules and returns the settings or the field errors. */
export function validateStrategy(d: StrategyDraft): { errors: DraftErrors; settings: StrategySettings | null } {
  const errors: DraftErrors = {};
  const days = { backwardDays: 0, forwardDays: 0 };
  for (const key of ["backwardDays", "forwardDays"] as const) {
    const raw = d[key].trim();
    const n = Number(raw);
    if (!/^\d+$/.test(raw) || n > 365) errors[key] = "Enter a whole number from 0 to 365.";
    else days[key] = n;
  }
  if (!errors.backwardDays && !errors.forwardDays && d.consumptionMode === "backward-forward" && days.backwardDays === 0 && days.forwardDays === 0) {
    errors.consumptionMode = "Backward, then forward needs a window of at least 1 day.";
  }
  if (Object.keys(errors).length > 0) return { errors, settings: null };
  return { errors, settings: { consumptionMode: d.consumptionMode, ...days, usesMarking: d.usesMarking, segregatedStock: d.segregatedStock } };
}

function newKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  return c?.randomUUID ? c.randomUUID() : `st-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

const windowText = (s: StrategySetup) =>
  s.code === "assemble-to-order-final"
    ? `${modeLabel[s.consumptionMode]}, ${s.backwardDays} d back, ${s.forwardDays} d forward`
    : "Not used";

const columns: DataTableColumn<StrategySetup>[] = [
  { key: "code", header: "Strategy", mono: true, render: (s) => s.code },
  { key: "name", header: "Name", render: (s) => strategyLabel[s.code] },
  { key: "effect", header: "In a plan run", render: (s) => strategyEffect[s.code] },
  { key: "window", header: "Consumption window", render: windowText },
  { key: "marking", header: "Marking", render: (s) => (s.usesMarking ? "On" : "Off") },
  { key: "state", header: "Setup", render: (s) => (s.configured ? <Badge tone="info">{`Stored, v${s.version}`}</Badge> : <Badge tone="neutral">Defaults</Badge>) },
];

/** Planning strategies (FR-22.19): the six strategies with their stored setup or defaults. */
export function StrategiesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listStrategies({ signal }), []);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Planning strategies"
        description="How forecast and sales demand interact for an item. An item takes the strategy of its item coverage; a run nets the forecast model accordingly."
      />
      <Alert tone="danger" title={state.error ? "Could not load planning strategies" : undefined} live="alert">
        {state.error ? `${state.error.message} (${state.error.code})` : null}
      </Alert>
      {state.error ? null : (
        <DataTable
          label="Planning strategies"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(s) => s.code}
          loading={state.loading}
          onRowOpen={(s) => navigate(`${LIST}/${encodeURIComponent(s.code)}`)}
          empty={{ title: "No planning strategies", description: "The service lists the six strategies; none came back." }}
        />
      )}
    </div>
  );
}

interface FormProps {
  strategy: StrategySetup;
  onSaved: (s: StrategySetup) => void;
  onReload?: () => void;
}

function StrategyForm({ strategy, onSaved, onReload }: FormProps) {
  const creating = !strategy.configured;
  const [draft, setDraft] = useState<StrategyDraft>(() => draftOf(strategy));
  const [errors, setErrors] = useState<DraftErrors>({});
  const [failure, setFailure] = useState<LoadError | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per create attempt, kept across retries of the same draft.
  const key = useRef(newKey());

  useEffect(() => {
    setDraft(draftOf(strategy));
    setErrors({});
  }, [strategy.code, strategy.version, strategy.configured]);

  const set = <K extends keyof StrategyDraft>(field: K, value: StrategyDraft[K]) => {
    setDraft((d) => ({ ...d, [field]: value }));
    key.current = newKey();
  };

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const { errors: found, settings } = validateStrategy(draft);
    setErrors(found);
    if (!settings) return;
    setBusy(true);
    setFailure(null);
    try {
      const saved = creating || strategy.version === undefined ? await createStrategy(strategy.code, settings, key.current) : await updateStrategy(strategy.code, strategy.version, settings);
      onSaved(saved);
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  // 412 on update, or 409 on create when someone else stored the setup first:
  // either way the form is behind the stored setup and a reload resolves it.
  const taken = failure?.code === "PLAN-STRATEGY-EXISTS";
  const stale = failure?.status === 412 || taken;
  const applies = strategy.code === "assemble-to-order-final";
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={`Setup of ${strategy.code}`}>
      <div className={styles.wide}>
        <Alert
          tone="danger"
          title={failure ? (taken ? "Someone else stored this strategy first" : stale ? "Someone else changed this strategy" : "The strategy was not saved") : undefined}
          live="alert"
          action={
            stale && onReload ? (
              <Button size="sm" onClick={onReload}>
                Reload strategy
              </Button>
            ) : undefined
          }
        >
          {failure ? `${failure.message} (${failure.code})` : null}
        </Alert>
      </div>
      <FormField
        id="st-mode"
        label="Consumption mode"
        hint={applies ? "Which window a sales line consumes forecast from." : "Used by assemble to order only; kept for reference here."}
        error={errors.consumptionMode}
      >
        {(a11y) => (
          <Select {...a11y} value={draft.consumptionMode} onChange={(e) => set("consumptionMode", e.target.value as ConsumptionMode)}>
            {(Object.keys(modeLabel) as ConsumptionMode[]).map((m) => (
              <option key={m} value={m}>
                {modeLabel[m]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="st-backward" label="Backward window (days)" hint="A sale on day d consumes forecast from d minus this many days up to d, latest first." error={errors.backwardDays}>
        {(a11y) => <Input {...a11y} inputMode="numeric" value={draft.backwardDays} onChange={(e) => set("backwardDays", e.target.value)} />}
      </FormField>
      <FormField id="st-forward" label="Forward window (days)" hint="Then forecast after d up to d plus this many days, earliest first." error={errors.forwardDays}>
        {(a11y) => <Input {...a11y} inputMode="numeric" value={draft.forwardDays} onChange={(e) => set("forwardDays", e.target.value)} />}
      </FormField>
      <fieldset className={`${styles.fieldset} ${styles.wide}`}>
        <legend className={styles.legend}>Pegging and stock (stored; a run does not apply them yet)</legend>
        <div className={styles.checks}>
          <span className={styles.check}>
            <Input id="st-marking" type="checkbox" checked={draft.usesMarking} onChange={(e) => set("usesMarking", e.target.checked)} />
            <label htmlFor="st-marking">Mark pegging so later runs cannot re-peg the supply</label>
          </span>
          <span className={styles.check}>
            <Input id="st-segregated" type="checkbox" checked={draft.segregatedStock} onChange={(e) => set("segregatedStock", e.target.checked)} />
            <label htmlFor="st-segregated">Segregate order-specific stock</label>
          </span>
        </div>
      </fieldset>
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {creating ? "Save strategy setup" : "Save changes"}
        </Button>
        <Link className={buttonClassName("secondary")} to={LIST}>
          Cancel
        </Link>
      </div>
    </form>
  );
}

/** Shows and edits one strategy; an unconfigured one is stored on the first save. */
export function StrategyPage() {
  const { code = "" } = useParams();
  const [reload, setReload] = useState(0);
  const state = useLoad((signal) => getStrategy(code, { signal }), [code, reload]);
  const [saved, setSaved] = useState<StrategySetup | null>(null);
  const strategy = saved && saved.code === code ? saved : state.data;

  useEffect(() => setSaved(null), [state.data]);

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Planning strategy ${code}`}
        description={strategy ? strategyEffect[strategy.code] : "Setup of a planning strategy."}
        actions={
          <Link className={buttonClassName("secondary")} to={LIST}>
            All strategies
          </Link>
        }
      />
      <Alert tone="danger" title={state.error ? `Could not load planning strategy ${code}` : undefined} live="alert">
        {state.error ? `${state.error.message} (${state.error.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {saved ? `Saved. Version ${saved.version}.` : null}
      </Alert>
      {state.loading ? (
        <Card>
          <div className={styles.stack} aria-busy="true" aria-label="Loading planning strategy">
            <Skeleton width="1/2" />
            <Skeleton height="control" />
            <Skeleton height="control" />
          </div>
        </Card>
      ) : null}
      {strategy ? (
        <Card title={strategy.configured ? "Setup" : "Setup (defaults, not stored yet)"}>
          <StrategyForm strategy={strategy} onSaved={setSaved} onReload={() => setReload((n) => n + 1)} />
        </Card>
      ) : null}
    </div>
  );
}
