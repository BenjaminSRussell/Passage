import { useState, type FormEvent } from "react";
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
  StatGroup,
  StatTile,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  massRoll,
  rollOne,
  type CostItem,
  type CostResult,
  type Diagnostic,
  type ExplosionMode,
  type Problem,
  type RollRun,
  type Split,
  type ValuationView,
} from "./api/client";
import { amount, dash } from "./format";
import { ExplosionModeField } from "./shared";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

/** The FR-39.4 worked example: SA-200 under FG-100 rolls to 104.32. */
export const workedExample: CostItem[] = [
  { id: "RM-300", kind: "Purchased", price: "4.00" },
  { id: "RM-301", kind: "Purchased", price: "2.50" },
  { id: "RM-302", kind: "Purchased", price: "1.00" },
  {
    id: "SA-200",
    kind: "Manufactured",
    costingLot: "1",
    bom: [
      { childId: "RM-301", qty: "3" },
      { childId: "RM-302", qty: "1", scrapPct: "10" },
    ],
    operations: [{ runHours: "0.25", rate: "60" }],
    sheet: [
      { id: "OH-MAT", type: "Surcharge", component: "IND-MAT", basis: ["DM"], percentage: "10" },
      { id: "OH-LAB", type: "Surcharge", component: "IND-LAB", basis: ["DL"], percentage: "50" },
    ],
  },
  {
    id: "FG-100",
    kind: "Manufactured",
    costingLot: "10",
    bom: [
      { childId: "SA-200", qty: "2" },
      { childId: "RM-300", qty: "1" },
    ],
    operations: [{ runHours: "0.5", setupHours: "1", rate: "40" }],
    sheet: [
      { id: "OH-MAT", type: "Surcharge", component: "IND-MAT", basis: ["DM"], percentage: "10" },
      { id: "OH-LAB", type: "Surcharge", component: "IND-LAB", basis: ["DL"], percentage: "50" },
    ],
  },
];

const exampleText = JSON.stringify(workedExample, null, 2);

interface Values {
  items: string;
  itemId: string;
  mode: ExplosionMode;
  fallbackToActive: boolean;
  view: ValuationView;
}

type Errors = Partial<Record<"items" | "itemId", string>>;

/** Parses and checks the item list the way the service will, so obvious mistakes never leave the browser. */
export function parseItems(text: string): { items?: CostItem[]; error?: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { error: "Enter the items as a JSON array, such as the worked example." };
  }
  if (!Array.isArray(parsed) || parsed.length === 0) {
    return { error: "Enter at least one item in a JSON array." };
  }
  for (const [i, raw] of parsed.entries()) {
    const it = raw as Partial<CostItem> | null;
    if (!it || typeof it !== "object" || typeof it.id !== "string" || it.id === "") {
      return { error: `Item ${i + 1} needs an id.` };
    }
    if (it.kind !== "Purchased" && it.kind !== "Manufactured") {
      return { error: `Item ${it.id}: kind must be Purchased or Manufactured.` };
    }
  }
  return { items: parsed as CostItem[] };
}

interface SplitRow {
  component: string;
  own: string;
  rolled: string;
  total: string;
}

/** One row per component: what arises at this level, what children carry up, and the sum. */
export function splitRows(result: CostResult, split: Split): SplitRow[] {
  const components = Array.from(new Set([...Object.keys(result.own), ...Object.keys(result.rolled), ...Object.keys(split)])).sort();
  return components.map((component) => ({
    component,
    own: result.own[component] ?? "0",
    rolled: result.rolled[component] ?? "0",
    total: split[component] ?? "0",
  }));
}

const splitColumns: DataTableColumn<SplitRow>[] = [
  { key: "component", header: "Component", mono: true },
  { key: "own", header: "Own level", align: "end", render: (row) => amount(row.own) },
  { key: "rolled", header: "Rolled from children", align: "end", render: (row) => amount(row.rolled) },
  { key: "total", header: "Per unit", align: "end", render: (row) => amount(row.total) },
];

const diagnosticColumns: DataTableColumn<Diagnostic>[] = [
  { key: "code", header: "Code", mono: true },
  {
    key: "severity",
    header: "Severity",
    render: (d) => <Badge tone={d.severity === "error" ? "danger" : "warning"}>{d.severity === "error" ? "Error" : "Warning"}</Badge>,
  },
  { key: "message", header: "Message" },
  { key: "ref", header: "Item", mono: true, render: (d) => d.ref ?? dash },
];

interface SubtotalRow {
  node: string;
  value: string;
}

/** Basis and Total sheet node values in node id order (FR-39.2). */
export function subtotalRows(result: CostResult): SubtotalRow[] {
  const subtotals = result.subtotals ?? {};
  return Object.keys(subtotals)
    .sort()
    .map((node) => ({ node, value: subtotals[node] }));
}

