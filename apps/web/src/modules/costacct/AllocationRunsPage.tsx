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
import { describeError, getRun, listCostElements, listCostObjects, listRuns, runAllocation, type Flow, type Posting, type Run } from "./api/client";
import { buildBasis, CURRENCY, formatAmount, isPeriod, netPostingsBalanced, UUID } from "./logic";
import styles from "./pages.module.css";
import { Detail, ErrorList, formatDateTime, LoadError, thisPeriod } from "./parts";
import { useLoad } from "./useLoad";

const MAX_GRID = 12;

const runColumns: DataTableColumn<Run>[] = [
  { key: "period", header: "Period", mono: true },
  { key: "method", header: "Method", render: (r) => (r.method === "exact-lu" ? "Exact solve" : "Iterative") },
  { key: "iterations", header: "Iterations", align: "end" },
  { key: "totalIn", header: "Cost in", align: "end", mono: true, render: (r) => formatAmount(r.totalIn) },
  { key: "totalOut", header: "Cost out", align: "end", mono: true, render: (r) => formatAmount(r.totalOut) },
  { key: "currency", header: "Currency", mono: true },
  { key: "converged", header: "Converged", render: (r) => <Badge tone={r.converged ? "success" : "warning"}>{r.converged ? "Yes" : "Residual swept"}</Badge> },
  { key: "createdAt", header: "Run at", render: (r) => formatDateTime(r.createdAt) },
];

/** Allocation runs (FR-38.7): one completed reciprocal step per legal entity and period. */
export function AllocationRunsPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const state = useLoad((signal) => listRuns(le || undefined, signal), le);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Allocation runs"
        description="Each run moves service-center cost to the centers it serves, solving reciprocal service exactly, and conserves cost to the cent."
        actions={
          <Link className={buttonClassName("primary")} to="/costacct/allocation-runs/new">
            New allocation run
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="runs-legal-entity" label="Legal entity" optional>
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load allocation runs" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Allocation runs"
          columns={runColumns}
          rows={state.data?.items ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading && !state.data}
          empty={{ title: "No allocation runs yet", description: "Run an allocation once the period's journal lines are mirrored." }}
          onRowOpen={(r) => navigate(`/costacct/allocation-runs/${r.id}`)}
        />
      )}
    </div>
  );
}

