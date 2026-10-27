import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  evaluateCompliance,
  getComposition,
  newKey,
  QualityApiError,
  saveComposition,
  type ComplianceEvaluation,
  type ComplianceResult,
  type Composition,
  type NewComposition,
  type Problem,
} from "./api/client";
import { KIND_LABELS } from "./ComplianceListsPage";
import { DECIMAL, qty, REGION, shortId, UUID, validCAS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Looks an item up by id and opens its composition and compliance. */
export function ItemLookupPage() {
  const navigate = useNavigate();
  const [itemId, setItemId] = useState("");
  const [error, setError] = useState<string | null>(null);

  function open(event: FormEvent) {
    event.preventDefault();
    const id = itemId.trim();
    if (!UUID.test(id)) {
      setError("Enter the item id (a UUID).");
      return;
    }
    navigate(`/quality/compliance/items/${id}`);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Item compliance"
        description="Declare what an item is made of: substances by CAS number and component items by mass fraction. Then evaluate it against every current compliance list for a destination region."
        actions={
          <Link className={buttonClassName("secondary")} to="/quality/compliance/lists">
            Compliance lists
          </Link>
        }
      />
      <Card>
        <form className={styles.toolbar} onSubmit={open} noValidate aria-label="Open item">
          <FormField id="compliance-item" label="Item id" error={error ?? undefined}>
            {(control) => <Input {...control} className={styles.mono} value={itemId} invalid={Boolean(error)} onChange={(event) => setItemId(event.target.value)} />}
          </FormField>
          <Button type="submit" variant="primary">
            Open item
          </Button>
        </form>
      </Card>
    </div>
  );
}

interface Row {
  key: string;
  label: string;
  value: string;
}

const substanceColumns: DataTableColumn<Row>[] = [
  { key: "label", header: "CAS number", mono: true },
  { key: "value", header: "Mass fraction", align: "end", mono: true, render: (row) => `${qty(row.value)} (${percent(row.value)} % w/w)` },
];

const partColumns: DataTableColumn<Row>[] = [
  { key: "label", header: "Component item", mono: true, render: (row) => <Link to={`/quality/compliance/items/${row.label}`}>{row.label}</Link> },
  { key: "value", header: "Mass fraction", align: "end", mono: true, render: (row) => qty(row.value) },
];

/** "0.0024" → "0.24": a fraction as a percentage, exact on decimal strings. */
export function percent(fraction: string): string {
  const m = /^(\d+)(?:\.(\d*))?$/.exec(fraction);
  if (!m) return fraction;
  const frac = (m[2] ?? "").padEnd(2, "0");
  const whole = (m[1] + frac.slice(0, 2)).replace(/^0+(?=\d)/, "");
  const rest = frac.slice(2);
  return qty(rest ? `${whole}.${rest}` : whole);
}

interface LineValues {
  id: string;
  fraction: string;
}

/** Checks that mirror the service's ValidateConstituent, and the request the form sends. */
export function validateComposition(itemId: string, substances: LineValues[], parts: LineValues[]): { errors: Record<string, string>; input?: NewComposition } {
  const errors: Record<string, string> = {};
  const direct: Record<string, string> = {};
  let directSum = 0;
  substances.forEach((s, i) => {
    const cas = s.id.trim();
    const f = s.fraction.trim();
    if (!validCAS(cas)) errors[`s${i}.id`] = "Enter a CAS number with a valid check digit, such as 7439-92-1.";
    else if (cas in direct) errors[`s${i}.id`] = "This substance is already declared.";
    if (!DECIMAL.test(f) || Number(f) > 1) errors[`s${i}.fraction`] = "Enter a fraction from 0 to 1, such as 0.0024 for 0.24 %.";
    direct[cas] = f;
    directSum += Number(f);
  });
  const seen = new Set<string>();
  let partSum = 0;
  const out: NewComposition["parts"] = [];
  parts.forEach((p, i) => {
    const id = p.id.trim();
    const f = p.fraction.trim();
    if (!UUID.test(id)) errors[`p${i}.id`] = "Enter the component's item id (a UUID).";
    else if (id === itemId) errors[`p${i}.id`] = "An item cannot be a part of itself.";
    else if (seen.has(id)) errors[`p${i}.id`] = "This component is already listed.";
    seen.add(id);
    if (!DECIMAL.test(f) || Number(f) <= 0 || Number(f) > 1) errors[`p${i}.fraction`] = "Enter a mass fraction above 0 and at most 1.";
    partSum += Number(f);
    out.push({ itemId: id, massFraction: f });
  });
  if (substances.length + parts.length === 0) errors.form = "Declare at least one substance or component.";
  if (directSum > 1 + 1e-9) errors.form = "The substance fractions add up to more than 1.";
  if (partSum > 1 + 1e-9) errors.form = "The component mass fractions add up to more than 1.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { direct, parts: out } };
}

function CompositionForm({ itemId, current, onSaved }: { itemId: string; current: Composition | null; onSaved(c: Composition): void }) {
  const [substances, setSubstances] = useState<LineValues[]>([]);
  const [parts, setParts] = useState<LineValues[]>([]);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  useEffect(() => {
    setSubstances(current ? Object.entries(current.direct).map(([id, fraction]) => ({ id, fraction: qty(fraction) })) : [{ id: "", fraction: "" }]);
    setParts(current ? current.parts.map((p) => ({ id: p.itemId, fraction: qty(p.massFraction) })) : []);
  }, [current]);

  function edit(list: "s" | "p", i: number, patch: Partial<LineValues>) {
    key.current = newKey();
    if (list === "s") setSubstances(substances.map((l, j) => (j === i ? { ...l, ...patch } : l)));
    else setParts(parts.map((l, j) => (j === i ? { ...l, ...patch } : l)));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const checked = validateComposition(itemId, substances, parts);
    setErrors(checked.errors);
    setFailure(null);
    if (!checked.input) return;
    setBusy(true);
    try {
      onSaved(await saveComposition(itemId, checked.input, key.current));
      key.current = newKey();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const line = (list: "s" | "p", values: LineValues[], i: number) => {
    const v = values[i];
    const what = list === "s" ? "Substance" : "Component";
    return (
      <fieldset key={`${list}${i}`} className={styles.valueGroup}>
        <legend className={styles.legend}>{`${what} ${i + 1}`}</legend>
        <FormField id={`comp-${list}${i}-id`} label={list === "s" ? "CAS number" : "Item id"} error={errors[`${list}${i}.id`]}>
          {(control) => <Input {...control} className={styles.mono} value={v.id} invalid={Boolean(errors[`${list}${i}.id`])} onChange={(event) => edit(list, i, { id: event.target.value })} />}
        </FormField>
        <FormField id={`comp-${list}${i}-fraction`} label="Mass fraction" hint="0 to 1 of the item's mass." error={errors[`${list}${i}.fraction`]}>
          {(control) => <Input {...control} inputMode="decimal" value={v.fraction} invalid={Boolean(errors[`${list}${i}.fraction`])} onChange={(event) => edit(list, i, { fraction: event.target.value })} />}
        </FormField>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            key.current = newKey();
            if (list === "s") setSubstances(substances.filter((_, j) => j !== i));
            else setParts(parts.filter((_, j) => j !== i));
          }}
        >
          {`Remove ${what.toLowerCase()} ${i + 1}`}
        </Button>
      </fieldset>
    );
  };

  return (
    <Card title={current ? `Save version ${current.version + 1}` : "Declare the composition"}>
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Save composition">
        {substances.map((_, i) => line("s", substances, i))}
        {parts.map((_, i) => line("p", parts, i))}
        {errors.form ? (
          <div className={styles.formActions}>
            <Alert tone="danger" live="alert">
              {errors.form}
            </Alert>
          </div>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the composition" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button onClick={() => setSubstances([...substances, { id: "", fraction: "" }])}>Add substance</Button>
          <Button onClick={() => setParts([...parts, { id: "", fraction: "" }])}>Add component</Button>
          <Button type="submit" variant="primary" loading={busy}>
            Save composition
          </Button>
        </div>
      </form>
    </Card>
  );
}

const resultColumns: DataTableColumn<ComplianceResult>[] = [
  { key: "listCode", header: "List", mono: true, render: (row) => `${row.listCode} v${row.listVersion}` },
  { key: "listKind", header: "Kind", render: (row) => KIND_LABELS[row.listKind] ?? row.listKind },
  {
    key: "code",
    header: "Outcome",
    render: (row) =>
      !row.code ? (
        <Badge tone="success">Below every threshold</Badge>
      ) : (
        <Badge tone={row.severity === "error" ? "danger" : "warning"}>{`${row.code} ${row.severity === "error" ? "blocked" : "warning"}`}</Badge>
      ),
  },
  {
    key: "hits",
    header: "Substances over the threshold",
    render: (row) => (row.hits.length === 0 ? "—" : row.hits.map((h) => `${h.name || h.cas}: ${qty(h.percentWw)} % > ${qty(h.thresholdPct)} %`).join("; ")),
  },
  { key: "version", header: "Result version", align: "end", render: (row) => (row.version ? `v${row.version}` : "—") },
];

function EvaluationPanel({ itemId }: { itemId: string }) {
  const [region, setRegion] = useState("EU");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ComplianceEvaluation | null>(null);

  async function run(event: FormEvent) {
    event.preventDefault();
    const r = region.trim();
    setFailure(null);
    if (!REGION.test(r)) {
      setError("Enter a region code in capitals, such as EU, DE or US-CA.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      setResult(await evaluateCompliance(itemId, r));
    } catch (error) {
      setResult(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Evaluate for a region">
      <div className={styles.stack}>
        <form className={styles.toolbar} onSubmit={run} noValidate aria-label="Evaluate compliance">
          <FormField id="compliance-region" label="Destination region" error={error ?? undefined}>
            {(control) => <Input {...control} value={region} invalid={Boolean(error)} onChange={(event) => setRegion(event.target.value)} />}
          </FormField>
          <Button type="submit" variant="primary" loading={busy}>
            Evaluate
          </Button>
        </form>
        {failure ? (
          <Alert tone="danger" title="Could not evaluate the item" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {result ? (
          <>
            {result.blocked ? (
              <Alert tone="danger" title={`Restricted in ${result.region}`} live="status">
                A restricted list in BLOCK mode is exceeded: sales lines, shipments and receipts for {result.region} are blocked (QM-0122).
              </Alert>
            ) : (
              <Alert tone="success" live="status">
                {`Not blocked in ${result.region} (composition v${result.compositionVersion}).`}
              </Alert>
            )}
            {result.undeclaredItems.length > 0 ? (
              <Alert tone="warning" title="Components without a composition">
                {`${result.undeclaredItems.map(shortId).join(", ")} count as containing nothing. Declare them for a complete roll-up.`}
              </Alert>
            ) : null}
            <DataTable
              label="Compliance results"
              columns={resultColumns}
              rows={result.results}
              getRowKey={(row) => `${row.listCode}-${row.listVersion}`}
              empty={{ title: `No list applies in ${result.region}`, description: "No current compliance list names this region." }}
            />
          </>
        ) : null}
      </div>
    </Card>
  );
}

/** One item's declared composition, a form for its next version, and its evaluation (FR-33.13, BR-33.11). */
export function ItemCompliancePage() {
  const { id = "" } = useParams();
  const state = useLoad(
    (signal) =>
      getComposition(id, signal).catch((error: unknown) => {
        if (error instanceof QualityApiError && error.status === 404) return null;
        throw error;
      }),
    `composition|${id}`,
  );
  const [saved, setSaved] = useState<Composition | null>(null);
  useEffect(() => setSaved(null), [id]);
  const current = saved ?? state.data ?? null;

  const substances: Row[] = current ? Object.entries(current.direct).map(([cas, value]) => ({ key: cas, label: cas, value })) : [];
  const parts: Row[] = current ? current.parts.map((p) => ({ key: p.itemId, label: p.itemId, value: p.massFraction })) : [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Item ${shortId(id)} compliance`}
        description="Concentrations roll up through the components: a substance's share of the item is its share of each component times the component's mass fraction."
        meta={<span className={styles.mono}>{id}</span>}
        actions={
          <Link className={buttonClassName("ghost")} to="/quality/compliance/items">
            Another item
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the composition" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : state.loading && !current ? (
        <div className={styles.stack} aria-busy="true">
          <span className="visually-hidden">Loading the composition…</span>
          <Skeleton height="tile" />
        </div>
      ) : current ? (
        <>
          {saved ? (
            <Alert tone="success" live="status">
              {`Composition version ${saved.version} saved. Evaluate again to record new results.`}
            </Alert>
          ) : null}
          <Card title={`Composition version ${current.version}`}>
            <div className={styles.stack}>
              <DataTable label="Declared substances" columns={substanceColumns} rows={substances} getRowKey={(row) => row.key} empty={{ title: "No substances declared on the item itself" }} />
              <DataTable label="Components" columns={partColumns} rows={parts} getRowKey={(row) => row.key} empty={{ title: "No components" }} />
            </div>
          </Card>
          <EvaluationPanel itemId={id} />
        </>
      ) : (
        <Card>
          <EmptyState title="No composition declared yet" description="Declare the item's substances or components below; until then it cannot be evaluated." />
        </Card>
      )}
      {state.loading && !current ? null : <CompositionForm itemId={id} current={current} onSaved={setSaved} />}
    </div>
  );
}
