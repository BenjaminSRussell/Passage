import { useState, type FormEvent } from "react";
import { Badge, Button, Card, DataTable, EmptyState, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { compareRuns, describeProblem, type Comparison, type ComparisonRow, type CostItem, type ExplosionMode, type Problem } from "./api/client";
import { amount, dash } from "./format";
import { parseItems, workedExample } from "./RollPage";
import { ExplosionModeField, ItemsField, ProblemAlert, SplitTable, itemsText } from "./shared";
import styles from "./pages.module.css";

/** What-if candidate: the worked example with RM-301 at 2.75 instead of 2.50. */
export const whatIfCandidate: CostItem[] = workedExample.map((it) => (it.id === "RM-301" ? { ...it, price: "2.75" } : it));

interface Values {
  baseline: string;
  candidate: string;
  mode: ExplosionMode;
  fallbackToActive: boolean;
}

const initial: Values = {
  baseline: itemsText(workedExample),
  candidate: itemsText(whatIfCandidate),
  mode: "Multilevel",
  fallbackToActive: false,
};

type Errors = Record<string, string>;

interface CompareRequest {
  baseline: { items: CostItem[]; options: { mode: ExplosionMode; fallbackToActive: boolean } };
  candidate: { items: CostItem[]; options: { mode: ExplosionMode; fallbackToActive: boolean } };
}

/** Parses both selections; errors are keyed as the service reports them (baseline.items, candidate.items). */
export function validateCompare(values: Values): { errors: Errors; request?: CompareRequest } {
  const errors: Errors = {};
  const baseline = parseItems(values.baseline);
  const candidate = parseItems(values.candidate);
  if (baseline.error) errors["baseline.items"] = baseline.error;
  if (candidate.error) errors["candidate.items"] = candidate.error;
  if (!baseline.items || !candidate.items) return { errors };
  const options = { mode: values.mode, fallbackToActive: values.fallbackToActive };
  return { errors, request: { baseline: { items: baseline.items, options }, candidate: { items: candidate.items, options } } };
}

function RowStatus({ status }: { status: ComparisonRow["status"] }) {
  switch (status) {
    case "Compared":
      return <Badge tone="success">Compared</Badge>;
    case "BaselineOnly":
      return <Badge tone="neutral">Baseline only</Badge>;
    case "CandidateOnly":
      return <Badge tone="info">Candidate only</Badge>;
    default:
      return <Badge tone="danger">Error</Badge>;
  }
}

const columns: DataTableColumn<ComparisonRow>[] = [
  { key: "itemId", header: "Item", mono: true },
  { key: "status", header: "Status", render: (r) => <RowStatus status={r.status} /> },
  { key: "baseline", header: "Baseline", align: "end", render: (r) => (r.status === "CandidateOnly" || r.status === "Error" ? dash : amount(r.baseline)) },
  { key: "candidate", header: "Candidate", align: "end", render: (r) => (r.status === "BaselineOnly" || r.status === "Error" ? dash : amount(r.candidate)) },
  { key: "delta", header: "Change", align: "end", render: (r) => (r.status === "Compared" ? amount(r.delta) : dash) },
  { key: "deltaPct", header: "Change %", align: "end", render: (r) => (r.status === "Compared" ? amount(r.deltaPct) : dash) },
];

/** Compare a baseline selection with a what-if candidate item by item and by component (FR-39.10). */
export function ComparePage() {
  const [values, setValues] = useState<Values>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Comparison | null>(null);
  const [opened, setOpened] = useState<ComparisonRow | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateCompare(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setResult(await compareRuns(request));
      setOpened(null);
    } catch (err) {
      const problem = describeProblem(err);
      setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const changed = result?.rows.filter((r) => r.status === "Compared" && !/^-?0+(\.0+)?$/.test(r.delta)).length ?? 0;
  const failedRows = result?.rows.filter((r) => r.status === "Error").length ?? 0;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Estimate comparison"
        description="Both selections are rolled the same way and compared item by item, with the change per cost component. Change the candidate's prices, rates or routes to simulate a what-if. Comparing stored versions or dates arrives with costing versions."
      />
      <Card title="Baseline and candidate" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <ItemsField
            id="compare-baseline"
            label="Baseline items"
            rows={10}
            value={values.baseline}
            error={errors["baseline.items"]}
            onChange={(baseline) => setValues({ ...values, baseline })}
          />
          <ItemsField
            id="compare-candidate"
            label="Candidate items"
            rows={10}
            value={values.candidate}
            error={errors["candidate.items"]}
            onChange={(candidate) => setValues({ ...values, candidate })}
          />
          <ExplosionModeField id="compare-mode" value={values.mode} onChange={(mode) => setValues({ ...values, mode })} />
          <div className={styles.check}>
            <Input
              id="compare-fallback"
              type="checkbox"
              checked={values.fallbackToActive}
              onChange={(e) => setValues({ ...values, fallbackToActive: e.target.checked })}
            />
            <label htmlFor="compare-fallback">Fall back to the active standard</label>
          </div>
          {failure ? <ProblemAlert title="The comparison was not computed" problem={failure} /> : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Compare
            </Button>
            <Button variant="ghost" onClick={() => setValues(initial)}>
              Load example
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Comparison by item" columns={columns} rows={[]} getRowKey={(r) => r.itemId} loading />
      ) : result ? (
        <>
          <StatGroup label="Comparison">
            <StatTile label="Items" value={result.rows.length} />
            <StatTile label="Changed" value={changed} tone="warning" />
            <StatTile label="Errors" value={failedRows} tone="danger" />
            <StatTile label="Total change" value={amount(result.totalDelta)} />
          </StatGroup>
          <DataTable
            label="Comparison by item"
            columns={columns}
            rows={result.rows}
            getRowKey={(r) => r.itemId}
            onRowOpen={(r: ComparisonRow) => setOpened(r)}
            empty={{ title: "No items compared", description: "Neither selection had items." }}
          />
          {opened ? (
            <Card title={`Change by component of ${opened.itemId}`} headingLevel={3}>
              {opened.status === "Compared" ? (
                <SplitTable label={`Change by component of ${opened.itemId}`} split={opened.byComponent} header="Change" />
              ) : (
                <p className={styles.note}>
                  {opened.status === "Error"
                    ? "The item failed to roll in one of the selections; roll it on the cost roll-up page to see its diagnostics."
                    : "The item is in only one selection, so there is nothing to compare."}
                </p>
              )}
            </Card>
          ) : null}
        </>
      ) : (
        <Card>
          <EmptyState
            icon="grid"
            headingLevel={3}
            title="No comparison yet"
            description="Enter a baseline and a candidate selection to see how each item's standard changes."
          />
        </Card>
      )}
    </div>
  );
}
