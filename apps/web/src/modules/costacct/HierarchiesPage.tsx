import { useRef, useState, type CSSProperties, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
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
  createHierarchy,
  describeError,
  getDistributionRun,
  getHierarchy,
  listCostElements,
  listCostObjects,
  listDistributionRuns,
  listHierarchies,
  listHierarchyBalances,
  type DistributionRun,
  type Hierarchy,
  type HierarchyNode,
  type HierarchyType,
} from "./api/client";
import { HierarchyActions } from "./HierarchyPanels";
import { formatAmount, isPeriod, nodeTree, UUID, validateCodeForm, type TreeRow } from "./logic";
import styles from "./pages.module.css";
import { dash, Detail, ErrorList, formatDateTime, kindLabel, LoadError, thisPeriod } from "./parts";
import { useLoad } from "./useLoad";

export const BASIS_LABEL: Record<string, string> = {
  ActualOutput: "Actual output",
  StandardCost: "Standard cost of output",
  FixedPercent: "Fixed percentage",
};

const TYPE_HINT: Record<HierarchyType, string> = {
  Collective: "Nodes may collect cost that no lower object can carry and distribute it to their children at period end.",
  Reporting: "Groups cost objects for reporting; no node collects cost.",
};

const columns: DataTableColumn<Hierarchy>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "type", header: "Type" },
  { key: "nodeCount", header: "Nodes", align: "end" },
  { key: "legalEntityId", header: "Legal entity", mono: true },
];

/** Cost object hierarchies (FR-38.18): trees of cost objects such as product group, product and order. */
export function HierarchiesPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const state = useLoad((signal) => listHierarchies(le || undefined, signal), le);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost object hierarchies"
        description="Arrange cost objects in trees, for example product group, product and production order. In a collective hierarchy a node collects cost that belongs to no single lower object and distributes it to its children at period end."
        actions={
          <Link className={buttonClassName("primary")} to="/costacct/hierarchies/new">
            New hierarchy
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="hierarchies-legal-entity" label="Legal entity" optional>
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load hierarchies" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Cost object hierarchies"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(h) => h.id}
          loading={state.loading && !state.data}
          empty={{ title: "No hierarchies yet", description: "Create a hierarchy, then add its cost objects as nodes." }}
          onRowOpen={(h) => navigate(`/costacct/hierarchies/${h.id}`)}
        />
      )}
    </div>
  );
}

