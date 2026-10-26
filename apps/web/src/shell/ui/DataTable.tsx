import { useEffect, useRef, useState, type ReactNode } from "react";
import { cx } from "./cx";
import styles from "./DataTable.module.css";
import { EmptyState } from "./EmptyState";
import { Skeleton } from "./Skeleton";

export interface DataTableColumn<T> {
  key: string;
  header: string;
  align?: "start" | "end";
  mono?: boolean;
  render?(row: T): ReactNode;
}

export interface DataTableProps<T> {
  label: string;
  columns: DataTableColumn<T>[];
  rows: T[];
  getRowKey(row: T, index: number): string;
  loading?: boolean;
  empty?: { title: string; description?: string };
  onRowOpen?(row: T): void;
}

const SKELETON_ROWS = 5;

function cellValue<T>(column: DataTableColumn<T>, row: T): ReactNode {
  if (column.render) {
    return column.render(row);
  }
  const value = (row as unknown as Record<string, unknown>)[column.key];
  return value === undefined || value === null ? "" : (value as ReactNode);
}

export function DataTable<T>({ label, columns, rows, getRowKey, loading = false, empty, onRowOpen }: DataTableProps<T>) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const [overflows, setOverflows] = useState(false);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) {
      return;
    }
    const measure = () => setOverflows(el.scrollWidth > el.clientWidth);
    measure();
    if (typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    if (el.firstElementChild) {
      observer.observe(el.firstElementChild);
    }
    return () => observer.disconnect();
  }, []);

  const cellClass = (column: DataTableColumn<T>) =>
    cx(styles.cell, column.align === "end" && styles.end, column.mono && styles.mono);
  return (
    <div
      ref={scrollRef}
      className={styles.scroll}
      {...(overflows ? { role: "region", "aria-label": `${label} table`, tabIndex: 0 } : {})}
    >
      <table className={styles.table} aria-label={label} aria-busy={loading || undefined}>
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.key} scope="col" className={cx(styles.head, column.align === "end" && styles.end)}>
                {column.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {loading ? (
            Array.from({ length: SKELETON_ROWS }, (_, i) => (
              <tr key={i} className={styles.row}>
                {columns.map((column) => (
                  <td key={column.key} className={styles.cell}>
                    <Skeleton width="3/4" />
                  </td>
                ))}
              </tr>
            ))
          ) : rows.length === 0 ? (
            <tr>
              <td colSpan={columns.length} className={styles.emptyCell}>
                <EmptyState compact title={empty?.title ?? "Nothing to show yet"} description={empty?.description} />
              </td>
            </tr>
          ) : (
            rows.map((row, index) => (
              <tr
                key={getRowKey(row, index)}
                className={cx(styles.row, onRowOpen && styles.clickable)}
                onClick={onRowOpen ? () => onRowOpen(row) : undefined}
              >
                {columns.map((column, columnIndex) => (
                  <td key={column.key} className={cellClass(column)}>
                    {onRowOpen && columnIndex === 0 ? (
                      <button type="button" className={styles.rowLink}>
                        {cellValue(column, row)}
                      </button>
                    ) : (
                      cellValue(column, row)
                    )}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
      {loading ? <span className="visually-hidden">Loading…</span> : null}
    </div>
  );
}
