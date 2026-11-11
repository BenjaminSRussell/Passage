import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Textarea, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, simulateDirectives, type Compliance, type Direction, type Problem, type SimulateRequest, type SimulateResponse, type TraceStep } from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const DECIMAL = /^\d+(\.\d+)?$/;
const INTEGER = /^\d+$/;

const exampleWarehouse = "0190b2a4-7c1e-7d3a-9b5f-2a4c6e8f0a11";

/** What the form holds. Quantities stay strings so they reach the service as decimals, never floats. */
export interface SimulatorDraft {
  itemId: string;
  workType: string;
  direction: Direction;
  warehouseId: string;
  qty: string;
  weight: string;
  volume: string;
  lps: string;
  attributes: string;
  directives: string;
  locations: string;
  /** Optional hazmat reference data as JSON; blank sends none and disables the hazmat checks. */
  compliance: string;
}

type Errors = Partial<Record<keyof SimulatorDraft, string>>;

/** The BR-31.1 worked example: directive 10 for fasteners in three quantity bands, directive 20 as catch-all. */
const exampleDirectives = [
  {
    id: "DIR-10",
    sequence: 10,
    workType: "PURCHASE_PUTAWAY",
    direction: "PUT",
    warehouseId: exampleWarehouse,
    query: { item_group: "FASTENERS" },
    lines: [
      { seq: 1, fromQty: "0", toQty: "100", actions: [{ seq: 1, strategy: "consolidate", zone: "FWD" }] },
      {
        seq: 2,
        fromQty: "100",
        toQty: "1200",
        actions: [
          { seq: 1, strategy: "consolidate", zone: "CASE" },
          { seq: 2, strategy: "empty_location", zone: "CASE" },
        ],
      },
      {
        seq: 3,
        fromQty: "1200",
        actions: [
          { seq: 1, strategy: "empty_location", zone: "RESERVE" },
          { seq: 2, strategy: "empty_location", zone: "OVERFLOW" },
        ],
      },
    ],
  },
  {
    id: "DIR-20",
    sequence: 20,
    workType: "PURCHASE_PUTAWAY",
    direction: "PUT",
    warehouseId: exampleWarehouse,
    lines: [{ seq: 1, fromQty: "0", actions: [{ seq: 1, strategy: "empty_location" }] }],
  },
];

const exampleLocations = [
  {
    id: "loc-r0",
    name: "R-05-02-00",
    zone: "RESERVE",
    pickSequence: 5020,
    status: "ACTIVE",
    capacity: { maxLps: 1 },
    used: { weight: "300", lps: 1 },
    stock: { "NUT-M8": "2400" },
  },
  { id: "loc-r1", name: "R-05-02-01", zone: "RESERVE", pickSequence: 5021, status: "ACTIVE", capacity: { maxWeight: "20", maxLps: 1 } },
  { id: "loc-o1", name: "O-01-01-01", zone: "OVERFLOW", pickSequence: 9001, status: "ACTIVE" },
  { id: "loc-f1", name: "F-01-01-01", zone: "FWD", pickSequence: 101, status: "ACTIVE", capacity: { allowMixedItems: false }, stock: { "BOLT-M8": "40" } },
];

/** BOLT-M8, 1,200 EA at 0.012 kg each on one pallet: resolves to R-05-02-01 (BR-31.1). */
export const exampleDraft: SimulatorDraft = {
  itemId: "BOLT-M8",
  workType: "PURCHASE_PUTAWAY",
  direction: "PUT",
  warehouseId: exampleWarehouse,
  qty: "1200",
  weight: "14.4",
  volume: "",
  lps: "1",
  attributes: "item_group=FASTENERS",
  directives: JSON.stringify(exampleDirectives, null, 2),
  locations: JSON.stringify(exampleLocations, null, 2),
  compliance: "",
};

/**
 * AC-31.14: section FCS-02 holds class 5.1 oxidizers, so a class 3 solvent put-away
 * skips FCS-02 and lands in FCS-01; the trace states the segregation reason.
 */
