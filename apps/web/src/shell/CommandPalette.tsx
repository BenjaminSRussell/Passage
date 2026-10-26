import { useEffect, useMemo, useRef, useState } from "react";
import styles from "./CommandPalette.module.css";
import { copy } from "./copy";
import { useFocusTrap } from "./hooks/useFocusTrap";
import { Button, Icon, type IconName } from "./ui";

export interface PaletteEntry {
  id: string;
  group: string;
  label: string;
  to: string;
  icon?: IconName;
}

export interface CommandPaletteProps {
  entries: PaletteEntry[];
  onSelect(entry: PaletteEntry): void;
  onClose(): void;
  announce(message: string): void;
}

const ANNOUNCE_DELAY_MS = 250;

function filterEntries(entries: PaletteEntry[], query: string): PaletteEntry[] {
  const needle = query.trim().toLowerCase();
  if (!needle) {
    return entries;
  }
  return entries.filter((e) => e.label.toLowerCase().includes(needle) || e.group.toLowerCase().includes(needle));
}

function groupEntries(visible: PaletteEntry[]): { group: string; items: { entry: PaletteEntry; index: number }[] }[] {
  const groups: { group: string; items: { entry: PaletteEntry; index: number }[] }[] = [];
  visible.forEach((entry, index) => {
    const last = groups[groups.length - 1];
    if (last && last.group === entry.group) {
      last.items.push({ entry, index });
    } else {
      groups.push({ group: entry.group, items: [{ entry, index }] });
    }
  });
  return groups;
}

export function CommandPalette({ entries, onSelect, onClose, announce }: CommandPaletteProps) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const panelRef = useRef<HTMLDivElement>(null);
  const announcedQuery = useRef("");
  const visible = useMemo(() => filterEntries(entries, query), [entries, query]);
  const groups = useMemo(() => groupEntries(visible), [visible]);
  const hasResults = visible.length > 0;

  useFocusTrap(panelRef, true);

  useEffect(() => {
    if (query === announcedQuery.current) {
      return;
    }
    const timer = window.setTimeout(() => {
      announcedQuery.current = query;
      announce(visible.length === 0 ? copy.announce.noResults : visible.length === 1 ? copy.announce.oneResult : copy.announce.results(visible.length));
    }, ANNOUNCE_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [query, visible.length, announce]);

  useEffect(() => {
    document.getElementById(`palette-option-${active}`)?.scrollIntoView?.({ block: "nearest" });
  }, [active, visible]);

  function move(delta: number) {
    if (!hasResults) {
      return;
    }
    setActive((current) => (current + delta + visible.length) % visible.length);
  }

  return (
    <div
      className={styles.overlay}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) {
          onClose();
        }
      }}
    >
      <div ref={panelRef} role="dialog" aria-modal="true" aria-labelledby="palette-title" className={styles.panel}>
        <h2 id="palette-title" className="visually-hidden">
          {copy.palette.title}
        </h2>
        <div className={styles.inputRow}>
          <span className={styles.searchIcon}>
            <Icon name="search" size={20} />
          </span>
          <label htmlFor="global-search" className="visually-hidden">
            {copy.palette.inputLabel}
          </label>
          <input
            id="global-search"
            className={styles.input}
            role="combobox"
            aria-expanded={hasResults}
            aria-controls="palette-listbox"
            aria-autocomplete="list"
            aria-activedescendant={hasResults ? `palette-option-${active}` : undefined}
            aria-describedby="palette-help"
            autoComplete="off"
            spellCheck={false}
            placeholder={copy.palette.placeholder}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActive(0);
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                move(1);
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                move(-1);
              } else if (e.key === "Enter") {
                e.preventDefault();
                if (hasResults) {
                  onSelect(visible[active]);
                }
              } else if (e.key === "Escape") {
                e.stopPropagation();
                onClose();
              }
            }}
          />
          <Button variant="ghost" size="sm" aria-label={copy.buttons.closeSearch} onClick={onClose}>
            <Icon name="close" />
          </Button>
        </div>
        <p id="palette-help" className="visually-hidden">
          {copy.palette.help}
        </p>
        {hasResults ? (
          <div
            id="palette-listbox"
            role="listbox"
            aria-label={copy.palette.listLabel}
            className={styles.list}
            onMouseDown={(e) => e.preventDefault()}
          >
            {groups.map((g, gi) => (
              <div key={g.group} role="group" aria-labelledby={`palette-group-${gi}`}>
                <div id={`palette-group-${gi}`} role="presentation" className={styles.groupLabel}>
                  {g.group}
                </div>
                {g.items.map(({ entry, index }) => (
                  <div
                    key={entry.id}
                    role="option"
                    id={`palette-option-${index}`}
                    aria-selected={index === active}
                    className={styles.option}
                    onMouseMove={() => setActive(index)}
                    onClick={() => onSelect(entry)}
                  >
                    <span className={styles.optionIcon}>
                      <Icon name={entry.icon ?? "grid"} />
                    </span>
                    <span className={styles.optionLabel}>{entry.label}</span>
                    {index === active ? (
                      <span className={styles.enterHint} aria-hidden="true">
                        ↵
                      </span>
                    ) : null}
                  </div>
                ))}
              </div>
            ))}
          </div>
        ) : (
          <div className={styles.empty}>
            <Icon name="search" size={20} />
            <p className={styles.emptyTitle}>{copy.palette.noResults(query)}</p>
            <p className={styles.emptyHint}>{copy.palette.noResultsHint}</p>
          </div>
        )}
        <footer className={styles.footer} aria-hidden="true">
          <span>
            <kbd>↑</kbd> <kbd>↓</kbd> to move
          </span>
          <span>
            <kbd>↵</kbd> to open
          </span>
          <span>
            <kbd>Esc</kbd> to close
          </span>
        </footer>
      </div>
    </div>
  );
}
