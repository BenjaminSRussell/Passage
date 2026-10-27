import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  createDirective,
  createLocations,
  describeProblem,
  listDirectives,
  listLocations,
  LOCATION_STATUSES,
  newKey,
  WORK_TYPES,
  type Capacity,
  type Direction,
  type Directive,
  type DirectiveLine,
  type Location,
  type LocationInput,
  type LocationStatus,
  type Problem,
} from "./api/client";
import styles from "./pages.module.css";
import {
  capacityText,
  ChooseWarehouse,
  DECIMAL,
  INTEGER,
  LOCATION_STATUS_TONES,
  sentence,
  shortId,
  stockText,
  usedText,
  useWarehouse,
  UUID,
  WarehouseField,
} from "./shared";
import { useLoad } from "./useLoad";

const PAGE = 100;

const locationColumns: DataTableColumn<Location>[] = [
  { key: "name", header: "Name", mono: true },
  { key: "zone", header: "Zone" },
  { key: "status", header: "Status", render: (l) => <Badge tone={LOCATION_STATUS_TONES[l.status] ?? "neutral"}>{sentence(l.status)}</Badge> },
  { key: "pickSequence", header: "Pick sequence", align: "end" },
  { key: "capacity", header: "Capacity", render: (l) => capacityText(l.capacity) },
  { key: "used", header: "Used", render: usedText },
  { key: "stock", header: "Stock", render: stockText },
];

