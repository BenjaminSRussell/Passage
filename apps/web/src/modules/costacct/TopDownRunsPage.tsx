import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Badge,
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
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import { describeError, getTopDownRun, listCostElements, listCostObjects, listTopDownRuns, runTopDown, type TopDownRun, type TopDownShare } from "./api/client";
import { formatAmount, formatTicks, previewTopDown, sumTicks, UUID, validateTopDown } from "./logic";
import styles from "./pages.module.css";
import { dash, Detail, ErrorList, formatDateTime, LoadError, thisPeriod } from "./parts";
import { useLoad } from "./useLoad";

const MAX_SEGMENTS = 50;

const runColumns: DataTableColumn<TopDownRun>[] = [
  { key: "period", header: "Period", mono: true },
  { key: "referencePeriod", header: "Basis period", mono: true, render: (r) => r.referencePeriod ?? dash },
  { key: "amount", header: "Amount", align: "end", mono: true, render: (r) => formatAmount(r.amount) },
  { key: "currency", header: "Currency", mono: true },
  { key: "shares", header: "Segments", align: "end", render: (r) => r.shares.length },
  { key: "createdAt", header: "Run at", render: (r) => formatDateTime(r.createdAt) },
];

/** Top-down distribution runs (FR-38.19): aggregated amounts spread to detailed segments by a reference basis. */
export function TopDownRunsPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const state = useLoad((signal) => listTopDownRuns(le || undefined, signal), le);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Top-down distribution"
        description="Each run takes an amount booked at an aggregated level, such as freight for the whole entity, and spreads it to customers, products or regions in proportion to a reference basis."
        actions={
          <Link className={buttonClassName("primary")} to="/costacct/top-down-runs/new">
            New top-down run
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="topdown-legal-entity" label="Legal entity" optional>
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load top-down runs" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Top-down runs"
          columns={runColumns}
          rows={state.data?.items ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading && !state.data}
          empty={{ title: "No top-down runs yet", description: "Distribute an aggregated amount once it is booked on its source cost object." }}
          onRowOpen={(r) => navigate(`/costacct/top-down-runs/${r.id}`)}
        />
      )}
    </div>
  );
}

