import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Button } from "./Button";
import { cx } from "./cx";
import { Input } from "./Input";
import { Select } from "./Select";
import styles from "./LineGrid.module.css";

export interface LineGridColumn<K extends string> {
  name: K;
  label: string;
  mono?: boolean;
  decimal?: boolean;
  options?: { value: string; label: string }[];
}

export interface LineGridProps<K extends string> {
  idPrefix: string;
  noun: string;
  columns: LineGridColumn<K>[];
  lines: Record<K, string>[];
  errors?: Record<string, string>;
  path?: string;
  newLine(): Record<K, string>;
  onChange(lines: Record<K, string>[]): void;
  onSave?(): void;
  onSubmit?(): void;
}

export type GridAction =
  | "up"
  | "down"
  | "left"
  | "right"
  | "next"
  | "previous"
  | "edit"
  | "commit"
  | "cancel"
  | "leave"
  | "addBelow"
  | "menu"
  | "save"
  | "submit";

export interface KeyChord {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
}

const ARROWS: Record<string, GridAction> = { ArrowUp: "up", ArrowDown: "down", ArrowLeft: "left", ArrowRight: "right" };

export function gridKeyAction(chord: KeyChord, editing: boolean): GridAction | null {
  const { key, shiftKey } = chord;
  if (chord.altKey) return null;
  if (chord.ctrlKey || chord.metaKey) {
    if (key === "Enter") return shiftKey ? "submit" : "addBelow";
    return key === "s" && !shiftKey ? "save" : null;
  }
  if (key === "ContextMenu" || (key === "F10" && shiftKey)) return "menu";
  if (key === "Tab") return shiftKey ? "previous" : "next";
  if (key === "Enter" && !shiftKey) return editing ? "commit" : "edit";
  if (key === "Escape") return editing ? "cancel" : "leave";
  return editing || shiftKey ? null : (ARROWS[key] ?? null);
}

interface Cell {
  r: number;
  c: number;
}

function cellOf(target: EventTarget): Cell | null {
  const raw = target instanceof HTMLElement ? target.dataset.cell : undefined;
  if (!raw) return null;
  const [r, c] = raw.split(":").map(Number);
  return { r, c };
}

