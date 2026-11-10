import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
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
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createFormula,
  describeProblem,
  getFormula,
  LINE_TYPES,
  listFormulas,
  newKey,
  previewScale,
  type Formula,
  type FormulaLine,
  type LineType,
  type NewFormula,
  type Problem,
  type ScaledLine,
  type ScalePreview,
} from "./api/client";
import { formatDateTime, LINE_TYPE_LABELS, PERCENT, percentAtMost100, percentBalanced, QUANTITY, shortId, sumPercent, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const listColumns: DataTableColumn<Formula>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "size", header: "Formula size", align: "end", render: (f) => `${f.size} ${f.unit}` },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "version", header: "Version", align: "end" },
  { key: "createdAt", header: "Created", render: (f) => formatDateTime(f.createdAt) },
];

/** Formulas: the recipes batch orders are created from (FR-24.1). */
export function FormulasPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listFormulas(signal), "formulas");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Formulas"
        description="Recipes with their formula size. Scalable lines grow with the batch; percent-controlled lines take their share of it."
        actions={
          <Link className={buttonClassName("primary")} to="/processmfg/formulas/new">
            New formula
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load formulas" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Formulas"
          columns={listColumns}
          rows={state.data ?? []}
          getRowKey={(f) => f.id}
          loading={state.loading}
          empty={{ title: "No formulas yet", description: "Create a formula before you plan a batch order." }}
          onRowOpen={(f) => navigate(`/processmfg/formulas/${f.id}`)}
        />
      )}
    </div>
  );
}

interface LineDraft {
  itemId: string;
  type: LineType;
  qty: string;
  percent: string;
  scalable: boolean;
  percentControlled: boolean;
}

interface FormulaValues {
  code: string;
  name: string;
  itemId: string;
  size: string;
  unit: string;
  lines: LineDraft[];
}

type FormulaErrors = Partial<Record<"code" | "name" | "itemId" | "size" | "unit" | "lines", string>> & { line?: Record<string, string> };

const CODE = /^[A-Z0-9][A-Z0-9_.-]{0,39}$/;

function blankLine(): LineDraft {
  return { itemId: "", type: "ingredient", qty: "", percent: "", scalable: true, percentControlled: false };
}

/** The running total of the percent-controlled scalable lines, or null when there are none. */
export function percentTotal(lines: LineDraft[]): string | null {
  const counted = lines.filter((l) => l.percentControlled && l.scalable);
  return counted.length === 0 ? null : sumPercent(counted.map((l) => l.percent));
}

/** Checks that mirror the service (FR-24.1), and the request the form produces. */
export function validateFormula(values: FormulaValues): { errors: FormulaErrors; input?: NewFormula } {
  const errors: FormulaErrors = {};
  const line: Record<string, string> = {};
  if (!CODE.test(values.code)) errors.code = "Use 1 to 40 capital letters, digits, _, . or -, starting with a letter or digit.";
  if (values.name.trim().length < 1 || values.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!UUID.test(values.itemId.trim())) errors.itemId = "Enter the product's item id (a UUID).";
  if (!QUANTITY.test(values.size) || /^0+(\.0+)?$/.test(values.size)) errors.size = "Enter a positive quantity with at most 6 decimals.";
  if (values.unit.trim().length < 1 || values.unit.trim().length > 10) errors.unit = "Enter a unit of up to 10 characters, such as kg.";
  if (values.lines.length === 0) errors.lines = "Add at least one line.";
  values.lines.forEach((l, i) => {
    if (!UUID.test(l.itemId.trim())) line[`${i}.itemId`] = "Enter an item id (a UUID).";
    if (!QUANTITY.test(l.qty || "0")) line[`${i}.qty`] = "Enter a quantity with at most 6 decimals.";
    if (!PERCENT.test(l.percent || "0") || !percentAtMost100(l.percent || "0")) line[`${i}.percent`] = "Enter 0 to 100 with at most 4 decimals.";
    if (l.percentControlled && (l.type === "co-product" || l.type === "by-product")) line[`${i}.percentControlled`] = "Outputs cannot be percent-controlled.";
  });
  const total = percentTotal(values.lines);
  if (total !== null && !percentBalanced(total)) errors.lines = `Percent-controlled lines sum to ${total}%, not 100%.`;
  if (Object.keys(line).length > 0) errors.line = line;
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      code: values.code,
      name: values.name.trim(),
      itemId: values.itemId.trim(),
      size: values.size,
      unit: values.unit.trim(),
      lines: values.lines.map(
        (l, i): FormulaLine => ({
          lineNumber: i + 1,
          itemId: l.itemId.trim(),
          type: l.type,
          qty: l.qty || "0",
          percent: l.percent || "0",
          scalable: l.scalable,
          percentControlled: l.percentControlled,
        }),
      ),
    },
  };
}

