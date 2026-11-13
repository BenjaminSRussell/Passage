import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
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
  Textarea,
  type DataTableColumn,
  FormErrorSummary,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
  FormSection,
} from "../../shell/ui";
import {
  BACKORDER_RUN_STATUSES,
  BACKORDER_STRATEGIES,
  createBackorderRun,
  describeProblem,
  listBackorderRuns,
  newKey,
  type BackorderRun,
  type BackorderRunFilter,
  type BackorderRunInput,
  type BackorderRunStatus,
  type BackorderStrategy,
  type Problem,
} from "./api/client";
import { strategyHelp } from "./BackorderRunPage";
import { DECIMAL, formatDateTime, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const CLASS = /^[1-9]$/;

export interface RunValues {
  legalEntityId: string;
  productId: string;
  siteId: string;
  warehouseId: string;
  strategy: BackorderStrategy;
  supply: string;
  supplyDate: string;
  loseThreshold: string;
  defaultPriorityClass: string;
  /** One "customer-uuid class" pair per line. */
  priorities: string;
}

export const emptyRunValues: RunValues = {
  legalEntityId: "",
  productId: "",
  siteId: "",
  warehouseId: "",
  strategy: "REDISTRIBUTE",
  supply: "",
  supplyDate: "",
  loseThreshold: "",
  defaultPriorityClass: "",
  priorities: "",
};

/** Checks that mirror the service (api/backorder_runs.go buildBackorderRun). Keys are the service's field names. */
export function validateRunRequest(values: RunValues): { errors: Errors; input?: BackorderRunInput } {
  const errors: Errors = {};
  for (const key of ["legalEntityId", "productId", "siteId", "warehouseId"] as const) {
    if (!UUID.test(values[key].trim())) errors[key] = "Enter a UUID.";
  }
  if (!DECIMAL.test(values.supply)) errors.supply = "Enter the supply (0 or more, up to 6 decimals).";
  if (!DATE.test(values.supplyDate)) errors.supplyDate = "Choose the date the supply is available.";
  if (values.loseThreshold !== "" && !CLASS.test(values.loseThreshold)) errors.loseThreshold = "Enter a priority class from 1 to 9.";
  if (values.defaultPriorityClass !== "" && !CLASS.test(values.defaultPriorityClass)) errors.defaultPriorityClass = "Enter a priority class from 1 to 9.";
  const priorityClasses: Record<string, number> = {};
  values.priorities
    .split("\n")
    .map((row) => row.trim())
    .filter(Boolean)
    .forEach((row) => {
      const [customer = "", cls = "", ...rest] = row.split(/\s+/);
      if (rest.length > 0 || !UUID.test(customer) || !CLASS.test(cls)) {
        errors.priorities = "Write one customer per line as: customer UUID, a space, then a class from 1 to 9.";
      } else {
        priorityClasses[customer] = Number(cls);
      }
    });
  if (Object.keys(errors).length > 0) return { errors };
  const input: BackorderRunInput = {
    legalEntityId: values.legalEntityId.trim(),
    productId: values.productId.trim(),
    siteId: values.siteId.trim(),
    warehouseId: values.warehouseId.trim(),
    strategy: values.strategy,
    supply: values.supply,
    supplyDate: values.supplyDate,
  };
  if (values.loseThreshold !== "") input.loseThreshold = Number(values.loseThreshold);
  if (values.defaultPriorityClass !== "") input.defaultPriorityClass = Number(values.defaultPriorityClass);
  if (Object.keys(priorityClasses).length > 0) input.priorityClasses = priorityClasses;
  return { errors, input };
}

export function RunStatusBadge({ status }: { status: BackorderRunStatus }) {
  return <Badge tone={status === "COMMITTED" ? "success" : "info"}>{sentence(status)}</Badge>;
}

const columns: DataTableColumn<BackorderRun>[] = [
  { key: "createdAt", header: "Planned", render: (r) => <Link to={`/sales/backorder-processing/${r.id}`}>{formatDateTime(r.createdAt)}</Link> },
  { key: "productId", header: "Product", render: (r) => <EntityName entity="product" id={r.productId} /> },
  { key: "strategy", header: "Strategy", render: (r) => sentence(r.strategy) },
  { key: "supply", header: "Supply", align: "end" },
  { key: "supplyDate", header: "Supply date", mono: true },
  { key: "changedCount", header: "Lines changed", align: "end" },
  { key: "deconfirmedCount", header: "Deconfirmed", align: "end" },
  { key: "status", header: "Status", render: (r) => <RunStatusBadge status={r.status} /> },
];

/** Stored backorder runs (FR-27.17): previews waiting for a commit and committed history. */
export function BackorderRunsPage() {
  const [draft, setDraft] = useState({ legalEntityId: "", productId: "", status: "" });
  const [filter, setFilter] = useState<BackorderRunFilter>({});
  const invalid: Errors = {};
  if (draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.productId.trim() && !UUID.test(draft.productId.trim())) invalid.productId = "Enter a product UUID.";
  const state = useLoad((signal) => listBackorderRuns(filter, signal), JSON.stringify(filter));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Backorder runs"
        description="Plans that re-confirm the open order lines of a product when supply changes. A run is a preview until it is committed; committing rewrites the schedule lines of the changed lines."
        actions={
          <Link className={buttonClassName("primary")} to="/sales/backorder-processing/new">
            New run
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        aria-label="Filter backorder runs"
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length === 0) {
            setFilter({ legalEntityId: draft.legalEntityId.trim(), productId: draft.productId.trim(), status: draft.status as BackorderRunStatus | "" });
          }
        }}
      >
        <FormField id="runs-le" label="Legal entity" error={invalid.legalEntityId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="runs-product" label="Product" error={invalid.productId} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.productId} onChange={(e) => setDraft({ ...draft, productId: e.target.value })} invalid={Boolean(invalid.productId)} />}
        </FormField>
        <FormField id="runs-status" label="Status" optional>
          {(control) => (
            <Select {...control} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value })}>
              <option value="">Any</option>
              {BACKORDER_RUN_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load the backorder runs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Backorder runs"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No backorder runs", description: "Plan a run when new supply arrives or a shortage forces lines to be re-confirmed." }}
        />
      )}
    </div>
  );
}

