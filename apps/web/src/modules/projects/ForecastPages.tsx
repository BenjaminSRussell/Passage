import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createForecast,
  describeProblem,
  getForecast,
  listForecasts,
  newKey,
  replaceForecast,
  TXN_TYPES,
  type ForecastInput,
  type ForecastLine,
  type ForecastModel,
  type ForecastRow,
  type ForecastView,
  type Problem,
  type TxnType,
} from "./api/client";
import { txnTypeLabel } from "./labels";
import plan from "./plans.module.css";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** One editable forecast line; amounts stay strings (decimal on the service, never float here). */
export interface ForecastRowValues {
  key: string;
  period: string;
  txnType: TxnType;
  wbsCode: string;
  quantity: string;
  costAmount: string;
  salesAmount: string;
}

const PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/;
const QUANTITY = /^\d+(\.\d{1,3})?$/;
const AMOUNT = /^\d+(\.\d+)?$/;
const SIGNED_AMOUNT = /^-?\d+(\.\d+)?$/;
const NAME_MAX = 80;

let lineSeq = 0;
const lineKey = () => `fline-${++lineSeq}`;

function thisMonth(): string {
  const now = new Date();
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;
}

function blankLine(period = thisMonth()): ForecastRowValues {
  return { key: lineKey(), period, txnType: "HOUR", wbsCode: "", quantity: "0", costAmount: "0", salesAmount: "0" };
}

function rowsOf(lines: ForecastLine[]): ForecastRowValues[] {
  return lines.map((l) => ({
    key: lineKey(),
    period: l.period,
    txnType: l.txnType,
    wbsCode: l.wbsCode ?? "",
    quantity: l.quantity,
    costAmount: l.costAmount,
    salesAmount: l.salesAmount,
  }));
}

/**
 * Checks that mirror domain.NormalizeForecastLines; error keys are the service's (name, lines[i].field) so
 * server field errors land on the same inputs. The service rounds amounts to the currency's minor unit.
 */
export function forecastBody(name: string, rows: ForecastRowValues[]): { errors: Record<string, string>; input?: ForecastInput } {
  const errors: Record<string, string> = {};
  const trimmed = name.trim();
  if (trimmed === "" || [...trimmed].length > NAME_MAX) errors.name = `Enter a name of up to ${NAME_MAX} characters.`;
  const seen = new Set<string>();
  rows.forEach((r, i) => {
    const k = `lines[${i}].`;
    if (!PERIOD.test(r.period)) errors[`${k}period`] = "Choose a month.";
    if (!QUANTITY.test(r.quantity.trim())) errors[`${k}quantity`] = "Enter a quantity of 0 or more, with up to 3 decimals.";
    if (!AMOUNT.test(r.costAmount.trim())) errors[`${k}costAmount`] = "Enter a cost of 0 or more.";
    const sales = r.salesAmount.trim();
    if (!(r.txnType === "ON_ACCOUNT" ? SIGNED_AMOUNT : AMOUNT).test(sales)) {
      errors[`${k}salesAmount`] = r.txnType === "ON_ACCOUNT" ? "Enter a sales amount." : "Enter a sales amount of 0 or more; only on-account lines may be negative.";
    }
    const dup = `${r.period}|${r.txnType}|${r.wbsCode.trim()}`;
    if (seen.has(dup) && !errors[`${k}period`]) errors[`${k}period`] = "Another line already has this month, type and WBS code.";
    seen.add(dup);
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      name: trimmed,
      lines: rows.map((r) => ({
        period: r.period,
        txnType: r.txnType,
        ...(r.wbsCode.trim() ? { wbsCode: r.wbsCode.trim() } : {}),
        quantity: r.quantity.trim(),
        costAmount: r.costAmount.trim(),
        salesAmount: r.salesAmount.trim(),
      })),
    },
  };
}

const lineLabel = (key: string) => {
  const m = /^lines\[(\d+)\]\.(\w+)$/.exec(key);
  return m ? `Line ${Number(m[1]) + 1} ${m[2]}` : key;
};

function ProjectMissing({ id, title }: { id: string; title: string }) {
  return (
    <div className={styles.stack}>
      <PageHeader title={title} />
      <Card>
        <EmptyState
          icon="grid"
          title="Project not found"
          description={`There is no project ${id} in your legal entities.`}
          action={
            <Link className={buttonClassName("secondary")} to="/projects/list">
              Back to projects
            </Link>
          }
        />
      </Card>
    </div>
  );
}