/** Formula designer: lines with type, quantity, percent and the scalable flags; the percent total runs live. */
export function NewFormulaPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<FormulaValues>({ code: "", name: "", itemId: "", size: "1000", unit: "kg", lines: [blankLine()] });
  const [errors, setErrors] = useState<FormulaErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<FormulaValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function updateLine(index: number, patch: Partial<LineDraft>) {
    update({ lines: values.lines.map((l, i) => (i === index ? { ...l, ...patch } : l)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateFormula(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const formula = await createFormula(input, key.current);
      navigate(`/processmfg/formulas/${formula.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "PMF-0103") setErrors({ code: "A formula with this code already exists." });
      else if (problem.code === "PMF-0001") setErrors({ lines: problem.message });
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const total = percentTotal(values.lines);
  const lineError = (i: number, field: string) => errors.line?.[`${i}.${field}`];

  return (
    <div className={styles.stack}>
      <PageHeader title="New formula" description="Lines are quantities for the formula size. A batch order scales them to its batch size." />
      <form className={styles.stack} onSubmit={submit} noValidate>
        <Card title="Formula">
          <div className={styles.form}>
            <FormField id="formula-code" label="Code" hint="For example DOUGH-1." error={errors.code}>
              {(c) => <Input {...c} value={values.code} maxLength={40} onChange={(e) => update({ code: e.target.value.toUpperCase() })} invalid={Boolean(errors.code)} />}
            </FormField>
            <FormField id="formula-name" label="Name" error={errors.name}>
              {(c) => <Input {...c} value={values.name} maxLength={120} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
            </FormField>
            <FormField id="formula-item" label="Product item id" hint="The item the batch produces." error={errors.itemId}>
              {(c) => <Input {...c} value={values.itemId} className={styles.mono} onChange={(e) => update({ itemId: e.target.value })} invalid={Boolean(errors.itemId)} />}
            </FormField>
            <FormField id="formula-size" label="Formula size" error={errors.size}>
              {(c) => <Input {...c} inputMode="decimal" value={values.size} onChange={(e) => update({ size: e.target.value })} invalid={Boolean(errors.size)} />}
            </FormField>
            <FormField id="formula-unit" label="Unit" error={errors.unit}>
              {(c) => <Input {...c} value={values.unit} maxLength={10} onChange={(e) => update({ unit: e.target.value })} invalid={Boolean(errors.unit)} />}
            </FormField>
          </div>
        </Card>
        <Card
          title="Lines"
          actions={
            <Button size="sm" onClick={() => update({ lines: [...values.lines, blankLine()] })}>
              Add line
            </Button>
          }
        >
          <div className={styles.stack}>
            {values.lines.map((line, i) => (
              <fieldset key={i} className={styles.lineRow}>
                <legend className={styles.groupsLegend}>{`Line ${i + 1}`}</legend>
                <FormField id={`line-${i}-item`} label="Item" error={lineError(i, "itemId")}>
                  {(c) => <EntityLookup entity="product" {...c} value={line.itemId} onChange={(e) => updateLine(i, { itemId: e.target.value })} invalid={Boolean(lineError(i, "itemId"))} />}
                </FormField>
                <FormField id={`line-${i}-type`} label="Type">
                  {(c) => (
                    <Select {...c} value={line.type} onChange={(e) => updateLine(i, { type: e.target.value as LineType })}>
                      {LINE_TYPES.map((t) => (
                        <option key={t} value={t}>
                          {LINE_TYPE_LABELS[t]}
                        </option>
                      ))}
                    </Select>
                  )}
                </FormField>
                <FormField id={`line-${i}-qty`} label="Quantity" error={lineError(i, "qty")}>
                  {(c) => <Input {...c} inputMode="decimal" value={line.qty} onChange={(e) => updateLine(i, { qty: e.target.value })} invalid={Boolean(lineError(i, "qty"))} />}
                </FormField>
                <FormField id={`line-${i}-percent`} label="Percent" error={lineError(i, "percent")}>
                  {(c) => <Input {...c} inputMode="decimal" value={line.percent} onChange={(e) => updateLine(i, { percent: e.target.value })} invalid={Boolean(lineError(i, "percent"))} />}
                </FormField>
                <div className={styles.lineChecks}>
                  <span className={styles.check}>
                    <Input type="checkbox" id={`line-${i}-scalable`} checked={line.scalable} onChange={(e) => updateLine(i, { scalable: e.target.checked })} />
                    <label htmlFor={`line-${i}-scalable`}>Scalable</label>
                  </span>
                  <span className={styles.check}>
                    <Input
                      type="checkbox"
                      id={`line-${i}-pct`}
                      checked={line.percentControlled}
                      onChange={(e) => updateLine(i, { percentControlled: e.target.checked })}
                    />
                    <label htmlFor={`line-${i}-pct`}>Percent-controlled</label>
                  </span>
                  {lineError(i, "percentControlled") ? (
                    <p role="alert" className={styles.fieldError}>
                      {lineError(i, "percentControlled")}
                    </p>
                  ) : null}
                  {values.lines.length > 1 ? (
                    <Button size="sm" variant="ghost" onClick={() => update({ lines: values.lines.filter((_, j) => j !== i) })}>
                      {`Remove line ${i + 1}`}
                    </Button>
                  ) : null}
                </div>
              </fieldset>
            ))}
            <p className={total !== null && !percentBalanced(total) ? styles.fieldError : styles.note} aria-live="polite">
              {total === null ? "No percent-controlled lines." : `Percent-controlled total: ${total}%`}
            </p>
            {errors.lines ? (
              <p role="alert" className={styles.fieldError}>
                {errors.lines}
              </p>
            ) : null}
          </div>
        </Card>
        {failure ? (
          <Alert tone="danger" title="Could not create the formula" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Create formula
          </Button>
          <Link className={buttonClassName("ghost")} to="/processmfg/formulas">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

const lineColumns: DataTableColumn<FormulaLine>[] = [
  { key: "lineNumber", header: "Line", align: "end" },
  { key: "itemId", header: "Item", mono: true, render: (l) => shortId(l.itemId) },
  { key: "type", header: "Type", render: (l) => LINE_TYPE_LABELS[l.type] ?? l.type },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "percent", header: "Percent", align: "end" },
  { key: "scalable", header: "Scaling", render: (l) => (l.percentControlled ? "Percent of batch" : l.scalable ? "Scalable" : "Fixed") },
];

const previewColumns: DataTableColumn<ScaledLine>[] = [
  { key: "lineNumber", header: "Line", align: "end" },
  { key: "itemId", header: "Item", mono: true, render: (l) => shortId(l.itemId) },
  { key: "type", header: "Type", render: (l) => LINE_TYPE_LABELS[l.type] ?? l.type },
  { key: "formulaQty", header: "Formula qty", align: "end" },
  { key: "qty", header: "Batch qty", align: "end" },
];

/** One formula, its lines and a side-by-side scale preview (BR-24.1). */
export function FormulaDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getFormula(id, signal), id);
  const [batchSize, setBatchSize] = useState("");
  const [preview, setPreview] = useState<ScalePreview | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Formula" />
        <Alert tone="danger" title="Could not load the formula" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Formula" />
        <span className="visually-hidden">Loading the formula…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const formula = state.data;

  async function scale(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!QUANTITY.test(batchSize) || /^0+(\.0+)?$/.test(batchSize)) {
      setPreviewError("Enter a positive batch size with at most 6 decimals.");
      return;
    }
    setPreviewError(null);
    setBusy(true);
    try {
      setPreview(await previewScale(formula.id, batchSize));
    } catch (error) {
      setPreview(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={formula.code}
        description={formula.name}
        meta={<Badge tone="neutral">{`Version ${formula.version}`}</Badge>}
        actions={
          <Link className={buttonClassName("primary")} to={`/processmfg/batch-orders/new?formulaId=${formula.id}`}>
            New batch order
          </Link>
        }
      />
      <StatGroup label="Formula facts">
        <StatTile label="Formula size" value={`${formula.size} ${formula.unit}`} />
        <StatTile label="Lines" value={formula.lineCount} />
      </StatGroup>
      <DataTable
        label="Formula lines"
        columns={lineColumns}
        rows={formula.lines ?? []}
        getRowKey={(l) => String(l.lineNumber)}
        empty={{ title: "No lines", description: "This formula has no lines." }}
      />
      <Card title="Scale preview">
        <div className={styles.stack}>
          <form className={styles.toolbar} onSubmit={scale} noValidate>
            <FormField id="preview-size" label={`Batch size (${formula.unit})`} error={previewError ?? undefined}>
              {(c) => <Input {...c} inputMode="decimal" value={batchSize} onChange={(e) => setBatchSize(e.target.value)} invalid={Boolean(previewError)} />}
            </FormField>
            <Button type="submit" loading={busy}>
              Preview scaling
            </Button>
          </form>
          {failure ? (
            <Alert tone="danger" title="Could not scale the formula" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          ) : null}
          {preview ? (
            <>
              <p className={styles.note} role="status">{`Scale factor ${preview.scaleFactor} for ${preview.batchSize} ${formula.unit}.`}</p>
              <DataTable label="Scaled lines" columns={previewColumns} rows={preview.lines} getRowKey={(l) => String(l.lineNumber)} />
            </>
          ) : null}
        </div>
      </Card>
    </div>
  );
}
