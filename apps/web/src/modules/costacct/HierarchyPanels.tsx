import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Select } from "../../shell/ui";
import {
  collectAtNode,
  createHierarchyNode,
  describeError,
  runHierarchyDistribution,
  type CostElement,
  type CostObject,
  type DistributionBasis,
  type DistributionRun,
  type Hierarchy,
  type NodeBalance,
} from "./api/client";
import { collectError, CURRENCY, formatAmount, previewHierarchy, UUID, validateDistribution } from "./logic";
import styles from "./pages.module.css";
import { dash, ErrorList } from "./parts";

const BASES: { value: DistributionBasis; label: string }[] = [
  { value: "ActualOutput", label: "Actual output" },
  { value: "StandardCost", label: "Standard cost of output" },
  { value: "FixedPercent", label: "Fixed percentage" },
];

interface ActionProps {
  hierarchy: Hierarchy;
  objects: CostObject[];
  elements: CostElement[];
  balances: NodeBalance[];
  /** The page's period, or "" when it is not a valid YYYY-MM. */
  period: string;
  onChanged(): void;
  onDistributed(run: DistributionRun): void;
}

/** A fresh Idempotency-Key per distinct body, reused when the same body is retried. */
function useKey() {
  const key = useRef({ body: "", value: crypto.randomUUID() });
  return {
    for(body: unknown): string {
      const text = JSON.stringify(body);
      if (key.current.body !== text) key.current = { body: text, value: crypto.randomUUID() };
      return key.current.value;
    },
    reset() {
      key.current = { body: "", value: crypto.randomUUID() };
    },
  };
}

/** Add a node, collect cost at a collecting node and distribute a node to its children (FR-38.18). */
export function HierarchyActions(props: ActionProps) {
  const collective = props.hierarchy.type === "Collective";
  const collecting = (props.hierarchy.nodes ?? []).filter((n) => n.collects);
  return (
    <div className={styles.split}>
      <AddNodeCard {...props} />
      {collective ? (
        <>
          <CollectCard {...props} />
          <DistributeCard {...props} />
        </>
      ) : (
        <p className={styles.note}>A reporting hierarchy only groups cost objects; create a collective hierarchy to collect and distribute cost.</p>
      )}
      {collective && collecting.length === 0 ? <p className={styles.note}>Add a collecting node to collect and distribute cost.</p> : null}
    </div>
  );
}

function objectLabel(objects: CostObject[], id: string): string {
  const o = objects.find((x) => x.id === id);
  return o ? `${o.code} ${o.name}` : id;
}

