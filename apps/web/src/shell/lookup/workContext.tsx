import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { EntityLookup } from "./EntityLookup";
import { lookupDefs } from "./entities";

const STORAGE_KEY = "passage.workContext";

export interface WorkContextValue {
  legalEntityId: string;
  siteId: string;
  setLegalEntityId(id: string): void;
  setSiteId(id: string): void;
}

const empty: WorkContextValue = {
  legalEntityId: "",
  siteId: "",
  setLegalEntityId: () => undefined,
  setSiteId: () => undefined,
};

const WorkContext = createContext<WorkContextValue>(empty);

interface Stored {
  legalEntityId: string;
  siteId: string;
}

function read(): Stored {
  try {
    const parsed = JSON.parse(
      window.localStorage.getItem(STORAGE_KEY) ?? "{}",
    ) as Partial<Stored>;
    return {
      legalEntityId:
        typeof parsed.legalEntityId === "string" ? parsed.legalEntityId : "",
      siteId: typeof parsed.siteId === "string" ? parsed.siteId : "",
    };
  } catch {
    return { legalEntityId: "", siteId: "" };
  }
}

function write(value: Stored): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(value));
  } catch {
    return;
  }
}

export function WorkContextProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<Stored>(read);
  const update = useCallback((next: Stored) => {
    write(next);
    setState(next);
  }, []);
  const setLegalEntityId = useCallback(
    (id: string) => update({ legalEntityId: id, siteId: "" }),
    [update],
  );
  const setSiteId = useCallback(
    (id: string) => update({ ...state, siteId: id }),
    [update, state],
  );
  const value = useMemo(
    () => ({ ...state, setLegalEntityId, setSiteId }),
    [state, setLegalEntityId, setSiteId],
  );
  return <WorkContext.Provider value={value}>{children}</WorkContext.Provider>;
}

export function useWorkContext(): WorkContextValue {
  return useContext(WorkContext);
}

export function ContextPicker() {
  const { legalEntityId, siteId, setLegalEntityId, setSiteId } =
    useWorkContext();

  useEffect(() => {
    if (legalEntityId !== "") {
      return;
    }
    const controller = new AbortController();
    lookupDefs.legalEntity.search("", {}, controller.signal).then(
      (found) => {
        if (found.length === 1) {
          setLegalEntityId(found[0].id);
        }
      },
      () => undefined,
    );
    return () => controller.abort();
  }, [legalEntityId, setLegalEntityId]);

  return (
    <div
      role="group"
      aria-label="Working context"
      style={{ display: "flex", gap: "var(--space-2)", alignItems: "center" }}
    >
      <EntityLookup
        compact
        entity="legalEntity"
        aria-label="Current legal entity"
        placeholder="Choose legal entity"
        value={legalEntityId}
        onChange={(e) => setLegalEntityId(e.target.value)}
      />
      <EntityLookup
        compact
        entity="site"
        aria-label="Current site"
        placeholder="Choose site"
        scope={{ legalEntityId: legalEntityId || undefined }}
        value={siteId}
        onChange={(e) => setSiteId(e.target.value)}
      />
    </div>
  );
}