/** Sets up one top-down run: source, element, amount and the basis per segment, with a cent-exact preview. */
export function NewTopDownRunPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState({ legalEntityId: "", period: thisPeriod(), referencePeriod: "", currency: "USD", sourceCostObjectId: "", costElementId: "", amount: "" });
  const [basis, setBasis] = useState<Record<string, string>>({});
  const [selected, setSelected] = useState<string[]>([]);
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });
  const le = UUID.test(form.legalEntityId.trim()) ? form.legalEntityId.trim() : "";
  const objects = useLoad((signal) => (le ? listCostObjects({ legalEntityId: le }, signal) : Promise.resolve({ items: [] })), le);
  const elements = useLoad((signal) => (le ? listCostElements(le, signal) : Promise.resolve({ items: [] })), le);
  const all = objects.data?.items ?? [];
  const candidates = all.filter((o) => o.id !== form.sourceCostObjectId);
  const segments = selected.filter((id) => id !== form.sourceCostObjectId).map((id) => ({ id, basis: (basis[id] ?? "").trim() }));
  const preview = previewTopDown(form.amount.trim(), segments);
  const code = (id: string) => all.find((o) => o.id === id)?.code ?? id;

  function set(field: keyof typeof form, value: string) {
    setForm((f) => ({ ...f, [field]: value }));
  }

  function toggle(id: string) {
    setSelected((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : cur.length < MAX_SEGMENTS ? [...cur, id] : cur));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = { ...form, legalEntityId: form.legalEntityId.trim(), referencePeriod: form.referencePeriod.trim(), amount: form.amount.trim() };
    const found = validateTopDown(clean, segments);
    setErrors(found);
    setFailure(null);
    if (found.length > 0) return;
    const input = {
      ...clean,
      referencePeriod: clean.referencePeriod || undefined,
      segments: segments.map((s) => ({ costObjectId: s.id, basis: s.basis })),
    };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const run = await runTopDown(input, key.current.value);
      navigate(`/costacct/top-down-runs/${run.id}`);
    } catch (err) {
      setFailure(describeError(err, "The top-down run could not be completed."));
    } finally {
      setBusy(false);
    }
  }

  const text = (field: keyof typeof form, label: string, hint?: string, optional = false) => (
    <FormField id={`topdown-${field}`} label={label} hint={hint} optional={optional}>
      {(c) => <Input {...c} className={styles.mono} value={form[field]} onChange={(e) => set(field, field === "currency" ? e.target.value.toUpperCase() : e.target.value)} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New top-down run"
        description="The source is credited and each segment debited on the same cost element, so the run nets to zero. Rounding cents go to the segment with the largest basis."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("legalEntityId", "Legal entity")}
          {text("period", "Period", "YYYY-MM, the period the amount is booked in.")}
          {text("referencePeriod", "Reference period", "YYYY-MM, the period the basis comes from.", true)}
          {text("currency", "Currency")}
          <FormField id="topdown-source" label="Source cost object" hint="The aggregated object that holds the amount.">
            {(c) => (
              <Select {...c} value={form.sourceCostObjectId} onChange={(e) => set("sourceCostObjectId", e.target.value)}>
                <option value="">{le ? "Choose the source" : "Enter a legal entity first"}</option>
                {all.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.code} {o.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="topdown-element" label="Cost element" hint="The element distributed, for example Freight.">
            {(c) => (
              <Select {...c} value={form.costElementId} onChange={(e) => set("costElementId", e.target.value)}>
                <option value="">{le ? "Choose an element" : "Enter a legal entity first"}</option>
                {(elements.data?.items ?? []).map((el) => (
                  <option key={el.id} value={el.id}>
                    {el.code} {el.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="topdown-amount" label="Amount" hint="At most what is booked on the source for the element and period.">
            {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={form.amount} onChange={(e) => set("amount", e.target.value)} />}
          </FormField>
          <fieldset className={styles.lineSet}>
            <legend className={styles.legend}>Segments (up to {MAX_SEGMENTS})</legend>
            {objects.error ? (
              <LoadError title="Could not load cost objects" error={objects.error} onRetry={objects.reload} />
            ) : !le ? (
              <p className={styles.note}>Enter a legal entity to choose its segments.</p>
            ) : candidates.length === 0 && !objects.loading ? (
              <EmptyState compact headingLevel={3} title="No other cost objects in this legal entity" description="Create the segments as cost objects first." />
            ) : (
              <ul className={styles.checkList}>
                {candidates.map((o) => (
                  <li key={o.id}>
                    <label className={styles.check}>
                      <Input type="checkbox" aria-label={`${o.code} ${o.name}`} checked={selected.includes(o.id)} onChange={() => toggle(o.id)} />
                      {o.code} {o.name}
                    </label>
                  </li>
                ))}
              </ul>
            )}
          </fieldset>
          {segments.length > 0 ? (
            <div className={styles.wide}>
              <table className={styles.matrix} aria-label="Segment basis">
                <thead>
                  <tr>
                    <th scope="col">Segment</th>
                    <th scope="col">Basis</th>
                    <th scope="col">Preview share</th>
                  </tr>
                </thead>
                <tbody>
                  {segments.map((s, i) => (
                    <tr key={s.id}>
                      <th scope="row">{code(s.id)}</th>
                      <td>
                        <Input
                          className={styles.matrixInput}
                          inputMode="decimal"
                          aria-label={`Basis of ${code(s.id)}`}
                          value={basis[s.id] ?? ""}
                          onChange={(e) => setBasis((b) => ({ ...b, [s.id]: e.target.value }))}
                        />
                      </td>
                      <td className={styles.mono}>{preview ? preview[i].amount : dash}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run distribution
            </Button>
            <Link className={buttonClassName("ghost")} to="/costacct/top-down-runs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <ErrorList errors={errors} failure={failure} />
    </div>
  );
}

/** One top-down run: the source, the amount and each segment's share. */
export function TopDownRunDetailPage() {
  const { id = "" } = useParams();
  const run = useLoad((signal) => getTopDownRun(id, signal), id);
  const le = run.data?.legalEntityId ?? "";
  const objects = useLoad((signal) => (le ? listCostObjects({ legalEntityId: le }, signal) : Promise.resolve({ items: [] })), le);
  const elements = useLoad((signal) => (le ? listCostElements(le, signal) : Promise.resolve({ items: [] })), le);
  const code = (oid: string) => (objects.data?.items ?? []).find((o) => o.id === oid)?.code ?? oid;

  if (run.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Top-down run" />
        <LoadError title="Could not load this top-down run" error={run.error} onRetry={run.reload} />
      </div>
    );
  }
  if (!run.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Top-down run" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const r = run.data;
  const element = (elements.data?.items ?? []).find((e) => e.id === r.costElementId);
  const distributed = sumTicks(r.shares.map((s) => s.amount));
  const exact = distributed !== null && distributed === sumTicks([r.amount]);
  const shareColumns: DataTableColumn<TopDownShare>[] = [
    { key: "segmentId", header: "Segment", mono: true, render: (s) => <Link to={`/costacct/cost-objects/${s.segmentId}`}>{code(s.segmentId)}</Link> },
    { key: "basis", header: "Basis", align: "end", mono: true },
    { key: "amount", header: `Share (${r.currency})`, align: "end", mono: true, render: (s) => formatAmount(s.amount) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Top-down run ${r.period}`}
        description="The source was credited with the amount and each segment debited its share on the same cost element."
        meta={<Badge tone={exact ? "success" : "danger"}>{exact ? "Distributed to the cent" : "Shares differ from the amount"}</Badge>}
      />
      <StatGroup label="Run totals">
        <StatTile label="Amount" value={formatAmount(r.amount)} />
        <StatTile label="Distributed" value={distributed === null ? dash : formatAmount(formatTicks(distributed))} />
        <StatTile label="Segments" value={r.shares.length} />
        <StatTile label="Zero-basis segments" value={r.excluded.length} tone="warning" />
      </StatGroup>
      <Card>
        <dl className={styles.details}>
          <Detail term="Source" mono>
            <Link to={`/costacct/cost-objects/${r.sourceCostObjectId}`}>{code(r.sourceCostObjectId)}</Link>
          </Detail>
          <Detail term="Cost element" mono>
            {element ? `${element.code} ${element.name}` : r.costElementId}
          </Detail>
          <Detail term="Basis period" mono>
            {r.referencePeriod ?? dash}
          </Detail>
          <Detail term="Currency" mono>
            {r.currency}
          </Detail>
          <Detail term="Run at">{formatDateTime(r.createdAt)}</Detail>
          <Detail term="Excluded" mono>
            {r.excluded.length > 0 ? r.excluded.map(code).join(", ") : "None"}
          </Detail>
        </dl>
      </Card>
      <DataTable label="Shares" columns={shareColumns} rows={r.shares} getRowKey={(s) => s.segmentId} />
    </div>
  );
}
