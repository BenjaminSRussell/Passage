import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  CATALOG_KINDS,
  CATALOG_LIFECYCLES,
  DUTY_HUB_CATALOG_WRITE,
  listCatalog,
  type CatalogEntry,
  type CatalogKind,
  type CatalogLifecycle,
} from "./api/hubClient";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export const LIFECYCLE_TONES: Record<CatalogLifecycle, { label: string; tone: BadgeTone }> = {
  active: { label: "Active", tone: "success" },
  deprecated: { label: "Deprecated", tone: "warning" },
  retired: { label: "Retired", tone: "neutral" },
};

const columns: DataTableColumn<CatalogEntry>[] = [
  { key: "title", header: "Title" },
  { key: "name", header: "Contract", mono: true, render: (row) => `${row.name} ${row.specVersion}` },
  { key: "kind", header: "Kind", render: (row) => (row.kind === "api" ? "API" : "Events") },
  { key: "ownerService", header: "Owner", mono: true },
  { key: "operationCount", header: "Operations", align: "end", render: (row) => (row.kind === "api" ? row.operationCount : `${row.operationCount} events`) },
  {
    key: "lifecycle",
    header: "Lifecycle",
    render: (row) => <Badge tone={LIFECYCLE_TONES[row.lifecycle].tone}>{LIFECYCLE_TONES[row.lifecycle].label}</Badge>,
  },
  { key: "sunsetAt", header: "Sunset", render: (row) => (row.sunsetAt ? formatDateTime(row.sunsetAt) : dash) },
];

/** The API and event catalog the developer portal browses: every published contract version (FR-35.8). */
export function CatalogPage() {
  const navigate = useNavigate();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_CATALOG_WRITE);
  const [kind, setKind] = useState<CatalogKind | "">("");
  const [lifecycle, setLifecycle] = useState<CatalogLifecycle | "">("");
  const [draft, setDraft] = useState("");
  const [q, setQ] = useState("");
  const [more, setMore] = useState<CatalogEntry[]>([]);
  const [cursor, setCursor] = useState<string | null | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const filterKey = `${kind}|${lifecycle}|${q}`;
  const state = useLoad(
    async (signal) => {
      const page = await listCatalog({ kind, lifecycle, q }, signal);
      setMore([]);
      setCursor(page.nextCursor);
      setMoreError(null);
      return page;
    },
    `hub-catalog:${filterKey}`,
  );
  const next = cursor === undefined ? (state.data?.nextCursor ?? null) : cursor;

  async function loadMore() {
    if (next === null) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listCatalog({ kind, lifecycle, q, cursor: next });
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (failure) {
      setMoreError(describeProblem(failure));
    } finally {
      setLoadingMore(false);
    }
  }

  function search(event: FormEvent) {
    event.preventDefault();
    setQ(draft.trim());
  }

  const filtered = kind !== "" || lifecycle !== "" || q !== "";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="API catalog"
        description="Every published API and event contract, with its owner, version and lifecycle. A deprecated contract announces its sunset date at least 12 months ahead."
        actions={
          canWrite ? (
            <Link className={buttonClassName("primary")} to="/integration/catalog/new">
              Publish contract
            </Link>
          ) : undefined
        }
      />
      <form className={styles.toolbar} onSubmit={search} role="search" aria-label="Catalog search">
        <FormField id="catalog-kind" label="Kind">
          {(control) => (
            <Select {...control} value={kind} onChange={(event) => setKind(event.target.value as CatalogKind | "")}>
              <option value="">All</option>
              {CATALOG_KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="catalog-lifecycle" label="Lifecycle">
          {(control) => (
            <Select {...control} value={lifecycle} onChange={(event) => setLifecycle(event.target.value as CatalogLifecycle | "")}>
              <option value="">All</option>
              {CATALOG_LIFECYCLES.map((l) => (
                <option key={l.value} value={l.value}>
                  {l.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="catalog-search" label="Search">
          {(control) => <Input {...control} value={draft} maxLength={100} onChange={(event) => setDraft(event.target.value)} />}
        </FormField>
        <Button type="submit">Search</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load the catalog" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          <DataTable
            label="API catalog"
            columns={columns}
            rows={[...(state.data?.items ?? []), ...more]}
            getRowKey={(row) => row.id}
            loading={state.loading}
            onRowOpen={(row) => navigate(`/integration/catalog/${row.id}`)}
            empty={
              filtered
                ? { title: "No contracts match", description: "Change the filters or the search text." }
                : { title: "The catalog is empty", description: "Publish an OpenAPI or AsyncAPI document so integrators can find and call it." }
            }
          />
          {moreError ? (
            <Alert tone="danger" title="Could not load more" live="alert">
              <ProblemMessage problem={moreError} />
            </Alert>
          ) : null}
          {next !== null && !state.loading ? (
            <div>
              <Button onClick={loadMore} loading={loadingMore}>
                Load more
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