const subtotalColumns: DataTableColumn<SubtotalRow>[] = [
  { key: "node", header: "Sheet node", mono: true },
  { key: "value", header: "Subtotal per unit", align: "end", render: (row) => amount(row.value) },
];

function StatusBadge({ status }: { status: CostResult["status"] }) {
  return status === "Error" ? <Badge tone="danger">Error</Badge> : <Badge tone="success">Pending price</Badge>;
}

/** Adds own and rolled amounts per component as strings, without floats (6-decimal unit costs). */
function combine(result: CostResult): Split {
  const out: Split = {};
  const scale = 1_000_000n;
  const toMicros = (v: string) => {
    const neg = v.startsWith("-");
    const [whole, fraction = ""] = v.replace("-", "").split(".");
    const micros = BigInt(whole || "0") * scale + BigInt(fraction.padEnd(6, "0").slice(0, 6) || "0");
    return neg ? -micros : micros;
  };
  const fromMicros = (m: bigint) => {
    const neg = m < 0n;
    const abs = neg ? -m : m;
    const fraction = (abs % scale).toString().padStart(6, "0").replace(/0+$/, "");
    return `${neg ? "-" : ""}${abs / scale}${fraction ? `.${fraction}` : ""}`;
  };
  for (const part of [result.own, result.rolled]) {
    for (const [c, v] of Object.entries(part)) {
      out[c] = fromMicros(toMicros(out[c] ?? "0") + toMicros(v));
    }
  }
  return out;
}

function ResultDetail({ result, split }: { result: CostResult; split: Split }) {
  const diagnostics = result.diagnostics ?? [];
  return (
    <Card title={`Item ${result.itemId}`} headingLevel={3}>
      <div className={styles.stack}>
        <StatGroup label={`Standard of ${result.itemId}`}>
          <StatTile label="Unit cost" value={result.status === "Error" ? dash : amount(result.unitCost)} />
          <StatTile label="BOM level" value={result.level} />
          <StatTile label="Diagnostics" value={diagnostics.length} tone={result.status === "Error" ? "danger" : "warning"} />
        </StatGroup>
        <p className={styles.note}>
          Status: <StatusBadge status={result.status} />
        </p>
        {result.status === "Error" ? null : (
          <DataTable
            label={`Cost component split of ${result.itemId}`}
            columns={splitColumns}
            rows={splitRows(result, split)}
            getRowKey={(row) => row.component}
            empty={{ title: "No cost components", description: "The item has no priced lines, operations or sheet nodes." }}
          />
        )}
        {result.status !== "Error" && subtotalRows(result).length > 0 ? (
          <DataTable
            label={`Costing sheet subtotals of ${result.itemId}`}
            columns={subtotalColumns}
            rows={subtotalRows(result)}
            getRowKey={(row) => row.node}
          />
        ) : null}
        {diagnostics.length > 0 ? (
          <DataTable
            label={`Diagnostics of ${result.itemId}`}
            columns={diagnosticColumns}
            rows={diagnostics}
            getRowKey={(d, i) => `${d.code}-${i}`}
          />
        ) : null}
      </div>
    </Card>
  );
}

const runColumns: DataTableColumn<CostResult>[] = [
  { key: "itemId", header: "Item", mono: true },
  { key: "level", header: "Level", align: "end" },
  { key: "status", header: "Status", render: (row) => <StatusBadge status={row.status} /> },
  { key: "unitCost", header: "Unit cost", align: "end", render: (row) => (row.status === "Error" ? dash : amount(row.unitCost)) },
  {
    key: "diagnostics",
    header: "Diagnostics",
    render: (row) => (row.diagnostics && row.diagnostics.length > 0 ? row.diagnostics.map((d) => d.code).join(", ") : dash),
  },
];

