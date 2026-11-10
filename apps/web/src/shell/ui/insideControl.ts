import { createContext, useContext } from "react";

/**
 * True inside an element that is itself a control, such as the button DataTable wraps around a row's first cell.
 * Content there must not add its own controls (axe nested-interactive): EntityName then shows the name only.
 */
export const InsideControl = createContext(false);

export function useInsideControl(): boolean {
  return useContext(InsideControl);
}
