import { useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  type DataTableColumn,
} from "../../shell/ui";
import {
  addLineageEdges,
  ASSET_FQN,
  describeProblem,
  getLineage,
  MAX_LINEAGE_DEPTH,
  type Lineage,
  type LineageAsset,
  type Problem,
} from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const assetColumns: DataTableColumn<LineageAsset>[] = [
  { key: "fqn", header: "Asset", mono: true },
  { key: "distance", header: "Hops", align: "end" },
];

type SearchErrors = Partial<Record<"asset" | "depth", string>>;

/** Checks that mirror GET /catalog/lineage/{asset_fqn}?depth=. */
export function validateLineageSearch(asset: string, depth: string): SearchErrors {
  const errors: SearchErrors = {};
  if (!ASSET_FQN.test(asset)) errors.asset = "Use a dotted lowercase asset name such as mart.fin_journal_line.";
  if (depth !== "" && (!/^\d+$/.test(depth) || Number(depth) > MAX_LINEAGE_DEPTH)) errors.depth = `Enter 0 to ${MAX_LINEAGE_DEPTH} hops.`;
  return errors;
}

type EdgeErrors = Partial<Record<"source" | "target", string>>;

/** Checks that mirror domain.ValidateEdges for one edge. */
export function validateEdge(source: string, target: string): EdgeErrors {
  const errors: EdgeErrors = {};
  if (!ASSET_FQN.test(source)) errors.source = "Use a dotted lowercase asset name such as raw.ledger.journal_line.";
  if (!ASSET_FQN.test(target)) errors.target = "Use a dotted lowercase asset name such as core.journal_line.";
  else if (source === target) errors.target = "An asset cannot feed itself.";
  return errors;
}

function LineageResult({ asset, depth, version, onOpen }: { asset: string; depth: number; version: number; onOpen(fqn: string): void }) {
  const state = useLoad((signal) => getLineage(asset, depth, undefined, signal), `lineage/${asset}/${depth}/${version}`);
  if (state.error) {
    return (
      <Alert tone="danger" title="Could not load lineage" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        <ProblemMessage problem={state.error} />
      </Alert>
    );
  }
  const data: Lineage | null = state.data;
  return (
    <div className={styles.stack}>
      <DataTable
        label={`Upstream of ${asset}`}
        columns={assetColumns}
        rows={data?.upstream ?? []}
        getRowKey={(a) => a.fqn}
        loading={state.loading}
        empty={{ title: "No upstream assets", description: `Nothing that feeds ${asset} is recorded.` }}
        onRowOpen={(a) => onOpen(a.fqn)}
      />
      <DataTable
        label={`Downstream of ${asset}`}
        columns={assetColumns}
        rows={data?.downstream ?? []}
        getRowKey={(a) => a.fqn}
        loading={state.loading}
        empty={{ title: "No downstream assets", description: `Nothing derived from ${asset} is recorded.` }}
        onRowOpen={(a) => onOpen(a.fqn)}
      />
    </div>
  );
}

function EdgeForm({ onAdded }: { onAdded(): void }) {
  const [source, setSource] = useState("");
  const [target, setTarget] = useState("");
  const [errors, setErrors] = useState<EdgeErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateEdge(source, target);
    setErrors(found);
    setFailure(null);
    setResult("");
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const added = await addLineageEdges([{ source, target }]);
      setResult(added > 0 ? `Recorded: ${source} feeds ${target}.` : `Already recorded: ${source} feeds ${target}.`);
      onAdded();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.fields["edges[0]"]) setErrors({ target: problem.fields["edges[0]"] });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Record a lineage edge">
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Lineage edge">
        <FormField id="edge-source" label="Source asset" hint="The asset that feeds the target." error={errors.source}>
          {(control) => <Input {...control} className={styles.mono} invalid={Boolean(errors.source)} value={source} onChange={(e) => setSource(e.target.value)} />}
        </FormField>
        <FormField id="edge-target" label="Target asset" hint="The asset derived from the source." error={errors.target}>
          {(control) => <Input {...control} className={styles.mono} invalid={Boolean(errors.target)} value={target} onChange={(e) => setTarget(e.target.value)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The edge was not recorded" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Record edge
          </Button>
        </div>
      </form>
      <p role="status" className={styles.muted}>
        {result}
      </p>
    </Card>
  );
}

/**
 * Lineage view (FR-46.12, PRD §9.46.14): the assets upstream and downstream of
 * a catalog asset, nearest first. Stewards record edges the catalog sync has
 * not delivered. The asset and depth live in the URL so a view can be shared.
 */
export function LineagePage() {
  const [params, setParams] = useSearchParams();
  const asset = params.get("asset") ?? "";
  const depthParam = params.get("depth") ?? "";
  const [draft, setDraft] = useState({ asset, depth: depthParam });
  const [errors, setErrors] = useState<SearchErrors>({});
  const [version, setVersion] = useState(0);
  const shown = asset !== "" && Object.keys(validateLineageSearch(asset, depthParam)).length === 0;

  function open(fqn: string, depth = depthParam) {
    setDraft({ asset: fqn, depth });
    setParams(depth === "" ? { asset: fqn } : { asset: fqn, depth });
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateLineageSearch(draft.asset.trim(), draft.depth.trim());
    setErrors(found);
    if (Object.keys(found).length === 0) open(draft.asset.trim(), draft.depth.trim());
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Lineage" description="Which assets feed a table, model, metric or dashboard, and which assets are derived from it." />
      <Card>
        <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Find lineage">
          <FormField id="lineage-asset" label="Asset" error={errors.asset}>
            {(control) => (
              <Input {...control} className={styles.mono} invalid={Boolean(errors.asset)} value={draft.asset} onChange={(e) => setDraft((d) => ({ ...d, asset: e.target.value }))} />
            )}
          </FormField>
          <FormField id="lineage-depth" label="Depth (hops)" optional hint={`Up to ${MAX_LINEAGE_DEPTH}; empty for all.`} error={errors.depth}>
            {(control) => (
              <Input {...control} inputMode="numeric" invalid={Boolean(errors.depth)} value={draft.depth} onChange={(e) => setDraft((d) => ({ ...d, depth: e.target.value }))} />
            )}
          </FormField>
          <Button type="submit" variant="primary">
            Show lineage
          </Button>
        </form>
      </Card>
      {shown ? (
        <LineageResult asset={asset} depth={depthParam === "" ? 0 : Number(depthParam)} version={version} onOpen={(fqn) => open(fqn)} />
      ) : (
        <Card>
          <EmptyState icon="database" title="Choose an asset" description="Enter a catalog asset such as mart.fin_journal_line to see what feeds it and what it feeds." />
        </Card>
      )}
      <EdgeForm onAdded={() => setVersion((v) => v + 1)} />
    </div>
  );
}