/** Locations of a warehouse with capacity, current use and stock (FR-31.1). */
export function LocationsPage() {
  const [warehouse, setWarehouse] = useWarehouse();
  const [zone, setZone] = useState("");
  const valid = UUID.test(warehouse.trim());
  const filter = { warehouseId: warehouse.trim(), zone: zone.trim(), limit: PAGE };
  const state = useLoad((signal) => (valid ? listLocations(filter, signal) : Promise.resolve(null)), `${filter.warehouseId}|${filter.zone}`);
  const [more, setMore] = useState<{ items: Location[]; next?: string }>({ items: [] });
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  useEffect(() => setMore({ items: [] }), [state.data]);
  const next = more.next ?? state.data?.nextCursor;
  const rows = [...(state.data?.items ?? []), ...more.items];

  async function loadMore() {
    if (!next) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listLocations({ ...filter, cursor: next });
      setMore((prev) => ({ items: [...prev.items, ...page.items], next: page.nextCursor ?? "" }));
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Locations"
        description="Bins of a warehouse with their capacity profile, current use and stock. Stock arrives through confirmed put-away work."
        actions={
          <Link className={buttonClassName("primary")} to={`/warehouse/locations/new?warehouse=${encodeURIComponent(warehouse.trim())}`}>
            Add locations
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <WarehouseField value={warehouse} onChange={setWarehouse} />
        <FormField id="loc-zone" label="Zone" optional>
          {(c) => <Input {...c} value={zone} onChange={(e) => setZone(e.target.value)} />}
        </FormField>
      </div>
      {!valid ? (
        <ChooseWarehouse />
      ) : state.error ? (
        <Alert tone="danger" title="Could not load the locations" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          <DataTable
            label="Locations"
            columns={locationColumns}
            rows={rows}
            getRowKey={(l) => l.id}
            loading={state.loading}
            empty={{ title: "No locations yet", description: "Add the zones' bins with Add locations, generated from a name format." }}
          />
          {moreError ? (
            <Alert tone="danger" title="Could not load more locations" live="alert">
              {moreError.message} ({moreError.code})
            </Alert>
          ) : null}
          {next && !state.loading ? (
            <div className={styles.actions}>
              <Button onClick={loadMore} loading={loadingMore}>
                Load more locations
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}

interface Range {
  from: string;
  to: string;
}

export interface LocationDraft {
  warehouseId: string;
  zone: string;
  prefix: string;
  aisles: Range;
  racks: Range;
  shelves: Range;
  firstPickSequence: string;
  status: LocationStatus;
  maxWeight: string;
  maxVolume: string;
  maxLps: string;
  maxItems: string;
  allowMixedItems: boolean;
}

type DraftErrors = Partial<Record<"warehouseId" | "zone" | "prefix" | "aisles" | "racks" | "shelves" | "firstPickSequence" | "maxWeight" | "maxVolume" | "maxLps" | "maxItems" | "count", string>>;

const emptyDraft = (warehouseId: string): LocationDraft => ({
  warehouseId,
  zone: "",
  prefix: "",
  aisles: { from: "1", to: "1" },
  racks: { from: "", to: "" },
  shelves: { from: "", to: "" },
  firstPickSequence: "1",
  status: "ACTIVE",
  maxWeight: "",
  maxVolume: "",
  maxLps: "",
  maxItems: "",
  allowMixedItems: false,
});

function rangeOf(r: Range): number[] | null | "skip" {
  if (r.from.trim() === "" && r.to.trim() === "") return "skip";
  if (!INTEGER.test(r.from) || !INTEGER.test(r.to)) return null;
  const from = Number(r.from);
  const to = Number(r.to);
  if (from > to || to > 99) return null;
  return Array.from({ length: to - from + 1 }, (_, i) => from + i);
}

/**
 * Location names from the LocationFormat (FR-31.1): prefix and two-digit
 * components, A-01-02-03 for aisle 1, rack 2, shelf 3. Racks and shelves are
 * optional; an empty range is left out of the name.
 */
export function generateNames(prefix: string, ranges: Range[]): string[] | null {
  let names = [prefix];
  for (const r of ranges) {
    const values = rangeOf(r);
    if (values === "skip") continue;
    if (values === null) return null;
    names = names.flatMap((n) => values.map((v) => `${n}-${String(v).padStart(2, "0")}`));
  }
  return names;
}

export function validateLocationDraft(d: LocationDraft): DraftErrors {
  const errors: DraftErrors = {};
  if (!UUID.test(d.warehouseId.trim())) errors.warehouseId = "Enter the warehouse id, a UUID.";
  if (d.zone.trim() === "") errors.zone = "Enter the zone.";
  if (!/^[A-Za-z0-9]{1,10}$/.test(d.prefix.trim())) errors.prefix = "Enter a prefix of letters and digits, without '-'.";
  const aisles = rangeOf(d.aisles);
  if (aisles === null || aisles === "skip") errors.aisles = "Enter aisles from and to, 0 to 99.";
  if (rangeOf(d.racks) === null) errors.racks = "Enter racks from and to, 0 to 99, or leave both blank.";
  if (rangeOf(d.shelves) === null) errors.shelves = "Enter shelves from and to, 0 to 99, or leave both blank.";
  if (!INTEGER.test(d.firstPickSequence)) errors.firstPickSequence = "Enter a whole number of 0 or more.";
  if (d.maxWeight !== "" && !DECIMAL.test(d.maxWeight)) errors.maxWeight = "Enter a weight such as 1000 or 12.5.";
  if (d.maxVolume !== "" && !DECIMAL.test(d.maxVolume)) errors.maxVolume = "Enter a volume such as 1.2.";
  if (d.maxLps !== "" && !INTEGER.test(d.maxLps)) errors.maxLps = "Enter a whole number.";
  if (d.maxItems !== "" && !INTEGER.test(d.maxItems)) errors.maxItems = "Enter a whole number.";
  const names = generateNames(d.prefix.trim(), [d.aisles, d.racks, d.shelves]);
  if (names && names.length > 1000) errors.count = `That makes ${names.length} locations; create at most 1,000 at a time.`;
  return errors;
}

export function toLocationInputs(d: LocationDraft): LocationInput[] {
  const capacity: Capacity = {
    ...(d.maxWeight !== "" ? { maxWeight: d.maxWeight } : {}),
    ...(d.maxVolume !== "" ? { maxVolume: d.maxVolume } : {}),
    ...(d.maxLps !== "" ? { maxLps: Number(d.maxLps) } : {}),
    ...(d.maxItems !== "" ? { maxItems: Number(d.maxItems) } : {}),
    allowMixedItems: d.allowMixedItems,
  };
  const first = Number(d.firstPickSequence);
  return (generateNames(d.prefix.trim(), [d.aisles, d.racks, d.shelves]) ?? []).map((name, i) => ({
    name,
    zone: d.zone.trim(),
    pickSequence: first + i,
    status: d.status,
    capacity,
  }));
}

/** Generates and creates the bins of a zone from a name format (FR-31.1, POST /locations:bulk). */
export function NewLocationsPage() {
  const [warehouse] = useWarehouse();
  const [draft, setDraft] = useState<LocationDraft>(() => emptyDraft(warehouse));
  const [errors, setErrors] = useState<DraftErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [created, setCreated] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const set = (patch: Partial<LocationDraft>) => {
    setDraft((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
    setCreated(null);
  };
  const names = generateNames(draft.prefix.trim(), [draft.aisles, draft.racks, draft.shelves]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateLocationDraft(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const page = await createLocations(draft.warehouseId.trim().toLowerCase(), toLocationInputs(draft), key.current);
      setCreated(page.items.length);
      key.current = newKey();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const text = (id: string, name: keyof DraftErrors & keyof LocationDraft, label: string, extra: { optional?: boolean; hint?: string } = {}) => (
    <FormField id={id} label={label} optional={extra.optional} hint={extra.hint} error={errors[name]}>
      {(c) => <Input {...c} value={draft[name] as string} onChange={(e) => set({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );
  const range = (id: string, name: "aisles" | "racks" | "shelves", label: string, optional: boolean) => (
    <>
      <FormField id={`${id}-from`} label={`${label} from`} optional={optional} error={errors[name]}>
        {(c) => <Input {...c} inputMode="numeric" value={draft[name].from} onChange={(e) => set({ [name]: { ...draft[name], from: e.target.value } })} invalid={Boolean(errors[name])} />}
      </FormField>
      <FormField id={`${id}-to`} label={`${label} to`} optional={optional}>
        {(c) => <Input {...c} inputMode="numeric" value={draft[name].to} onChange={(e) => set({ [name]: { ...draft[name], to: e.target.value } })} invalid={Boolean(errors[name])} />}
      </FormField>
    </>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Add locations" description="Generate a zone's bins from a name format, such as A-01-02-03 for aisle 1, rack 2, shelf 3, with one capacity profile." />
      <Card title="Locations to create">
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>
            <WarehouseField id="new-loc-warehouse" value={draft.warehouseId} onChange={(v) => set({ warehouseId: v })} error={errors.warehouseId} />
          </div>
          {text("new-loc-zone", "zone", "Zone", { hint: "For example RESERVE, FWD or HAZ." })}
          {text("new-loc-prefix", "prefix", "Name prefix", { hint: "The first part of every name, such as A." })}
          {range("new-loc-aisles", "aisles", "Aisles", false)}
          {range("new-loc-racks", "racks", "Racks", true)}
          {range("new-loc-shelves", "shelves", "Shelves", true)}
          {text("new-loc-pick", "firstPickSequence", "First pick sequence", { hint: "Each generated location takes the next number." })}
          <FormField id="new-loc-status" label="Status">
            {(c) => (
              <Select {...c} value={draft.status} onChange={(e) => set({ status: e.target.value as LocationStatus })}>
                {LOCATION_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {sentence(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("new-loc-weight", "maxWeight", "Maximum weight (kg)", { optional: true })}
          {text("new-loc-volume", "maxVolume", "Maximum volume (m³)", { optional: true })}
          {text("new-loc-lps", "maxLps", "Maximum license plates", { optional: true })}
          {text("new-loc-items", "maxItems", "Maximum items", { optional: true })}
          <span className={styles.check}>
            <Input type="checkbox" id="new-loc-mixed" checked={draft.allowMixedItems} onChange={(e) => set({ allowMixedItems: e.target.checked })} />
            <label htmlFor="new-loc-mixed">Allow mixed items</label>
          </span>
          <p className={`${styles.note} ${styles.wide}`} role="status">
            {names && names.length > 0 ? `Creates ${names.length} location${names.length === 1 ? "" : "s"}: ${names[0]}${names.length > 1 ? ` to ${names[names.length - 1]}` : ""}.` : "Enter a prefix and the aisles to preview the names."}
          </p>
          {errors.count ? (
            <div className={styles.formActions}>
              <Alert tone="warning">{errors.count}</Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create locations
            </Button>
            <Link className={buttonClassName("secondary")} to={`/warehouse/locations?warehouse=${encodeURIComponent(draft.warehouseId.trim())}`}>
              Back to locations
            </Link>
          </div>
          <div className={styles.formActions}>
            <Alert tone="danger" title={failure ? "The locations were not created" : undefined} live="alert">
              {failure ? `${failure.message} (${failure.code})${Object.keys(failure.fields).length > 0 ? `: ${Object.entries(failure.fields).map(([f, m]) => `${f} ${m}`).join("; ")}` : ""}` : null}
            </Alert>
            <Alert tone="success" live="status">
              {created !== null ? `${created} location${created === 1 ? "" : "s"} created.` : null}
            </Alert>
          </div>
        </form>
      </Card>
    </div>
  );
}

function bandText(line: DirectiveLine): string {
  const actions = [...line.actions]
    .sort((a, b) => a.seq - b.seq)
    .map((a) => `${a.strategy}${a.zone ? ` in ${a.zone}` : ""}`)
    .join(", then ");
  return `${line.fromQty}–${line.toQty ?? "∞"}: ${actions}`;
}

const directiveColumns: DataTableColumn<Directive>[] = [
  { key: "workType", header: "Work type", render: (d) => sentence(d.workType) },
  { key: "direction", header: "Direction", render: (d) => (d.direction === "PUT" ? "Put-away" : "Pick") },
  { key: "sequence", header: "Sequence", align: "end" },
  {
    key: "query",
    header: "Applies to",
    render: (d) =>
      d.query && Object.keys(d.query).length > 0
        ? Object.entries(d.query)
            .map(([k, v]) => `${k} = ${v}`)
            .join(", ")
        : "Every line",
  },
  { key: "lines", header: "Quantity bands and actions", render: (d) => [...d.lines].sort((a, b) => a.seq - b.seq).map(bandText).join("; ") },
  { key: "id", header: "Id", mono: true, render: (d) => <span title={d.id}>{shortId(d.id)}</span> },
];

/** Location directives of a warehouse in evaluation order (FR-31.3). */
export function DirectivesPage() {
  const [warehouse, setWarehouse] = useWarehouse();
  const [workType, setWorkType] = useState("");
  const valid = UUID.test(warehouse.trim());
  const state = useLoad(
    (signal) => (valid ? listDirectives({ warehouseId: warehouse.trim(), workType }, signal) : Promise.resolve(null)),
    `${warehouse.trim()}|${workType}`,
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Location directives"
        description="The rules that choose where put-away goes and where picks come from, by work type, in sequence order."
        actions={
          <Link className={buttonClassName("primary")} to={`/warehouse/directives/new?warehouse=${encodeURIComponent(warehouse.trim())}`}>
            New directive
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <WarehouseField value={warehouse} onChange={setWarehouse} />
        <FormField id="dir-work-type" label="Work type">
          {(c) => (
            <Select {...c} value={workType} onChange={(e) => setWorkType(e.target.value)}>
              <option value="">All work types</option>
              {WORK_TYPES.map((t) => (
                <option key={t} value={t}>
                  {sentence(t)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {!valid ? (
        <ChooseWarehouse />
      ) : state.error ? (
        <Alert tone="danger" title="Could not load the directives" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Location directives"
          columns={directiveColumns}
          rows={state.data?.items ?? []}
          getRowKey={(d) => d.id}
          loading={state.loading}
          empty={{ title: "No directives yet", description: "Without a directive, new work for this work type is blocked (WMS-0101)." }}
        />
      )}
    </div>
  );
}

export interface DirectiveDraft {
  warehouseId: string;
  workType: string;
  direction: Direction;
  sequence: string;
  query: string;
  lines: string;
}

type DirectiveErrors = Partial<Record<keyof DirectiveDraft, string>>;

const exampleLines: DirectiveLine[] = [
  { seq: 1, fromQty: "0", toQty: "100", actions: [{ seq: 1, strategy: "consolidate", zone: "FWD" }] },
  { seq: 2, fromQty: "100", actions: [{ seq: 1, strategy: "empty_location", zone: "RESERVE" }] },
];

function parseQuery(text: string): Record<string, string> | null {
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

function parseLines(text: string): DirectiveLine[] | null {
  try {
    const value: unknown = JSON.parse(text);
    return Array.isArray(value) && value.length > 0 ? (value as DirectiveLine[]) : null;
  } catch {
    return null;
  }
}

export function validateDirectiveDraft(d: DirectiveDraft): DirectiveErrors {
  const errors: DirectiveErrors = {};
  if (!UUID.test(d.warehouseId.trim())) errors.warehouseId = "Enter the warehouse id, a UUID.";
  if (!INTEGER.test(d.sequence) || Number(d.sequence) < 1) errors.sequence = "Enter a sequence of 1 or more.";
  if (parseQuery(d.query) === null) errors.query = "Write one key=value pair per line.";
  if (parseLines(d.lines) === null) errors.lines = "Enter the quantity bands as a JSON array with at least one band.";
  return errors;
}

/** Maps the service's field paths (sequence, lines[0].actions[0].strategy, …) onto the form. */
export function directiveFieldErrors(fields: Record<string, string>): DirectiveErrors {
  const errors: DirectiveErrors = {};
  for (const [path, message] of Object.entries(fields)) {
    const target: keyof DirectiveDraft | undefined =
      path === "warehouseId" ? "warehouseId" : path === "sequence" ? "sequence" : path === "workType" ? "workType" : path === "direction" ? "direction" : path.startsWith("lines") ? "lines" : undefined;
    if (!target) continue;
    const text = target === "lines" ? `${path} ${message}` : `This ${message}.`;
    errors[target] = errors[target] ? `${errors[target]}; ${text}` : text;
  }
  return errors;
}

/** Creates a location directive (POST /location-directives); run the simulator to try it first. */
export function NewDirectivePage() {
  const navigate = useNavigate();
  const [warehouse] = useWarehouse();
  const [draft, setDraft] = useState<DirectiveDraft>({
    warehouseId: warehouse,
    workType: "PURCHASE_PUTAWAY",
    direction: "PUT",
    sequence: "10",
    query: "",
    lines: JSON.stringify(exampleLines, null, 2),
  });
  const [errors, setErrors] = useState<DirectiveErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const set = (patch: Partial<DirectiveDraft>) => {
    setDraft((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateDirectiveDraft(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    const warehouseId = draft.warehouseId.trim().toLowerCase();
    const query = parseQuery(draft.query) ?? {};
    try {
      await createDirective(
        {
          warehouseId,
          workType: draft.workType,
          direction: draft.direction,
          sequence: Number(draft.sequence),
          ...(Object.keys(query).length > 0 ? { query } : {}),
          lines: parseLines(draft.lines) ?? [],
        },
        key.current,
      );
      navigate(`/warehouse/directives?warehouse=${encodeURIComponent(warehouseId)}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(directiveFieldErrors(problem.fields));
      setFailure(problem);
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New location directive" description="Directives run in sequence order for their work type and direction; the first band that covers the quantity runs its actions in order." />
      <Card title="Directive">
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>
            <WarehouseField id="new-dir-warehouse" value={draft.warehouseId} onChange={(v) => set({ warehouseId: v })} error={errors.warehouseId} />
          </div>
          <FormField id="new-dir-work-type" label="Work type" error={errors.workType}>
            {(c) => (
              <Select {...c} value={draft.workType} onChange={(e) => set({ workType: e.target.value })}>
                {WORK_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="new-dir-direction" label="Direction" error={errors.direction}>
            {(c) => (
              <Select {...c} value={draft.direction} onChange={(e) => set({ direction: e.target.value as Direction })}>
                <option value="PUT">Put-away</option>
                <option value="PICK">Pick</option>
              </Select>
            )}
          </FormField>
          <FormField id="new-dir-sequence" label="Sequence" hint="Unique for the work type and direction; lower runs first." error={errors.sequence}>
            {(c) => <Input {...c} inputMode="numeric" value={draft.sequence} onChange={(e) => set({ sequence: e.target.value })} invalid={Boolean(errors.sequence)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="new-dir-query" label="Applies to" optional hint="One key=value per line, matched against work line attributes. Blank applies to every line." error={errors.query}>
              {(c) => <Textarea {...c} mono rows={3} spellCheck={false} value={draft.query} onChange={(e) => set({ query: e.target.value })} invalid={Boolean(errors.query)} />}
            </FormField>
          </div>
          <div className={styles.wide}>
            <FormField
              id="new-dir-lines"
              label="Quantity bands (JSON)"
              hint={`Each band: seq, fromQty, optional toQty (exclusive) and actions with seq, strategy (${["consolidate", "empty_location", "fifo", "fefo"].join(", ")}, …) and zone. Bands must not overlap.`}
              error={errors.lines}
            >
              {(c) => <Textarea {...c} mono rows={12} spellCheck={false} value={draft.lines} onChange={(e) => set({ lines: e.target.value })} invalid={Boolean(errors.lines)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create directive
            </Button>
            <Link className={buttonClassName("secondary")} to="/warehouse">
              Try it in the simulator
            </Link>
          </div>
          <div className={styles.formActions}>
            <Alert tone="danger" title={failure ? "The directive was not created" : undefined} live="alert">
              {failure ? `${failure.message} (${failure.code})` : null}
            </Alert>
          </div>
        </form>
      </Card>
    </div>
  );
}
