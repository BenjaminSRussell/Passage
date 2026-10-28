import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { ALL, describeProblem, listLegalEntities, type LegalEntity, type Problem, type Scope } from "./api/client";

const STORAGE_KEY = "passage.org.legalEntity";
const ALL_SCOPE_DUTY = "org.switcher.all_scope";
const REFERENCE_WRITE_DUTY = "org.reference.write";
const HIERARCHY_WRITE_DUTY = "org.hierarchy.write";
const HIERARCHY_PUBLISH_DUTY = "org.hierarchy.publish";
const HIERARCHY_BACKDATE_DUTY = "org.hierarchy.backdate";

export interface LegalEntityContextValue {
  entities: LegalEntity[];
  scope: Scope;
  setScope(scope: Scope): void;
  canAll: boolean;
  canWriteReference: boolean;
  canWriteHierarchy: boolean;
  canPublishHierarchy: boolean;
  canBackdateHierarchy: boolean;
  loading: boolean;
  error: Problem | null;
}

const LegalEntityContext = createContext<LegalEntityContextValue | null>(null);

function readStored(): string | null {
  try {
    return window.localStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
}

function writeStored(value: string | null): void {
  try {
    if (value === null) {
      window.localStorage.removeItem(STORAGE_KEY);
    } else {
      window.localStorage.setItem(STORAGE_KEY, value);
    }
  } catch {
    return;
  }
}

export function LegalEntityProvider({ duties, children }: { duties?: readonly string[]; children: ReactNode }) {
  const canAll = Array.isArray(duties) && duties.includes(ALL_SCOPE_DUTY);
  const canWriteReference = Array.isArray(duties) && duties.includes(REFERENCE_WRITE_DUTY);
  const canWriteHierarchy = Array.isArray(duties) && duties.includes(HIERARCHY_WRITE_DUTY);
  const canPublishHierarchy = Array.isArray(duties) && duties.includes(HIERARCHY_PUBLISH_DUTY);
  const canBackdateHierarchy = Array.isArray(duties) && duties.includes(HIERARCHY_BACKDATE_DUTY);
  const [entities, setEntities] = useState<LegalEntity[]>([]);
  const [scope, setScopeState] = useState<Scope>("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Problem | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listLegalEntities(ALL, { signal: controller.signal }).then(
      ({ items }) => {
        if (controller.signal.aborted) {
          return;
        }
        const active = items.filter((entity) => entity.status !== "inactive");
        const stored = readStored();
        const restored = stored === ALL ? (canAll ? ALL : undefined) : active.find((entity) => entity.id === stored)?.id;
        if (stored !== null && restored === undefined) {
          writeStored(null);
        }
        setEntities(active);
        setScopeState(restored ?? active[0]?.id ?? (canAll ? ALL : ""));
        setLoading(false);
      },
      (failure) => {
        if (!controller.signal.aborted) {
          setError(describeProblem(failure));
          setLoading(false);
        }
      },
    );
    return () => controller.abort();
  }, [canAll]);

  const setScope = useCallback(
    (next: Scope) => {
      const allowed = next === ALL ? canAll : entities.some((entity) => entity.id === next);
      if (allowed) {
        setScopeState(next);
        writeStored(next);
      }
    },
    [canAll, entities],
  );

  const value = useMemo(
    () => ({ entities, scope, setScope, canAll, canWriteReference, canWriteHierarchy, canPublishHierarchy, canBackdateHierarchy, loading, error }),
    [entities, scope, setScope, canAll, canWriteReference, canWriteHierarchy, canPublishHierarchy, canBackdateHierarchy, loading, error],
  );
  return <LegalEntityContext.Provider value={value}>{children}</LegalEntityContext.Provider>;
}

export function useLegalEntity(): LegalEntityContextValue {
  const value = useContext(LegalEntityContext);
  if (!value) {
    throw new Error("useLegalEntity must be used inside LegalEntityProvider");
  }
  return value;
}
