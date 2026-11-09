import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import type { PassageModule } from "../../modules/registry";
import {
  effectivePrefs,
  groupByArea,
  loadWorkspaceConfig,
  resolveNavEntries,
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
    const { held, active } = (parsed ?? {}) as { held?: unknown; active?: unknown };
    return {
      held: Array.isArray(held) ? held.filter((h): h is string => typeof h === "string") : undefined,
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
  modules: PassageModule[];
  config?: WorkspaceConfig;
  children: ReactNode;
}

export function WorkspaceNavProvider({ modules, config, children }: WorkspaceNavProviderProps) {
  const resolvedConfig = useMemo(() => config ?? loadWorkspaceConfig(), [config]);
  const [prefs, setPrefs] = useState<WorkspacePrefs>(readPrefs);
  const entries = useMemo(() => resolveNavEntries(modules), [modules]);
  const areaGroups = useMemo(() => groupByArea(entries, resolvedConfig.areas), [entries, resolvedConfig]);
  const available = useMemo(() => resolveWorkspaces(resolvedConfig, entries), [resolvedConfig, entries]);
  const { held, active } = useMemo(() => effectivePrefs(prefs, available), [prefs, available]);

  const update = useCallback((next: WorkspacePrefs) => {
    setPrefs(next);
    writePrefs(next);
  }, []);

  const setActive = useCallback((id: string) => update({ held: prefs.held, active: id }), [prefs.held, update]);

  const toggleHeld = useCallback(
    (id: string) => {
      const heldIds = held.map((w) => w.id);
      const nextIds = heldIds.includes(id) ? heldIds.filter((h) => h !== id) : [...heldIds, id];
      if (nextIds.length === 0) {
        return;
      }
      update({ held: nextIds, active: active !== undefined && nextIds.includes(active.id) ? active.id : nextIds[0] });
    },
    [held, active, update],
  );

  const value = useMemo(
    () => ({ entries, areaGroups, available, held, active, setActive, toggleHeld }),
    [entries, areaGroups, available, held, active, setActive, toggleHeld],
  );

  return <WorkspaceNavContext.Provider value={value}>{children}</WorkspaceNavContext.Provider>;
}
