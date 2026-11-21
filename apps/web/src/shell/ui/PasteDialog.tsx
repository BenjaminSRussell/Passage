import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { copy } from "../copy";
import { useFocusTrap } from "../hooks/useFocusTrap";
import { Button } from "./Button";
import { DataTable, type DataTableColumn } from "./DataTable";
import styles from "./PasteDialog.module.css";
import { Textarea } from "./Textarea";

export const MAX_PASTE_ROWS = 500;

export interface PasteField {
  key: string;
  header: string;
  parse?(cell: string): unknown;
  validate?(value: unknown, row: Record<string, unknown>): string | null;
}

export interface PastedRow {
  values: Record<string, unknown>;
  errors: string[];
}

export interface Pasted {
  rows: PastedRow[];
  tooMany: boolean;
}

export function parsePaste(text: string, fields: PasteField[]): Pasted {
  const lines = text.split(/\r?\n/);
  while (lines.length > 0 && lines[lines.length - 1].trim() === "") {
    lines.pop();
  }
  const first = lines[0]?.split("\t").map((cell) => cell.trim().toLowerCase());
  const headed =
    first !== undefined &&
    first.length === fields.length &&
    fields.every((f, i) => f.header.toLowerCase() === first[i]);
  // A blank line between rows is not a row; sending it on would show an empty "Ready" row and commit it.
  const body = (headed ? lines.slice(1) : lines).filter((line) => line.trim() !== "");
  if (body.length > MAX_PASTE_ROWS) {
    return { rows: [], tooMany: true };
  }
  const rows = body.map((line) => {
    const cells = line.split("\t");
    const values: Record<string, unknown> = {};
    const errors: string[] = [];
    for (const [i, field] of fields.entries()) {
      const raw = (cells[i] ?? "").trim();
      values[field.key] = field.parse ? field.parse(raw) : raw;
      const blankParsed = field.parse !== undefined && raw === "";
      if (
        blankParsed ||
        (typeof values[field.key] === "number" &&
          !Number.isFinite(values[field.key]))
      ) {
        errors.push(copy.lists.pasteBadNumber(field.header));
      }
    }
    for (const field of fields) {
      const why = field.validate?.(values[field.key], values) ?? null;
      if (why) {
        errors.push(why);
      }
    }
    if (cells.length > fields.length) {
      errors.push(copy.lists.pasteExtraCells(cells.length - fields.length));
    }
    return { values, errors };
  });
  return { rows, tooMany: false };
}

export interface PasteDialogProps {
  fields: PasteField[];
  onApply(rows: Record<string, unknown>[]): void;
  onClose(): void;
}

export function PasteDialog({ fields, onApply, onClose }: PasteDialogProps) {
  const panel = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const boxId = useId();
  const opener = useRef<Element | null>(document.activeElement);
  const [text, setText] = useState("");
  useFocusTrap(panel, true);
  useEffect(() => {
    const previous = opener.current;
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) {
        previous.focus();
      }
    };
  }, []);

  const pasted = useMemo(() => parsePaste(text, fields), [text, fields]);
  const invalid = pasted.rows.some((row) => row.errors.length > 0);
  const columns: DataTableColumn<PastedRow & { number: number }>[] = [
    { key: "number", header: copy.lists.pasteRowNumber },
    ...fields.map((f) => ({
      key: f.key,
      header: f.header,
      render: (row: PastedRow) => String(row.values[f.key] ?? ""),
    })),
    {
      key: "problem",
      header: copy.lists.pasteProblem,
      render: (row) =>
        row.errors.length > 0 ? row.errors.join("; ") : copy.lists.pasteRowOk,
    },
  ];

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      event.stopPropagation();
      onClose();
    }
  };

  return (
    <div
      className={styles.overlay}
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={styles.panel}
        onKeyDown={onKeyDown}
      >
        <h2 id={titleId} className={styles.title}>
          {copy.lists.pasteTitle}
        </h2>
        <p className={styles.help}>
          {copy.lists.pasteHelp(fields.map((f) => f.header).join(", "))}
        </p>
        <label htmlFor={boxId} className="visually-hidden">
          {copy.lists.pasteBox}
        </label>
        <Textarea
          id={boxId}
          mono
          rows={6}
          value={text}
          onChange={(e) => setText(e.target.value)}
          spellCheck={false}
          autoFocus
        />
        {pasted.tooMany ? (
          <p role="alert">{copy.lists.pasteTooMany(MAX_PASTE_ROWS)}</p>
        ) : pasted.rows.length > 0 ? (
          <DataTable
            label={copy.lists.pasteTitle}
            columns={columns}
            rows={pasted.rows.map((row, i) => ({ ...row, number: i + 1 }))}
            getRowKey={(row) => String(row.number)}
          />
        ) : null}
        <div className={styles.actions}>
          <Button onClick={onClose}>{copy.lists.pasteClose}</Button>
          <Button
            variant="primary"
            disabled={pasted.rows.length === 0 || invalid}
            onClick={() => onApply(pasted.rows.map((row) => row.values))}
          >
            {copy.lists.pasteCommit(pasted.rows.length)}
          </Button>
        </div>
      </div>
    </div>
  );
}
