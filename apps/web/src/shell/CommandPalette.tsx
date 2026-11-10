import { useEffect, useMemo, useRef, useState } from "react";
import styles from "./CommandPalette.module.css";
import { copy } from "./copy";
import { trackSearchNoResults } from "./uxEvents";
import { useFocusTrap } from "./hooks/useFocusTrap";
import { isFavourite, pushRecent, readFavourites, readRecent, toggleFavourite } from "./search/recentRecords";
import { MIN_RECORD_QUERY, RECORD_TYPE_LABEL, RECORD_TYPE_NOUN, searchRecords as defaultSearchRecords, type RecordHit, type RecordSearch, type RecordType } from "./search/records";
import { Button, Icon, type IconName } from "./ui";

export interface PaletteEntry {
  id: string;
  group: string;
  label: string;
  to: string;
  icon?: IconName;
  keywords?: string;
  /** Set on a record result: the document or master record the entry opens. */
  record?: RecordHit;
  /** Status and key fields shown after the label, for a record result. */
  detail?: string;
}

export interface CommandPaletteProps {
  entries: PaletteEntry[];
  onSelect(entry: PaletteEntry): void;
  onClose(): void;
  announce(message: string): void;
  /** Whose recent and favourite records are kept; empty keeps none. */
  identity?: string;
  /** Finds records by number or name. Defaults to the search service. */
  searchRecords?: RecordSearch;
}

const SEARCH_DELAY_MS = 200;

const RECORD_ICON: Record<RecordType, IconName> = {
  party: "database",
  product: "box",
  purchase_order: "note",
  receipt: "receipt",
  vendor_invoice: "receipt",
  sales_order: "note",
  packing_slip: "box",
  customer_invoice: "receipt",
};

const statusWord = (status: string) => status.charAt(0).toUpperCase() + status.slice(1).toLowerCase().replace(/_/g, " ");

function recordEntry(hit: RecordHit, group: string): PaletteEntry {
  const label = hit.title && hit.title !== hit.number ? `${hit.number} ${hit.title}` : hit.number;
  const detail = [hit.status ? statusWord(hit.status) : "", hit.subtitle ?? ""].filter(Boolean).join(" · ");
  return { id: `record:${hit.type}:${hit.id}`, group, label, to: hit.link, icon: RECORD_ICON[hit.type], record: hit, detail: detail || undefined };
}

/** Result entries ordered so the entries of one type sit together, types in the order the service ranked them. */
function recordEntries(hits: RecordHit[]): PaletteEntry[] {
  const order: RecordType[] = [];
  for (const hit of hits) {
    if (!order.includes(hit.type)) {
      order.push(hit.type);
    }
  }
  return order.flatMap((type) => hits.filter((h) => h.type === type).map((h) => recordEntry(h, RECORD_TYPE_LABEL[type])));
}

function optionLabel(entry: PaletteEntry, favourites: RecordHit[]): string {
  if (!entry.record) {
    return `${entry.label}, ${entry.group}`;
  }
  const parts = [`${RECORD_TYPE_NOUN[entry.record.type]} ${entry.label}`, entry.detail ?? "", isFavourite(favourites, entry.record) ? copy.palette.favouriteState : ""];
  return parts.filter(Boolean).join(", ");
}

const ANNOUNCE_DELAY_MS = 250;

