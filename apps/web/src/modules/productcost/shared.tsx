import { Alert, Badge, DataTable, FormField, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import type { CostItem, Diagnostic, ExplosionMode, JournalLine, Problem, Split } from "./api/client";
import { amount, dash } from "./format";
import styles from "./pages.module.css";

/** Explosion modes as the selects offer them (FR-39.4 step 3). */
export const explosionModes: { value: ExplosionMode; label: string }[] = [
  { value: "Multilevel", label: "Multilevel" },
  { value: "SingleLevel", label: "Single level" },
  { value: "MakeToOrder", label: "Make-to-order" },
  { value: "ByLineType", label: "By BOM line type" },
];

export const explosionModeHint =
  "Single level takes every child at its active standard. Make-to-order and by line type price purchased children in this roll; make-to-order also explodes make-to-order lines, by line type takes every sub-assembly at its active standard. Phantoms are always costed in place.";

/** The explosion mode select, shared by the pages that roll items. */
export function ExplosionModeField({ id, value, onChange }: { id: string; value: ExplosionMode; onChange(mode: ExplosionMode): void }) {
  return (
    <FormField id={id} label="Explosion mode" hint={explosionModeHint}>
      {(control) => (
        <Select {...control} value={value} onChange={(e) => onChange(e.target.value as ExplosionMode)}>
          {explosionModes.map((m) => (
            <option key={m.value} value={m.value}>
              {m.label}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

/** A JSON textarea for an item structure (there are no stored items, BOMs or versions yet). */
export function ItemsField({
  id,
  label = "Items",
  value,
  error,
  rows = 12,
  onChange,
}: {
  id: string;
  label?: string;
  value: string;
  error?: string;
  rows?: number;
  onChange(value: string): void;
}) {
  return (
    <div className={styles.wide}>
      <FormField
        id={id}
        label={label}
        hint="A JSON array of items: id, kind, price or costingLot, bom lines (childId, qty, scrapPct, phantom, makeToOrder), operations (setupHours, runHours, rate, scrapPct) and sheet nodes."
        error={error}
      >
        {(control) => (
          <Textarea
            {...control}
            mono
            rows={rows}
            spellCheck={false}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            invalid={Boolean(error)}
          />
        )}
      </FormField>
    </div>
  );
}

/** Pretty JSON for an item list the user can edit. */
export const itemsText = (items: CostItem[]) => JSON.stringify(items, null, 2);

/** A refused request: the service's message, its code and any field errors. */
export function ProblemAlert({ title, problem }: { title: string; problem: Problem }) {
  const fields = Object.entries(problem.fields);
  return (
    <div className={styles.formActions}>
      <Alert tone="danger" title={title} live="alert">
        {problem.message} ({problem.code})
        {fields.length > 0 ? (
          <ul className={styles.fieldErrors}>
            {fields.map(([field, message]) => (
              <li key={field}>
                <code>{field}</code> {message}
              </li>
            ))}
          </ul>
        ) : null}
      </Alert>
    </div>
  );
}

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

export function DiagnosticsTable({ label, diagnostics }: { label: string; diagnostics: Diagnostic[] }) {
  return <DataTable label={label} columns={diagnosticColumns} rows={diagnostics} getRowKey={(d, i) => `${d.code}-${i}`} />;
}

interface ComponentRow {
  component: string;
  amount: string;
}

/** One row per cost component of a split, sorted by code. */
export function componentRows(split: Split): ComponentRow[] {
  return Object.keys(split)
    .sort()
    .map((component) => ({ component, amount: split[component] }));
}

export function SplitTable({ label, split, header = "Amount" }: { label: string; split: Split; header?: string }) {
  const columns: DataTableColumn<ComponentRow>[] = [
    { key: "component", header: "Component", mono: true },
    { key: "amount", header, align: "end", render: (r) => amount(r.amount) },
  ];
  return (
    <DataTable
      label={label}
      columns={columns}
      rows={componentRows(split)}
      getRowKey={(r) => r.component}
      empty={{ title: "No cost components", description: "Nothing was costed." }}
    />
  );
}

const zero = /^-?0+(\.0+)?$/;

export const journalColumns: DataTableColumn<JournalLine>[] = [
  { key: "account", header: "Account" },
  { key: "debit", header: "Debit", align: "end", render: (j) => (zero.test(j.debit) ? dash : amount(j.debit)) },
  { key: "credit", header: "Credit", align: "end", render: (j) => (zero.test(j.credit) ? dash : amount(j.credit)) },
];

/** Status badge of a roll result (Pending price, Error, Excluded alternative). */
export function RollStatus({ status }: { status: "Pending" | "Error" | "Excluded" }) {
  if (status === "Error") return <Badge tone="danger">Error</Badge>;
  if (status === "Excluded") return <Badge tone="neutral">Excluded</Badge>;
  return <Badge tone="success">Pending price</Badge>;
}

const DEC = /^(-?)(\d+)(?:\.(\d+))?$/;

/**
 * Sums decimal strings exactly (BigInt at `scale` decimals), returning
 * undefined when one is not a decimal. Used for ratio totals, never for money
 * that the service computes.
 */
export function sumDecimals(values: string[], scale = 6): string | undefined {
  let total = 0n;
  const unit = 10n ** BigInt(scale);
  for (const v of values) {
    const m = DEC.exec(v.trim());
    if (!m) return undefined;
    const fraction = (m[3] ?? "").padEnd(scale, "0").slice(0, scale);
    const n = BigInt(m[2]) * unit + BigInt(fraction || "0");
    total += m[1] === "-" ? -n : n;
  }
  const neg = total < 0n;
  const abs = neg ? -total : total;
  const fraction = (abs % unit).toString().padStart(scale, "0").replace(/0+$/, "");
  return `${neg ? "-" : ""}${abs / unit}${fraction ? `.${fraction}` : ""}`;
}
