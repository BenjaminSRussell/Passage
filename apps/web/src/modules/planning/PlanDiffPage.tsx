import { useSearchParams } from "react-router-dom";
import { Alert, Badge, Card, DataTable, EmptyState, FormField, PageHeader, Select, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { diffVersions, listVersions, type DiffOrder, type VersionDiff } from "./api/client";
import { PlanPicker } from "./pages";
import { useLoad, usePlanCode, type LoadError } from "./hooks";
import { trimQty } from "./NetRequirementsPage";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const dash = "—";

/** Orders listed per table; the counts always cover every order. */
const diffLimit = 500;

function LoadAlert({ what, error }: { what: string; error: LoadError | null }) {
  return (
    <Alert tone="danger" title={error ? `Could not load ${what}` : undefined} live="alert">
      {error ? <ProblemMessage problem={error} /> : null}
    </Alert>
  );
}

const orderColumns: DataTableColumn<DiffOrder>[] = [
  { key: "number", header: "Number", mono: true },
  { key: "itemId", header: "Item", mono: true, render: (o) => <EntityName entity="product" id={o.itemId} fallback={o.itemId} /> },
  { key: "warehouseId", header: "Scope", mono: true, render: (o) => (o.warehouseId ? <EntityName entity="warehouse" id={o.warehouseId} fallback={o.warehouseId} /> : "Site") },
  { key: "orderType", header: "Type" },
  { key: "qty", header: "Quantity", align: "end", render: (o) => trimQty(o.qty) },
  { key: "orderDate", header: "Order date" },
  { key: "deliveryDate", header: "Delivery date" },
  { key: "vendorId", header: "Vendor", mono: true, render: (o) => (o.vendorId ? <EntityName entity="vendor" id={o.vendorId} fallback={o.vendorId} /> : dash) },
];

/** "a → b" when the value moved between the versions, otherwise the value. */
const moved = (from: string, to: string) => (from === to ? to : `${from} → ${to}`);

type ChangedRow = VersionDiff["changed"][number] & { key: string };

const changedColumns: DataTableColumn<ChangedRow>[] = [
  { key: "item", header: "Item", mono: true, render: (c) => <EntityName entity="product" id={c.to.itemId} fallback={c.to.itemId} /> },
  { key: "type", header: "Type", render: (c) => c.to.orderType },
  { key: "delivery", header: "Delivery date", render: (c) => c.to.deliveryDate },
  { key: "qty", header: "Quantity", align: "end", render: (c) => `${trimQty(c.from.qty)} → ${trimQty(c.to.qty)}` },
  { key: "delta", header: "Change", align: "end", render: (c) => (trimQty(c.qtyDelta) === "0" ? dash : signed(c.qtyDelta)) },
  { key: "orderDate", header: "Order date", render: (c) => moved(c.from.orderDate, c.to.orderDate) },
  {
    key: "vendor",
    header: "Vendor",
    render: (c) => {
      const vendor = (id?: string | null) => (id ? <EntityName entity="vendor" id={id} fallback={id} /> : dash);
      return c.from.vendorId === c.to.vendorId ? (
        vendor(c.to.vendorId)
      ) : (
        <>
          {vendor(c.from.vendorId)} → {vendor(c.to.vendorId)}
        </>
      );
    },
  },
];

/** "+20" or "-10" from a decimal string, without going through a float. */
export function signed(qty: string): string {
  const t = trimQty(qty);
  return t.startsWith("-") || t === "0" ? t : `+${t}`;
}

function DiffView({ diff }: { diff: VersionDiff }) {
  const none = diff.addedCount + diff.removedCount + diff.changedCount === 0;
  return (
    <>
      <StatGroup label="Differences">
        <StatTile label="Added" value={diff.addedCount} />
        <StatTile label="Removed" value={diff.removedCount} />
        <StatTile label="Changed" value={diff.changedCount} />
        <StatTile label="Unchanged" value={diff.unchanged} />
        <StatTile label="Messages" value={signed(String(diff.messagesDelta))} />
      </StatGroup>
      <p className={styles.summary}>
        {diff.identical ? <Badge tone="success">Identical plan hash</Badge> : <Badge tone="warning">Plan hashes differ</Badge>}{" "}
        {Object.entries(diff.qtyDeltaByOrderType)
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([t, q]) => `${t} ${signed(q)}`)
          .join(", ")}
      </p>
      {diff.truncated ? (
        <Alert tone="info" title="Showing the first orders of each list">
          The counts and quantity changes above cover every planned order; each table below lists the first {diffLimit}.
        </Alert>
      ) : null}
      {none ? (
        <Card>
          <EmptyState compact icon="grid" title="No planned-order differences" description="Both versions plan the same orders for the same quantities and dates." />
        </Card>
      ) : (
        <>
          <Card title="Added in the later version">
            <DataTable label="Added orders" columns={orderColumns} rows={diff.added} getRowKey={(o) => o.number} empty={{ title: "None added", description: "" }} />
          </Card>
          <Card title="Removed from the earlier version">
            <DataTable label="Removed orders" columns={orderColumns} rows={diff.removed} getRowKey={(o) => o.number} empty={{ title: "None removed", description: "" }} />
          </Card>
          <Card title="Changed">
            <DataTable
              label="Changed orders"
              columns={changedColumns}
              rows={diff.changed.map((c) => ({ ...c, key: c.from.number + c.to.number }))}
              getRowKey={(c) => c.key}
              empty={{ title: "None changed", description: "" }}
            />
          </Card>
        </>
      )}
    </>
  );
}

