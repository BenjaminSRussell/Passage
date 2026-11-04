import { useEffect, useRef, useState } from "react";
import { Alert, Button, DataTable, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import type { ReferenceStatus } from "./api/reference";
import { useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";

export type StatusFilterValue = "" | ReferenceStatus;

export const statusOptions = [
  { value: "", label: "All" },
  { value: "active", label: "Active" },
  { value: "inactive", label: "Inactive" },
];

export interface RowsState<T> {
  rows: T[];
  loading: boolean;
  error: Problem | null;
  reload(): void;
  update(change: (rows: T[]) => T[]): void;
}

export function useRows<T>(load: (signal: AbortSignal) => Promise<T[]>, key: string): RowsState<T> {
  const [state, setState] = useState<{ rows: T[]; loading: boolean; error: Problem | null }>({
    rows: [],
    loading: true,
    error: null,
  });
  const [version, setVersion] = useState(0);
  const latest = useRef(0);

  useEffect(() => {
    const token = ++latest.current;
    const controller = new AbortController();
    setState({ rows: [], loading: true, error: null });
    load(controller.signal).then(
      (rows) => {
        if (token === latest.current) {
          setState({ rows, loading: false, error: null });
        }
      },
      (failure) => {
        if (token === latest.current) {
          setState({ rows: [], loading: false, error: describeProblem(failure) });
        }
      },
    );
    return () => controller.abort();
  }, [key, version]);

  return {
    ...state,
    reload: () => setVersion((value) => value + 1),
    update: (change) => setState((current) => ({ ...current, rows: change(current.rows) })),
  };
}

export interface FilterSelectProps {
  id: string;
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange(value: string): void;
}

export function FilterSelect({ id, label, value, options, onChange }: FilterSelectProps) {
  return (
    <div className={styles.filter}>
      <label htmlFor={id} className={styles.filterLabel}>
        {label}
      </label>
      <Select id={id} value={value} onChange={(event) => onChange(event.target.value)}>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </Select>
    </div>
  );
}

interface StatusActionProps {
  subject: string;
  status: ReferenceStatus;
  apply(next: ReferenceStatus): Promise<void>;
}

export function StatusAction({ subject, status, apply }: StatusActionProps) {
  const next: ReferenceStatus = status === "active" ? "inactive" : "active";
  const verb = next === "inactive" ? "Deactivate" : "Activate";
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const actionButton = useRef<HTMLButtonElement>(null);
  const confirmButton = useRef<HTMLButtonElement>(null);
  const opened = useRef(false);

  useEffect(() => {
    if (confirming) {
      opened.current = true;
      confirmButton.current?.focus();
    } else if (opened.current) {
      opened.current = false;
      actionButton.current?.focus();
    }
  }, [confirming]);

  const confirm = async () => {
    setBusy(true);
    setFailure(null);
    try {
      await apply(next);
      setConfirming(false);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={styles.rowActions}>
      {confirming ? (
        <>
          <Button ref={confirmButton} size="sm" variant="danger" loading={busy} aria-label={`Confirm ${verb.toLowerCase()} ${subject}`} onClick={confirm}>
            Confirm
          </Button>
          <Button size="sm" variant="ghost" aria-label={`Cancel ${verb.toLowerCase()} ${subject}`} onClick={() => setConfirming(false)}>
            Cancel
          </Button>
        </>
      ) : (
        <Button
          ref={actionButton}
          size="sm"
          aria-label={`${verb} ${subject}`}
          onClick={() => {
            setFailure(null);
            setConfirming(true);
          }}
        >
          {verb}
        </Button>
      )}
      {failure ? (
        <span role="alert" className={styles.rowError}>
          {failure.message} ({failure.code})
        </span>
      ) : null}
    </div>
  );
}

interface StatusTableProps<T extends { status: string }> {
  label: string;
  emptyTitle: string;
  columns: DataTableColumn<T>[];
  state: RowsState<T>;
  getRowKey(row: T): string;
  action?: { subject(row: T): string; apply(row: T, next: ReferenceStatus): Promise<Partial<T>> };
}

export function StatusTable<T extends { status: string }>({ label, emptyTitle, columns, state, getRowKey, action }: StatusTableProps<T>) {
  const { canWriteReference } = useLegalEntity();
  const all: DataTableColumn<T>[] = [...columns, { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> }];
  if (action && canWriteReference) {
    all.push({
      key: "actions",
      header: "Actions",
      render: (row) => (
        <StatusAction
          subject={action.subject(row)}
          status={row.status as ReferenceStatus}
          apply={async (next) => {
            const updated = await action.apply(row, next);
            state.update((rows) => rows.map((item) => (getRowKey(item) === getRowKey(row) ? { ...item, ...updated } : item)));
          }}
        />
      ),
    });
  }
  return (
    <>
      {state.error ? (
        <Alert tone="danger" title={`Could not load ${label.toLowerCase()}`} live="alert">
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      <DataTable label={label} columns={all} rows={state.rows} getRowKey={getRowKey} loading={state.loading} empty={{ title: emptyTitle }} />
    </>
  );
}
