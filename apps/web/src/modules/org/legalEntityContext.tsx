import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ALL, describeProblem, listLegalEntities, type LegalEntity, type Problem, type Scope } from "./api/client";

const STORAGE_KEY = "passage.org.legalEntity";
const ALL_SCOPE_DUTY = "org.switcher.all_scope";
const REFERENCE_WRITE_DUTY = "org.reference.write";
const LEGAL_ENTITY_WRITE_DUTY = "org.legal-entity.write";
const FISCAL_CALENDAR_WRITE_DUTY = "org.fiscal-calendar.write";
const OPERATING_UNIT_WRITE_DUTY = "org.operating-unit.write";
const SITE_WRITE_DUTY = "org.site.write";
// The org service refuses tenant-level writes (new legal entities, fiscal calendars and years, teams) unless the
// caller also holds tenant.administer and carries no legal-entity claim (rejectScopedCaller in services/org).
const TENANT_ADMIN_DUTY = "tenant.administer";
const HIERARCHY_WRITE_DUTY = "org.hierarchy.write";
const HIERARCHY_PUBLISH_DUTY = "org.hierarchy.publish";
const HIERARCHY_BACKDATE_DUTY = "org.hierarchy.backdate";
const PACKAGE_DUTY = "org.package.manage";

export interface LegalEntityContextValue {
  entities: LegalEntity[];
  scope: Scope;
  setScope(scope: Scope): void;
  // reload re-reads the entity list (after a create or a deactivation) and keeps the chosen scope while it is still valid.
  reload(): void;
  canAll: boolean;
  canWriteReference: boolean;
  canWriteLegalEntity: boolean;
  canWriteFiscalCalendar: boolean;
  canWriteOperatingUnit: boolean;
  canWriteSite: boolean;
  canAdministerTenant: boolean;
  canWriteHierarchy: boolean;
  // Export and import of the organization model package: org.package.manage with tenant.administer.
  canManagePackage: boolean;
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
  const held = (duty: string) => Array.isArray(duties) && duties.includes(duty);
  const canAdministerTenant = held(TENANT_ADMIN_DUTY);
  // Reference status and unit writes, and every hierarchy write, are tenant-level in the service
  // (rejectScopedCaller): the route duty alone is refused with 403 AUTH-0003, so the actions need both.
  const canWriteReference = held(REFERENCE_WRITE_DUTY) && canAdministerTenant;
  const canWriteLegalEntity = Array.isArray(duties) && duties.includes(LEGAL_ENTITY_WRITE_DUTY);
  const canWriteFiscalCalendar = Array.isArray(duties) && duties.includes(FISCAL_CALENDAR_WRITE_DUTY);
  const canWriteOperatingUnit = Array.isArray(duties) && duties.includes(OPERATING_UNIT_WRITE_DUTY);
  const canWriteSite = held(SITE_WRITE_DUTY);
  const canWriteHierarchy = held(HIERARCHY_WRITE_DUTY) && canAdministerTenant;
  const canManagePackage = held(PACKAGE_DUTY) && canAdministerTenant;
  const canPublishHierarchy = held(HIERARCHY_PUBLISH_DUTY) && canAdministerTenant;
  const canBackdateHierarchy = Array.isArray(duties) && duties.includes(HIERARCHY_BACKDATE_DUTY);
  const [entities, setEntities] = useState<LegalEntity[]>([]);
  const [scope, setScopeState] = useState<Scope>("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Problem | null>(null);
  const [reloads, setReloads] = useState(0);
  const current = useRef<Scope>("");
  const reload = useCallback(() => setReloads((count) => count + 1), []);

  useEffect(() => {
    const controller = new AbortController();
    listLegalEntities(ALL, { signal: controller.signal }).then(
      ({ items }) => {
        if (controller.signal.aborted) {
          return;
        }
        const active = items.filter((entity) => entity.status !== "inactive");
        const stored = current.current !== "" ? current.current : readStored();
        const restored = stored === ALL ? (canAll ? ALL : undefined) : active.find((entity) => entity.id === stored)?.id;
        if (stored !== null && restored === undefined) {
          writeStored(null);
        }
        const next = restored ?? active[0]?.id ?? (canAll ? ALL : "");
        current.current = next;
        setEntities(active);
        setScopeState(next);
        setError(null);
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
  }, [canAll, reloads]);

  const setScope = useCallback(
    (next: Scope) => {
      const allowed = next === ALL ? canAll : entities.some((entity) => entity.id === next);
      if (allowed) {
        current.current = next;
        setScopeState(next);
        writeStored(next);
      }
    },
    [canAll, entities],
  );

  const value = useMemo(
    () => ({ entities, scope, setScope, reload, canAll, canWriteReference, canWriteLegalEntity, canWriteFiscalCalendar, canWriteOperatingUnit, canWriteSite, canAdministerTenant, canWriteHierarchy, canManagePackage, canPublishHierarchy, canBackdateHierarchy, loading, error }),
    [entities, scope, setScope, reload, canAll, canWriteReference, canWriteLegalEntity, canWriteFiscalCalendar, canWriteOperatingUnit, canWriteSite, canAdministerTenant, canWriteHierarchy, canManagePackage, canPublishHierarchy, canBackdateHierarchy, loading, error],
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