/** The forecast line grid shared by the create and edit forms. */
function LinesEditor({
  rows,
  errors,
  onChange,
  prefix,
}: {
  rows: ForecastRowValues[];
  errors: Record<string, string>;
  onChange(rows: ForecastRowValues[]): void;
  prefix: string;
}) {
  const edit = (key: string, patch: Partial<ForecastRowValues>) => onChange(rows.map((r) => (r.key === key ? { ...r, ...patch } : r)));
  const err = (key: string) => (errors[key] ? <p className={plan.rowError}>{errors[key]}</p> : null);
  return (
    <div className={styles.stack}>
      {errors.lines ? <p className={plan.rowError}>{errors.lines}</p> : null}
      {rows.length === 0 ? (
        <p className={styles.note}>No lines yet. Add a line for each month and transaction type you expect.</p>
      ) : (
        <div className={plan.scroll}>
          <table className={plan.grid} aria-label={`${prefix} forecast lines`}>
            <thead>
              <tr>
                <th scope="col" className={plan.head}>Month</th>
                <th scope="col" className={plan.head}>Type</th>
                <th scope="col" className={plan.head}>WBS code</th>
                <th scope="col" className={plan.head}>Quantity</th>
                <th scope="col" className={plan.head}>Cost</th>
                <th scope="col" className={plan.head}>Sales</th>
                <th scope="col" className={plan.head}>
                  <span className="visually-hidden">Remove</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={r.key}>
                  <td className={plan.cell}>
                    <Input aria-label={`Month of line ${i + 1}`} className={plan.date} type="month" value={r.period} invalid={Boolean(errors[`lines[${i}].period`])} onChange={(e) => edit(r.key, { period: e.target.value })} />
                    {err(`lines[${i}].period`)}
                  </td>
                  <td className={plan.cell}>
                    <Select aria-label={`Type of line ${i + 1}`} value={r.txnType} onChange={(e) => edit(r.key, { txnType: e.target.value as TxnType })}>
                      {TXN_TYPES.map((t) => (
                        <option key={t} value={t}>
                          {txnTypeLabel[t]}
                        </option>
                      ))}
                    </Select>
                  </td>
                  <td className={plan.cell}>
                    <Input aria-label={`WBS code of line ${i + 1}`} className={`${plan.code} ${styles.mono}`} value={r.wbsCode} placeholder="Project" invalid={Boolean(errors[`lines[${i}].wbsCode`])} onChange={(e) => edit(r.key, { wbsCode: e.target.value })} />
                    {err(`lines[${i}].wbsCode`)}
                  </td>
                  <td className={plan.cell}>
                    <Input aria-label={`Quantity of line ${i + 1}`} className={plan.number} inputMode="decimal" value={r.quantity} invalid={Boolean(errors[`lines[${i}].quantity`])} onChange={(e) => edit(r.key, { quantity: e.target.value })} />
                    {err(`lines[${i}].quantity`)}
                  </td>
                  <td className={plan.cell}>
                    <Input aria-label={`Cost of line ${i + 1}`} className={plan.number} inputMode="decimal" value={r.costAmount} invalid={Boolean(errors[`lines[${i}].costAmount`])} onChange={(e) => edit(r.key, { costAmount: e.target.value })} />
                    {err(`lines[${i}].costAmount`)}
                  </td>
                  <td className={plan.cell}>
                    <Input aria-label={`Sales of line ${i + 1}`} className={plan.number} inputMode="decimal" value={r.salesAmount} invalid={Boolean(errors[`lines[${i}].salesAmount`])} onChange={(e) => edit(r.key, { salesAmount: e.target.value })} />
                    {err(`lines[${i}].salesAmount`)}
                  </td>
                  <td className={plan.cell}>
                    <Button size="sm" variant="ghost" aria-label={`Remove line ${i + 1}`} onClick={() => onChange(rows.filter((x) => x.key !== r.key))}>
                      Remove
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className={styles.actions}>
        <Button size="sm" onClick={() => onChange([...rows, blankLine(rows.at(-1)?.period)])}>
          Add line
        </Button>
      </div>
    </div>
  );
}

function FailureAlert({ failure, errors, title }: { failure: Problem; errors: Record<string, string>; title: string }) {
  const list = Object.entries(errors);
  return (
    <Alert tone="danger" title={failure.code === "PROJ-FC-001" ? "That name is taken" : title} live="alert">
      <ProblemMessage problem={failure} />
      {list.length > 0 ? (
        <ul className={plan.errorList}>
          {list.map(([k, v]) => (
            <li key={k}>
              {lineLabel(k)}: {v}
            </li>
          ))}
        </ul>
      ) : null}
    </Alert>
  );
}

/** Forecast models of a project (FR-41.20): the list and a form to add a model with its lines. */
export function ForecastsPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => listForecasts(id, signal), id);
  const [name, setName] = useState("");
  const [rows, setRows] = useState<ForecastRowValues[]>(() => [blankLine()]);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per form content, so a retry of the same body after a lost response replays.
  const key = useRef(newKey());

  if (state.error?.status === 404) return <ProjectMissing id={id} title="Forecasts" />;
  const items = state.data?.items ?? [];

  async function create(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = forecastBody(name, rows);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const out = await createForecast(id, input, key.current);
      key.current = newKey();
      navigate(`/projects/${encodeURIComponent(id)}/forecasts/${encodeURIComponent(out.id)}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse" || problem.status === 400 || problem.status === 422) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<ForecastModel>[] = [
    { key: "name", header: "Model" },
    { key: "lineCount", header: "Lines", align: "end" },
    { key: "totalCost", header: "Forecast cost", align: "end", mono: true, render: (m) => `${m.totalCost} ${m.currency}` },
    { key: "totalSales", header: "Forecast sales", align: "end", mono: true, render: (m) => `${m.totalSales} ${m.currency}` },
    { key: "version", header: "Version", align: "end" },
    { key: "updatedAt", header: "Updated", mono: true, render: (m) => m.updatedAt.slice(0, 10) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Forecasts for ${id}`}
        description="A forecast model holds the hours, expenses, items, fees and on-account amounts you expect per month. Open a model to compare it with the recorded transactions."
        actions={
          <Link className={buttonClassName("secondary")} to={`/projects/${encodeURIComponent(id)}`}>
            Back to project
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the forecast models" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Forecast models"
          columns={columns}
          rows={items}
          getRowKey={(m) => m.id}
          loading={state.loading}
          onRowOpen={(m) => navigate(`/projects/${encodeURIComponent(id)}/forecasts/${encodeURIComponent(m.id)}`)}
          empty={{ title: "No forecast models yet", description: "Add a model below with a line per month and transaction type." }}
        />
      )}
      <Card title="Add a forecast model">
        <form className={styles.stack} onSubmit={create} noValidate aria-label="New forecast model">
          <FormField id="forecast-new-name" label="Name" error={errors.name}>
            {(control) => (
              <Input
                {...control}
                value={name}
                maxLength={NAME_MAX}
                onChange={(e) => {
                  setName(e.target.value);
                  key.current = newKey();
                }}
              />
            )}
          </FormField>
          <LinesEditor
            prefix="New"
            rows={rows}
            errors={errors}
            onChange={(next) => {
              setRows(next);
              key.current = newKey();
            }}
          />
          {failure ? <FailureAlert failure={failure} errors={errors} title="Could not add the forecast model" /> : null}
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              Add forecast model
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

const varianceText = (v: string) => (v.startsWith("-") || /^0(\.0+)?$/.test(v) ? v : `+${v}`);

/** One forecast model: forecast vs actual by month and type (§8.41.14) and its lines, replaced as a whole. */
export function ForecastDetailPage() {
  const { id = "", modelId = "" } = useParams();
  const state = useLoad((signal) => getForecast(id, modelId, signal), `${id}|${modelId}`);
  const [latest, setLatest] = useState<ForecastView | null>(null);
  const view = latest ?? state.data;
  const [name, setName] = useState("");
  const [rows, setRows] = useState<ForecastRowValues[]>([]);
  const [dirty, setDirty] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!view || dirty) return;
    setName(view.model.name);
    setRows(rowsOf(view.model.lines));
    // Rebuild the editor only when a new version of the model arrives (not on unsaved edits).
  }, [view?.model.id, view?.model.version, dirty]);

  if (!view) {
    if (state.error?.status === 404) {
      return (
        <div className={styles.stack}>
          <PageHeader title="Forecast model" />
          <Card>
            <EmptyState
              icon="grid"
              title="Forecast model not found"
              description="This forecast model does not exist for the project."
              action={
                <Link className={buttonClassName("secondary")} to={`/projects/${encodeURIComponent(id)}/forecasts`}>
                  Back to forecasts
                </Link>
              }
            />
          </Card>
        </div>
      );
    }
    if (state.error) {
      return (
        <div className={styles.stack}>
          <PageHeader title="Forecast model" />
          <Alert tone="danger" title="Could not load the forecast model" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        </div>
      );
    }
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Forecast model" />
        <span className="visually-hidden">Loading the forecast model…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const current = view;
  const model = current.model;

  function reload() {
    setLatest(null);
    setDirty(false);
    setErrors({});
    setFailure(null);
    state.reload();
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = forecastBody(name, rows);
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (!input) return;
    setBusy(true);
    try {
      await replaceForecast(id, modelId, input, model.version);
      // The comparison depends on the lines, so read the model again rather than patching it here.
      const fresh = await getForecast(id, modelId);
      setDirty(false);
      setLatest(fresh);
      setNotice(`The forecast is saved (version ${fresh.model.version}).`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "conflict_version";
  const columns: DataTableColumn<ForecastRow>[] = [
    { key: "period", header: "Month", mono: true },
    { key: "txnType", header: "Type", render: (r) => txnTypeLabel[r.txnType] },
    { key: "forecastQuantity", header: "Forecast qty", align: "end", mono: true },
    { key: "actualQuantity", header: "Actual qty", align: "end", mono: true },
    { key: "forecastCost", header: "Forecast cost", align: "end", mono: true },
    { key: "actualCost", header: "Actual cost", align: "end", mono: true },
    { key: "costVariance", header: "Cost variance", align: "end", mono: true, render: (r) => varianceText(r.costVariance) },
    { key: "forecastSales", header: "Forecast sales", align: "end", mono: true },
    { key: "actualSales", header: "Actual sales", align: "end", mono: true },
    { key: "salesVariance", header: "Sales variance", align: "end", mono: true, render: (r) => varianceText(r.salesVariance) },
  ];
  const facts: [string, string][] = [
    ["Currency", model.currency],
    ["Lines", String(model.lineCount)],
    ["Forecast cost", `${model.totalCost} ${model.currency}`],
    ["Forecast sales", `${model.totalSales} ${model.currency}`],
    ["Version", String(model.version)],
    ["Created by", model.createdBy],
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${model.name} for ${id}`}
        description="Actuals are the project transactions recorded in each month. A variance is actual minus forecast, so a positive cost variance is overspend."
        actions={
          <Link className={buttonClassName("secondary")} to={`/projects/${encodeURIComponent(id)}/forecasts`}>
            All forecasts
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Summary">
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <DataTable
        label="Forecast vs actual"
        columns={columns}
        rows={current.comparison}
        getRowKey={(r) => `${r.period}|${r.txnType}`}
        empty={{ title: "Nothing to compare yet", description: "Add forecast lines below or record transactions on the project." }}
      />
      <Card title="Edit forecast">
        <form className={styles.stack} onSubmit={save} noValidate aria-label="Edit forecast model">
          <p className={styles.note}>Saving replaces every line of the model with the lines below.</p>
          <FormField id="forecast-edit-name" label="Name" error={errors.name}>
            {(control) => (
              <Input
                {...control}
                value={name}
                maxLength={NAME_MAX}
                onChange={(e) => {
                  setName(e.target.value);
                  setDirty(true);
                }}
              />
            )}
          </FormField>
          <LinesEditor
            prefix="Edit"
            rows={rows}
            errors={errors}
            onChange={(next) => {
              setRows(next);
              setDirty(true);
            }}
          />
          {failure ? (
            stale ? (
              <Alert tone="danger" title="Someone else changed this forecast" live="alert" action={<Button size="sm" onClick={reload}>Reload forecast</Button>}>
                Reload to see the latest lines; your unsaved changes are discarded.
              </Alert>
            ) : (
              <FailureAlert failure={failure} errors={errors} title="Could not save the forecast" />
            )
          ) : null}
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy} disabled={!dirty}>
              Save forecast
            </Button>
            <Button
              variant="ghost"
              disabled={!dirty}
              onClick={() => {
                setDirty(false);
                setErrors({});
                setFailure(null);
                setName(model.name);
                setRows(rowsOf(model.lines));
              }}
            >
              Discard changes
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
