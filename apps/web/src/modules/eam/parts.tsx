import type { ReactNode } from "react";
import { Alert, Badge, Button, DataTable, FormField, Select, type DataTableColumn } from "../../shell/ui";
import type { Problem } from "./api/client";
import { sentence, statusTone } from "./format";
import styles from "./pages.module.css";
import type { PagedState } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** A status as a toned badge with sentence-case text. */
export function State({ value }: { value: string }) {
  return <Badge tone={statusTone[value] ?? "neutral"}>{sentence(value)}</Badge>;
}

/** One term and value in a details list. */
export function Entry({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

export function Failure({ title, problem, retry }: { title: string; problem: Problem; retry?: () => void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={retry ? <Button size="sm" onClick={retry}>Try again</Button> : undefined}>
      <ProblemMessage problem={problem} />
    </Alert>
  );
}

/** A status filter as a select; "" means every status. */
export function StatusFilter({ id, value, options, onChange }: { id: string; value: string; options: string[]; onChange(next: string): void }) {
  return (
    <FormField id={id} label="Status">
      {(control) => (
        <Select {...control} value={value} onChange={(event) => onChange(event.target.value)}>
          <option value="">All statuses</option>
          {options.map((o) => (
            <option key={o} value={o}>
              {sentence(o)}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

/** A paged table with its error, empty and load-more states. */
export function PagedTable<T extends { id: string }>({
  label,
  noun,
  columns,
  state,
  empty,
  onRowOpen,
}: {
  label: string;
  noun: string;
  columns: DataTableColumn<T>[];
  state: PagedState<T>;
  empty: { title: string; description?: string };
  onRowOpen?(row: T): void;
}) {
  return (
    <>
      {state.error ? (
        <Failure title={`Could not load ${noun}`} problem={state.error} retry={state.reload} />
      ) : (
        <DataTable label={label} columns={columns} rows={state.rows} getRowKey={(row) => row.id} loading={state.loading} empty={empty} onRowOpen={onRowOpen} />
      )}
      {state.moreError ? <Failure title={`Could not load more ${noun}`} problem={state.moreError} /> : null}
      {state.hasMore ? (
        <div className={styles.more}>
          <Button onClick={state.loadMore} loading={state.loadingMore}>
            Load more {noun}
          </Button>
        </div>
      ) : null}
    </>
  );
}