/**
 * Compare two versions of a plan (FR-22.14): which planned orders were added,
 * removed or changed between runs. The two versions are kept in `?from=` and
 * `?to=`; the later one defaults to the newest run and the earlier one to the
 * current operational version (or the run before the later one when that is
 * the current version), so a simulation is compared with the operational plan.
 */
export function PlanDiffPage() {
  const [plan] = usePlanCode();
  const [params, setParams] = useSearchParams();
  const versions = useLoad((signal) => listVersions(plan, 50, { signal }), [plan]);
  const numbers = (versions.data ?? []).map((v) => v.number).sort((a, b) => a - b);
  const pick = (name: string, fallback: number) => {
    const n = Number(params.get(name));
    return numbers.includes(n) ? n : fallback;
  };
  const to = pick("to", numbers[numbers.length - 1] ?? 0);
  // A simulation (FR-22.15) is compared with the operational plan by default:
  // the earlier version is the current one unless that is the later version.
  const current = (versions.data ?? []).find((v) => v.status === "current")?.number;
  const from = pick("from", current !== undefined && current !== to ? current : (numbers.filter((n) => n !== to).pop() ?? 0));
  const ready = numbers.length >= 2 && from > 0 && to > 0 && from !== to;
  const diff = useLoad(
    (signal) => (ready ? diffVersions(plan, Math.min(from, to), Math.max(from, to), diffLimit, { signal }) : Promise.resolve(null)),
    [plan, ready, from, to],
  );
  const set = (name: string) => (value: string) => {
    const next = new URLSearchParams(params);
    next.set(name, value);
    setParams(next);
  };
  const byNumber = new Map((versions.data ?? []).map((v) => [v.number, v]));
  const optionLabel = (n: number) => {
    const v = byNumber.get(n);
    if (v?.kind === "simulation") return `Version ${n} (simulation${v.scenarioCode ? ` ${v.scenarioCode}` : ""})`;
    return v?.status === "current" ? `Version ${n} (current)` : `Version ${n}`;
  };
  const options = numbers.map((n) => (
    <option key={n} value={n}>
      {optionLabel(n)}
    </option>
  ));
  return (
    <div className={styles.stack}>
      <PageHeader title="Compare plan versions" description={`Planned orders added, removed or changed between two runs of plan ${plan}.`} />
      <PlanPicker />
      <LoadAlert what="plan versions" error={versions.error} />
      {versions.error ? null : versions.loading ? (
        <div aria-busy="true">
          <Skeleton width="1/2" />
          <span className="visually-hidden">Loading…</span>
        </div>
      ) : numbers.length < 2 ? (
        <Card>
          <EmptyState compact icon="grid" title="Nothing to compare yet" description="A plan needs at least two runs before its versions can be compared." />
        </Card>
      ) : (
        <>
          <div className={styles.toolbar}>
            <div className={styles.toolbarField}>
              <FormField id="diff-from" label="Earlier version">
                {(a11y) => (
                  <Select {...a11y} value={from} onChange={(e) => set("from")(e.target.value)}>
                    {options}
                  </Select>
                )}
              </FormField>
            </div>
            <div className={styles.toolbarField}>
              <FormField id="diff-to" label="Later version">
                {(a11y) => (
                  <Select {...a11y} value={to} onChange={(e) => set("to")(e.target.value)}>
                    {options}
                  </Select>
                )}
              </FormField>
            </div>
          </div>
          {from === to ? (
            <Alert tone="warning" title="Choose two different versions">Pick an earlier and a later version to compare.</Alert>
          ) : (
            <>
              <LoadAlert what="the comparison" error={diff.error} />
              {diff.error ? null : diff.loading || !diff.data ? <p className={styles.summary}>Comparing versions…</p> : <DiffView diff={diff.data} />}
            </>
          )}
        </>
      )}
    </div>
  );
}
