import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { copy } from "../copy";
import { EntityLookup } from "../lookup/EntityLookup";
import { formatOption, resolveOption, type LookupEntityKey } from "../lookup/entities";
import { Button } from "./Button";
import { FormField } from "./FormField";
import { Input } from "./Input";
import styles from "./ListPage.module.css";
import { activeFilters, rangeKeys, withoutFilter, type FilterValues, type ListFilterDef, type SavedView, type ViewStore } from "./listViews";
import { Select } from "./Select";

const FOCUSABLE = "input, select, button, [href], [tabindex]:not([tabindex='-1'])";

export function Disclosure({ label, children }: { label: string; children: (api: { focusPanel(): void }) => ReactNode }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const panelId = useId();

  useEffect(() => {
    if (!open) {
      return;
    }
    panelRef.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus();
    const away = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("pointerdown", away);
    return () => document.removeEventListener("pointerdown", away);
  }, [open]);

  function close(restoreFocus: boolean) {
    setOpen(false);
    if (restoreFocus) {
      triggerRef.current?.focus();
    }
  }

  return (
    <div
      ref={rootRef}
      className={styles.disclosure}
      onKeyDown={(event) => {
        if (event.key === "Escape" && open) {
          event.stopPropagation();
          close(true);
        }
      }}
      onBlur={(event) => {
        const next = event.relatedTarget as Node | null;
        if (open && next && !rootRef.current?.contains(next)) {
          setOpen(false);
        }
      }}
    >
      <Button ref={triggerRef} size="sm" aria-expanded={open} aria-controls={open ? panelId : undefined} onClick={() => (open ? close(false) : setOpen(true))}>
        {label}
      </Button>
      {open ? (
        <div ref={panelRef} id={panelId} role="group" aria-label={label} tabIndex={-1} className={styles.panel}>
          {children({ focusPanel: () => panelRef.current?.focus() })}
        </div>
      ) : null}
    </div>
  );
}

function useEntityText(entity: LookupEntityKey, id: string): string {
  const [text, setText] = useState("");
  useEffect(() => {
    let live = true;
    setText("");
    if (id === "") {
      return;
    }
    void resolveOption(entity, id).then((found) => {
      if (live) {
        setText(found ? formatOption(found) : "");
      }
    });
    return () => {
      live = false;
    };
  }, [entity, id]);
  return text;
}

