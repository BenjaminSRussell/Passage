import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import {
  effectivePrefs,
  groupByArea,
  resolveWorkspaces,
  type AreaGroup,
  type NavEntry,
  type ResolvedWorkspace,
  type WorkspaceConfig,
  type WorkspacePrefs,
} from "./model";

const STORAGE_KEY = "passage.workspaces.v1";

function readPrefs(): WorkspacePrefs {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    const parsed: unknown = raw === null ? {} : JSON.parse(raw);
    const { dropped, active } = (parsed ?? {}) as { dropped?: unknown; active?: unknown };
    return {
      dropped: Array.isArray(dropped) ? dropped.filter((d): d is string => typeof d === "string") : undefined,
      active: typeof active === "string" ? active : undefined,
    };
  } catch {
    return {};
  }
}

function writePrefs(prefs: WorkspacePrefs): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
  } catch {
    return;
  }
}

export interface WorkspaceNavValue {
  entries: NavEntry[];
  areaGroups: AreaGroup[];
  available: ResolvedWorkspace[];
  held: ResolvedWorkspace[];
  active: ResolvedWorkspace | undefined;
  setActive(id: string): void;
  toggleHeld(id: string): void;
}

const emptyValue: WorkspaceNavValue = {
  entries: [],
  areaGroups: [],
  available: [],
  held: [],
  active: undefined,
  setActive: () => undefined,
  toggleHeld: () => undefined,
};

const WorkspaceNavContext = createContext<WorkspaceNavValue>(emptyValue);

export function useWorkspaceNav(): WorkspaceNavValue {
  return useContext(WorkspaceNavContext);
}

export interface WorkspaceNavProviderProps {
  entries: NavEntry[];
  config: WorkspaceConfig;
  children: ReactNode;
}

export function WorkspaceNavProvider({ entries, config, children }: WorkspaceNavProviderProps) {
  const [prefs, setPrefs] = useState<WorkspacePrefs>(readPrefs);
  const areaGroups = useMemo(() => groupByArea(entries, config.areas), [entries, config]);
  const available = useMemo(() => resolveWorkspaces(config, entries), [config, entries]);
  const { held, active } = useMemo(() => effectivePrefs(prefs, available), [prefs, available]);

  const update = useCallback((next: WorkspacePrefs) => {
    setPrefs(next);
    writePrefs(next);
  }, []);

  const setActive = useCallback((id: string) => update({ dropped: prefs.dropped, active: id }), [prefs.dropped, update]);

  const toggleHeld = useCallback(
    (id: string) => {
      const heldIds = held.map((w) => w.id);
      const dropped = prefs.dropped ?? [];
      if (heldIds.includes(id)) {
        if (heldIds.length === 1) {
          return;
        }
        update({ dropped: [...dropped, id], active: active?.id === id ? undefined : active?.id });
      } else {
        update({ dropped: dropped.filter((d) => d !== id), active: active?.id });
      }
    },
    [held, active, prefs.dropped, update],
  );

  const value = useMemo(
    () => ({ entries, areaGroups, available, held, active, setActive, toggleHeld }),
    [entries, areaGroups, available, held, active, setActive, toggleHeld],
  );

  return <WorkspaceNavContext.Provider value={value}>{children}</WorkspaceNavContext.Provider>;
}
