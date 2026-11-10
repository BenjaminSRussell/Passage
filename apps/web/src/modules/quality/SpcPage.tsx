import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { getSpc, listTestGroups, type SpcLimits, type SpcResult, type Violation } from "./api/client";
import { shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const RULES: Record<number, string> = {
  1: "Rule 1: a point beyond 3σ",
  2: "Rule 2: 2 of 3 beyond 2σ on one side",
  3: "Rule 3: 4 of 5 beyond 1σ on one side",
  4: "Rule 4: 8 in a row on one side",
};

/** Rules flagged per point index for one chart, e.g. {3: [1, 2]}. */
export function rulesByPoint(violations: Violation[], chart: Violation["chart"]): Map<number, number[]> {
  const out = new Map<number, number[]>();
  for (const v of violations) {
    if (v.chart !== chart) continue;
    const rules = out.get(v.index) ?? [];
    if (!rules.includes(v.rule)) rules.push(v.rule);
    out.set(v.index, rules.sort());
  }
  return out;
}

function fmt(n: number): string {
  return Number.isFinite(n) ? String(Math.round(n * 10000) / 10000) : "—";
}

const W = 640;
const H = 200;
const PAD = 24;

/**
 * A control chart as inline SVG: the series, its centre line and control limits
 * (one set, or one per point for a p chart), with flagged points marked. The
 * figure is described in words for screen readers; the table below carries the data.
 */
export function ControlChart({ label, points, limits, flagged }: { label: string; points: number[]; limits: SpcLimits | SpcLimits[]; flagged: Map<number, number[]> }) {
  const per = Array.isArray(limits) ? limits : points.map(() => limits);
  const values = [...points, ...per.flatMap((l) => [l.ucl, l.lcl, l.cl])].filter(Number.isFinite);
  const lo = Math.min(...values);
  const hi = Math.max(...values);
  const span = hi - lo || 1;
  const x = (i: number) => PAD + (points.length <= 1 ? (W - 2 * PAD) / 2 : (i * (W - 2 * PAD)) / (points.length - 1));
  const y = (v: number) => H - PAD - ((v - lo) / span) * (H - 2 * PAD);
  const line = (pick: (l: SpcLimits) => number) => per.map((l, i) => `${x(i)},${y(pick(l))}`).join(" ");
  const summary = `${label}: ${points.length} points, centre ${fmt(per[per.length - 1]?.cl ?? NaN)}, ${flagged.size} flagged.`;
  return (
    <figure className={styles.chart}>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={summary} className={styles.chartSvg}>
        <polyline className={styles.chartLimit} points={line((l) => l.ucl)} />
        <polyline className={styles.chartLimit} points={line((l) => l.lcl)} />
        <polyline className={styles.chartCentre} points={line((l) => l.cl)} />
        <polyline className={styles.chartSeries} points={points.map((v, i) => `${x(i)},${y(v)}`).join(" ")} />
        {points.map((v, i) => (
          <circle key={i} cx={x(i)} cy={y(v)} r={flagged.has(i) ? 5 : 3} className={flagged.has(i) ? styles.chartFlag : styles.chartPoint} />
        ))}
      </svg>
      <figcaption className={styles.note}>{summary}</figcaption>
    </figure>
  );
}

interface PointRow {
  index: number;
  orderId: string;
  value: number;
  second?: number;
  rules: number[];
}

function pointColumns(valueHeader: string, secondHeader?: string): DataTableColumn<PointRow>[] {
  const cols: DataTableColumn<PointRow>[] = [
    { key: "index", header: "Point", align: "end", render: (row) => String(row.index + 1) },
    { key: "orderId", header: "Quality order", mono: true, render: (row) => <Link to={`/quality/orders/${row.orderId}`} aria-label={`Open the order of point ${row.index + 1}`}>{shortId(row.orderId)}</Link> },
    { key: "value", header: valueHeader, align: "end", mono: true, render: (row) => fmt(row.value) },
  ];
  if (secondHeader) cols.push({ key: "second", header: secondHeader, align: "end", mono: true, render: (row) => (row.second === undefined ? "—" : fmt(row.second)) });
  cols.push({
    key: "rules",
    header: "Rule violations",
    render: (row) =>
      row.rules.length === 0 ? (
        "—"
      ) : (
        <span className={styles.flags}>
          {row.rules.map((r) => (
            <Badge key={r} tone="danger">
              {RULES[r] ?? `Rule ${r}`}
            </Badge>
          ))}
        </span>
      ),
  });
  return cols;
}

function SpcChart({ result }: { result: SpcResult }) {
  if (result.chart === "NONE") {
    return (
      <Card>
        <EmptyState title="No results yet for this item and test" description="Charts appear once quality orders for the item have first-attempt results for the test." />
      </Card>
    );
  }
  if (result.chart === "XBAR_R" && result.xbarR) {
    const c = result.xbarR;
    const xFlags = rulesByPoint(c.violations, "XBAR");
    const rFlags = rulesByPoint(c.violations, "R");
    const rows: PointRow[] = c.xbar.map((v, i) => ({
      index: i,
      orderId: result.orderIds[i] ?? "",
      value: v,
      second: c.range[i],
      rules: [...new Set([...(xFlags.get(i) ?? []), ...(rFlags.get(i) ?? [])])].sort(),
    }));
    return (
      <>
        <StatGroup label="Chart summary">
          <StatTile label="Subgroups" value={c.xbar.length} />
          <StatTile label="Subgroup size" value={c.subgroupSize} />
          <StatTile label="Points flagged" value={rows.filter((r) => r.rules.length > 0).length} tone="danger" />
          <StatTile label="Orders left out" value={result.skippedSubgroups} tone="warning" hint="Sample size differs from the usual size" />
        </StatGroup>
        <Card title={`X-bar chart (CL ${fmt(c.xbarLimits.cl)}, UCL ${fmt(c.xbarLimits.ucl)}, LCL ${fmt(c.xbarLimits.lcl)})`}>
          <ControlChart label="X-bar chart" points={c.xbar} limits={c.xbarLimits} flagged={xFlags} />
        </Card>
        <Card title={`R chart (CL ${fmt(c.rLimits.cl)}, UCL ${fmt(c.rLimits.ucl)}, LCL ${fmt(c.rLimits.lcl)})`}>
          <ControlChart label="R chart" points={c.range} limits={c.rLimits} flagged={rFlags} />
        </Card>
        <DataTable label="Subgroups" columns={pointColumns("X-bar", "Range")} rows={rows} getRowKey={(row) => String(row.index)} />
      </>
    );
  }
  const p = result.pChart;
  if (!p) return null;
  const flags = rulesByPoint(p.violations, "P");
  const rows: PointRow[] = p.points.map((v, i) => ({ index: i, orderId: result.orderIds[i] ?? "", value: v, second: p.sizes[i], rules: flags.get(i) ?? [] }));
  return (
    <>
      <StatGroup label="Chart summary">
        <StatTile label="Inspections" value={p.points.length} />
        <StatTile label="Average fraction defective" value={fmt(p.pBar)} />
        <StatTile label="Points flagged" value={flags.size} tone="danger" />
      </StatGroup>
      <Card title={`p chart (p̄ ${fmt(p.pBar)})`}>
        <ControlChart label="p chart" points={p.points} limits={p.limits} flagged={flags} />
      </Card>
      {p.np ? (
        <Card title={`np chart, n = ${p.np.subgroupSize} (CL ${fmt(p.np.limits.cl)}, UCL ${fmt(p.np.limits.ucl)}, LCL ${fmt(p.np.limits.lcl)})`}>
          <ControlChart label="np chart" points={p.np.points} limits={p.np.limits} flagged={flags} />
        </Card>
      ) : null}
      <DataTable label="Inspections" columns={pointColumns("Fraction defective", "Samples")} rows={rows} getRowKey={(row) => String(row.index)} />
    </>
  );
}

/** SPC dashboard (FR-33.11): X-bar/R for a variable test, p (and np) for an attribute test, Western Electric rules 1-4. */
export function SpcPage() {
  const [params, setParams] = useSearchParams();
  const itemId = params.get("itemId") ?? "";
  const testId = params.get("testId") ?? "";
  const groups = useLoad((signal) => listTestGroups(signal), "spc-test-groups");
  const [item, setItem] = useState(itemId);
  const [groupId, setGroupId] = useState("");
  const [test, setTest] = useState(testId);
  const [errors, setErrors] = useState<{ item?: string; test?: string }>({});
  const ready = UUID.test(itemId) && UUID.test(testId);
  const chart = useLoad((signal) => (ready ? getSpc(itemId, testId, signal) : Promise.resolve(null)), `spc|${itemId}|${testId}`);

  const group = (groups.data ?? []).find((g) => g.id === groupId);
  const tests = group ? group.tests : [];

  function show(event: FormEvent) {
    event.preventDefault();
    const found: { item?: string; test?: string } = {};
    if (!UUID.test(item.trim())) found.item = "Enter the item id (a UUID).";
    if (!UUID.test(test.trim())) found.test = "Choose a test group and a test, or enter the test id.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setParams(new URLSearchParams({ itemId: item.trim(), testId: test.trim() }));
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="SPC charts"
        description="Control charts from recorded results: each quality order is one subgroup. Limits come from the last 25 subgroups; Western Electric rules 1 to 4 are flagged."
      />
      <Card>
        <form className={styles.toolbar} onSubmit={show} noValidate aria-label="Choose a chart">
          <FormField id="spc-item" label="Item id" error={errors.item}>
            {(control) => <Input {...control} className={styles.mono} value={item} invalid={Boolean(errors.item)} onChange={(event) => setItem(event.target.value)} />}
          </FormField>
          <FormField id="spc-group" label="Test group" optional hint={groups.error ? "Test groups could not be loaded." : undefined}>
            {(control) => (
              <Select {...control} value={groupId} onChange={(event) => { setGroupId(event.target.value); setTest(""); }}>
                <option value="">{groups.loading ? "Loading test groups…" : "Choose a test group"}</option>
                {(groups.data ?? []).map((g) => (
                  <option key={g.id} value={g.id}>{`${g.code} v${g.version}`}</option>
                ))}
              </Select>
            )}
          </FormField>
          {group ? (
            <FormField id="spc-test" label="Test" error={errors.test}>
              {(control) => (
                <Select {...control} value={test} invalid={Boolean(errors.test)} onChange={(event) => setTest(event.target.value)}>
                  <option value="">Choose a test</option>
                  {tests.map((t) => (
                    <option key={t.testId} value={t.testId}>{`${t.name || shortId(t.testId)} (${t.kind === "VARIABLE" ? "variable" : "attribute"})`}</option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : (
            <FormField id="spc-test" label="Test id" error={errors.test}>
              {(control) => <Input {...control} className={styles.mono} value={test} invalid={Boolean(errors.test)} onChange={(event) => setTest(event.target.value)} />}
            </FormField>
          )}
          <Button type="submit" variant="primary">
            Show chart
          </Button>
        </form>
      </Card>
      {!ready ? (
        <Card>
          <EmptyState title="Choose an item and a test" description="The chart covers the item's newest 50 quality orders that have results for the test." />
        </Card>
      ) : chart.error ? (
        <Alert tone="danger" title="Could not compute the chart" live="alert" action={<Button size="sm" onClick={chart.reload}>Try again</Button>}>
          <ProblemMessage problem={chart.error} />
        </Alert>
      ) : chart.loading || !chart.data ? (
        <div className={styles.stack} aria-busy="true">
          <span className="visually-hidden">Computing the chart…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <SpcChart result={chart.data} />
      )}
    </div>
  );
}