const hazmatLocations = [
  {
    id: "loc-h1",
    name: "H-01-01-01",
    zone: "HAZ",
    pickSequence: 1,
    status: "ACTIVE",
    fireSection: "FCS-02",
    rackGroup: "RK-1",
    used: { weight: "50", lps: 1 },
    stock: { "OX-51": "10" },
  },
  { id: "loc-h2", name: "H-01-01-02", zone: "HAZ", pickSequence: 2, status: "ACTIVE", fireSection: "FCS-02", rackGroup: "RK-2" },
  { id: "loc-h3", name: "H-03-01-01", zone: "HAZ", pickSequence: 30, status: "ACTIVE", fireSection: "FCS-01", rackGroup: "RK-3", allowedStorageClasses: ["3"] },
];

const hazmatCompliance: Compliance = {
  hazmatProfiles: [
    { itemId: "SOLV-3", storageClass: "3", unNumber: "UN1263", shippingName: "PAINT" },
    { itemId: "OX-51", storageClass: "5.1", unNumber: "UN1479" },
  ],
  segregation: [{ a: "3", b: "5.1", rel: "prohibited" }],
  sectionLimitsKg: { "FCS-01": "1000", "FCS-02": "1000" },
};

export const hazmatDraft: SimulatorDraft = {
  itemId: "SOLV-3",
  workType: "PURCHASE_PUTAWAY",
  direction: "PUT",
  warehouseId: exampleWarehouse,
  qty: "10",
  weight: "20",
  volume: "",
  lps: "1",
  attributes: "",
  directives: JSON.stringify(
    [
      {
        id: "DIR-HAZ",
        sequence: 10,
        workType: "PURCHASE_PUTAWAY",
        direction: "PUT",
        warehouseId: exampleWarehouse,
        lines: [{ seq: 1, fromQty: "0", actions: [{ seq: 1, strategy: "empty_location", zone: "HAZ" }] }],
      },
    ],
    null,
    2,
  ),
  locations: JSON.stringify(hazmatLocations, null, 2),
  compliance: JSON.stringify(hazmatCompliance, null, 2),
};

function parseAttributes(text: string): Record<string, string> | null {
  const out: Record<string, string> = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;
    const eq = line.indexOf("=");
    if (eq <= 0) return null;
    out[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  }
  return out;
}

function parseObject(text: string): Compliance | null {
  try {
    const value: unknown = JSON.parse(text);
    return value !== null && typeof value === "object" && !Array.isArray(value) ? (value as Compliance) : null;
  } catch {
    return null;
  }
}

function parseArray(text: string): unknown[] | null {
  try {
    const value: unknown = JSON.parse(text);
    return Array.isArray(value) ? value : null;
  } catch {
    return null;
  }
}

/** Client checks that mirror the service validation, so most mistakes never leave the page. */
export function validateDraft(d: SimulatorDraft): Errors {
  const errors: Errors = {};
  if (d.itemId.trim() === "") errors.itemId = "Enter the item.";
  if (d.workType.trim() === "") errors.workType = "Enter the work type, such as PURCHASE_PUTAWAY.";
  if (!UUID.test(d.warehouseId.trim())) errors.warehouseId = "Choose a warehouse.";
  if (!DECIMAL.test(d.qty) || Number(d.qty) <= 0) errors.qty = "Enter a quantity above 0.";
  if (d.weight !== "" && !DECIMAL.test(d.weight)) errors.weight = "Enter a weight of 0 or more.";
  if (d.volume !== "" && !DECIMAL.test(d.volume)) errors.volume = "Enter a volume of 0 or more.";
  if (d.lps !== "" && !INTEGER.test(d.lps)) errors.lps = "Enter a whole number of license plates.";
  if (parseAttributes(d.attributes) === null) errors.attributes = "Write one key=value pair per line.";
  if (parseArray(d.directives) === null) errors.directives = "Enter the directives as a JSON array.";
  if (parseArray(d.locations) === null) errors.locations = "Enter the candidate locations as a JSON array.";
  if (d.compliance.trim() !== "" && parseObject(d.compliance) === null) errors.compliance = "Enter the hazmat data as a JSON object, or leave it blank.";
  return errors;
}

