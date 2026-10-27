import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createMeasure,
  describeError,
  listCostObjects,
  listMeasures,
  listMeasureValues,
  loadMeasureValues,
  type MeasureValue,
  type StatisticalMeasure,
} from "./api/client";
import { isPeriod, MEASURE_CODE, UUID, validateMeasureValues } from "./logic";
import styles from "./pages.module.css";
import { dash, Detail, ErrorList, formatDateTime, LoadError, thisPeriod } from "./parts";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<StatisticalMeasure>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "unit", header: "Unit" },
  { key: "formula", header: "In formulas", mono: true, render: (m) => `m.${m.code}` },
  { key: "legalEntityId", header: "Legal entity", mono: true },
];

/** Statistical measures (FR-38.17 inputs): driver quantities per cost object and period. */
export function MeasuresPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const state = useLoad((signal) => listMeasures(le || undefined, signal), le);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Statistical measures"
        description="Quantities such as inspection lots, machine hours or floor space, loaded per cost object and period. Template formulas read them as m.<code>."
        actions={
          <Link className={buttonClassName("primary")} to="/costacct/measures/new">
            New measure
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="measures-legal-entity" label="Legal entity" optional>
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load statistical measures" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Statistical measures"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(m) => m.id}
          loading={state.loading && !state.data}
          empty={{ title: "No statistical measures yet", description: "Create a measure, then load its values for a period." }}
          onRowOpen={(m) => navigate(`/costacct/measures/${m.id}`)}
        />
      )}
    </div>
  );
}

/** Creates a statistical measure. The code is what formulas reference. */
export function CreateMeasurePage() {
  const navigate = useNavigate();
  const [form, setForm] = useState({ legalEntityId: "", code: "", name: "", unit: "" });
  const [errors, setErrors] = useState<Partial<Record<keyof typeof form, string>>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = { legalEntityId: form.legalEntityId.trim(), code: form.code.trim(), name: form.name.trim(), unit: form.unit.trim() };
    const found: typeof errors = {};
    if (!UUID.test(clean.legalEntityId)) found.legalEntityId = "Enter the legal entity's id (a UUID).";
    if (!MEASURE_CODE.test(clean.code)) found.code = "Start with a letter or _, then up to 39 letters, digits or _.";
    if (clean.name === "" || clean.name.length > 200) found.name = "Enter a name of up to 200 characters.";
    if (clean.unit === "" || clean.unit.length > 20) found.unit = "Enter a unit of up to 20 characters.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const body = JSON.stringify(clean);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const created = await createMeasure(clean, key.current.value);
      navigate(`/costacct/measures/${created.id}`);
    } catch (err) {
      setFailure(describeError(err, "The measure could not be saved."));
    } finally {
      setBusy(false);
    }
  }

  const field = (id: keyof typeof form, label: string, hint?: string, mono = false) => (
    <FormField id={`measure-${id}`} label={label} hint={hint} error={errors[id]}>
      {(c) => <Input {...c} className={mono ? styles.mono : undefined} value={form[id]} invalid={Boolean(errors[id])} onChange={(e) => setForm({ ...form, [id]: e.target.value })} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New statistical measure" />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("legalEntityId", "Legal entity", undefined, true)}
          {field("code", "Code", "Formulas read it as m.<code>, for example inspectionLots.", true)}
          {field("name", "Name")}
          {field("unit", "Unit", "For example lot, hour or m².")}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save measure
            </Button>
            <Link className={buttonClassName("ghost")} to="/costacct/measures">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <ErrorList errors={[]} failure={failure} />
    </div>
  );
}

