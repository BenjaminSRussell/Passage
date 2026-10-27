import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
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
  StatusPill,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  activateVersion,
  copyItemCosts,
  describeError,
  getVersion,
  importItemCostsCsv,
  listItemCosts,
  listVersions,
  previewActivation,
  type ActivationDelta,
  type ActivationResponse,
  type CostingVersion,
  type ItemCostRow,
} from "./api/client";
import styles from "./pages.module.css";
import { DECIMAL } from "./shared";
import { useLoad } from "./useLoad";
import { costDelta, dash, Detail, formatDateTime, LoadError, parseOnHandCsv, problemCode } from "./versionParts";

const COST_CSV_SAMPLE = "productId,siteId,unitCost\n";
const ON_HAND_SAMPLE = "productId,siteId,qty,bookValue\n";

function itemColumns(currency: string): DataTableColumn<ItemCostRow>[] {
  return [
    { key: "productId", header: "Product", mono: true },
    { key: "siteId", header: "Site", mono: true },
    { key: "unitCost", header: `Unit cost (${currency})`, align: "end", mono: true },
    { key: "activeCost", header: "Active cost", align: "end", mono: true, render: (row) => row.activeCost ?? dash },
    { key: "delta", header: "Change", align: "end", mono: true, render: (row) => costDelta(row.unitCost, row.activeCost) },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    { key: "source", header: "Source", render: (row) => row.source.charAt(0) + row.source.slice(1).toLowerCase() },
    { key: "revaluationAmount", header: "Revaluation", align: "end", mono: true, render: (row) => row.revaluationAmount ?? dash },
  ];
}

const siteColumns: DataTableColumn<{ siteId: string; revaluation: string }>[] = [
  { key: "siteId", header: "Site", mono: true },
  { key: "revaluation", header: "Revaluation voucher", align: "end", mono: true },
];

const deltaColumns: DataTableColumn<ActivationDelta>[] = [
  { key: "productId", header: "Product", mono: true },
  { key: "siteId", header: "Site", mono: true },
  { key: "old", header: "Old cost", align: "end", mono: true, render: (row) => row.previousCost ?? `Book value ${row.bookValue ?? dash}` },
  { key: "newCost", header: "New cost", align: "end", mono: true },
  { key: "onHandQty", header: "On hand", align: "end", mono: true },
  { key: "revaluation", header: "Revaluation", align: "end", mono: true },
];

/** Item costs with cursor paging; reloads when key changes. */
function ItemCosts({ version, reloadKey }: { version: CostingVersion; reloadKey: string }) {
  const state = useLoad((signal) => listItemCosts(version.id, undefined, signal), reloadKey);
  const [more, setMore] = useState<{ key: string; rows: ItemCostRow[]; next?: string }>({ key: "", rows: [] });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const extra = more.key === reloadKey ? more : { key: reloadKey, rows: [], next: undefined };
  const rows = [...(state.data?.items ?? []), ...extra.rows];
  const next = extra.rows.length > 0 ? extra.next : state.data?.nextCursor;

  async function loadMore() {
    if (!next) return;
    setBusy(true);
    setError(null);
    try {
      const page = await listItemCosts(version.id, next);
      setMore({ key: reloadKey, rows: [...extra.rows, ...page.items], next: page.nextCursor });
    } catch (err) {
      setError(describeError(err, "The next page could not be loaded."));
    } finally {
      setBusy(false);
    }
  }

  if (state.error) return <LoadError title="Could not load the item costs" error={state.error} onRetry={state.reload} />;
  return (
    <>
      <DataTable
        label="Item costs"
        columns={itemColumns(version.currency)}
        rows={rows}
        getRowKey={(row) => `${row.siteId}|${row.productId}`}
        loading={state.loading && !state.data}
        empty={{ title: "No item costs yet", description: "Import a CSV file or copy the costs of another version." }}
      />
      {error ? (
        <Alert tone="danger" live="alert">
          {error}
        </Alert>
      ) : null}
      {next ? (
        <div className={styles.more}>
          <Button loading={busy} onClick={loadMore}>
            Load more item costs
          </Button>
        </div>
      ) : null}
    </>
  );
}