export function toRequest(d: SimulatorDraft): SimulateRequest {
  const attributes = parseAttributes(d.attributes) ?? {};
  return {
    line: {
      itemId: d.itemId.trim(),
      workType: d.workType.trim(),
      direction: d.direction,
      warehouseId: d.warehouseId.trim().toLowerCase(),
      qty: d.qty,
      ...(d.weight !== "" ? { weight: d.weight } : {}),
      ...(d.volume !== "" ? { volume: d.volume } : {}),
      ...(d.lps !== "" ? { lps: Number(d.lps) } : {}),
      ...(Object.keys(attributes).length > 0 ? { attributes } : {}),
    },
    directives: parseArray(d.directives) ?? [],
    locations: parseArray(d.locations) ?? [],
    ...(d.compliance.trim() !== "" ? { compliance: parseObject(d.compliance) ?? {} } : {}),
  };
}

/** Maps the service's field paths (line.qty, directives[0].lines[1].fromQty, …) onto the form's fields. */
export function fieldErrors(fields: Record<string, string>): Errors {
  const errors: Errors = {};
  const lineFields: Record<string, keyof SimulatorDraft> = {
    "line.itemId": "itemId",
    "line.workType": "workType",
    "line.direction": "direction",
    "line.warehouseId": "warehouseId",
    "line.qty": "qty",
    "line.weight": "weight",
    "line.volume": "volume",
    "line.lps": "lps",
  };
  for (const [path, message] of Object.entries(fields).sort(([a], [b]) => a.localeCompare(b))) {
    const json = ["directives", "locations", "compliance"].find((f) => path === f || path.startsWith(`${f}[`) || path.startsWith(`${f}.`)) as
      | "directives"
      | "locations"
      | "compliance"
      | undefined;
    const target = lineFields[path] ?? json;
    if (!target) continue;
    const text = target === "compliance" && path === "compliance" ? message : json ? `${path} ${message}` : `This ${message}.`;
    errors[target] = errors[target] ? `${errors[target]}; ${text}` : text;
  }
  return errors;
}

const outcomeTone: Record<TraceStep["outcome"], BadgeTone> = { hit: "success", miss: "warning", skip: "neutral" };
const outcomeLabel: Record<TraceStep["outcome"], string> = { hit: "Hit", miss: "Miss", skip: "Out of scope" };

interface TraceRow extends TraceStep {
  step: number;
}

const traceColumns: DataTableColumn<TraceRow>[] = [
  { key: "step", header: "Step", align: "end" },
  { key: "directive", header: "Directive", mono: true },
  { key: "band", header: "Line", align: "end", render: (r) => r.band ?? "—" },
  { key: "action", header: "Action", align: "end", render: (r) => r.action ?? "—" },
  { key: "outcome", header: "Outcome", render: (r) => <Badge tone={outcomeTone[r.outcome]}>{outcomeLabel[r.outcome]}</Badge> },
  { key: "reason", header: "Reason" },
];

function summary(result: SimulateResponse): string {
  if (result.blocked) {
    return `Blocked (${result.code ?? "WMS-0101"}): no directive action found a location for this line.`;
  }
  const winner = result.trace[result.trace.length - 1];
  const where = winner?.band ? `, line ${winner.band}, action ${winner.action ?? 1}` : "";
  return `Location ${result.location ?? ""} chosen by ${result.directive ?? ""}${where}.`;
}

/**
 * Location directive simulator (FR-31.3, BR-31.1): evaluates a line against
 * directives and candidate locations, optionally with hazmat storage
 * compatibility (FR-31.19, BR-31.15), and explains every step. Nothing is saved.
 */