function FilterField({ listId, def, draft, onChange }: { listId: string; def: ListFilterDef; draft: FilterValues; onChange(values: FilterValues): void }) {
  const id = `${listId}-filter-${def.key}`;
  const set = (key: string, value: string) => onChange({ ...draft, [key]: value });
  if (def.kind === "dateRange") {
    const { from, to } = rangeKeys(def.key);
    return (
      <>
        <FormField id={`${id}-from`} label={`${def.label} ${copy.lists.from.toLowerCase()}`}>
          {(c) => <Input {...c} type="date" value={draft[from] ?? ""} onChange={(e) => set(from, e.target.value)} />}
        </FormField>
        <FormField id={`${id}-to`} label={`${def.label} ${copy.lists.to.toLowerCase()}`}>
          {(c) => <Input {...c} type="date" value={draft[to] ?? ""} onChange={(e) => set(to, e.target.value)} />}
        </FormField>
      </>
    );
  }
  return (
    <FormField id={id} label={def.label}>
      {(c) => {
        if (def.kind === "entity") {
          return <EntityLookup entity={def.entity} prefill={false} {...c} value={draft[def.key] ?? ""} onChange={(e) => set(def.key, e.target.value)} />;
        }
        if (def.kind === "select") {
          return (
            <Select {...c} value={draft[def.key] ?? ""} onChange={(e) => set(def.key, e.target.value)}>
              <option value="">{def.anyLabel ?? copy.lists.anyValue}</option>
              {def.options.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          );
        }
        return <Input {...c} type="search" value={draft[def.key] ?? ""} onChange={(e) => set(def.key, e.target.value)} />;
      }}
    </FormField>
  );
}

export function FilterBar({
  listId,
  label,
  defs,
  draft,
  onDraftChange,
  onApply,
}: {
  listId: string;
  label: string;
  defs: ListFilterDef[];
  draft: FilterValues;
  onDraftChange(values: FilterValues): void;
  onApply(): void;
}) {
  function submit(event: FormEvent) {
    event.preventDefault();
    onApply();
  }
  return (
    <form role="search" aria-label={label} className={styles.filterBar} onSubmit={submit} noValidate>
      {defs.map((def) => (
        <FilterField key={def.key} listId={listId} def={def} draft={draft} onChange={onDraftChange} />
      ))}
      <Button type="submit">{copy.lists.apply}</Button>
    </form>
  );
}

function ChipText({ def, values }: { def: ListFilterDef; values: FilterValues }) {
  const entityId = def.kind === "entity" ? (values[def.key] ?? "") : "";
  const entityText = useEntityText(def.kind === "entity" ? def.entity : "vendor", entityId);
  if (def.kind === "entity") {
    return <>{`${def.label}: ${entityText || "…"}`}</>;
  }
  if (def.kind === "select") {
    const chosen = def.options.find((o) => o.value === values[def.key]);
    return <>{`${def.label}: ${chosen?.label ?? values[def.key]}`}</>;
  }
  if (def.kind === "dateRange") {
    const { from, to } = rangeKeys(def.key);
    const parts = [values[from] ? `${copy.lists.from.toLowerCase()} ${values[from]}` : "", values[to] ? `${copy.lists.to.toLowerCase()} ${values[to]}` : ""];
    return <>{`${def.label}: ${parts.filter(Boolean).join(" ")}`}</>;
  }
  return <>{`${def.label}: ${values[def.key]}`}</>;
}

export function FilterChips({
  defs,
  applied,
  onChange,
  onClearAll,
  onEmptied,
}: {
  defs: ListFilterDef[];
  applied: FilterValues;
  onChange(values: FilterValues): void;
  onClearAll(): void;
  onEmptied(): void;
}) {
  const active = activeFilters(defs, applied);
  if (active.length === 0) {
    return null;
  }
  return (
    <div className={styles.chips}>
      <ul aria-label={copy.lists.activeFilters} className={styles.chipList}>
        {active.map(({ def, values }) => (
          <li key={def.key} className={styles.chip}>
            <span>
              <ChipText def={def} values={values} />
            </span>
            <button type="button" className={styles.chipRemove} aria-label={copy.lists.removeFilter(def.label)} onClick={(event) => {
                const item = event.currentTarget.closest("li");
                const next = (item?.nextElementSibling ?? item?.previousElementSibling)?.querySelector("button");
                if (next) {
                  next.focus();
                } else {
                  onEmptied();
                }
                onChange(withoutFilter(applied, def));
              }}>
              ×
            </button>
          </li>
        ))}
      </ul>
      <Button variant="ghost" size="sm" onClick={onClearAll}>
        {copy.lists.clearAll}
      </Button>
    </div>
  );
}

export function ColumnChooser({
  columns,
  visible,
  onToggle,
  onReset,
}: {
  columns: { key: string; header: string }[];
  visible: string[];
  onToggle(key: string): void;
  onReset(): void;
}) {
  const hint = useId();
  const prefix = useId();
  return (
    <Disclosure label={copy.lists.columns}>
      {() => (
        <>
          <p id={hint} className={styles.panelHelp}>
            {copy.lists.columnsHelp}
          </p>
          <ul className={styles.checkList} aria-describedby={hint}>
            {columns.map((column) => {
              const checked = visible.includes(column.key);
              const last = checked && visible.length === 1;
              return (
                <li key={column.key}>
                  <span className={styles.checkRow}>
                    <input id={`${prefix}-${column.key}`} type="checkbox" checked={checked} disabled={last} onChange={() => onToggle(column.key)} />
                    <label htmlFor={`${prefix}-${column.key}`}>{column.header}</label>
                  </span>
                </li>
              );
            })}
          </ul>
          {visible.length === 1 ? <p className={styles.panelHelp}>{copy.lists.keepOneColumn}</p> : null}
          <Button size="sm" variant="ghost" onClick={onReset}>
            {copy.lists.resetColumns}
          </Button>
        </>
      )}
    </Disclosure>
  );
}

export interface ViewActions {
  onApply(id: string): void;
  onSaveNew(name: string): void;
  onUpdate(id: string): void;
  onDelete(id: string): void;
  onSetDefault(id: string | null): void;
}

export function ViewsMenu({ store, active, modified, actions }: { store: ViewStore; active: SavedView | undefined; modified: boolean; actions: ViewActions }) {
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const inputId = useId();

  function save(event: FormEvent) {
    event.preventDefault();
    if (name.trim() === "") {
      setError(copy.lists.viewNameRequired);
      return;
    }
    setError("");
    actions.onSaveNew(name.trim());
    setName("");
  }

  return (
    <Disclosure label={copy.lists.views}>
      {({ focusPanel }) => (
        <>
          {store.views.length === 0 ? (
            <p className={styles.panelHelp}>{copy.lists.noViews}</p>
          ) : (
            <ul className={styles.viewList}>
              {store.views.map((view) => (
                <li key={view.id} className={styles.viewRow}>
                  <Button size="sm" variant={view.id === active?.id ? "primary" : "secondary"} aria-label={copy.lists.applyView(view.name)} aria-pressed={view.id === active?.id} onClick={() => actions.onApply(view.id)}>
                    {view.name}
                  </Button>
                  {view.id === store.defaultId ? <span className={styles.defaultMark}>{copy.lists.defaultView}</span> : null}
                  <Button size="sm" variant="ghost" onClick={() => actions.onSetDefault(view.id === store.defaultId ? null : view.id)}>
                    {view.id === store.defaultId ? copy.lists.clearDefault(view.name) : copy.lists.makeDefault(view.name)}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      actions.onDelete(view.id);
                      focusPanel();
                    }}
                  >
                    {copy.lists.deleteView(view.name)}
                  </Button>
                </li>
              ))}
            </ul>
          )}
          {active && modified ? (
            <Button
              size="sm"
              onClick={() => {
                actions.onUpdate(active.id);
                focusPanel();
              }}
            >
              {copy.lists.updateView(active.name)}
            </Button>
          ) : null}
          <form className={styles.saveForm} onSubmit={save} noValidate>
            <FormField id={inputId} label={copy.lists.viewName} error={error || undefined}>
              {(c) => <Input {...c} value={name} onChange={(e) => setName(e.target.value)} />}
            </FormField>
            <Button type="submit" size="sm">
              {copy.lists.saveAsNew}
            </Button>
          </form>
        </>
      )}
    </Disclosure>
  );
}
