import { createContext, useContext } from "react";

export interface WorkContextValue {
  identity: string;
  legalEntityId: string;
  siteId: string;
  setLegalEntityId(id: string): void;
  setSiteId(id: string): void;
}

const empty: WorkContextValue = {
  identity: "",
  legalEntityId: "",
  siteId: "",
  setLegalEntityId: () => undefined,
  setSiteId: () => undefined,
};

export const WorkContext = createContext<WorkContextValue>(empty);

export function useWorkContext(): WorkContextValue {
  return useContext(WorkContext);
}
