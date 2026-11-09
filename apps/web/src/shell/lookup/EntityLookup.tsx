import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { Link } from "react-router-dom";
import { Input } from "../ui";
import styles from "./EntityLookup.module.css";
import {
  formatOption,
  lookupDefs,
  rememberOption,
  resolveOption,
  type LookupEntityKey,
  type LookupOption,
  type LookupScope,
} from "./entities";
import { pushRecent, readRecent } from "./recent";

const DEBOUNCE_MS = 200;

export interface LookupChange {
  target: { name: string; value: string };
  option: LookupOption | null;
}

export interface EntityLookupProps {
  entity: LookupEntityKey;
  id?: string;
  name?: string;
  value: string;
  onChange(event: LookupChange): void;
  scope?: LookupScope;
  allowCreate?: boolean;
  invalid?: boolean;
  disabled?: boolean;
  compact?: boolean;
  placeholder?: string;
  "aria-label"?: string;
  "aria-describedby"?: string;
  "aria-required"?: boolean;
  "aria-invalid"?: boolean | "true" | "false";
}

type Phase = "idle" | "loading" | "error";

export function EntityLookup({
  entity,
  id,
  name = "",
  value,
  onChange,
  scope,
  allowCreate = false,
  invalid,
  disabled,
  compact,
  placeholder,
  ...aria
}: EntityLookupProps) {
  const def = lookupDefs[entity];
  const generated = useId();
  const inputId = id ?? generated;
  const listId = `${inputId}-listbox`;
  const [selected, setSelected] = useState<LookupOption | null>(null);
  const [typed, setTyped] = useState<string | null>(null);
  const [results, setResults] = useState<LookupOption[]>([]);
  const [recent, setRecent] = useState<LookupOption[]>([]);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [phase, setPhase] = useState<Phase>("idle");
  const requestId = useRef(0);
  const scopeKey = `${scope?.legalEntityId ?? ""}|${scope?.siteId ?? ""}`;

  useEffect(() => {
    if (value === "") {
      setSelected(null);
      return;
    }
    if (selected?.id === value) {
      return;
    }
    let live = true;
    void resolveOption(entity, value).then((entry) => {
      if (live) {
        setSelected(entry);
      }
    });
    return () => {
      live = false;
    };
  }, [entity, value, selected?.id]);

  const query = typed ?? "";
  useEffect(() => {
    if (!open) {
      return;
    }
    const mine = ++requestId.current;
    const controller = new AbortController();
    setPhase("loading");
    const timer = window.setTimeout(
      () => {
        def.search(query.trim(), scope ?? {}, controller.signal).then(
          (found) => {
            if (mine === requestId.current) {
              setResults(found);
              setActive(0);
              setPhase("idle");
            }
          },
          () => {
            if (mine === requestId.current && !controller.signal.aborted) {
              setResults([]);
              setPhase("error");
            }
          },
        );
      },
      query === "" ? 0 : DEBOUNCE_MS,
    );
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [open, query, scopeKey, def]);

  const showRecent = open && query.trim() === "";
  const recentOptions = showRecent
    ? recent.filter((entry) => entry.id !== "")
    : [];
  const listed =
    showRecent && recentOptions.length > 0
      ? [
          ...recentOptions,
          ...results.filter(
            (entry) => !recentOptions.some((r) => r.id === entry.id),
          ),
        ]
      : results;

  const openList = useCallback(() => {
    setRecent(readRecent(entity));
    setOpen(true);
  }, [entity]);

  function choose(entry: LookupOption) {
    rememberOption(entity, entry);
    pushRecent(entity, entry);
    setSelected(entry);
    setTyped(null);
    setOpen(false);
    onChange({ target: { name, value: entry.id }, option: entry });
  }

  function clear() {
    setSelected(null);
    setTyped(null);
    onChange({ target: { name, value: "" }, option: null });
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      if (!open) {
        openList();
      } else {
        setActive((index) =>
          Math.min(index + 1, Math.max(listed.length - 1, 0)),
        );
      }
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setActive((index) => Math.max(index - 1, 0));
    } else if (event.key === "Home" && open) {
      event.preventDefault();
      setActive(0);
    } else if (event.key === "End" && open) {
      event.preventDefault();
      setActive(Math.max(listed.length - 1, 0));
    } else if (event.key === "Enter" && open && listed[active]) {
      event.preventDefault();
      choose(listed[active]);
    } else if (event.key === "Escape" && open) {
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      setTyped(null);
    }
  }

  const label = selected
    ? formatOption(selected)
    : value !== ""
      ? `Selected ${def.noun}`
      : "";
  const status =
    phase === "loading"
      ? "Searching…"
      : phase === "error"
        ? `Could not load ${def.noun} matches. Keep typing to try again.`
        : listed.length === 0
          ? `No ${def.noun} found.`
          : `${listed.length} ${listed.length === 1 ? "match" : "matches"}.`;
  const activeId =
    open && listed[active] ? `${inputId}-option-${active}` : undefined;

  return (
    <div className={`${styles.root} ${compact ? styles.compact : ""}`}>
      <div className={styles.field}>
        <Input
          id={inputId}
          role="combobox"
          aria-expanded={open}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={activeId}
          aria-label={aria["aria-label"]}
          aria-describedby={aria["aria-describedby"]}
          aria-required={aria["aria-required"]}
          aria-invalid={aria["aria-invalid"]}
          autoComplete="off"
          invalid={invalid}
          disabled={disabled}
          placeholder={placeholder ?? `Search ${def.noun}s by name or number`}
          value={typed ?? label}
          onFocus={openList}
          onChange={(e) => {
            setTyped(e.target.value);
            if (!open) {
              openList();
            }
          }}
          onBlur={() => {
            window.setTimeout(() => {
              setOpen(false);
              setTyped(null);
            }, 120);
          }}
          onKeyDown={onKeyDown}
        />
        {value !== "" && !disabled ? (
          <button
            type="button"
            className={styles.clear}
            aria-label={`Clear ${def.noun}`}
            onClick={clear}
          >
            ×
          </button>
        ) : null}
      </div>
      <input type="hidden" name={name || undefined} value={value} readOnly />
      {open ? (
        <ul
          id={listId}
          role="listbox"
          aria-label={`${def.noun} matches`}
          className={styles.listbox}
        >
          {showRecent && recentOptions.length > 0 ? (
            <li role="presentation" className={styles.group}>
              Recent
            </li>
          ) : null}
          {listed.map((entry, index) => (
            <li
              key={entry.id}
              id={`${inputId}-option-${index}`}
              role="option"
              aria-selected={index === active}
              className={styles.option}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => choose(entry)}
              onMouseMove={() => setActive(index)}
            >
              <span>{entry.name}</span>
              {entry.number ? (
                <span className={styles.number}>{entry.number}</span>
              ) : null}
            </li>
          ))}
          <li role="presentation" className={styles.status}>
            {status}
          </li>
        </ul>
      ) : null}
      <div role="status" aria-live="polite" className="visually-hidden">
        {open ? status : ""}
      </div>
      {allowCreate && def.createHref ? (
        <Link className={styles.create} to={def.createHref}>
          {`Create a new ${def.noun}`}
        </Link>
      ) : null}
    </div>
  );
}