const NEW_BACKORDER_RUN_FIELDS: Record<string, FormFieldTarget> = {
  legalEntityId: { id: "run-legalEntityId", label: "Legal entity" },
  productId: { id: "run-productId", label: "Product" },
  siteId: { id: "run-siteId", label: "Site" },
  warehouseId: { id: "run-warehouseId", label: "Warehouse" },
  supply: { id: "run-supply", label: "Supply" },
  supplyDate: { id: "run-supplyDate", label: "Supply date" },
  loseThreshold: { id: "run-loseThreshold", label: "Lose threshold" },
  defaultPriorityClass: { id: "run-defaultPriorityClass", label: "Default priority class" },
  priorities: { id: "run-priorities", label: "Customer priority classes" },
};

/** Plans a run over the open lines the service holds for a product at a site and warehouse (FR-27.17, BR-27.11). */
export function NewBackorderRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<RunValues>(emptyRunValues);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));

  function update(patch: Partial<RunValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRunRequest(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const saved = await createBackorderRun(input, key.current);
      release();
      navigate(`/sales/backorder-processing/${saved.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: keyof RunValues, label: string, extra: { type?: string; mono?: boolean; decimal?: boolean; hint?: string; optional?: boolean } = {}) => (
    <FormField id={`run-${name}`} label={label} hint={extra.hint} error={errors[name]} optional={extra.optional}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          inputMode={extra.decimal ? "decimal" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New backorder run"
        description="The service reads the open lines of the confirmed orders for the product at the site and warehouse and plans how the supply is shared among them. Nothing changes until you commit the run."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New backorder run">
          <FormErrorSummary items={summaryItems(errors, NEW_BACKORDER_RUN_FIELDS)} attempt={attempt} />
          {text("legalEntityId", "Legal entity", { mono: true })}
          {text("productId", "Product", { mono: true })}
          {text("siteId", "Site", { mono: true })}
          {text("warehouseId", "Warehouse", { mono: true })}
          <FormField id="run-strategy" label="Strategy" hint={strategyHelp[values.strategy]}>
            {(control) => (
              <Select {...control} value={values.strategy} onChange={(e) => update({ strategy: e.target.value as BackorderStrategy })}>
                {BACKORDER_STRATEGIES.map((s) => (
                  <option key={s} value={s}>
                    {sentence(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("supply", "Supply", { decimal: true, hint: "The whole quantity available for these lines, including what they hold today." })}
          {text("supplyDate", "Supply date", { type: "date", hint: "A line that gains quantity is promised no earlier than this." })}
          <FormSection collapse="advanced">
            {text("loseThreshold", "Lose threshold", { optional: true, hint: "Priority class from which LOSE may deconfirm. Default 3." })}
            {text("defaultPriorityClass", "Default priority class", { optional: true, hint: "For customers not listed below. Default 5; 1 is the highest." })}
            <FormField id="run-priorities" label="Customer priority classes" optional error={errors.priorities} hint="One per line: customer UUID, a space, a class from 1 to 9.">
              {(control) => (
                <Textarea
                  {...control}
                  className={styles.mono}
                  rows={3}
                  value={values.priorities}
                  onChange={(e) => update({ priorities: e.target.value })}
                  invalid={Boolean(errors.priorities)}
                />
              )}
            </FormField>
          </FormSection>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0027" ? "The run cannot be planned" : "Could not plan the run"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Plan run
            </Button>
            <Link className={buttonClassName("ghost")} to="/sales/backorder-processing">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}
