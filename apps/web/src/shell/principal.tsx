import { createContext, useContext, type ReactNode } from "react";

declare const __PASSAGE_DEV_DUTIES__: string | undefined;

export function parseDuties(raw: string | undefined): string[] {
  return (raw ?? "")
    .split(",")
    .map((duty) => duty.trim())
    .filter((duty) => duty !== "");
}

export function devDuties(): string[] {
  return parseDuties(typeof __PASSAGE_DEV_DUTIES__ === "string" ? __PASSAGE_DEV_DUTIES__ : undefined);
}

const PrincipalContext = createContext<readonly string[]>([]);

export function PrincipalProvider({ duties, children }: { duties: readonly string[]; children: ReactNode }) {
  return <PrincipalContext.Provider value={duties}>{children}</PrincipalContext.Provider>;
}

export function usePrincipalDuties(): readonly string[] {
  return useContext(PrincipalContext);
}
