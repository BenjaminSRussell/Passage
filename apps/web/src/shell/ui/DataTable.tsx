import { useEffect, useRef, useState, type ReactNode } from "react";
import { cx } from "./cx";
import styles from "./DataTable.module.css";
import { EmptyState } from "./EmptyState";
import { Icon } from "./Icon";
import { InsideControl } from "./insideControl";
import { Skeleton } from "./Skeleton";

export type SortDirection = "ascending" | "descending";

export interface DataTableColumn<T> {
  key: string;
  header: string;
  align?: "start" | "end";
  mono?: boolean;
  sortable?: boolean;
  render?(row: T): ReactNode;
}

export interface DataTableSelection<T> {
  selected: ReadonlySet<string>;
  onToggle(key: string): void;
  onToggleAll(): void;
  label(row: T, index: number): string;
  allLabel: string;
}

export interface DataTableProps<T> {
  label: string;
  columns: DataTableColumn<T>[];
  rows: T[];
  getRowKey(row: T, index: number): string;
  loading?: boolean;
  empty?: { title: string; description?: string };
  onRowOpen?(row: T): void;
  sort?: { key: string; direction: SortDirection } | null;
  sortDescribedBy?: string;
  onSortChange?(key: string): void;
  selection?: DataTableSelection<T>;
}

const SKELETON_ROWS = 5;

function cellValue<T>(column: DataTableColumn<T>, row: T): ReactNode {
  if (column.render) {
    return column.render(row);
  }
  const value = (row as unknown as Record<string, unknown>)[column.key];
  return value === undefined || value === null ? "" : (value as ReactNode);
}

export function DataTable<T>({ label, columns, rows, getRowKey, loading = false, empty, onRowOpen, sort, onSortChange, sortDescribedBy, selection }: DataTableProps<T>) {
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

  const chosen = selection ? rows.filter((row, index) => selection.selected.has(getRowKey(row, index))).length : 0;
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
            {selection ? (
              <th scope="col" className={cx(styles.head, styles.selectCell)}>
                <input
                  type="checkbox"
                  aria-label={selection.allLabel}
                  checked={rows.length > 0 && chosen === rows.length}
                  ref={(el) => {
                    if (el) el.indeterminate = chosen > 0 && chosen < rows.length;
                  }}
                  onChange={selection.onToggleAll}
                />
              </th>
            ) : null}
            {columns.map((column) => {
              const sortable = Boolean(column.sortable && onSortChange);
              const direction = sort?.key === column.key ? sort.direction : undefined;
              return (
                <th
                  key={column.key}
                  scope="col"
                  className={cx(styles.head, column.align === "end" && styles.end)}
                  aria-sort={sortable ? (direction ?? "none") : undefined}
                >
                  {sortable ? (
                    <button type="button" className={styles.sortButton} aria-describedby={direction ? sortDescribedBy : undefined} onClick={() => onSortChange?.(column.key)}>
                      {column.header}
                      <span className={cx(styles.sortMark, direction === "ascending" && styles.ascending, direction && styles.sorted)} aria-hidden="true">
                        <Icon name="chevronDown" />
                      </span>
                    </button>
                  ) : (
                    column.header
                  )}
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {loading ? (
            Array.from({ length: SKELETON_ROWS }, (_, i) => (
              <tr key={i} className={styles.row}>
                {selection ? <td className={cx(styles.cell, styles.selectCell)} /> : null}
                {columns.map((column) => (
                  <td key={column.key} className={styles.cell}>
                    <Skeleton width="3/4" />
                  </td>
                ))}
              </tr>
            ))
          ) : rows.length === 0 ? (
            <tr>
              <td colSpan={columns.length + (selection ? 1 : 0)} className={styles.emptyCell}>
                <EmptyState compact title={empty?.title ?? "Nothing to show yet"} description={empty?.description} />
              </td>
            </tr>
          ) : (
            rows.map((row, index) => {
              const key = getRowKey(row, index);
              const picked = selection?.selected.has(key) ?? false;
              return (
              <tr
                key={key}
                className={cx(styles.row, onRowOpen && styles.clickable, picked && styles.selectedRow)}
                aria-selected={selection ? picked : undefined}
                onClick={onRowOpen ? () => onRowOpen(row) : undefined}
              >
                {selection ? (
                  <td className={cx(styles.cell, styles.selectCell)} onClick={(event) => event.stopPropagation()}>
                    <input type="checkbox" aria-label={selection.label(row, index)} checked={picked} onChange={() => selection.onToggle(key)} />
                  </td>
                ) : null}
                {columns.map((column, columnIndex) => (
                  <td key={column.key} className={cellClass(column)}>
                    {onRowOpen && columnIndex === 0 ? (
                      <button type="button" className={styles.rowLink}>
                        <InsideControl.Provider value={true}>{cellValue(column, row)}</InsideControl.Provider>
                      </button>
                    ) : (
                      cellValue(column, row)
                    )}
                  </td>
                ))}
              </tr>
              );
            })
          )}
        </tbody>
      </table>
      {loading ? <span className="visually-hidden">Loading…</span> : null}
    </div>
  );
}
