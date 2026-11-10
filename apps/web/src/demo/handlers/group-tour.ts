import { uid } from "../store";
import type { TourStop } from "../tour";

// Group consolidation and intercompany screens for the home page demo tour, in walkthrough order.
export const TOUR: TourStop[] = [
  { area: "Group", label: "Consolidation groups", to: "/consolidation/groups", blurb: "The Cedar Ridge Pumps group of three legal entities in dollars and euros." },
  { area: "Group", label: "Group structure", to: `/consolidation/groups/${uid(400, 1)}`, blurb: "Each unit's ledger and ownership history, with the Spanish subsidiary's stake rising from 40 to 60 percent. Collect balances and open the trial balance." },
  { area: "Group", label: "Posted intercompany journals", to: "/intercompany/journals", blurb: "One journal posted to both ledgers, and one reversed because the German ledger refused its side." },
  { area: "Group", label: "Intercompany charge log", to: "/intercompany/charge-log", blurb: "A posted engineering charge priced cost plus 10 percent, and a design charge on suspense for lack of a transfer price." },
  { area: "Group", label: "Reconciliation runs", to: "/intercompany/reconciliation-runs", blurb: "Last month's run: open the pairs that need action and resolve the mismatch with a note." },
];