/** CSV import into a draft (FR-20.5): all rows or none. */
function ImportCard({ version, onDone }: { version: CostingVersion; onDone(message: string): void }) {
  const [csv, setCsv] = useState(COST_CSV_SAMPLE);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit() {
    setError(null);
    if (csv.trim().split(/\r?\n/).length < 2) {
      setError("Add at least one row under the header.");
      return;
    }
    setBusy(true);
    try {
      const out = await importItemCostsCsv(version.id, csv);
      setCsv(COST_CSV_SAMPLE);
      onDone(`${out.imported} item costs imported.`);
    } catch (err) {
      setError(describeError(err, "The import failed."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Import costs" headingLevel={3}>
      <div className={styles.stack}>
        <FormField
          id="import-csv"
          label="Item costs (CSV)"
          hint={`One row per product and site, unit cost in ${version.currency} with at most 6 decimals. A row for a product and site already in the draft replaces its cost.`}
          error={error ?? undefined}
        >
          {(c) => <Textarea {...c} mono rows={6} value={csv} invalid={Boolean(error)} onChange={(e) => setCsv(e.target.value)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button loading={busy} onClick={submit}>
            Import costs
          </Button>
        </div>
      </div>
    </Card>
  );
}

/** Copy another version of the same legal entity and currency, moved by a percent (FR-20.5). */
function CopyCard({ version, onDone }: { version: CostingVersion; onDone(message: string): void }) {
  const sources = useLoad((signal) => listVersions({ legalEntityId: version.legalEntityId }, signal), version.legalEntityId);
  const options = (sources.data?.items ?? []).filter((v) => v.id !== version.id && v.currency === version.currency && v.itemCount > 0);
  const [source, setSource] = useState("");
  const [percent, setPercent] = useState("0");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const chosen = source || options[0]?.id || "";

  async function submit() {
    setError(null);
    if (!DECIMAL.test(percent.trim()) || Number(percent) <= -100) {
      setError("Enter a percent greater than -100, such as 2.5 or -1.");
      return;
    }
    setBusy(true);
    try {
      const out = await copyItemCosts(version.id, chosen, percent.trim());
      onDone(`${out.copied} item costs copied.`);
    } catch (err) {
      setError(describeError(err, "The copy failed."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Copy from another version" headingLevel={3}>
      {sources.error ? (
        <LoadError title="Could not load the versions to copy from" error={sources.error} onRetry={sources.reload} />
      ) : options.length === 0 && !sources.loading ? (
        <p className={styles.note}>No other version of this legal entity in {version.currency} has costs to copy.</p>
      ) : (
        <div className={styles.form}>
          <FormField id="copy-source" label="Source version">
            {(c) => (
              <Select {...c} value={chosen} disabled={sources.loading} onChange={(e) => setSource(e.target.value)}>
                {options.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.code} ({v.itemCount} costs)
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="copy-percent" label="Percent change" hint="Applied to every cost, rounded to 6 decimals." error={error ?? undefined}>
            {(c) => <Input {...c} inputMode="decimal" value={percent} invalid={Boolean(error)} onChange={(e) => setPercent(e.target.value)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button loading={busy} disabled={!chosen} onClick={submit}>
              Copy costs
            </Button>
          </div>
        </div>
      )}
    </Card>
  );
}

function RevaluationResult({ result }: { result: ActivationResponse }) {
  const heading =
    result.status === "PREVIEW" ? "Revaluation preview" : result.status === "ACTIVATED" ? "Posted revaluation" : "Recorded revaluation";
  return (
    <section className={styles.stack} aria-label={heading}>
      <h4>{heading}</h4>
      <StatGroup label="Revaluation totals">
        <StatTile label="Item costs" value={result.lineCount} />
        <StatTile label={`Total (${result.currency})`} value={result.revaluationTotal} />
        <StatTile label="Site vouchers" value={result.sites.length} />
      </StatGroup>
      <div className={styles.split}>
        <DataTable
          label="Revaluation by site"
          columns={siteColumns}
          rows={result.sites}
          getRowKey={(row) => row.siteId}
          empty={{ title: "Nothing to revalue" }}
        />
        <DataTable
          label="Largest revaluation deltas"
          columns={deltaColumns}
          rows={result.topDeltas}
          getRowKey={(row) => `${row.siteId}|${row.productId}`}
          empty={{ title: "No deltas" }}
        />
      </div>
    </section>
  );
}

/**
 * Activation (FR-20.5, BR-20.7): the revaluation preview is mandatory, so
 * "Activate version" stays disabled until a preview for the current on-hand
 * text and version has been shown, and the activation is sent at that version.
 */
function ActivationCard({ version, onActivated }: { version: CostingVersion; onActivated(message: string): void }) {
  const [onHand, setOnHand] = useState(ON_HAND_SAMPLE);
  const [result, setResult] = useState<ActivationResponse | null>(null);
  const [previewedFor, setPreviewedFor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [inputError, setInputError] = useState<string | null>(null);
  const [busy, setBusy] = useState<"preview" | "activate" | null>(null);
  const draft = version.status === "DRAFT";
  const inputKey = `${version.version}|${onHand}`;
  const previewed = result?.status === "PREVIEW" && previewedFor === inputKey;

  async function run(commit: boolean) {
    setError(null);
    const parsed: ReturnType<typeof parseOnHandCsv> = draft ? parseOnHandCsv(onHand) : { ok: true, value: [] };
    if (!parsed.ok) {
      setInputError(parsed.error);
      return;
    }
    setInputError(null);
    setBusy(commit ? "activate" : "preview");
    try {
      if (commit && result) {
        const done = await activateVersion(version.id, result.version, parsed.value);
        setResult(done);
        onActivated(done.status === "ACTIVATED" ? `Version ${done.versionCode} is active. Revaluation ${done.revaluationTotal} ${done.currency}.` : `Version ${done.versionCode} was already active.`);
      } else {
        setResult(await previewActivation(version.id, parsed.value));
        setPreviewedFor(inputKey);
      }
    } catch (err) {
      if (problemCode(err) === "conflict_version") {
        setResult(null);
        setError("The version changed since the preview. Preview the revaluation again before activating.");
      } else {
        setError(describeError(err, commit ? "The activation failed." : "The preview failed."));
      }
    } finally {
      setBusy(null);
    }
  }

  if (version.type !== "STANDARD") {
    return (
      <Card title="Activation" headingLevel={3}>
        <p className={styles.note}>Planned versions are not activated. Their costs feed cost roll-up and simulations.</p>
      </Card>
    );
  }
  return (
    <Card title="Activation" headingLevel={3}>
      <div className={styles.stack}>
        {draft ? (
          <>
            {version.blockActivation ? (
              <Alert tone="warning" title="Activation is blocked">
                This version has the block flag set. Activation is refused until the flag is removed.
              </Alert>
            ) : null}
            <FormField
              id="activation-onhand"
              label="On hand at activation (CSV)"
              hint="Physical on-hand per product and site, revalued at (new − old) × qty. Where a product and site has no standard yet and holds stock, give its current bookValue. Leave only the header if nothing is on hand."
              error={inputError ?? undefined}
            >
              {(c) => <Textarea {...c} mono rows={6} value={onHand} invalid={Boolean(inputError)} onChange={(e) => setOnHand(e.target.value)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button loading={busy === "preview"} disabled={busy === "activate"} onClick={() => run(false)}>
                Preview revaluation
              </Button>
              <Button variant="primary" loading={busy === "activate"} disabled={!previewed || busy === "preview"} onClick={() => run(true)}>
                Activate version
              </Button>
            </div>
            {!previewed ? <p className={styles.note}>Preview the revaluation for the on-hand above to enable activation.</p> : null}
          </>
        ) : (
          <div className={styles.formActions}>
            <Button loading={busy === "preview"} onClick={() => run(false)}>
              Show recorded revaluation
            </Button>
          </div>
        )}
        <Alert tone="danger" live="alert">
          {error}
        </Alert>
        {result ? <RevaluationResult result={result} /> : null}
      </div>
    </Card>
  );
}

/** One costing version: details, item costs compared to the active standard, import, copy and activation. */
export function VersionDetailPage() {
  const { id = "" } = useParams();
  const [refresh, setRefresh] = useState(0);
  const [notice, setNotice] = useState("");
  const state = useLoad((signal) => getVersion(id, signal), `${id}|${refresh}`);
  const version = state.data;

  const back = (
    <Link className={buttonClassName("ghost")} to="/costing/versions">
      Back to costing versions
    </Link>
  );

  function changed(message: string) {
    setNotice(message);
    setRefresh((n) => n + 1);
  }

  if (state.error) {
    if (problemCode(state.error) === "CST-VER-NOT-FOUND") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Costing version not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <LoadError title="Could not load the costing version" error={state.error} onRetry={state.reload} />;
  }
  if (!version) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading costing version…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const draft = version.status === "DRAFT";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Costing version ${version.code}`}
        meta={<StatusPill status={version.status} />}
        description={`${version.type === "STANDARD" ? "Standard" : "Planned"} costs in ${version.currency}, effective from ${version.effectiveFrom}.`}
        actions={back}
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Details" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {version.legalEntityId}
          </Detail>
          <Detail term="Item costs">{String(version.itemCount)}</Detail>
          <Detail term="Block activation">{version.blockActivation ? "Yes" : "No"}</Detail>
          <Detail term="Created">{formatDateTime(version.createdAt)}</Detail>
          <Detail term="Activated">{formatDateTime(version.activatedAt)}</Detail>
          <Detail term="Revision" mono>
            {String(version.version)}
          </Detail>
        </dl>
      </Card>
      <ItemCosts version={version} reloadKey={`${version.id}|${refresh}`} />
      {draft ? (
        <div className={styles.split}>
          <ImportCard version={version} onDone={changed} />
          <CopyCard version={version} onDone={changed} />
        </div>
      ) : null}
      <ActivationCard key={version.id} version={version} onActivated={changed} />
    </div>
  );
}
