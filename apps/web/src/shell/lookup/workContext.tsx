import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { EntityLookup } from "./EntityLookup";
import { lookupDefs, rememberOption, resolveOption } from "./entities";
import { useWorkContext, WorkContext } from "./workContextState";

export { useWorkContext };

const storageKey = (identity: string) => `passage.workContext.${identity}`;

interface Stored {
  legalEntityId: string;
  siteId: string;
}

const none: Stored = { legalEntityId: "", siteId: "" };

function read(identity: string): Stored {
  if (identity === "") {
    return none;
  }
  try {
    const parsed = JSON.parse(
      window.localStorage.getItem(storageKey(identity)) ?? "{}",
    ) as Partial<Stored>;
    return {
      legalEntityId:
        typeof parsed.legalEntityId === "string" ? parsed.legalEntityId : "",
      siteId: typeof parsed.siteId === "string" ? parsed.siteId : "",
    };
  } catch {
    return none;
  }
}

function write(identity: string, value: Stored): void {
  if (identity === "") {
    return;
  }
  try {
    window.localStorage.setItem(storageKey(identity), JSON.stringify(value));
  } catch {
    return;
  }
}

export function WorkContextProvider({
  identity = "",
  enabled = true,
  children,
}: {
  identity?: string;
  enabled?: boolean;
  children: ReactNode;
}) {
  const [state, setState] = useState<Stored>(() =>
    enabled ? read(identity) : none,
  );
  useEffect(() => {
    setState(enabled ? read(identity) : none);
  }, [identity, enabled]);
  const update = useCallback(
    (next: Stored) => {
      write(identity, next);
      setState(next);
    },
    [identity],
  );
  const setLegalEntityId = useCallback(
    (id: string) => update({ legalEntityId: id, siteId: "" }),
    [update],
  );
  const setSiteId = useCallback(
    (id: string) => update({ ...state, siteId: id }),
    [update, state],
  );
  const value = useMemo(
    () => ({ ...state, identity: enabled ? identity : "", setLegalEntityId, setSiteId }),
    [state, enabled, identity, setLegalEntityId, setSiteId],
  );
  return <WorkContext.Provider value={value}>{children}</WorkContext.Provider>;
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
          rememberOption("legalEntity", found[0]);
          setLegalEntityId(found[0].id);
        }
      },
      () => undefined,
    );
    return () => controller.abort();
  }, [legalEntityId, setLegalEntityId]);

  useEffect(() => {
    if (legalEntityId === "") {
      return;
    }
    let live = true;
    void resolveOption("legalEntity", legalEntityId).then((entry) => {
      if (live && entry === null) {
        setLegalEntityId("");
      }
    });
    return () => {
      live = false;
    };
  }, [legalEntityId, setLegalEntityId]);

  useEffect(() => {
    if (siteId === "") {
      return;
    }
    let live = true;
    void resolveOption("site", siteId).then((entry) => {
      if (live && entry === null) {
        setSiteId("");
      }
    });
    return () => {
      live = false;
    };
  }, [siteId, setSiteId]);

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
