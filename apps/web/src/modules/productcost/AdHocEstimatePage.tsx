import { useState, type FormEvent } from "react";
import {
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  StatGroup,
  StatTile,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  adHocEstimate,
  describeProblem,
  type AdHocEstimate,
  type AdHocEstimateRequest,
  type Characteristic,
  type EstimateLine,
  type EstimateObjectType,
  type ModelLine,
  type ModelLineType,
  type Price,
  type Problem,
  type SheetNode,
} from "./api/client";
import { amount, dash, isDecimal, isSignedDecimal } from "./format";
import { LineGrid, type LineField } from "./LineGrid";
import { DiagnosticsTable, ProblemAlert, SplitTable } from "./shared";
import styles from "./pages.module.css";

type CharKey = "name" | "unit" | "default" | "value";
type LineKey = "type" | "ref" | "component" | "quantityFormula" | "priceFormula";
type PriceKey = "ref" | "amount" | "source";
type Errors = Record<string, string>;

interface Values {
  currency: string;
  objectType: EstimateObjectType;
  objectId: string;
  modelCode: string;
  characteristics: Record<CharKey, string>[];
  lines: Record<LineKey, string>[];
  prices: Record<PriceKey, string>[];
  sheet: string;
  routingHours: string;
}

const charFields: LineField<CharKey>[] = [
  { name: "name", label: "Name", mono: true },
  { name: "unit", label: "Unit" },
  { name: "default", label: "Default", decimal: true },
  { name: "value", label: "Value for this estimate", decimal: true },
];

const lineTypes: { value: ModelLineType; label: string }[] = [
  { value: "Item", label: "Item" },
  { value: "Activity", label: "Activity" },
  { value: "Subcontract", label: "Subcontract" },
  { value: "Overhead", label: "Overhead" },
];

const lineFields: LineField<LineKey>[] = [
  { name: "type", label: "Line type", options: lineTypes },
  { name: "ref", label: "Item, activity or overhead", mono: true },
  { name: "component", label: "Cost component", mono: true },
  { name: "quantityFormula", label: "Quantity formula", mono: true },
  { name: "priceFormula", label: "Price formula (optional)", mono: true },
];

const priceFields: LineField<PriceKey>[] = [
  { name: "ref", label: "Item, activity or service", mono: true },
  { name: "amount", label: "Unit price", decimal: true },
  {
    name: "source",
    label: "Price source",
    options: [
      { value: "Version", label: "Costing version" },
      { value: "Fallback", label: "Fallback to active standard" },
    ],
  },
];

const emptyChar = (): Record<CharKey, string> => ({ name: "", unit: "", default: "", value: "" });
const emptyLine = (): Record<LineKey, string> => ({ type: "Item", ref: "", component: "", quantityFormula: "", priceFormula: "" });
const emptyPrice = (): Record<PriceKey, string> => ({ ref: "", amount: "", source: "Version" });

/**
 * An injection-moulded part quoted from a costing model (FR-39.13): granulate by
 * weight × cavities at 3.00, machine time at 40.00 per hour and machine overhead
 * at 2.00 per hour. With weight 0.5 and 0.5 hours the estimate is 27.00.
 */
export const injectionExample: Values = {
  currency: "USD",
  objectType: "Quote",
  objectId: "Q-1001",
  modelCode: "INJ-PART",
  characteristics: [
    { name: "cavities", unit: "pcs", default: "4", value: "" },
    { name: "weight", unit: "kg", default: "", value: "0.5" },
    { name: "hours", unit: "h", default: "", value: "0.5" },
  ],
  lines: [
    { type: "Item", ref: "RM-GRAN", component: "DM", quantityFormula: "weight * cavities", priceFormula: "" },
    { type: "Activity", ref: "MACHINE", component: "DL", quantityFormula: "hours", priceFormula: "" },
    { type: "Overhead", ref: "OH-MACH", component: "OVH", quantityFormula: "hours", priceFormula: "2" },
  ],
  prices: [
    { ref: "RM-GRAN", amount: "3.00", source: "Version" },
    { ref: "MACHINE", amount: "40.00", source: "Version" },
  ],
  sheet: "",
  routingHours: "",
};