/** Sets up one reciprocal step: the centers taking part and the basis each sender uses to share its cost. */
export function NewAllocationRunPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const [period, setPeriod] = useState(thisPeriod());
  const [currency, setCurrency] = useState("USD");
  const [secondaryElementId, setSecondaryElementId] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [grid, setGrid] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const objects = useLoad((signal) => (le ? listCostObjects({ legalEntityId: le }, signal) : Promise.resolve({ items: [] })), le);
  const elements = useLoad((signal) => (le ? listCostElements(le, signal) : Promise.resolve({ items: [] })), le);
  const secondaries = (elements.data?.items ?? []).filter((e) => e.kind === "Secondary");
  const byId = new Map((objects.data?.items ?? []).map((o) => [o.id, o]));

  function toggle(id: string) {
    setSelected((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : cur.length < MAX_GRID ? [...cur, id] : cur));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const found: string[] = [];
    if (!le) found.push("Enter the legal entity's id (a UUID).");
    if (!isPeriod(period)) found.push("Enter the period as YYYY-MM.");
    if (!CURRENCY.test(currency)) found.push("Use a three-letter currency code.");
    if (!UUID.test(secondaryElementId)) found.push("Choose the secondary element that carries the allocated cost.");
    if (selected.length < 2) found.push("Choose at least two cost objects.");
    const basis = buildBasis(selected.map((from) => selected.map((to) => grid[`${from}>${to}`] ?? "")));
    if (!basis.ok) found.push(basis.error);
    setErrors(found);
    setFailure(null);
    if (found.length > 0 || !basis.ok) return;
    const input = { legalEntityId: le, period, currency, secondaryElementId, costObjectIds: selected, basis: basis.value };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const run = await runAllocation(input, key.current.value);
      navigate(`/costacct/allocation-runs/${run.runId}`);
    } catch (err) {
      setFailure(describeError(err, "The allocation could not be run."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New allocation run"
        description="Each center's cost is its mirrored journal cost for the period. A center with no basis keeps its cost and is reported as unallocated."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="run-le" label="Legal entity">
            {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
          </FormField>
          <FormField id="run-period" label="Period" hint="YYYY-MM">
            {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} />}
          </FormField>
          <FormField id="run-currency" label="Currency">
            {(c) => <Input {...c} className={styles.mono} value={currency} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />}
          </FormField>
          <FormField id="run-element" label="Secondary element">
            {(c) => (
              <Select {...c} value={secondaryElementId} onChange={(e) => setSecondaryElementId(e.target.value)}>
                <option value="">{le ? "Choose an element" : "Enter a legal entity first"}</option>
                {secondaries.map((el) => (
                  <option key={el.id} value={el.id}>
                    {el.code} {el.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <fieldset className={styles.lineSet}>
            <legend className={styles.legend}>Cost objects in the step (up to {MAX_GRID})</legend>
            {objects.error ? (
              <LoadError title="Could not load cost objects" error={objects.error} onRetry={objects.reload} />
            ) : !le ? (
              <p className={styles.note}>Enter a legal entity to choose its cost objects.</p>
            ) : (objects.data?.items ?? []).length === 0 && !objects.loading ? (
              <EmptyState compact headingLevel={3} title="No cost objects in this legal entity" description="Create cost objects first." />
            ) : (
              <ul className={styles.checkList}>
                {(objects.data?.items ?? []).map((o) => (
                  <li key={o.id}>
                    <label className={styles.check}>
                      <Input type="checkbox" checked={selected.includes(o.id)} onChange={() => toggle(o.id)} />
                      {o.code} {o.name}
                    </label>
                  </li>
                ))}
              </ul>
            )}
          </fieldset>
          {selected.length >= 2 ? (
            <div className={styles.wide}>
              <p className={styles.note}>Basis: how much each sender (row) gives each receiver (column). Shares are the basis divided by the row total; blanks are zero.</p>
              <table className={styles.matrix} aria-label="Basis matrix">
                <thead>
                  <tr>
                    <th scope="col">Sender</th>
                    {selected.map((to) => (
                      <th key={to} scope="col">
                        {byId.get(to)?.code ?? to}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {selected.map((from) => (
                    <tr key={from}>
                      <th scope="row">{byId.get(from)?.code ?? from}</th>
                      {selected.map((to) => (
                        <td key={to}>
                          {from === to ? (
                            "—"
                          ) : (
                            <Input
                              className={styles.matrixInput}
                              inputMode="decimal"
                              aria-label={`Basis from ${byId.get(from)?.code ?? from} to ${byId.get(to)?.code ?? to}`}
                              value={grid[`${from}>${to}`] ?? ""}
                              onChange={(e) => setGrid((g) => ({ ...g, [`${from}>${to}`]: e.target.value }))}
                            />
                          )}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run allocation
            </Button>
            <Link className={buttonClassName("ghost")} to="/costacct/allocation-runs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <ErrorList errors={errors} failure={failure} />
    </div>
  );
}

/** One run: totals, flows between centers and the net posting each center received. */
export function AllocationRunDetailPage() {
  const { id = "" } = useParams();
  const run = useLoad((signal) => getRun(id, signal), id);
  const objects = useLoad((signal) => (run.data ? listCostObjects({ legalEntityId: run.data.legalEntityId }, signal) : Promise.resolve({ items: [] })), run.data?.legalEntityId ?? "");
  const code = (oid: string) => (objects.data?.items ?? []).find((o) => o.id === oid)?.code ?? oid;

  if (run.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Allocation run" />
        <LoadError title="Could not load this allocation run" error={run.error} onRetry={run.reload} />
      </div>
    );
  }
  if (!run.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Allocation run" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const r = run.data;
  const flowColumns: DataTableColumn<Flow>[] = [
    { key: "from", header: "Sender", mono: true, render: (f) => code(f.from) },
    { key: "to", header: "Receiver", mono: true, render: (f) => code(f.to) },
    { key: "amount", header: `Amount (${r.currency})`, align: "end", mono: true, render: (f) => formatAmount(f.amount) },
  ];
  const postingColumns: DataTableColumn<Posting>[] = [
    { key: "centerId", header: "Cost object", mono: true, render: (p) => <Link to={`/costacct/cost-objects/${p.centerId}`}>{code(p.centerId)}</Link> },
    { key: "amount", header: `Net posting (${r.currency})`, align: "end", mono: true, render: (p) => formatAmount(p.amount) },
  ];
  const balanced = netPostingsBalanced(r.postings.map((p) => p.amount)) && r.totalIn === r.totalOut;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Allocation run ${r.period}`}
        description="Senders end at zero; the final receivers take exactly the cost the senders brought in."
        meta={<Badge tone={balanced ? "success" : "danger"}>{balanced ? "Conserved to the cent" : "Totals differ"}</Badge>}
      />
      <StatGroup label="Run totals">
        <StatTile label="Cost in" value={formatAmount(r.totalIn)} />
        <StatTile label="Cost out" value={formatAmount(r.totalOut)} />
        <StatTile label="Residual swept" value={formatAmount(r.swept)} />
        <StatTile label="Unallocated senders" value={r.unallocated.length} tone="warning" />
      </StatGroup>
      <Card>
        <dl className={styles.details}>
          <Detail term="Method">{r.method === "exact-lu" ? "Exact solve (LU)" : `Iterative (${r.iterations} iterations)`}</Detail>
          <Detail term="Converged">{r.converged ? "Yes" : "No: the residual was swept to the largest receivers"}</Detail>
          <Detail term="Currency" mono>
            {r.currency}
          </Detail>
          <Detail term="Legal entity" mono>
            {r.legalEntityId}
          </Detail>
          <Detail term="Run at">{formatDateTime(r.createdAt)}</Detail>
          <Detail term="Unallocated" mono>
            {r.unallocated.length > 0 ? r.unallocated.map(code).join(", ") : "None"}
          </Detail>
        </dl>
      </Card>
      <DataTable label="Net postings" columns={postingColumns} rows={r.postings} getRowKey={(p) => p.centerId} />
      <DataTable
        label="Flows"
        columns={flowColumns}
        rows={r.flows}
        getRowKey={(f) => `${f.from}>${f.to}`}
        empty={{ title: "No flows", description: "No center had a basis to share its cost." }}
      />
    </div>
  );
}
