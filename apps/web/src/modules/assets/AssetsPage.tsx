import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { ASSET_STATUSES, describeProblem, listAssets, type Asset, type AssetStatus, type Problem } from "./api/client";
import { StatusBadge } from "./badges";
import { dash, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Asset>[] = [
  { key: "assetNumber", header: "Number", mono: true },
  { key: "name", header: "Name" },
  { key: "classCode", header: "Class", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusBadge status={row.status} /> },
  { key: "acquiredDate", header: "In service", mono: true, render: (row) => row.acquiredDate ?? dash },
  { key: "bookCount", header: "Books", align: "end" },
];

/** Asset register (PRD 7.37.14 "Asset register"), ordered by asset number. */
export function AssetsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<AssetStatus | "">("");
  const state = useLoad((signal) => listAssets({ status }, signal), status);
  const [more, setMore] = useState<Asset[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listAssets({ status, after: cursor });
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  const rows = [...(state.data?.items ?? []), ...more];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Asset register"
        description="Every fixed asset in your legal entities. Open an asset to see its books, depreciation schedule and transactions."
        actions={
          <Link className={buttonClassName("primary")} to="/assets/new">
            New asset
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="assets-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as AssetStatus | "")}>
              <option value="">Any status</option>
              {ASSET_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load assets" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Assets"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No assets yet", description: "Assets you create appear here. Add a book to acquire one." }}
          onRowOpen={(row) => navigate(`/assets/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more assets" live="alert">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
      {cursor && !state.error ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