/** Roll one item or a whole selection from BOMs, routings and costing sheets (FR-39.4, FR-39.5). */
export function RollPage() {
  const [values, setValues] = useState<Values>({
    items: exampleText,
    itemId: "FG-100",
    mode: "Multilevel",
    fallbackToActive: false,
    view: "Legal",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<"one" | "mass" | null>(null);
  const [single, setSingle] = useState<{ result: CostResult; split: Split } | null>(null);
  const [run, setRun] = useState<RollRun | null>(null);
  const [opened, setOpened] = useState<CostResult | null>(null);

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
  }

  async function submit(kind: "one" | "mass", event?: FormEvent) {
    event?.preventDefault();
    const found: Errors = {};
    const { items, error } = parseItems(values.items);
    if (error) found.items = error;
    if (kind === "one" && values.itemId.trim() === "") found.itemId = "Enter the id of the item to roll.";
    if (kind === "one" && items && values.itemId.trim() !== "" && !items.some((it) => it.id === values.itemId.trim())) {
      found.itemId = "The item must be one of the items above.";
    }
    setErrors(found);
    setFailure(null);
    if (!items || Object.keys(found).length > 0) return;
    const options = { mode: values.mode, fallbackToActive: values.fallbackToActive, view: values.view };
    setBusy(kind);
    try {
      if (kind === "one") {
        const res = await rollOne({ items, itemId: values.itemId.trim(), options });
        setSingle(res);
        setRun(null);
      } else {
        const res = await massRoll({ items, options });
        setRun(res);
        setSingle(null);
        setOpened(null);
      }
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(null);
    }
  }

  const fieldErrors = failure ? Object.entries(failure.fields) : [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost roll-up"
        description="Roll standard costs leaves first. Purchased items take their price (or the active standard when fallback is on); manufactured items add their BOM lines with scrap, routing time at the cost category rate, and costing sheet surcharges and rates. Each child's split is carried into its parent."
      />
      <Card title="Selection" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit("one", e)} noValidate>
          <div className={styles.wide}>
            <FormField
              id="roll-items"
              label="Items"
              hint="A JSON array of items: id, kind, price or costingLot, bom lines (childId, qty, scrapPct, phantom, makeToOrder), operations (setupHours, runHours, rate, scrapPct), sheet nodes (Basis, Surcharge, Rate, Total) and, for intercompany purchases, intercompany and legalCost."
              error={errors.items}
            >
              {(control) => (
                <Textarea
                  {...control}
                  mono
                  rows={16}
                  spellCheck={false}
                  value={values.items}
                  onChange={(e) => update({ items: e.target.value })}
                  invalid={Boolean(errors.items)}
                />
              )}
            </FormField>
          </div>
          <FormField id="roll-item" label="Item to roll" hint="Used by Roll item; Mass roll rolls every item." error={errors.itemId}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.itemId}
                onChange={(e) => update({ itemId: e.target.value })}
                invalid={Boolean(errors.itemId)}
              />
            )}
          </FormField>
          <ExplosionModeField id="roll-mode" value={values.mode} onChange={(mode) => update({ mode })} />
          <FormField
            id="roll-view"
            label="Valuation view"
            hint="Group costs an intercompany purchase at its supplier's Legal-view cost instead of the intercompany price. Profit center costs a purchase from another profit center at its transfer price, or its cost plus the cost-plus percent."
          >
            {(control) => (
              <Select {...control} value={values.view} onChange={(e) => update({ view: e.target.value as ValuationView })}>
                <option value="Legal">Legal</option>
                <option value="Group">Group</option>
                <option value="ProfitCenter">Profit center</option>
              </Select>
            )}
          </FormField>
          <div className={styles.check}>
            <Input
              id="roll-fallback"
              type="checkbox"
              checked={values.fallbackToActive}
              onChange={(e) => update({ fallbackToActive: e.target.checked })}
            />
            <label htmlFor="roll-fallback">Fall back to the active standard</label>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The roll was refused" live="alert">
                <ProblemMessage problem={failure} />
                {fieldErrors.length > 0 ? (
                  <ul className={styles.fieldErrors}>
                    {fieldErrors.map(([field, message]) => (
                      <li key={field}>
                        <code>{field}</code> {message}
                      </li>
                    ))}
                  </ul>
                ) : null}
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "one"}>
              Roll item
            </Button>
            <Button loading={busy === "mass"} onClick={() => void submit("mass")}>
              Mass roll
            </Button>
            <Button variant="ghost" onClick={() => update({ items: exampleText, itemId: "FG-100" })}>
              Load worked example
            </Button>
          </div>
        </form>
      </Card>

      {busy ? (
        <DataTable label="Roll results" columns={runColumns} rows={[]} getRowKey={(row) => row.itemId} loading />
      ) : single ? (
        <ResultDetail result={single.result} split={single.split} />
      ) : run ? (
        <>
          <StatGroup label="Roll run">
            <StatTile label="Items" value={run.itemCount} />
            <StatTile label="Errors" value={run.errorCount} tone="danger" />
            <StatTile label="Duration (ms)" value={run.durationMs} />
          </StatGroup>
          <DataTable
            label="Roll results"
            columns={runColumns}
            rows={run.results}
            getRowKey={(row) => row.itemId}
            onRowOpen={(row: CostResult) => setOpened(row)}
            empty={{ title: "No items rolled", description: "The run had no items." }}
          />
          {opened ? <ResultDetail result={opened} split={combine(opened)} /> : null}
        </>
      ) : (
        <Card>
          <EmptyState
            icon="box"
            headingLevel={3}
            title="No roll yet"
            description="Roll one item to see its cost component split, or mass roll the selection to see every item's standard and diagnostics."
          />
        </Card>
      )}
    </div>
  );
}
