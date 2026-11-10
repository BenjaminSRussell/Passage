import { useEffect, useState } from "react";
import { Alert, Button, DataTable, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Page, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface PagedTableProps<T extends { id: string }> {
  /** Table name; also the noun in the error titles ("Could not load {label}"). */
  label: string;
  columns: DataTableColumn<T>[];
  /** Loads one page; cursor is the previous page's nextCursor. */
  load(cursor: string | undefined, signal?: AbortSignal): Promise<Page<T>>;
  /** Changing the key reloads from the first page (for filters). */
  loadKey: string;
  empty: { title: string; description?: string };
  onRowOpen?(row: T): void;
}

/** A list over the service's cursor pagination, newest first, with loading, empty and error states and "Load more". */
export function PagedTable<T extends { id: string }>({ label, columns, load, loadKey, empty, onRowOpen }: PagedTableProps<T>) {
  const state = useLoad((signal) => load(undefined, signal), loadKey);
  const [more, setMore] = useState<T[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const noun = label.toLowerCase();

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
      const page = await load(cursor);
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  if (state.error) {
    return (
      <Alert tone="danger" title={`Could not load ${noun}`} live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        <ProblemMessage problem={state.error} />
      </Alert>
    );
  }
  const rows = [...(state.data?.items ?? []), ...more];
  return (
    <>
      <DataTable label={label} columns={columns} rows={rows} getRowKey={(row) => row.id} loading={state.loading} empty={empty} onRowOpen={onRowOpen} />
      {moreError ? (
        <Alert tone="danger" title={`Could not load more ${noun}`} live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </>
  );
}