const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
const blank = (row: Record<string, string>, keys: string[]) => keys.every((k) => row[k].trim() === "");

/** Checks that mirror the service, and the request the form sends. Error keys follow the grids (lines[0].ref). */
export function validateAdHoc(values: Values): { errors: Errors; request?: AdHocEstimateRequest } {
  const errors: Errors = {};
  const currency = values.currency.trim().toUpperCase();
  if (currency !== "" && !/^[A-Z]{3}$/.test(currency))
    errors.currency = "Enter a three-letter currency code such as USD, or leave it empty.";
  const objectId = values.objectId.trim();
  if (objectId === "") errors.objectId = "Enter the quote, internal order or project task.";
  const code = values.modelCode.trim();
  if (code === "") errors.modelCode = "Enter the costing model code.";

  const characteristics: Characteristic[] = [];
  const charValues: Record<string, string> = {};
  values.characteristics.forEach((c, i) => {
    if (blank(c, ["name", "unit", "default", "value"])) return;
    const name = c.name.trim();
    if (!NAME.test(name)) errors[`characteristics[${i}].name`] = "Use letters, digits and underscores, starting with a letter.";
    else if (characteristics.some((x) => x.name === name)) errors[`characteristics[${i}].name`] = "Each characteristic appears once.";
    if (c.default.trim() !== "" && !isSignedDecimal(c.default)) errors[`characteristics[${i}].default`] = "Enter a number.";
    if (c.value.trim() !== "" && !isSignedDecimal(c.value)) errors[`characteristics[${i}].value`] = "Enter a number.";
    if (c.default.trim() === "" && c.value.trim() === "") errors[`characteristics[${i}].value`] = "Enter a value or a default.";
    characteristics.push({
      name,
      unit: c.unit.trim() || undefined,
      default: c.default.trim() || undefined,
    });
    if (c.value.trim() !== "") charValues[name] = c.value.trim();
  });

  const lines: ModelLine[] = values.lines.map((ln, i) => {
    for (const key of ["ref", "component", "quantityFormula"] as const) {
      if (ln[key].trim() === "") errors[`lines[${i}].${key}`] = "Required.";
    }
    return {
      type: ln.type as ModelLineType,
      ref: ln.ref.trim(),
      component: ln.component.trim(),
      quantityFormula: ln.quantityFormula.trim(),
      priceFormula: ln.priceFormula.trim() || undefined,
    };
  });
  if (lines.length === 0) errors.lines = "Add at least one line.";

  const prices: Record<string, Price> = {};
  values.prices.forEach((p, i) => {
    if (blank(p, ["ref", "amount"])) return;
    const ref = p.ref.trim();
    if (ref === "") errors[`prices[${i}].ref`] = "Enter what the price is for.";
    else if (ref in prices) errors[`prices[${i}].ref`] = "Each price appears once.";
    if (!isDecimal(p.amount)) errors[`prices[${i}].amount`] = "Enter a price, 0 or more.";
    if (ref !== "") prices[ref] = { amount: p.amount.trim(), source: p.source };
  });

  let sheet: SheetNode[] | undefined;
  if (values.sheet.trim() !== "") {
    try {
      const parsed: unknown = JSON.parse(values.sheet);
      if (!Array.isArray(parsed)) throw new Error("not an array");
      sheet = parsed as SheetNode[];
    } catch {
      errors.sheet = "Enter the costing sheet as a JSON array of nodes, or leave it empty.";
    }
  }
  const hours = values.routingHours.trim();
  if (hours !== "" && !isDecimal(hours)) errors.routingHours = "Enter the hours, 0 or more, or leave it empty.";

  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      currency: currency || undefined,
      model: { code, characteristics, lines },
      objectType: values.objectType,
      objectId,
      characteristics: charValues,
      prices,
      sheet,
      routingHours: hours || undefined,
    },
  };
}