/** Creates a hierarchy: legal entity, code, name and whether its nodes may collect cost. */
export function CreateHierarchyPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState({ legalEntityId: "", code: "", name: "", type: "Collective" as HierarchyType });
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  async function submit(e: FormEvent) {
    e.preventDefault();
    const input = { legalEntityId: form.legalEntityId.trim(), code: form.code.trim(), name: form.name.trim(), type: form.type };
    const found = Object.values(validateCodeForm(input)).filter((m): m is string => Boolean(m));
    setErrors(found);
    setFailure(null);
    if (found.length > 0) return;
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const h = await createHierarchy(input, key.current.value);
      navigate(`/costacct/hierarchies/${h.id}`);
    } catch (err) {
      setFailure(describeError(err, "The hierarchy could not be created."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New cost object hierarchy" description="Add the cost objects as nodes once the hierarchy exists." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="hierarchy-legal-entity" label="Legal entity">
            {(c) => <Input {...c} className={styles.mono} value={form.legalEntityId} onChange={(e) => setForm((f) => ({ ...f, legalEntityId: e.target.value }))} />}
          </FormField>
          <FormField id="hierarchy-code" label="Code" hint="2-40 characters of A-Z, 0-9, _ or -, unique in the legal entity.">
            {(c) => <Input {...c} className={styles.mono} value={form.code} onChange={(e) => setForm((f) => ({ ...f, code: e.target.value.toUpperCase() }))} />}
          </FormField>
          <FormField id="hierarchy-name" label="Name">
            {(c) => <Input {...c} value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} />}
          </FormField>
          <FormField id="hierarchy-type" label="Type" hint={TYPE_HINT[form.type]}>
            {(c) => (
              <Select {...c} value={form.type} onChange={(e) => setForm((f) => ({ ...f, type: e.target.value as HierarchyType }))}>
                <option value="Collective">Collective</option>
                <option value="Reporting">Reporting</option>
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create hierarchy
            </Button>
            <Link className={buttonClassName("ghost")} to="/costacct/hierarchies">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <ErrorList errors={errors} failure={failure} />
    </div>
  );
}

/** One hierarchy: its node tree, what each node holds in a period, the node actions and the run history. */
export function HierarchyDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [reload, setReload] = useState(0);
  const [period, setPeriod] = useState(thisPeriod());
  const validPeriod = isPeriod(period.trim()) ? period.trim() : "";
  const hierarchy = useLoad((signal) => getHierarchy(id, signal), `${id}|${reload}`);
  const le = hierarchy.data?.legalEntityId ?? "";
  const objects = useLoad((signal) => (le ? listCostObjects({ legalEntityId: le }, signal) : Promise.resolve({ items: [] })), le);
  const elements = useLoad((signal) => (le ? listCostElements(le, signal) : Promise.resolve({ items: [] })), le);
  const balances = useLoad(
    (signal) => (validPeriod ? listHierarchyBalances(id, validPeriod, signal) : Promise.resolve({ items: [] })),
    `${id}|${validPeriod}|${reload}`,
  );
  const runs = useLoad((signal) => listDistributionRuns({ hierarchyId: id }, signal), `${id}|${reload}`);

  if (hierarchy.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Cost object hierarchy" />
        <LoadError title="Could not load this hierarchy" error={hierarchy.error} onRetry={hierarchy.reload} />
      </div>
    );
  }
  if (!hierarchy.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Cost object hierarchy" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const h = hierarchy.data;
  const nodes = h.nodes ?? [];
  const objectList = objects.data?.items ?? [];
  const object = (oid: string) => objectList.find((o) => o.id === oid);
  const nodeLabel = (nid: string) => {
    const n = nodes.find((x) => x.id === nid);
    return n ? (object(n.costObjectId)?.code ?? n.costObjectId) : nid;
  };
  const elementLabel = (eid: string) => (elements.data?.items ?? []).find((e) => e.id === eid)?.code ?? eid;
  const balanceRows = balances.data?.items ?? [];
  const held = (nid: string) =>
    balanceRows
      .filter((b) => b.nodeId === nid)
      .map((b) => `${formatAmount(b.amount)} ${b.currency} ${elementLabel(b.costElementId)}`)
      .join(", ") || dash;

  const treeColumns: DataTableColumn<TreeRow<HierarchyNode>>[] = [
    {
      key: "node",
      header: "Cost object",
      render: (r) => {
        const o = object(r.node.costObjectId);
        return (
          <span className={styles.treeCell} style={{ "--depth": r.depth } as CSSProperties}>
            <Link className={styles.mono} to={`/costacct/cost-objects/${r.node.costObjectId}`}>
              {o?.code ?? r.node.costObjectId}
            </Link>
            {o ? ` ${o.name}` : null}
          </span>
        );
      },
    },
    {
      key: "kind",
      header: "Kind",
      render: (r) => {
        const o = object(r.node.costObjectId);
        return o ? kindLabel(o.kind) : dash;
      },
    },
    { key: "collects", header: "Collects", render: (r) => (r.node.collects ? <Badge tone="info">Collects</Badge> : "No") },
    { key: "basis", header: "Distribution basis", render: (r) => (r.node.distributionBasis ? BASIS_LABEL[r.node.distributionBasis] : dash) },
    { key: "held", header: `Held in ${validPeriod || "the period"}`, align: "end", mono: true, render: (r) => held(r.node.id) },
  ];

  const runColumns: DataTableColumn<DistributionRun>[] = [
    { key: "period", header: "Period", mono: true },
    { key: "nodeId", header: "Node", mono: true, render: (r) => nodeLabel(r.nodeId) },
    { key: "costElementId", header: "Cost element", mono: true, render: (r) => elementLabel(r.costElementId) },
    { key: "amount", header: "Held", align: "end", mono: true, render: (r) => formatAmount(r.amount) },
    { key: "distributed", header: "Distributed", align: "end", mono: true, render: (r) => formatAmount(r.distributed) },
    { key: "residual", header: "Residual", align: "end", mono: true, render: (r) => formatAmount(r.residual) },
    { key: "currency", header: "Currency", mono: true },
    { key: "createdAt", header: "Run at", render: (r) => formatDateTime(r.createdAt) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${h.code} ${h.name}`}
        description={TYPE_HINT[h.type]}
        meta={<Badge tone={h.type === "Collective" ? "info" : "neutral"}>{h.type}</Badge>}
      />
      <Card>
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {h.legalEntityId}
          </Detail>
          <Detail term="Nodes">{h.nodeCount}</Detail>
          <Detail term="Created">{formatDateTime(h.createdAt)}</Detail>
        </dl>
      </Card>
      <div className={styles.toolbar}>
        <FormField id="hierarchy-period" label="Period" hint="YYYY-MM, for the balances, collections and distributions below.">
          {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} />}
        </FormField>
      </div>
      {objects.error ? <LoadError title="Could not load cost objects" error={objects.error} onRetry={objects.reload} /> : null}
      {balances.error ? <LoadError title="Could not load node balances" error={balances.error} onRetry={balances.reload} /> : null}
      <DataTable
        label="Nodes"
        columns={treeColumns}
        rows={nodeTree(nodes)}
        getRowKey={(r) => r.node.id}
        empty={{ title: "No nodes yet", description: "Add the top cost object as a root node, then its children under it." }}
      />
      <HierarchyActions
        hierarchy={h}
        objects={objectList}
        elements={elements.data?.items ?? []}
        balances={balanceRows}
        period={validPeriod}
        onChanged={() => setReload((n) => n + 1)}
        onDistributed={(run) => navigate(`/costacct/hierarchy-distributions/${run.id}`)}
      />
      {runs.error ? (
        <LoadError title="Could not load distribution runs" error={runs.error} onRetry={runs.reload} />
      ) : (
        <DataTable
          label="Distribution runs"
          columns={runColumns}
          rows={runs.data?.items ?? []}
          getRowKey={(r) => r.id}
          loading={runs.loading && !runs.data}
          empty={{ title: "No distribution runs yet", description: "Distribute a collecting node once its period's cost is collected." }}
          onRowOpen={(r) => navigate(`/costacct/hierarchy-distributions/${r.id}`)}
        />
      )}
    </div>
  );
}

/** One distribution run: what the node held, each child's share and the residual left on the node. */
export function DistributionRunDetailPage() {
  const { id = "" } = useParams();
  const run = useLoad((signal) => getDistributionRun(id, signal), id);
  const hierarchyId = run.data?.hierarchyId ?? "";
  const le = run.data?.legalEntityId ?? "";
  const hierarchy = useLoad((signal) => (hierarchyId ? getHierarchy(hierarchyId, signal) : Promise.resolve(null)), hierarchyId);
  const objects = useLoad((signal) => (le ? listCostObjects({ legalEntityId: le }, signal) : Promise.resolve({ items: [] })), le);
  const elements = useLoad((signal) => (le ? listCostElements(le, signal) : Promise.resolve({ items: [] })), le);
  const code = (oid: string) => (objects.data?.items ?? []).find((o) => o.id === oid)?.code ?? oid;

  if (run.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Distribution run" />
        <LoadError title="Could not load this distribution run" error={run.error} onRetry={run.reload} />
      </div>
    );
  }
  if (!run.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Distribution run" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const r = run.data;
  const node = hierarchy.data?.nodes?.find((n) => n.id === r.nodeId);
  const element = (elements.data?.items ?? []).find((e) => e.id === r.costElementId);
  const kept = r.warnings.length > 0;
  const shareColumns: DataTableColumn<DistributionRun["shares"][number]>[] = [
    { key: "costObjectId", header: "Child", mono: true, render: (s) => <Link to={`/costacct/cost-objects/${s.costObjectId}`}>{code(s.costObjectId)}</Link> },
    { key: "basis", header: r.basis === "FixedPercent" ? "Percent" : "Basis", align: "end", mono: true },
    { key: "amount", header: `Share (${r.currency})`, align: "end", mono: true, render: (s) => formatAmount(s.amount) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Distribution ${r.period}${node ? ` from ${code(node.costObjectId)}` : ""}`}
        description="The node was credited with the distributed total and each child debited its share on the same cost element, so the run nets to zero."
        meta={<Badge tone={kept ? "warning" : "success"}>{kept ? "Residual kept on the node" : "Fully distributed"}</Badge>}
      />
      <StatGroup label="Run totals">
        <StatTile label="Held by the node" value={formatAmount(r.amount)} />
        <StatTile label="Distributed" value={formatAmount(r.distributed)} />
        <StatTile label="Residual" value={formatAmount(r.residual)} tone="warning" />
      </StatGroup>
      {r.warnings.map((w) => (
        <p key={w.code} className={styles.note}>
          {w.code}: {w.detail}. Review the children's basis; a later run can distribute what is left.
        </p>
      ))}
      <Card>
        <dl className={styles.details}>
          <Detail term="Hierarchy" mono>
            <Link to={`/costacct/hierarchies/${r.hierarchyId}`}>{hierarchy.data?.code ?? r.hierarchyId}</Link>
          </Detail>
          <Detail term="Cost element" mono>
            {element ? `${element.code} ${element.name}` : r.costElementId}
          </Detail>
          <Detail term="Basis">{BASIS_LABEL[r.basis] ?? r.basis}</Detail>
          <Detail term="Currency" mono>
            {r.currency}
          </Detail>
          <Detail term="Run at">{formatDateTime(r.createdAt)}</Detail>
        </dl>
      </Card>
      <DataTable
        label="Shares"
        columns={shareColumns}
        rows={r.shares}
        getRowKey={(s) => s.nodeId}
        empty={{ title: "No child received cost", description: "Every basis was zero, so the whole amount stayed on the node." }}
      />
    </div>
  );
}