function filterEntries(entries: PaletteEntry[], query: string): PaletteEntry[] {
  const needle = query.trim().toLowerCase();
  if (!needle) {
    return entries;
  }
  return entries.filter((e) => [e.label, e.group, e.keywords ?? ""].some((text) => text.toLowerCase().includes(needle)));
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

export function CommandPalette({ entries, onSelect, onClose, announce, identity = "", searchRecords = defaultSearchRecords }: CommandPaletteProps) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [hits, setHits] = useState<RecordHit[]>([]);
  const [searchFailed, setSearchFailed] = useState(false);
  const [favourites, setFavourites] = useState<RecordHit[]>(() => readFavourites(identity));
  const [recent] = useState<RecordHit[]>(() => readRecent(identity));
  const panelRef = useRef<HTMLDivElement>(null);
  const announcedQuery = useRef("");
  const needle = query.trim();
  const searching = needle.length >= MIN_RECORD_QUERY;
  const visible = useMemo(() => {
    const pages = filterEntries(entries, query);
    if (searching) {
      return [...recordEntries(hits), ...pages];
    }
    if (needle !== "") {
      return pages;
    }
    const recents = recent.filter((r) => !isFavourite(favourites, r));
    return [...favourites.map((h) => recordEntry(h, copy.palette.favourites)), ...recents.map((h) => recordEntry(h, copy.palette.recent)), ...pages];
  }, [entries, query, searching, needle, hits, favourites, recent]);
  const groups = useMemo(() => groupEntries(visible), [visible]);
  const hasResults = visible.length > 0;

  useFocusTrap(panelRef, true);

  useEffect(() => {
    if (!searching) {
      setHits([]);
      setSearchFailed(false);
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      searchRecords(needle, controller.signal).then(
        (found) => {
          if (!controller.signal.aborted) {
            setHits(found);
            setSearchFailed(false);
          }
        },
        () => {
          if (!controller.signal.aborted) {
            setHits([]);
            setSearchFailed(true);
          }
        },
      );
    }, SEARCH_DELAY_MS);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [needle, searching, searchRecords]);

  useEffect(() => {
    if (query === announcedQuery.current) {
      return;
    }
    const timer = window.setTimeout(() => {
      announcedQuery.current = query;
      if (visible.length === 0) trackSearchNoResults(query);
      announce(visible.length === 0 ? copy.announce.noResults : visible.length === 1 ? copy.announce.oneResult : copy.announce.results(visible.length));
    }, ANNOUNCE_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [query, visible.length, announce]);

  useEffect(() => {
    document.getElementById(`palette-option-${active}`)?.scrollIntoView?.({ block: "nearest" });
  }, [active, visible]);

  const activeEntry = hasResults ? visible[Math.min(active, visible.length - 1)] : undefined;
  const activeRecord = activeEntry?.record;

  function choose(entry: PaletteEntry) {
    if (entry.record) {
      pushRecent(identity, entry.record);
    }
    onSelect(entry);
  }

  function toggleActiveFavourite() {
    if (!activeRecord || identity === "") {
      return;
    }
    const next = toggleFavourite(identity, activeRecord);
    setFavourites(next);
    announce(isFavourite(next, activeRecord) ? copy.announce.favouriteAdded(activeRecord.number) : copy.announce.favouriteRemoved(activeRecord.number));
  }

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
                  choose(visible[active]);
                }
              } else if (e.altKey && e.key.toLowerCase() === "f") {
                e.preventDefault();
                toggleActiveFavourite();
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
                  aria-label={optionLabel(entry, favourites)}
                  aria-selected={index === active}
                  className={styles.option}
                  onMouseMove={() => setActive(index)}
                  onClick={() => choose(entry)}
                >
                  <span className={styles.optionIcon}>
                    <Icon name={entry.icon ?? "grid"} />
                  </span>
                  <span className={styles.optionLabel}>{entry.label}</span>
                  {entry.detail ? <span className={styles.optionDetail}>{entry.detail}</span> : null}
                  {entry.record && isFavourite(favourites, entry.record) ? (
                    <span className={styles.favourite} aria-hidden="true">
                      ★
                    </span>
                  ) : null}
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
        {searchFailed ? (
          <p className={styles.note} role="status">
            {copy.palette.recordsUnavailable}
          </p>
        ) : null}
        {activeRecord && identity !== "" ? (
          <div className={styles.actions}>
            <Button variant="ghost" size="sm" onMouseDown={(e) => e.preventDefault()} onClick={toggleActiveFavourite}>
              {isFavourite(favourites, activeRecord) ? copy.palette.unfavourite(activeRecord.number) : copy.palette.favourite(activeRecord.number)}
            </Button>
          </div>
        ) : null}
        {hasResults ? null : (
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
          {activeRecord && identity !== "" ? (
            <span>
              <kbd>Alt</kbd> <kbd>F</kbd> to favourite
            </span>
          ) : null}
          <span>
            <kbd>Esc</kbd> to close
          </span>
        </footer>
      </div>
    </div>
  );
}