const estimateLineColumns: DataTableColumn<EstimateLine>[] = [
  { key: "type", header: "Type" },
  { key: "ref", header: "Reference", mono: true },
  { key: "component", header: "Component", mono: true },
  { key: "quantity", header: "Quantity", align: "end", render: (l) => amount(l.quantity, 0) },
  { key: "price", header: "Unit price", align: "end", render: (l) => amount(l.price) },
  { key: "amount", header: "Amount", align: "end", render: (l) => amount(l.amount) },
  { key: "source", header: "Price source", render: (l) => l.source || dash },
];

interface SubtotalRow {
  node: string;
  value: string;
}

const subtotalColumns: DataTableColumn<SubtotalRow>[] = [
  { key: "node", header: "Sheet node", mono: true },
  { key: "value", header: "Subtotal", align: "end", render: (r) => amount(r.value) },
];

const objectLabels: Record<EstimateObjectType, string> = {
  Quote: "Quote",
  InternalOrder: "Internal order",
  ProjectTask: "Project task",
};

/** Ad-hoc cost estimate of a quote, internal order or project task from a costing model (FR-39.13). */
export function AdHocEstimatePage() {
  const [values, setValues] = useState<Values>(injectionExample);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [estimate, setEstimate] = useState<AdHocEstimate | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateAdHoc(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setEstimate(await adHocEstimate(request));
    } catch (err) {
      const problem = describeProblem(err);
      setErrors(problem.fields);
      setFailure(problem);
      setEstimate(null);
    } finally {
      setBusy(false);
    }
  }

  const subtotals: SubtotalRow[] = Object.entries(estimate?.subtotals ?? {}).map(([node, value]) => ({ node, value }));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Ad-hoc estimate"
        description="Cost a quote, internal order or project task from a costing model. Characteristics such as weight, cavities or machine hours drive each line's quantity and, optionally, its price; other prices come from the costing version. The costing sheet is applied to the result. Models and estimates are not stored yet."
      />
      <Card title="Costing model and object" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <FormField id="adhoc-object-type" label="Costed object">
            {(control) => (
              <Select
                {...control}
                value={values.objectType}
                onChange={(e) => setValues({ ...values, objectType: e.target.value as EstimateObjectType })}
              >
                {(Object.keys(objectLabels) as EstimateObjectType[]).map((t) => (
                  <option key={t} value={t}>
                    {objectLabels[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="adhoc-object-id" label="Object id" error={errors.objectId}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.objectId}
                onChange={(e) => setValues({ ...values, objectId: e.target.value })}
                invalid={Boolean(errors.objectId)}
              />
            )}
          </FormField>
          <FormField id="adhoc-model-code" label="Costing model" error={errors.modelCode}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.modelCode}
                onChange={(e) => setValues({ ...values, modelCode: e.target.value })}
                invalid={Boolean(errors.modelCode)}
              />
            )}
          </FormField>
          <FormField
            id="adhoc-currency"
            label="Currency"
            hint="Sets the rounding of the total: two decimals by default, none for JPY."
            optional
            error={errors.currency}
          >
            {(control) => (
              <Input
                {...control}
                value={values.currency}
                onChange={(e) => setValues({ ...values, currency: e.target.value })}
                invalid={Boolean(errors.currency)}
              />
            )}
          </FormField>
          <fieldset className={`${styles.wide} ${styles.lineCard}`}>
            <legend className={styles.lineLegend}>Characteristics</legend>
            <p className={`${styles.note} ${styles.wide}`}>
              Formulas read characteristics by name. A value left empty takes the default.
            </p>
            <div className={styles.wide}>
              <LineGrid
                idPrefix="adhoc-char"
                noun="Characteristic"
                path="characteristics"
                fields={charFields}
                lines={values.characteristics}
                errors={errors}
                newLine={emptyChar}
                onChange={(characteristics) => setValues({ ...values, characteristics })}
              />
            </div>
          </fieldset>
          <fieldset className={`${styles.wide} ${styles.lineCard}`}>
            <legend className={styles.lineLegend}>Model lines</legend>
            <p className={`${styles.note} ${styles.wide}`}>
              Formulas use numbers, characteristic names, + − × ÷ (written + - * /) and parentheses. A line without a price formula takes
              its price from the prices below.
            </p>
            <div className={styles.wide}>
              <LineGrid
                idPrefix="adhoc-line"
                noun="Line"
                fields={lineFields}
                lines={values.lines}
                errors={errors}
                newLine={emptyLine}
                onChange={(lines) => setValues({ ...values, lines })}
              />
            </div>
          </fieldset>
          <fieldset className={`${styles.wide} ${styles.lineCard}`}>
            <legend className={styles.lineLegend}>Prices from the costing version</legend>
            <div className={styles.wide}>
              <LineGrid
                idPrefix="adhoc-price"
                noun="Price"
                path="prices"
                fields={priceFields}
                lines={values.prices}
                errors={errors}
                newLine={emptyPrice}
                onChange={(prices) => setValues({ ...values, prices })}
              />
            </div>
          </fieldset>
          <div className={styles.wide}>
            <FormField
              id="adhoc-sheet"
              label="Costing sheet"
              hint="Optional JSON array of sheet nodes (Basis, Surcharge, Rate, Total), as on the roll-up page."
              optional
              error={errors.sheet}
            >
              {(control) => (
                <Textarea
                  {...control}
                  mono
                  rows={4}
                  spellCheck={false}
                  value={values.sheet}
                  onChange={(e) => setValues({ ...values, sheet: e.target.value })}
                  invalid={Boolean(errors.sheet)}
                />
              )}
            </FormField>
          </div>
          <FormField id="adhoc-hours" label="Routing hours" hint="Drives sheet rates charged per hour." optional error={errors.routingHours}>
            {(control) => (
              <Input
                {...control}
                inputMode="decimal"
                value={values.routingHours}
                onChange={(e) => setValues({ ...values, routingHours: e.target.value })}
                invalid={Boolean(errors.routingHours)}
              />
            )}
          </FormField>
          {failure ? <ProblemAlert title="The estimate was not computed" problem={failure} /> : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Compute the estimate
            </Button>
            <Button variant="ghost" onClick={() => setValues(injectionExample)}>
              Load example
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Estimate lines" columns={estimateLineColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : estimate ? (
        <Card title={`Estimate for ${objectLabels[estimate.objectType]} ${estimate.objectId}`} headingLevel={3}>
          <div className={styles.stack}>
            <StatGroup label="Estimate total">
              <StatTile label="Total cost" value={amount(estimate.total)} />
              <StatTile label="Lines" value={String(estimate.lines.length)} />
              <StatTile label="Costing model" value={estimate.modelCode} />
            </StatGroup>
            <DataTable
              label={`Lines of ${estimate.objectId}`}
              columns={estimateLineColumns}
              rows={estimate.lines}
              getRowKey={(l, i) => `${l.ref}-${i}`}
              empty={{ title: "No lines", description: "The model priced no lines." }}
            />
            <SplitTable label={`Cost by component of ${estimate.objectId}`} split={estimate.split} />
            {subtotals.length > 0 ? (
              <DataTable
                label={`Sheet subtotals of ${estimate.objectId}`}
                columns={subtotalColumns}
                rows={subtotals}
                getRowKey={(r) => r.node}
              />
            ) : null}
            {estimate.diagnostics.length > 0 ? (
              <DiagnosticsTable label={`Diagnostics of ${estimate.objectId}`} diagnostics={estimate.diagnostics} />
            ) : null}
          </div>
        </Card>
      ) : (
        <Card>
          <EmptyState
            icon="note"
            headingLevel={3}
            title="No estimate yet"
            description="Describe the costing model, enter the characteristic values and prices, then compute the estimate by line and cost component."
          />
        </Card>
      )}
    </div>
  );
}