export function DirectiveSimulatorPage({ initial = exampleDraft }: { initial?: SimulatorDraft }) {
  const [draft, setDraft] = useState<SimulatorDraft>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<SimulateResponse | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const set = (patch: Partial<SimulatorDraft>) => setDraft((prev) => ({ ...prev, ...patch }));

  async function run(event: FormEvent) {
    event.preventDefault();
    const found = validateDraft(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await simulateDirectives(toRequest(draft)));
    } catch (error) {
      const problem = describeProblem(error);
      setResult(null);
      setErrors(fieldErrors(problem.fields));
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  function loadExample(example: SimulatorDraft) {
    setDraft(example);
    setErrors({});
    setFailure(null);
    setResult(null);
  }

  const text = (id: string, name: keyof SimulatorDraft, label: string, extra: { optional?: boolean; hint?: string; decimal?: boolean } = {}) => (
    <FormField id={id} label={label} optional={extra.optional} hint={extra.hint} error={errors[name]}>
      {(c) => (
        <Input
          {...c}
          inputMode={extra.decimal ? "decimal" : undefined}
          value={draft[name]}
          onChange={(e) => set({ [name]: e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  const json = (id: string, name: "directives" | "locations" | "compliance", label: string, hint: string, optional = false) => (
    <div className={styles.wide}>
      <FormField id={id} label={label} hint={hint} optional={optional} error={errors[name]}>
        {(c) => <Textarea {...c} mono rows={12} spellCheck={false} value={draft[name]} onChange={(e) => set({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
      </FormField>
    </div>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Location directive simulator"
        description="Enter a work line, the directives, the candidate locations and any hazmat data, then read which location the rules choose and why. Nothing is saved."
        actions={
          <>
            <Button variant="secondary" onClick={() => loadExample(exampleDraft)}>
              Load worked example
            </Button>
            <Button variant="secondary" onClick={() => loadExample(hazmatDraft)}>
              Load hazmat example
            </Button>
          </>
        }
      />
      <Card title="Work line">
        <form className={styles.form} onSubmit={run} noValidate>
          {text("sim-item", "itemId", "Item")}
          {text("sim-work-type", "workType", "Work type", { hint: "For example PURCHASE_PUTAWAY or SALES_PICK." })}
          <FormField id="sim-direction" label="Direction" error={errors.direction}>
            {(c) => (
              <Select {...c} value={draft.direction} onChange={(e) => set({ direction: e.target.value as Direction })}>
                <option value="PUT">Put-away</option>
                <option value="PICK">Pick</option>
              </Select>
            )}
          </FormField>
          <LookupFormField id="sim-warehouse" label="Warehouse" entity="warehouse" value={draft.warehouseId} error={errors.warehouseId} onChange={(warehouseId) => set({ warehouseId })} />
          {text("sim-qty", "qty", "Quantity", { decimal: true, hint: "In the item's base unit." })}
          {text("sim-weight", "weight", "Weight (kg)", { optional: true, decimal: true })}
          {text("sim-volume", "volume", "Volume (m³)", { optional: true, decimal: true })}
          {text("sim-lps", "lps", "License plates", { optional: true })}
          <div className={styles.wide}>
            <FormField id="sim-attributes" label="Line attributes" optional hint="One key=value per line; directive queries match on these." error={errors.attributes}>
              {(c) => <Textarea {...c} mono rows={3} spellCheck={false} value={draft.attributes} onChange={(e) => set({ attributes: e.target.value })} invalid={Boolean(errors.attributes)} />}
            </FormField>
          </div>
          {json("sim-directives", "directives", "Directives (JSON)", "An array of directives with their lines (quantity bands) and actions, as in the service API.")}
          {json(
            "sim-locations",
            "locations",
            "Candidate locations (JSON)",
            "An array of locations with zone, status, capacity, current use and stock; hazmat storage adds fireSection, rackGroup and allowedStorageClasses.",
          )}
          {json(
            "sim-compliance",
            "compliance",
            "Hazmat compliance (JSON)",
            "An object with hazmatProfiles, segregation (allowed, separate or prohibited per storage-class pair) and sectionLimitsKg. Leave blank to skip the hazmat checks.",
            true,
          )}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Simulate
            </Button>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The simulation failed" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
        </form>
      </Card>
      {result ? (
        <Card title="Result">
          <div className={styles.stack}>
            <p role="status" className={styles.summary}>
              <Badge tone={result.blocked ? "danger" : "success"}>{result.blocked ? "Blocked" : "Location found"}</Badge> {summary(result)}
            </p>
            <DataTable
              label="Rule trace"
              columns={traceColumns}
              rows={result.trace.map((s, i) => ({ ...s, step: i + 1 }))}
              getRowKey={(r) => String(r.step)}
              empty={{ title: "No directive was evaluated", description: "Add a directive for this work type, direction and warehouse." }}
            />
          </div>
        </Card>
      ) : (
        <Card>
          <EmptyState icon="box" title="No simulation run yet" description="Run a simulation to see the chosen location and the rule-by-rule trace." headingLevel={3} />
        </Card>
      )}
    </div>
  );
}
