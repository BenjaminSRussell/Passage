// The screens the demo is built to show. The home page lists them in demo builds, grouped by area.
import { TOUR as GROUP } from "./handlers/group-tour";
import { TOUR as MFG } from "./handlers/mfg/tour";
import { TOUR as O2C } from "./handlers/o2c/tour";
import { TOUR as SUPPLY } from "./handlers/supply/tour";
import { TOUR as TAX } from "./handlers/tax/tour";

export interface TourStop {
  area: "Manufacturing" | "Supply chain" | "Order to cash" | "Tax" | "Group";
  label: string;
  to: string;
  blurb: string;
}

export const TOUR: TourStop[] = [...MFG, ...SUPPLY, ...O2C, ...TAX, ...GROUP];