function AddNodeCard({ hierarchy, objects, onChanged }: ActionProps) {
  const nodes = hierarchy.nodes ?? [];
  const placed = new Set(nodes.map((n) => n.costObjectId));
  const free = objects.filter((o) => !placed.has(o.id));
  const [form, setForm] = useState({ parentNodeId: "", costObjectId: "", collects: false, distributionBasis: "" as DistributionBasis | "" });
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useKey();

  async function submit(e: FormEvent) {
    e.preventDefault();
    const found: string[] = [];
    if (!UUID.test(form.costObjectId)) found.push("Choose the cost object for the node.");
    if (form.collects && !form.distributionBasis) found.push("Choose how the collecting node distributes its cost.");
    setErrors(found);
    setFailure(null);
    if (found.length > 0) return;
    const input = {
      parentNodeId: form.parentNodeId || undefined,
      costObjectId: form.costObjectId,
      collects: form.collects,
      distributionBasis: form.collects ? (form.distributionBasis as DistributionBasis) : undefined,
    };
    setBusy(true);
    try {
      await createHierarchyNode(hierarchy.id, input, key.for(input));
      key.reset();
      setForm({ parentNodeId: form.parentNodeId, costObjectId: "", collects: false, distributionBasis: "" });
      onChanged();
    } catch (err) {
      setFailure(describeError(err, "The node could not be added."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Add a node" headingLevel={2}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="node-parent" label="Parent node" hint="Leave empty for a root node.">
          {(c) => (
            <Select {...c} value={form.parentNodeId} onChange={(e) => setForm((f) => ({ ...f, parentNodeId: e.target.value }))}>
              <option value="">No parent (root)</option>
              {nodes.map((n) => (
                <option key={n.id} value={n.id}>
                  {objectLabel(objects, n.costObjectId)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="node-object" label="Cost object" hint="Each cost object appears once in a hierarchy.">
          {(c) => (
            <Select {...c} value={form.costObjectId} onChange={(e) => setForm((f) => ({ ...f, costObjectId: e.target.value }))}>
              <option value="">Choose a cost object</option>
              {free.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.code} {o.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {hierarchy.type === "Collective" ? (
          <>
            <label className={styles.check}>
              <Input type="checkbox" aria-label="Collects cost that no lower object can carry" checked={form.collects} onChange={(e) => setForm((f) => ({ ...f, collects: e.target.checked, distributionBasis: e.target.checked ? f.distributionBasis : "" }))} />
              Collects cost that no lower object can carry
            </label>
            {form.collects ? (
              <FormField id="node-basis" label="Distribution basis">
                {(c) => (
                  <Select {...c} value={form.distributionBasis} onChange={(e) => setForm((f) => ({ ...f, distributionBasis: e.target.value as DistributionBasis }))}>
                    <option value="">Choose a basis</option>
                    {BASES.map((b) => (
                      <option key={b.value} value={b.value}>
                        {b.label}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            ) : null}
          </>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Add node
          </Button>
        </div>
      </form>
      <ErrorList errors={errors} failure={failure} />
    </Card>
  );
}

function CollectCard({ hierarchy, objects, elements, period, onChanged }: ActionProps) {
  const collecting = (hierarchy.nodes ?? []).filter((n) => n.collects);
  const [form, setForm] = useState({ nodeId: "", costElementId: "", currency: "USD", amount: "" });
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useKey();

  async function submit(e: FormEvent) {
    e.preventDefault();
    const input = { period, currency: form.currency.trim(), costElementId: form.costElementId, amount: form.amount.trim() };
    const found: string[] = [];
    if (!period) found.push("Enter the period above as YYYY-MM.");
    if (!UUID.test(form.nodeId)) found.push("Choose the collecting node.");
    if (!UUID.test(input.costElementId)) found.push("Choose the cost element.");
    if (!CURRENCY.test(input.currency)) found.push("Use a three-letter currency code.");
    const amountError = collectError(input.amount);
    if (amountError) found.push(amountError);
    setErrors(found);
    setFailure(null);
    setDone(null);
    if (found.length > 0) return;
    setBusy(true);
    try {
      const c = await collectAtNode(form.nodeId, input, key.for({ node: form.nodeId, ...input }));
      key.reset();
      setDone(`Collected ${formatAmount(c.amount)} ${c.currency}; the node now holds ${formatAmount(c.balance)} ${c.currency} on this element in ${c.period}.`);
      setForm((f) => ({ ...f, amount: "" }));
      onChanged();
    } catch (err) {
      setFailure(describeError(err, "The cost could not be collected."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Collect cost at a node" headingLevel={2}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="collect-node" label="Collecting node">
          {(c) => (
            <Select {...c} value={form.nodeId} onChange={(e) => setForm((f) => ({ ...f, nodeId: e.target.value }))}>
              <option value="">Choose a collecting node</option>
              {collecting.map((n) => (
                <option key={n.id} value={n.id}>
                  {objectLabel(objects, n.costObjectId)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="collect-element" label="Cost element">
          {(c) => (
            <Select {...c} value={form.costElementId} onChange={(e) => setForm((f) => ({ ...f, costElementId: e.target.value }))}>
              <option value="">Choose an element</option>
              {elements.map((el) => (
                <option key={el.id} value={el.id}>
                  {el.code} {el.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="collect-currency" label="Currency">
          {(c) => <Input {...c} className={styles.mono} value={form.currency} onChange={(e) => setForm((f) => ({ ...f, currency: e.target.value.toUpperCase() }))} />}
        </FormField>
        <FormField id="collect-amount" label="Amount to collect" hint="A negative amount corrects an earlier collection.">
          {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={form.amount} onChange={(e) => setForm((f) => ({ ...f, amount: e.target.value }))} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Collect
          </Button>
        </div>
      </form>
      <ErrorList errors={errors} failure={failure} />
      <Alert tone="success" live="status">
        {done}
      </Alert>
    </Card>
  );
}

function DistributeCard({ hierarchy, objects, elements, balances, period, onDistributed }: ActionProps) {
  const nodes = hierarchy.nodes ?? [];
  const collecting = nodes.filter((n) => n.collects);
  const [form, setForm] = useState({ nodeId: "", costElementId: "", currency: "USD" });
  const [basis, setBasis] = useState<Record<string, string>>({});
  const [selected, setSelected] = useState<string[]>([]);
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useKey();

  const node = collecting.find((n) => n.id === form.nodeId);
  const kids = nodes.filter((n) => n.parentNodeId === form.nodeId);
  const children = selected.filter((id) => kids.some((k) => k.id === id)).map((id) => ({ id, basis: (basis[id] ?? "").trim() }));
  const held = balances.find((b) => b.nodeId === form.nodeId && b.costElementId === form.costElementId && b.currency === form.currency.trim());
  const preview = node && held ? previewHierarchy(held.amount, node.distributionBasis ?? "", children) : null;
  const percent = node?.distributionBasis === "FixedPercent";
  const childLabel = (id: string) => objectLabel(objects, nodes.find((n) => n.id === id)?.costObjectId ?? id);

  function toggle(id: string) {
    setSelected((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const found: string[] = node ? [] : ["Choose the collecting node to distribute."];
    found.push(...validateDistribution({ period, currency: form.currency.trim(), costElementId: form.costElementId, basis: node?.distributionBasis ?? "" }, children));
    setErrors(found);
    setFailure(null);
    if (found.length > 0 || !node) return;
    const input = { nodeId: node.id, period, currency: form.currency.trim(), costElementId: form.costElementId, children: children.map((c) => ({ nodeId: c.id, basis: c.basis })) };
    setBusy(true);
    try {
      const run = await runHierarchyDistribution(input, key.for(input));
      key.reset();
      onDistributed(run);
    } catch (err) {
      setFailure(describeError(err, "The distribution could not be run."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Distribute a node" headingLevel={2}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="distribute-node" label="Node to distribute">
          {(c) => (
            <Select
              {...c}
              value={form.nodeId}
              onChange={(e) => {
                setForm((f) => ({ ...f, nodeId: e.target.value }));
                setSelected([]);
              }}
            >
              <option value="">Choose a collecting node</option>
              {collecting.map((n) => (
                <option key={n.id} value={n.id}>
                  {objectLabel(objects, n.costObjectId)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="distribute-element" label="Cost element">
          {(c) => (
            <Select {...c} value={form.costElementId} onChange={(e) => setForm((f) => ({ ...f, costElementId: e.target.value }))}>
              <option value="">Choose an element</option>
              {elements.map((el) => (
                <option key={el.id} value={el.id}>
                  {el.code} {el.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="distribute-currency" label="Currency">
          {(c) => <Input {...c} className={styles.mono} value={form.currency} onChange={(e) => setForm((f) => ({ ...f, currency: e.target.value.toUpperCase() }))} />}
        </FormField>
        <p className={styles.note}>
          {node
            ? `Held in ${period || "the period"}: ${held ? `${formatAmount(held.amount)} ${held.currency}` : "nothing for this element and currency"}. The whole amount is split ${percent ? "by the percentage of each child; what is not assigned stays on the node" : "in proportion to each child's basis"}.`
            : "Choose a collecting node to see its children."}
        </p>
        {node ? (
          <fieldset className={styles.lineSet}>
            <legend className={styles.legend}>Children</legend>
            {kids.length === 0 ? (
              <p className={styles.note}>This node has no children yet; add nodes under it first.</p>
            ) : (
              <table className={styles.matrix} aria-label="Distribution basis">
                <thead>
                  <tr>
                    <th scope="col">Child</th>
                    <th scope="col">{percent ? "Percent" : "Basis"}</th>
                    <th scope="col">Preview share</th>
                  </tr>
                </thead>
                <tbody>
                  {kids.map((k) => {
                    const i = children.findIndex((c) => c.id === k.id);
                    return (
                      <tr key={k.id}>
                        <th scope="row">
                          <label className={styles.check}>
                            <Input type="checkbox" aria-label={`Include ${childLabel(k.id)}`} checked={selected.includes(k.id)} onChange={() => toggle(k.id)} />
                            {childLabel(k.id)}
                          </label>
                        </th>
                        <td>
                          <Input
                            className={styles.matrixInput}
                            inputMode="decimal"
                            aria-label={`${percent ? "Percent" : "Basis"} of ${childLabel(k.id)}`}
                            value={basis[k.id] ?? ""}
                            onChange={(e) => setBasis((b) => ({ ...b, [k.id]: e.target.value }))}
                          />
                        </td>
                        <td className={styles.mono}>{preview && i >= 0 ? preview.shares[i] : dash}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </fieldset>
        ) : null}
        {preview ? <p className={styles.note}>Stays on the node: {preview.residual}</p> : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Run distribution
          </Button>
        </div>
      </form>
      <ErrorList errors={errors} failure={failure} />
    </Card>
  );
}