/** One measure: its values for a period, and a form to load them per cost object. */
export function MeasureValuesPage() {
  const { id = "" } = useParams();
  const measures = useLoad((signal) => listMeasures(undefined, signal), "all");
  const measure = measures.data?.items.find((m) => m.id === id);
  const le = measure?.legalEntityId ?? "";
  const [period, setPeriod] = useState(thisPeriod());
  const validPeriod = isPeriod(period) ? period : "";
  const values = useLoad((signal) => (validPeriod ? listMeasureValues(id, validPeriod, signal) : Promise.resolve({ items: [] })), `${id}|${validPeriod}`);
  const objects = useLoad((signal) => (le ? listCostObjects({ legalEntityId: le }, signal) : Promise.resolve({ items: [] })), le);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  if (measures.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Statistical measure" />
        <LoadError title="Could not load this measure" error={measures.error} onRetry={measures.reload} />
      </div>
    );
  }
  if (!measures.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Statistical measure" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  if (!measure) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Statistical measure" />
        <EmptyState headingLevel={3} title="Measure not found" description="It may belong to a legal entity outside your scope." />
      </div>
    );
  }
  const m = measure;
  const all = objects.data?.items ?? [];
  const code = (oid: string) => all.find((o) => o.id === oid)?.code ?? oid;
  const valueColumns: DataTableColumn<MeasureValue>[] = [
    { key: "costObjectId", header: "Cost object", mono: true, render: (v) => <Link to={`/costacct/cost-objects/${v.costObjectId}`}>{code(v.costObjectId)}</Link> },
    { key: "value", header: `Value (${m.unit})`, align: "end", mono: true },
    { key: "updatedAt", header: "Loaded", render: (v) => formatDateTime(v.updatedAt) },
  ];

  async function submit(e: FormEvent) {
    e.preventDefault();
    const rows = all.map((o) => ({ costObjectId: o.id, label: o.code, value: (draft[o.id] ?? "").trim() }));
    const found = validateMeasureValues(period, rows);
    setErrors(found);
    setFailure(null);
    setDone(null);
    if (found.length > 0) return;
    const input = { legalEntityId: m.legalEntityId, period, values: rows.filter((r) => r.value !== "").map((r) => ({ costObjectId: r.costObjectId, value: r.value })) };
    const body = JSON.stringify({ id: m.id, input });
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const out = await loadMeasureValues(m.id, input, key.current.value);
      setDone(`${out.loaded} values loaded for ${out.period}.`);
      setDraft({});
      values.reload();
    } catch (err) {
      setFailure(describeError(err, "The values could not be loaded."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={`${m.code} ${m.name}`} description={`Formulas read this measure as m.${m.code}. A value loaded again for the same period replaces the earlier one.`} />
      <Card>
        <dl className={styles.details}>
          <Detail term="Unit">{m.unit}</Detail>
          <Detail term="Legal entity" mono>
            {m.legalEntityId}
          </Detail>
        </dl>
      </Card>
      <div className={styles.toolbar}>
        <FormField id="measure-period" label="Period" hint="YYYY-MM">
          {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} />}
        </FormField>
      </div>
      {values.error ? (
        <LoadError title="Could not load the values" error={values.error} onRetry={values.reload} />
      ) : (
        <DataTable
          label="Measure values"
          columns={valueColumns}
          rows={values.data?.items ?? []}
          getRowKey={(v) => v.costObjectId}
          loading={values.loading && !values.data}
          empty={{ title: validPeriod ? `No values for ${validPeriod}` : "Enter a period", description: "Load values per cost object below." }}
        />
      )}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Load values">
          <h3 className={`${styles.sectionTitle} ${styles.wide}`}>Load values for {validPeriod || dash}</h3>
          {objects.error ? (
            <div className={styles.wide}>
              <LoadError title="Could not load cost objects" error={objects.error} onRetry={objects.reload} />
            </div>
          ) : all.length === 0 && !objects.loading ? (
            <div className={styles.wide}>
              <EmptyState compact headingLevel={3} title="No cost objects in this legal entity" description="Create cost objects first." />
            </div>
          ) : (
            all.map((o) => (
              <FormField key={o.id} id={`measure-value-${o.id}`} label={`${o.code} ${o.name}`} optional>
                {(c) => (
                  <Input {...c} inputMode="decimal" className={styles.mono} value={draft[o.id] ?? ""} onChange={(e) => setDraft((d) => ({ ...d, [o.id]: e.target.value }))} />
                )}
              </FormField>
            ))
          )}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Load values
            </Button>
          </div>
        </form>
        <ErrorList errors={errors} failure={failure} />
        {done ? (
          <Alert tone="success" live="status">
            {done}
          </Alert>
        ) : null}
      </Card>
    </div>
  );
}
