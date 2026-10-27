import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listWorkers, type Problem, type Worker } from "./api/client";
import { dash, workerTypeLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Worker>[] = [
  { key: "personnelNumber", header: "Personnel number", mono: true },
  { key: "legalName", header: "Legal name" },
  { key: "preferredName", header: "Preferred name", render: (row) => row.preferredName ?? dash },
  { key: "workerType", header: "Type", render: (row) => <Badge tone="neutral">{workerTypeLabel[row.workerType] ?? row.workerType}</Badge> },
];

/** Workers in the tenant (FR-42.1), searchable by number or name. PII is never listed. */
export function WorkersPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const [draft, setDraft] = useState(q);
  const state = useLoad((signal) => listWorkers({ q }, signal), `workers|${q}`);
  const [more, setMore] = useState<{ q: string; items: Worker[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  const extra = more && more.q === q ? more : null;
  const rows = [...(state.data?.items ?? []), ...(extra?.items ?? [])];
  const cursor = extra ? extra.cursor : state.data?.nextCursor;

  function search(event: FormEvent) {
    event.preventDefault();
    setMore(null);
    setParams(draft.trim() ? { q: draft.trim() } : {});
  }

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await listWorkers({ q, cursor });
      setMore({ q, items: [...(extra?.items ?? []), ...next.items], cursor: next.nextCursor });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Workers"
        description="Employees and contractors with their employments, positions and manager on any date. National IDs and bank details stay masked unless you reveal them with a reason."
        actions={
          <Link className={buttonClassName("primary")} to="/hr/workers/new">
            New worker
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={search} role="search" aria-label="Find workers">
        <FormField id="worker-search" label="Name or number" hint="Matches the personnel number, legal name or preferred name.">
          {(control) => <Input {...control} value={draft} maxLength={100} onChange={(event) => setDraft(event.target.value)} />}
        </FormField>
        <Button type="submit">Find workers</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load workers" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Workers"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            q
              ? { title: "No workers match", description: "Try another personnel number or name." }
              : { title: "No workers yet", description: "Create a worker, then start an employment and assign a position." }
          }
          onRowOpen={(row) => navigate(`/hr/workers/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more workers" live="alert">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
      {!state.error && !state.loading && cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more workers
          </Button>
        </div>
      ) : null}
    </div>
  );
}