export function LineGrid<K extends string>({
  idPrefix,
  noun,
  columns,
  lines,
  errors = {},
  path = "lines",
  newLine,
  onChange,
  onSave,
  onSubmit,
}: LineGridProps<K>) {
  const gridRef = useRef<HTMLDivElement>(null);
  const addRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const pendingFocus = useRef<Cell | null>(null);
  const editStart = useRef("");
  const [active, setActive] = useState<Cell>({ r: 0, c: 0 });
  const [editing, setEditing] = useState(false);
  const [menuRow, setMenuRow] = useState<number | null>(null);
  const [removed, setRemoved] = useState<{ index: number; line: Record<K, string> } | null>(null);

  const lastRow = lines.length - 1;
  const lastCol = columns.length - 1;
  const here = { r: Math.min(active.r, lastRow), c: Math.min(active.c, lastCol) };
  const lowerNoun = noun.toLowerCase();
  const value = (r: number, name: K) => String(lines[r]?.[name] ?? "");
  const errorOf = (r: number, name: K) => {
    const message = errors[`${path}[${r}].${name}`];
    return typeof message === "string" && message ? message : undefined;
  };

  useEffect(() => {
    const target = pendingFocus.current;
    if (!target) return;
    pendingFocus.current = null;
    gridRef.current?.querySelector<HTMLElement>(`[data-cell="${target.r}:${target.c}"]`)?.focus();
  });

  useEffect(() => {
    if (menuRow !== null) menuRef.current?.querySelector<HTMLElement>("button:not([disabled])")?.focus();
  }, [menuRow]);

  const focusCell = (cell: Cell) => {
    pendingFocus.current = cell;
    setActive(cell);
    setEditing(false);
  };

  const patch = (r: number, name: K, next: string) =>
    onChange(lines.map((line, i) => (i === r ? { ...line, [name]: next } : line)));

  const insertAt = (index: number, line: Record<K, string>, col: number) => {
    onChange([...lines.slice(0, index), line, ...lines.slice(index)]);
    focusCell({ r: index, c: col });
  };

  const rowComplete = (r: number) => columns.every((col) => value(r, col.name) !== "");

  const move = (to: Cell) => focusCell({ r: Math.max(0, Math.min(to.r, lastRow)), c: Math.max(0, Math.min(to.c, lastCol)) });

  const step = (cell: Cell, delta: number): boolean => {
    const index = cell.r * columns.length + cell.c + delta;
    if (index < 0 || index >= lines.length * columns.length) return false;
    focusCell({ r: Math.floor(index / columns.length), c: index % columns.length });
    return true;
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const cell = cellOf(event.target);
    if (!cell) return;
    const action = gridKeyAction(event, editing);
    if (!action) return;
    const consume = () => event.preventDefault();
    switch (action) {
      case "up":
      case "down":
      case "left":
      case "right":
        consume();
        move({ r: cell.r + (action === "down" ? 1 : action === "up" ? -1 : 0), c: cell.c + (action === "right" ? 1 : action === "left" ? -1 : 0) });
        break;
      case "next":
      case "previous":
        if (step(cell, action === "next" ? 1 : -1)) consume();
        break;
      case "edit":
        consume();
        editStart.current = value(cell.r, columns[cell.c].name);
        setEditing(true);
        break;
      case "commit":
        consume();
        if (cell.r < lastRow) move({ r: cell.r + 1, c: cell.c });
        else if (cell.c === lastCol && rowComplete(cell.r)) insertAt(cell.r + 1, newLine(), 0);
        else setEditing(false);
        break;
      case "cancel":
        consume();
        patch(cell.r, columns[cell.c].name, editStart.current);
        setEditing(false);
        break;
      case "leave":
        consume();
        addRef.current?.focus();
        break;
      case "addBelow":
        consume();
        insertAt(cell.r + 1, newLine(), cell.c);
        break;
      case "menu":
        consume();
        setMenuRow(cell.r);
        break;
      case "save":
        if (onSave) {
          consume();
          onSave();
        }
        break;
      case "submit":
        if (onSubmit) {
          consume();
          onSubmit();
        }
        break;
    }
  };

  const closeMenu = (refocus: Cell | null) => {
    setMenuRow(null);
    if (refocus) focusCell(refocus);
  };

  const fillDown = (r: number) => {
    const name = columns[here.c].name;
    patch(r, name, value(r - 1, name));
    closeMenu({ r, c: here.c });
  };

  const duplicate = (r: number) => {
    setMenuRow(null);
    insertAt(r + 1, { ...lines[r] }, here.c);
  };

  const remove = (r: number) => {
    setRemoved({ index: r, line: lines[r] });
    onChange(lines.filter((_, i) => i !== r));
    closeMenu({ r: Math.min(r, lastRow - 1), c: here.c });
  };

  const undo = () => {
    if (!removed) return;
    insertAt(removed.index, removed.line, here.c);
    setRemoved(null);
  };

  const gridColumns = `repeat(${columns.length}, minmax(8rem, 1fr)) auto`;
  const helpId = `${idPrefix}-keys`;
  const listError = errors[path];

  return (
    <div className={styles.wrap}>
      <div className={styles.scroll}>
        <div
          ref={gridRef}
          role="grid"
          aria-label={`${noun} lines`}
          aria-describedby={helpId}
          aria-rowcount={lines.length + 1}
          className={styles.grid}
          onKeyDown={handleKeyDown}
        >
          <div role="row" aria-rowindex={1} className={styles.row} style={{ gridTemplateColumns: gridColumns }}>
            {columns.map((col, c) => (
              <span key={col.name} role="columnheader" aria-colindex={c + 1} className={styles.header}>
                {col.label}
              </span>
            ))}
            <span role="columnheader" aria-colindex={columns.length + 1} className={styles.srOnly}>
              Actions
            </span>
          </div>
          {lines.map((_, r) => (
            <div key={r} role="row" aria-rowindex={r + 2} className={styles.row} style={{ gridTemplateColumns: gridColumns }}>
              {columns.map((col, c) => {
                const id = `${idPrefix}-${r}-${col.name}`;
                const error = errorOf(r, col.name);
                const shared = {
                  id,
                  "data-cell": `${r}:${c}`,
                  "aria-label": `${col.label}, ${lowerNoun} ${r + 1}`,
                  "aria-describedby": error ? `${id}-error` : undefined,
                  tabIndex: r === here.r && c === here.c ? 0 : -1,
                  invalid: Boolean(error),
                  value: value(r, col.name),
                  onFocus: () => {
                    if (r !== active.r || c !== active.c) {
                      setActive({ r, c });
                      setEditing(false);
                    }
                  },
                };
                const begin = () => {
                  if (!editing) {
                    editStart.current = value(r, col.name);
                    setEditing(true);
                  }
                };
                return (
                  <span key={col.name} role="gridcell" aria-colindex={c + 1} className={styles.cell}>
                    {col.options ? (
                      <Select
                        {...shared}
                        onChange={(e) => {
                          begin();
                          patch(r, col.name, e.target.value);
                        }}
                      >
                        {col.options.map((o) => (
                          <option key={o.value} value={o.value}>
                            {o.label}
                          </option>
                        ))}
                      </Select>
                    ) : (
                      <Input
                        {...shared}
                        inputMode={col.decimal ? "decimal" : undefined}
                        className={cx(col.mono && styles.mono)}
                        onChange={(e) => {
                          begin();
                          patch(r, col.name, e.target.value);
                        }}
                      />
                    )}
                    {error ? (
                      <span id={`${id}-error`} className={styles.error}>
                        {error}
                      </span>
                    ) : null}
                  </span>
                );
              })}
              <span role="gridcell" aria-colindex={columns.length + 1} className={styles.cell}>
                <Button size="sm" variant="ghost" tabIndex={-1} aria-label={`${noun} ${r + 1} actions`} onClick={() => setMenuRow(r)}>
                  Actions
                </Button>
              </span>
            </div>
          ))}
        </div>
      </div>
      {menuRow !== null ? (
        <div
          ref={menuRef}
          role="menu"
          aria-label={`${noun} ${menuRow + 1} actions`}
          className={styles.menu}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              event.preventDefault();
              closeMenu(here);
            }
          }}
        >
          <Button role="menuitem" size="sm" disabled={menuRow === 0} onClick={() => fillDown(menuRow)}>
            Fill down from the row above
          </Button>
          <Button role="menuitem" size="sm" onClick={() => duplicate(menuRow)}>
            Duplicate {lowerNoun}
          </Button>
          <Button role="menuitem" size="sm" variant="danger" disabled={lines.length <= 1} onClick={() => remove(menuRow)}>
            Remove {lowerNoun}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => closeMenu(here)}>
            Close
          </Button>
        </div>
      ) : null}
      <div role="status" className={styles.status}>
        {removed ? (
          <>
            {noun} {removed.index + 1} removed.{" "}
            <Button size="sm" variant="ghost" onClick={undo}>
              Undo
            </Button>
          </>
        ) : null}
      </div>
      {listError ? (
        <p role="alert" className={styles.error}>
          {listError}
        </p>
      ) : null}
      <div>
        <Button ref={addRef} onClick={() => insertAt(lines.length, newLine(), 0)}>
          Add {lowerNoun}
        </Button>
      </div>
      <details className={styles.help}>
        <summary>Keyboard shortcuts</summary>
        <ul id={helpId}>
          <li>Arrow keys move between cells; Enter edits a cell and Enter again moves down.</li>
          <li>Tab and Shift+Tab move to the next or previous cell.</li>
          <li>Ctrl+Enter (Cmd+Enter) adds a {lowerNoun} below.</li>
          <li>Shift+F10 or the Menu key opens row actions.</li>
          <li>Escape cancels an edit; a second Escape leaves the grid.</li>
        </ul>
      </details>
    </div>
  );
}
